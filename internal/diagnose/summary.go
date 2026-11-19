// Package diagnose builds bounded metric summaries, calls configured model
// endpoints, validates JSON output and produces local reports.
package diagnose

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/store"
)

// Evidence is a single numeric fact that a model may cite.
type Evidence struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Value any    `json:"value"`
	Unit  string `json:"unit,omitempty"`
}

// MetricStat is a bounded aggregate.
type MetricStat struct {
	Avg     *float64 `json:"avg"`
	Peak    *float64 `json:"peak"`
	Min     *float64 `json:"min"`
	Samples int      `json:"samples"`
}

// Coverage records whether the selected window actually had enough samples.
type Coverage struct {
	Metric         string   `json:"metric"`
	CadenceSeconds int      `json:"cadence_seconds"`
	Expected       int      `json:"expected"`
	Observed       int      `json:"observed"`
	MissingPercent *float64 `json:"missing_percent"`
}

// DiskStat intentionally has no volume path.
type DiskStat struct {
	Alias           string   `json:"alias"`
	UsedPercentAvg  *float64 `json:"used_percent_avg"`
	UsedPercentPeak *float64 `json:"used_percent_peak"`
	UsedBytes       *float64 `json:"used_bytes"`
	TotalBytes      *float64 `json:"total_bytes"`
}

// IOStat intentionally has no device name.
type IOStat struct {
	Alias        string   `json:"alias"`
	ReadBPSAvg   *float64 `json:"read_bytes_per_second_avg"`
	ReadBPSPeak  *float64 `json:"read_bytes_per_second_peak"`
	WriteBPSAvg  *float64 `json:"write_bytes_per_second_avg"`
	WriteBPSPeak *float64 `json:"write_bytes_per_second_peak"`
	ReadIOPS     *float64 `json:"read_iops"`
	WriteIOPS    *float64 `json:"write_iops"`
}

// RTTStat has no target host; the model only needs whether data exists. It is
// retained for v0.1-compatible single-target summaries.
type RTTStat struct {
	TargetConfigured bool     `json:"target_configured"`
	Status           string   `json:"status"`
	AvgMS            *float64 `json:"avg_ms"`
	MaxMS            *float64 `json:"max_ms"`
	LossPercent      *float64 `json:"loss_percent"`
	Samples          int      `json:"samples"`
}

// RTTTargetStat is one aliased target in a v0.2 multi-target summary. Alias is
// local-only (R1, R2, …); the real hostname is never sent.
type RTTTargetStat struct {
	Alias       string   `json:"alias"`
	Status      string   `json:"status"`
	AvgMS       *float64 `json:"avg_ms"`
	MaxMS       *float64 `json:"max_ms"`
	LossPercent *float64 `json:"loss_percent"`
	Samples     int      `json:"samples"`
}

// ProcessStat carries the report alias and, only when the user opts in, the
// real process name.
type ProcessStat struct {
	Alias         string   `json:"alias"`
	Name          string   `json:"name,omitempty"`
	MaxCPUPercent *float64 `json:"max_cpu_percent"`
	WorkingSetMax *float64 `json:"working_set_max_bytes"`
	Samples       int      `json:"samples"`
}

// Summary is the entire data scope sent to DeepSeek.
type Summary struct {
	WindowMinutes int             `json:"window_minutes,omitempty"`
	WindowSeconds int             `json:"window_seconds,omitempty"`
	WindowStart   string          `json:"window_start_utc"`
	WindowEnd     string          `json:"window_end_utc"`
	Scope         string          `json:"scope"`
	Coverage      []Coverage      `json:"coverage"`
	CPU           MetricStat      `json:"cpu"`
	Memory        MetricStat      `json:"memory_used_percent"`
	MemUsedBytes  *float64        `json:"memory_used_bytes"`
	MemTotalBytes *float64        `json:"memory_total_bytes"`
	Disks         []DiskStat      `json:"logical_disks,omitempty"`
	DiskIO        []IOStat        `json:"physical_disk_io"`
	RTT           *RTTStat        `json:"rtt,omitempty"`
	RTTTargets    []RTTTargetStat `json:"rtt_targets,omitempty"`
	Processes     []ProcessStat   `json:"processes"`
	DataGaps      []string        `json:"data_gaps"`
	Evidence      []Evidence      `json:"evidence"`
}

const summaryScope = "仅本机选定时间窗的聚合指标；不含主机名、用户名、命令行、文件路径、日志原文或稳定设备标识。进程默认使用别名。"

// BuildSummary is the v0.1-compatible single-target, minute-window summary.
func BuildSummary(ctx context.Context, st *store.Store, minutes int, includeProcessNames bool, now time.Time, rttTarget string) (Summary, map[string]string, error) {
	if minutes != 5 && minutes != 15 && minutes != 60 {
		return Summary{}, nil, fmt.Errorf("诊断窗口仅支持 5、15 或 60 分钟")
	}
	return buildSummary(ctx, st, minutes*60, minutes, includeProcessNames, now, []string{rttTarget}, true, false)
}

// BuildDesktopSummary is the v0.2 summary: any 15–3600 second window, ordered
// RTT aliases, no logical-volume capacity and no target hostnames.
func BuildDesktopSummary(ctx context.Context, st *store.Store, seconds int, includeProcessNames bool, now time.Time, rttTargets []string) (Summary, map[string]string, error) {
	if seconds < 15 || seconds > 3600 {
		return Summary{}, nil, fmt.Errorf("诊断窗口必须在 15–3600 秒之间")
	}
	return buildSummary(ctx, st, seconds, 0, includeProcessNames, now, rttTargets, false, true)
}

func buildSummary(ctx context.Context, st *store.Store, seconds, minutes int, includeProcessNames bool, now time.Time, rttTargets []string, includeDisks, desktop bool) (Summary, map[string]string, error) {
	from := now.Add(-time.Duration(seconds) * time.Second).UnixMilli()
	to := now.UnixMilli()
	win, err := st.QueryWindow(ctx, from, to, "")
	if err != nil {
		return Summary{}, nil, err
	}
	targets := normalizeRTTTargets(rttTargets)
	rttByTarget, err := st.QueryRTTTargets(ctx, from, to, targets)
	if err != nil {
		return Summary{}, nil, err
	}
	s := Summary{
		WindowMinutes: minutes,
		WindowSeconds: seconds,
		WindowStart:   time.UnixMilli(from).UTC().Format(time.RFC3339),
		WindowEnd:     time.UnixMilli(to).UTC().Format(time.RFC3339),
		Scope:         summaryScope,
	}
	aliases := make(map[string]string)
	evidence := make(map[string]Evidence)
	addEvidence := func(e Evidence) {
		if isNilValue(e.Value) {
			return
		}
		evidence[e.ID] = e
	}

	cpuStat := statsFromPoints(win.CPU, func(p store.Point) *float64 { return p.Avg }, func(p store.Point) *float64 { return p.Max })
	s.CPU = MetricStat{Avg: cpuStat.avg, Peak: cpuStat.peak, Min: cpuStat.min, Samples: win.Counts["cpu"]}
	addEvidence(Evidence{ID: "cpu.avg", Label: "CPU 平均利用率", Value: cpuStat.avg, Unit: "percent"})
	addEvidence(Evidence{ID: "cpu.peak", Label: "CPU 峰值利用率", Value: cpuStat.peak, Unit: "percent"})
	addEvidence(Evidence{ID: "cpu.samples", Label: "CPU 样本数", Value: win.Counts["cpu"], Unit: "samples"})

	memStat := statsFromPoints(win.Memory, func(p store.Point) *float64 { return p.Avg }, func(p store.Point) *float64 { return p.Max })
	s.Memory = MetricStat{Avg: memStat.avg, Peak: memStat.peak, Min: memStat.min, Samples: win.Counts["mem"]}
	s.MemUsedBytes, s.MemTotalBytes = win.MemUsed, win.MemTotal
	addEvidence(Evidence{ID: "mem.avg_pct", Label: "内存平均使用率", Value: memStat.avg, Unit: "percent"})
	addEvidence(Evidence{ID: "mem.peak_pct", Label: "内存峰值使用率", Value: memStat.peak, Unit: "percent"})
	addEvidence(Evidence{ID: "mem.used_bytes", Label: "内存已用（最新）", Value: win.MemUsed, Unit: "bytes"})
	addEvidence(Evidence{ID: "mem.total_bytes", Label: "内存总量", Value: win.MemTotal, Unit: "bytes"})

	procCount, err := st.CountProcessSamples(ctx, from, to)
	if err != nil {
		return Summary{}, nil, err
	}
	rttObserved, err := st.CountRTTTargets(ctx, from, to, targets)
	if err != nil {
		return Summary{}, nil, err
	}
	s.Coverage = []Coverage{
		{Metric: "cpu", CadenceSeconds: 5, Expected: expectedSamples(seconds, 5), Observed: win.Counts["cpu"], MissingPercent: missingPct(expectedSamples(seconds, 5), win.Counts["cpu"])},
		{Metric: "memory", CadenceSeconds: 5, Expected: expectedSamples(seconds, 5), Observed: win.Counts["mem"], MissingPercent: missingPct(expectedSamples(seconds, 5), win.Counts["mem"])},
		{Metric: "process", CadenceSeconds: 10, Expected: expectedSamples(seconds, 10), Observed: procCount, MissingPercent: missingPct(expectedSamples(seconds, 10), procCount)},
		{Metric: "rtt", CadenceSeconds: 15, Expected: expectedSamples(seconds, 15) * maxInt(1, len(targets)), Observed: rttObserved, MissingPercent: missingPct(expectedSamples(seconds, 15)*maxInt(1, len(targets)), rttObserved)},
	}

	if includeDisks {
		for i, d := range win.Disks {
			if i >= 8 {
				break
			}
			alias := fmt.Sprintf("D%d", i+1)
			st := statsFromPoints(d.Points, func(p store.Point) *float64 { return p.Avg }, func(p store.Point) *float64 { return p.Max })
			ds := DiskStat{Alias: alias, UsedPercentAvg: st.avg, UsedPercentPeak: st.peak, UsedBytes: d.Used, TotalBytes: d.Total}
			s.Disks = append(s.Disks, ds)
			addEvidence(Evidence{ID: "disk." + alias + ".used_pct", Label: "逻辑盘 " + alias + " 平均已用率", Value: st.avg, Unit: "percent"})
			addEvidence(Evidence{ID: "disk." + alias + ".peak_pct", Label: "逻辑盘 " + alias + " 峰值已用率", Value: st.peak, Unit: "percent"})
		}
	}
	for i, io := range win.DiskIO {
		if i >= 8 {
			break
		}
		alias := fmt.Sprintf("IO%d", i+1)
		rstat := statsFromPoints(io.Read, func(p store.Point) *float64 { return p.Avg }, func(p store.Point) *float64 { return p.Max })
		wstat := statsFromPoints(io.Write, func(p store.Point) *float64 { return p.Avg }, func(p store.Point) *float64 { return p.Max })
		s.DiskIO = append(s.DiskIO, IOStat{Alias: alias, ReadBPSAvg: rstat.avg, ReadBPSPeak: rstat.peak, WriteBPSAvg: wstat.avg, WriteBPSPeak: wstat.peak, ReadIOPS: io.ReadIOPS, WriteIOPS: io.WriteIOPS})
		addEvidence(Evidence{ID: "io." + alias + ".read_peak", Label: "物理盘 " + alias + " 读峰值", Value: rstat.peak, Unit: "bytes_per_second"})
		addEvidence(Evidence{ID: "io." + alias + ".write_peak", Label: "物理盘 " + alias + " 写峰值", Value: wstat.peak, Unit: "bytes_per_second"})
	}

	if !desktop {
		rttConfigured := len(targets) > 0
		rt := RTTStat{TargetConfigured: rttConfigured, Status: "ok"}
		var pts []store.RTTPoint
		if rttConfigured {
			pts = rttByTarget[targets[0]]
		}
		if !rttConfigured {
			rt.Status = "no_target"
			s.DataGaps = append(s.DataGaps, "未配置 RTT 目标，缺少业务 RTT 证据")
		} else if len(pts) == 0 {
			rt.Status = "no_data"
			s.DataGaps = append(s.DataGaps, "RTT 目标已配置，但窗口内没有可用探测结果")
		} else {
			allLost := fillRTTStat(&rt, pts)
			if allLost {
				s.DataGaps = append(s.DataGaps, "RTT 窗口内全部探测丢包，缺少有效延迟证据")
			}
			addEvidence(Evidence{ID: "rtt.avg", Label: "RTT 平均延迟", Value: rt.AvgMS, Unit: "milliseconds"})
			addEvidence(Evidence{ID: "rtt.max", Label: "RTT 最大延迟", Value: rt.MaxMS, Unit: "milliseconds"})
			addEvidence(Evidence{ID: "rtt.loss", Label: "RTT 平均丢包率", Value: rt.LossPercent, Unit: "percent"})
			addEvidence(Evidence{ID: "rtt.samples", Label: "RTT 样本数", Value: rt.Samples, Unit: "samples"})
		}
		s.RTT = &rt
	} else {
		if len(targets) == 0 {
			s.DataGaps = append(s.DataGaps, "未配置 RTT 目标，缺少业务 RTT 证据")
		}
		for i, target := range targets {
			alias := fmt.Sprintf("R%d", i+1)
			aliases[alias] = target
			ts := RTTTargetStat{Alias: alias, Status: "ok"}
			pts := rttByTarget[target]
			if len(pts) == 0 {
				ts.Status = "no_data"
				s.DataGaps = append(s.DataGaps, "RTT 目标 "+alias+" 窗口内没有可用探测结果")
			} else {
				allLost := fillRTTTargetStat(&ts, pts)
				if allLost {
					s.DataGaps = append(s.DataGaps, "RTT 目标 "+alias+" 窗口内全部探测丢包，缺少有效延迟证据")
				}
			}
			s.RTTTargets = append(s.RTTTargets, ts)
			addEvidence(Evidence{ID: "rtt." + alias + ".avg", Label: "RTT 目标 " + alias + " 平均延迟", Value: ts.AvgMS, Unit: "milliseconds"})
			addEvidence(Evidence{ID: "rtt." + alias + ".max", Label: "RTT 目标 " + alias + " 最大延迟", Value: ts.MaxMS, Unit: "milliseconds"})
			addEvidence(Evidence{ID: "rtt." + alias + ".loss", Label: "RTT 目标 " + alias + " 平均丢包率", Value: ts.LossPercent, Unit: "percent"})
			addEvidence(Evidence{ID: "rtt." + alias + ".samples", Label: "RTT 目标 " + alias + " 样本数", Value: ts.Samples, Unit: "samples"})
		}
	}

	// Process union history is bounded by the store. If the history is empty
	// (young install), fall back to the latest snapshot.
	aggs, err := st.TopProcesses(ctx, from, to, 12)
	if err != nil {
		return Summary{}, nil, err
	}
	if len(aggs) == 0 {
		rows, _, qerr := st.QueryProcesses(ctx, "cpu", "", 12)
		if qerr != nil {
			return Summary{}, nil, qerr
		}
		for _, p := range rows {
			aggs = append(aggs, store.ProcessAgg{Name: p.Name, MaxCPU: p.CPU, MaxWorkingSet: p.WorkingSet, Samples: 1})
		}
	}
	for i, p := range aggs {
		if i >= 12 {
			break
		}
		alias := fmt.Sprintf("P%02d", i+1)
		aliases[alias] = p.Name
		ps := ProcessStat{Alias: alias, MaxCPUPercent: p.MaxCPU, WorkingSetMax: p.MaxWorkingSet, Samples: p.Samples}
		if includeProcessNames {
			ps.Name = p.Name
		}
		s.Processes = append(s.Processes, ps)
		addEvidence(Evidence{ID: "proc." + alias + ".cpu", Label: "进程 " + alias + " 最大 CPU", Value: p.MaxCPU, Unit: "percent"})
		addEvidence(Evidence{ID: "proc." + alias + ".mem", Label: "进程 " + alias + " 最大 Working Set", Value: p.MaxWorkingSet, Unit: "bytes"})
		if includeProcessNames {
			addEvidence(Evidence{ID: "proc." + alias + ".name", Label: "进程 " + alias + " 名称", Value: p.Name})
		}
	}
	if len(s.Processes) == 0 {
		s.DataGaps = append(s.DataGaps, "窗口内没有进程快照，无法判断忙进程")
	}
	if win.Counts["cpu"] == 0 {
		s.DataGaps = append(s.DataGaps, "CPU 窗口内没有样本")
	}
	if win.Counts["mem"] == 0 {
		s.DataGaps = append(s.DataGaps, "内存窗口内没有样本")
	}
	if win.Truncated {
		s.DataGaps = append(s.DataGaps, "窗口数据超过上限，摘要可能不完整")
	}
	if s.CPU.Avg == nil && !containsGap(s.DataGaps, "CPU") {
		s.DataGaps = append(s.DataGaps, "CPU 缺少可用数值")
	}
	if s.Memory.Avg == nil && !containsGap(s.DataGaps, "内存") {
		s.DataGaps = append(s.DataGaps, "内存缺少可用数值")
	}

	// Stable evidence list for prompt and report display.
	ids := make([]string, 0, len(evidence))
	for id := range evidence {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s.Evidence = append(s.Evidence, evidence[id])
	}
	if err := boundSummary(&s, aliases); err != nil {
		return Summary{}, nil, err
	}
	return s, aliases, nil
}

// boundSummary enforces the outbound size limit by dropping lower-priority
// details, never by truncating JSON.
func boundSummary(s *Summary, aliases map[string]string) error {
	for {
		data, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if len(data) <= 24<<10 {
			return nil
		}
		switch {
		case len(s.Processes) > 0:
			last := s.Processes[len(s.Processes)-1]
			s.Processes = s.Processes[:len(s.Processes)-1]
			delete(aliases, last.Alias)
			s.Evidence = filterEvidence(s.Evidence, []string{"proc." + last.Alias + ".cpu", "proc." + last.Alias + ".mem", "proc." + last.Alias + ".name"})
		case len(s.RTTTargets) > 0:
			last := s.RTTTargets[len(s.RTTTargets)-1]
			s.RTTTargets = s.RTTTargets[:len(s.RTTTargets)-1]
			delete(aliases, last.Alias)
			s.Evidence = filterEvidence(s.Evidence, []string{
				"rtt." + last.Alias + ".avg", "rtt." + last.Alias + ".max",
				"rtt." + last.Alias + ".loss", "rtt." + last.Alias + ".samples",
			})
		case len(s.DiskIO) > 0:
			last := s.DiskIO[len(s.DiskIO)-1]
			s.DiskIO = s.DiskIO[:len(s.DiskIO)-1]
			s.Evidence = filterEvidence(s.Evidence, []string{"io." + last.Alias + ".read_peak", "io." + last.Alias + ".write_peak"})
		case len(s.Disks) > 0:
			last := s.Disks[len(s.Disks)-1]
			s.Disks = s.Disks[:len(s.Disks)-1]
			s.Evidence = filterEvidence(s.Evidence, []string{"disk." + last.Alias + ".used_pct", "disk." + last.Alias + ".peak_pct"})
		case len(s.Evidence) > 0:
			s.Evidence = s.Evidence[:len(s.Evidence)-1]
		default:
			return fmt.Errorf("诊断摘要超过发送上限")
		}
	}
}

func filterEvidence(in []Evidence, drop []string) []Evidence {
	blocked := make(map[string]bool, len(drop))
	for _, id := range drop {
		blocked[id] = true
	}
	out := in[:0]
	for _, e := range in {
		if !blocked[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

func missingPct(expected, observed int) *float64 {
	if expected <= 0 {
		return nil
	}
	if observed >= expected {
		return floatPtr(0)
	}
	return floatPtr(float64(expected-observed) / float64(expected) * 100)
}

func floatPtr(v float64) *float64 { return &v }

func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	if p, ok := v.(*float64); ok {
		return p == nil
	}
	return false
}

func containsGap(gaps []string, substr string) bool {
	for _, g := range gaps {
		if strings.Contains(g, substr) {
			return true
		}
	}
	return false
}

type pointStats struct {
	avg, peak, min *float64
}

func statsFromPoints(points []store.Point, avgFn, peakFn func(store.Point) *float64) pointStats {
	var sum float64
	var weight int
	var peak, minv *float64
	for _, p := range points {
		a := avgFn(p)
		if a != nil {
			w := p.Count
			if w < 1 {
				w = 1
			}
			sum += *a * float64(w)
			weight += w
		}
		if v := peakFn(p); v != nil && (peak == nil || *v > *peak) {
			x := *v
			peak = &x
		}
		if p.Min != nil && (minv == nil || *p.Min < *minv) {
			x := *p.Min
			minv = &x
		}
	}
	out := pointStats{peak: peak, min: minv}
	if weight > 0 {
		v := sum / float64(weight)
		out.avg = &v
	}
	if out.peak == nil && out.avg != nil {
		out.peak = out.avg
	}
	return out
}

func normalizeRTTTargets(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func expectedSamples(seconds, cadence int) int {
	if seconds <= 0 || cadence <= 0 {
		return 0
	}
	return (seconds + cadence - 1) / cadence
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func fillRTTStat(rt *RTTStat, pts []store.RTTPoint) bool {
	var avgSum, lossSum, maxVal float64
	var avgN, lossN, maxN int
	allLost := true
	for _, p := range pts {
		if p.Avg != nil {
			avgSum += *p.Avg
			avgN++
		}
		if p.Max != nil && (maxN == 0 || *p.Max > maxVal) {
			maxVal = *p.Max
			maxN++
		}
		if p.Loss != nil {
			lossSum += *p.Loss
			lossN++
			if *p.Loss < 100 {
				allLost = false
			}
		}
	}
	if avgN > 0 {
		v := avgSum / float64(avgN)
		rt.AvgMS = &v
	}
	if maxN > 0 {
		v := maxVal
		rt.MaxMS = &v
	}
	if lossN > 0 {
		v := lossSum / float64(lossN)
		rt.LossPercent = &v
	}
	rt.Samples = len(pts)
	if allLost && lossN > 0 {
		rt.Status = "unreachable"
	}
	return allLost && lossN > 0
}

func fillRTTTargetStat(ts *RTTTargetStat, pts []store.RTTPoint) bool {
	var rt RTTStat
	allLost := fillRTTStat(&rt, pts)
	ts.AvgMS, ts.MaxMS, ts.LossPercent, ts.Samples = rt.AvgMS, rt.MaxMS, rt.LossPercent, rt.Samples
	if allLost {
		ts.Status = "unreachable"
	} else if ts.LossPercent != nil && *ts.LossPercent > 0 {
		ts.Status = "partial"
	}
	return allLost
}
