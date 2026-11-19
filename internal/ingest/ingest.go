// Package ingest converts Telegraf's JSON output into DeskPulse Agent's bounded,
// normalized metric model. All timestamps are UTC Unix milliseconds.
package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	// MaxPayloadBytes bounds one Telegraf HTTP flush. The v0.1 collector
	// flushes at most 2048 metrics per batch, so this leaves ample headroom.
	MaxPayloadBytes = 4 << 20
	// MaxMetricsPerBatch bounds a single accepted batch.
	MaxMetricsPerBatch = 5000
	// MaxSamplesPerBatch bounds normalized samples.
	MaxSamplesPerBatch = 20000
	// MaxProcessesPerBatch bounds normalized process rows.
	MaxProcessesPerBatch = 3000
)

// ErrPayloadTooLarge is returned when the request body exceeds the configured
// hard limit.
var ErrPayloadTooLarge = errors.New("telegraf payload too large")

// RawMetric is one object from Telegraf's JSON serializer. Field values are
// left as decoded JSON numbers/strings until normalization.
type RawMetric struct {
	Name      string
	Tags      map[string]string
	Fields    map[string]any
	Timestamp int64
}

// Sample is one normalized numeric series point. Units are fixed by Kind:
//
//	cpu:    A = percent 0..100
//	mem:    A = used bytes, B = total bytes, C = used percent
//	disk:   A = used bytes, B = total bytes, C = used percent
//	diskio: A = read B/s, B = write B/s, C = read IOPS, D = write IOPS
//	rtt:    A = average ms, B = maximum ms, C = packet loss percent
//
// Nil values mean "not observable" (first counter sample, counter reset,
// packet loss, or a missing field). They are stored as SQL NULL and rendered
// as gaps, never as zero.
type Sample struct {
	TS     int64
	Kind   string
	Series string
	A      *float64
	B      *float64
	C      *float64
	D      *float64
}

// ProcessSample is one process row. Command lines, users and paths are never
// carried into this model.
type ProcessSample struct {
	TS         int64
	Name       string
	PID        int32
	CPU        *float64
	WorkingSet *float64
}

// CounterState is the last raw cumulative disk counter observation.
type CounterState struct {
	TS         int64
	ReadBytes  float64
	WriteBytes float64
	Reads      float64
	Writes     float64
}

// Batch is a fully normalized Telegraf batch. Counters must be persisted in
// the same transaction as Samples so rates survive a restart.
type Batch struct {
	Samples   []Sample
	Processes []ProcessSample
	ProcessTS int64
	Counters  map[string]CounterState
	Received  int
	Ignored   int
	Warnings  []string
}

// Normalizer is safe for concurrent use. A single HTTP ingest handler calls it,
// but the lock also protects tests and future fan-in.
type Normalizer struct {
	cores  int
	loader func() (map[string]CounterState, error)

	mu       sync.Mutex
	loaded   bool
	counters map[string]CounterState
}

// NewNormalizer creates a normalizer. loader may be nil (counters are then
// process-local only). cores is used to turn multi-core process percentages
// into whole-machine percentages.
func NewNormalizer(cores int, loader func() (map[string]CounterState, error)) *Normalizer {
	if cores < 1 {
		cores = 1
	}
	return &Normalizer{
		cores:    cores,
		loader:   loader,
		counters: make(map[string]CounterState),
	}
}

// ReloadCounters discards pending state after a batch could not be committed.
func (n *Normalizer) ReloadCounters() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.counters = make(map[string]CounterState)
	n.loaded = false
}

// Normalize converts raw Telegraf metrics. It deliberately accepts both
// single-object and {"metrics": [...]} serializer output, plus NDJSON.
func (n *Normalizer) Normalize(ctx context.Context, raws []RawMetric) (Batch, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.loaded && n.loader != nil {
		prev, err := n.loader()
		if err != nil {
			return Batch{}, fmt.Errorf("load counter state: %w", err)
		}
		if prev != nil {
			n.counters = prev
		}
		n.loaded = true
	}

	batch := Batch{Received: len(raws), Counters: make(map[string]CounterState)}
	seenProcess := make(map[int32]bool)

	add := func(s Sample) {
		if s.TS <= 0 || s.Kind == "" || s.Series == "" {
			batch.Ignored++
			return
		}
		if len(batch.Samples) >= MaxSamplesPerBatch {
			if !hasWarning(batch.Warnings, "样本数超过上限，部分已丢弃") {
				batch.Warnings = append(batch.Warnings, "样本数超过上限，部分已丢弃")
			}
			batch.Ignored++
			return
		}
		batch.Samples = append(batch.Samples, s)
	}
	addProcess := func(p ProcessSample) {
		if p.TS <= 0 || p.Name == "" || p.PID <= 0 {
			batch.Ignored++
			return
		}
		if seenProcess[p.PID] {
			return
		}
		if len(batch.Processes) >= MaxProcessesPerBatch {
			if !hasWarning(batch.Warnings, "进程数超过上限，部分已丢弃") {
				batch.Warnings = append(batch.Warnings, "进程数超过上限，部分已丢弃")
			}
			batch.Ignored++
			return
		}
		seenProcess[p.PID] = true
		if p.TS > batch.ProcessTS {
			batch.ProcessTS = p.TS
		}
		batch.Processes = append(batch.Processes, p)
	}

	for _, raw := range raws {
		ts := TimestampMS(raw.Timestamp)
		switch strings.ToLower(raw.Name) {
		case "cpu":
			if tag := raw.Tags["cpu"]; tag != "" && tag != "total" && tag != "cpu-total" {
				// Generated config uses totalcpu only; ignore accidental
				// per-core rows rather than mixing units.
				continue
			}
			idle, ok := Number(raw.Fields["usage_idle"])
			var pct *float64
			if ok {
				v := clamp(100-idle, 0, 100)
				pct = &v
			} else {
				var sum float64
				var anyField bool
				for _, k := range []string{"usage_user", "usage_system", "usage_nice", "usage_irq", "usage_softirq", "usage_steal", "usage_guest", "usage_guest_nice"} {
					if v, ok := Number(raw.Fields[k]); ok {
						sum += v
						anyField = true
					}
				}
				if anyField {
					v := clamp(sum, 0, 100)
					pct = &v
				}
			}
			if pct != nil {
				add(Sample{TS: ts, Kind: "cpu", Series: "total", A: pct})
			}
		case "mem":
			total, okTotal := Number(raw.Fields["total"])
			used, okUsed := Number(raw.Fields["used"])
			if !okUsed {
				if avail, ok := Number(raw.Fields["available"]); ok {
					used, okUsed = total-avail, true
				}
			}
			if !okTotal || !okUsed || total <= 0 || used < 0 {
				continue
			}
			pct := clamp(used/total*100, 0, 100)
			add(Sample{TS: ts, Kind: "mem", Series: "memory", A: &used, B: &total, C: &pct})
		case "disk":
			series := firstNonEmpty(raw.Tags["path"], raw.Tags["device"], raw.Tags["volume"])
			series = cleanSeries(series)
			total, okTotal := Number(raw.Fields["total"])
			used, okUsed := Number(raw.Fields["used"])
			if !okUsed {
				if free, ok := Number(raw.Fields["free"]); ok {
					used, okUsed = total-free, true
				}
			}
			if series == "" || !okTotal || !okUsed || total <= 0 || used < 0 {
				continue
			}
			pct := clamp(used/total*100, 0, 100)
			add(Sample{TS: ts, Kind: "disk", Series: series, A: &used, B: &total, C: &pct})
		case "diskio":
			series := cleanSeries(firstNonEmpty(raw.Tags["name"], raw.Tags["device"]))
			if series == "" {
				continue
			}
			rb, okRB := Number(raw.Fields["read_bytes"])
			wb, okWB := Number(raw.Fields["write_bytes"])
			rd, okRD := Number(raw.Fields["reads"])
			wr, okWR := Number(raw.Fields["writes"])
			if !okRB || !okWB || !okRD || !okWR {
				continue
			}
			cur := CounterState{TS: ts, ReadBytes: rb, WriteBytes: wb, Reads: rd, Writes: wr}
			var a, b, c, d *float64
			if prev, ok := n.counters[series]; ok {
				if ts <= prev.TS {
					batch.Ignored++
					continue
				}
				dt := float64(ts-prev.TS) / 1000.0
				if dt > 0 && dt <= 300 {
					if rb >= prev.ReadBytes && wb >= prev.WriteBytes && rd >= prev.Reads && wr >= prev.Writes {
						rbs := (rb - prev.ReadBytes) / dt
						wbs := (wb - prev.WriteBytes) / dt
						ri := (rd - prev.Reads) / dt
						wi := (wr - prev.Writes) / dt
						a, b, c, d = finitePtr(rbs), finitePtr(wbs), finitePtr(ri), finitePtr(wi)
					} else {
						if !hasWarning(batch.Warnings, "磁盘计数器回退，本次速率留空") {
							batch.Warnings = append(batch.Warnings, "磁盘计数器回退，本次速率留空")
						}
					}
				} else if dt > 300 {
					if !hasWarning(batch.Warnings, "磁盘计数器间隔过长，本次速率留空") {
						batch.Warnings = append(batch.Warnings, "磁盘计数器间隔过长，本次速率留空")
					}
				}
			}
			n.counters[series] = cur
			batch.Counters[series] = cur
			add(Sample{TS: ts, Kind: "diskio", Series: series, A: a, B: b, C: c, D: d})
		case "procstat":
			name := strings.TrimSpace(raw.Tags["process_name"])
			if name == "" {
				name, _ = raw.Fields["name"].(string)
			}
			pid, okPID := Number(raw.Fields["pid"])
			if !okPID {
				pid, okPID = Number(raw.Tags["pid"])
			}
			if name == "" || !okPID || pid <= 0 {
				continue
			}
			var cpu *float64
			if v, ok := Number(raw.Fields["cpu_usage"]); ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
				c := normalizeProcessCPU(v, n.cores)
				cpu = &c
			}
			var ws *float64
			if v, ok := Number(raw.Fields["memory_rss"]); ok && v >= 0 && !math.IsInf(v, 0) && !math.IsNaN(v) {
				ws = &v
			}
			addProcess(ProcessSample{TS: ts, Name: truncate(name, 260), PID: int32(pid), CPU: cpu, WorkingSet: ws})
		case "win_proc":
			// Optional compatibility with a win_perf_counters Process/Process V2
			// configuration. Generated v0.1 config uses inputs.procstat.
			name := strings.TrimSpace(firstNonEmpty(raw.Tags["instance"], raw.Tags["process_name"]))
			name = strings.TrimSuffix(name, "#0")
			if name == "" {
				continue
			}
			pid, okPID := Number(raw.Fields["ID Process"])
			if !okPID {
				continue
			}
			var cpu *float64
			if v, ok := Number(raw.Fields["% Processor Time"]); ok {
				c := normalizeProcessCPU(v, n.cores)
				cpu = &c
			}
			var ws *float64
			if v, ok := Number(raw.Fields["Working Set"]); ok && v >= 0 {
				ws = &v
			}
			addProcess(ProcessSample{TS: ts, Name: truncate(name, 260), PID: int32(pid), CPU: cpu, WorkingSet: ws})
		case "ping":
			series := cleanSeries(firstNonEmpty(raw.Tags["url"], raw.Tags["host"], raw.Tags["destination"]))
			if series == "" {
				continue
			}
			avg, okAvg := Number(raw.Fields["average_response_ms"])
			maxv, okMax := Number(raw.Fields["maximum_response_ms"])
			loss, okLoss := Number(raw.Fields["percent_reply_loss"])
			if !okLoss {
				loss, okLoss = Number(raw.Fields["percent_packet_loss"])
			}
			if !okLoss {
				// No loss field means no usable ICMP result.
				continue
			}
			if loss >= 100 {
				okAvg, okMax = false, false
			}
			var a, b *float64
			if okAvg && avg >= 0 {
				a = &avg
			}
			if okMax && maxv >= 0 {
				b = &maxv
			}
			l := clamp(loss, 0, 100)
			add(Sample{TS: ts, Kind: "rtt", Series: series, A: a, B: b, C: &l})
		default:
			// Unknown measurements are ignored but counted so the UI can
			// surface a mismatched Telegraf version instead of silently
			// pretending everything is healthy.
			batch.Ignored++
		}
	}

	sort.SliceStable(batch.Processes, func(i, j int) bool {
		return batch.Processes[i].PID < batch.Processes[j].PID
	})
	return batch, nil
}

func hasWarning(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func cleanSeries(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return truncate(s, 160)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func finitePtr(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil
	}
	return &v
}

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// normalizeProcessCPU converts a process percentage to whole-machine percent.
// Windows per-process counters commonly report N×100% on an N-core machine.
// Values already <=100 are left untouched, then all results are clamped.
func normalizeProcessCPU(v float64, cores int) float64 {
	if cores < 1 {
		cores = 1
	}
	// Telegraf's default procstat CPU and PDH's Process counter both use
	// 100% per logical processor, including when the raw value is below 100.
	return clamp(v/float64(cores), 0, 100)
}

// Number accepts the JSON number types produced by encoding/json with
// UseNumber, plus plain ints/floats/strings for fixture convenience.
func Number(v any) (float64, bool) {
	switch n := v.(type) {
	case nil:
		return 0, false
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// TimestampMS interprets Telegraf's numeric timestamp. The generated config
// requests 1ms, but accepting seconds/microseconds/nanoseconds makes fixtures
// and older configs safe.
func TimestampMS(v int64) int64 {
	switch {
	case v <= 0:
		return 0
	case v < 100_000_000_000: // seconds before 5138
		return v * 1000
	case v < 100_000_000_000_000: // milliseconds
		return v
	case v < 100_000_000_000_000_000: // microseconds
		return v / 1000
	default: // nanoseconds
		return v / 1_000_000
	}
}

// ParsePayload accepts:
//   - a JSON array of metrics
//   - a single metric object
//   - a {"metrics": [...]} batch object
//   - newline-delimited JSON objects (Telegraf non-batch serializer)
func ParsePayload(data []byte) ([]RawMetric, error) {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil, errors.New("empty payload")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out []RawMetric
	for {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode telegraf json: %w", err)
		}
		if err := collectValue(v, &out); err != nil {
			return nil, err
		}
		if len(out) > MaxMetricsPerBatch {
			return nil, fmt.Errorf("too many metrics: %d > %d", len(out), MaxMetricsPerBatch)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("payload contains no metrics")
	}
	return out, nil
}

func collectValue(v any, out *[]RawMetric) error {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			if err := collectValue(item, out); err != nil {
				return err
			}
		}
	case map[string]any:
		if raw, ok := x["metrics"]; ok {
			return collectValue(raw, out)
		}
		m, err := rawFromObject(x)
		if err != nil {
			return err
		}
		*out = append(*out, m)
	default:
		return fmt.Errorf("unsupported top-level JSON value %T", v)
	}
	return nil
}

func rawFromObject(obj map[string]any) (RawMetric, error) {
	m := RawMetric{Tags: map[string]string{}, Fields: map[string]any{}}
	name, _ := obj["name"].(string)
	m.Name = strings.TrimSpace(name)
	if m.Name == "" {
		return m, errors.New("telegraf metric missing name")
	}
	if fields, ok := obj["fields"].(map[string]any); ok {
		m.Fields = fields
	} else {
		return m, errors.New("telegraf metric missing fields")
	}
	if tags, ok := obj["tags"].(map[string]any); ok {
		for k, val := range tags {
			if s, ok := val.(string); ok {
				m.Tags[k] = s
			}
		}
	}
	if ts, ok := Number(obj["timestamp"]); ok {
		m.Timestamp = int64(ts)
	}
	return m, nil
}
