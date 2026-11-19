//go:build windows

package winmetrics

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/ingest"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/store"
)

// Options configures the in-process sampler.
type Options struct {
	Store   *store.Store
	Targets func() []string
	Logger  *slog.Logger
	Now     func() time.Time
	Enabled func(settings.MetricMask) bool
}

// Sampler owns all Windows sampling goroutines. It writes directly to SQLite
// and publishes an immutable snapshot for the Win32 UI.
type Sampler struct {
	opts Options

	mu       sync.RWMutex
	snap     Snapshot
	cpuPrev  cpuCounters
	cpuSeen  bool
	diskPrev diskCounters
	diskAt   time.Time
	diskSeen bool
	procPrev map[int32]procCPUState

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a sampler in the pending state.
func New(opts Options) *Sampler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Targets == nil {
		opts.Targets = func() []string { return nil }
	}
	if opts.Enabled == nil {
		opts.Enabled = func(settings.MetricMask) bool { return true }
	}
	return &Sampler{
		opts:     opts,
		procPrev: make(map[int32]procCPUState),
		snap: Snapshot{
			MetricState:  StatePending,
			ProcessState: StatePending,
			RTTState:     StatePending,
		},
	}
}

// Start launches metric (5s), process (10s) and RTT (15s) loops.
func (s *Sampler) Start(parent context.Context) {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.mu.Unlock()

	s.wg.Add(3)
	go func() {
		defer s.wg.Done()
		s.metricLoop(ctx)
	}()
	go func() {
		defer s.wg.Done()
		s.processLoop(ctx)
	}()
	go func() {
		defer s.wg.Done()
		s.rttLoop(ctx)
	}()
}

// Stop cancels and waits for every sampler goroutine.
func (s *Sampler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// Latest returns the current immutable UI snapshot.
func (s *Sampler) Latest() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSnapshot(s.snap)
}

func (s *Sampler) metricLoop(ctx context.Context) {
	s.collectMetrics(ctx)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.collectMetrics(ctx)
		}
	}
}

func (s *Sampler) processLoop(ctx context.Context) {
	s.collectProcesses(ctx)
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.collectProcesses(ctx)
		}
	}
}

func (s *Sampler) rttLoop(ctx context.Context) {
	s.collectRTT(ctx)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.collectRTT(ctx)
		}
	}
}

func (s *Sampler) collectMetrics(ctx context.Context) {
	now := s.opts.Now()
	ts := now.UnixMilli()
	var samples []ingest.Sample
	var errs []string

	hadCPU := s.cpuSeen
	var cpu *float64
	var sysErr error
	if s.opts.Enabled(settings.CPU) {
		cur, err := readSystemTimes()
		sysErr = err
		if err == nil {
			cpu = cpuPercentDelta(cur, s.cpuPrev)
			s.cpuPrev = cur
		} else {
			errs = append(errs, "CPU: "+err.Error())
		}
		s.cpuSeen = err == nil
	} else {
		s.cpuPrev = cpuCounters{}
		s.cpuSeen = false
	}

	var mem *Memory
	if s.opts.Enabled(settings.Memory) {
		if used, total, pct, err := readMemory(); err == nil {
			mem = &Memory{Used: used, Total: total, Percent: pct, TS: ts}
			samples = append(samples, ingest.Sample{TS: ts, Kind: "mem", Series: "memory", A: &used, B: &total, C: &pct})
		} else {
			errs = append(errs, "内存: "+err.Error())
		}
	}

	var disk DiskRate
	disk.TS = ts
	if !s.opts.Enabled(settings.Disk) {
		disk.State = StateUnsupported
		s.diskSeen = false
	} else if dc, err := readDiskCounters(); err == nil {
		disk.State = StatePending
		if s.diskSeen {
			dt := now.Sub(s.diskAt).Seconds()
			disk.ReadBPS = rateFromCounter(dc.readBytes, s.diskPrev.readBytes, dt)
			disk.WriteBPS = rateFromCounter(dc.writeBytes, s.diskPrev.writeBytes, dt)
			disk.ReadIOPS = rateFromCounter(dc.readCount, s.diskPrev.readCount, dt)
			disk.WriteIOPS = rateFromCounter(dc.writeCount, s.diskPrev.writeCount, dt)
			if disk.ReadBPS == nil && disk.WriteBPS == nil {
				disk.State = StatePartial
				disk.Message = "计数器回退或间隔异常，本次速率留空"
			} else {
				disk.State = StateOK
			}
		} else {
			disk.Message = "等待第二个磁盘样本"
		}
		s.diskPrev = dc
		s.diskAt = now
		s.diskSeen = true
		samples = append(samples, ingest.Sample{
			TS: ts, Kind: "diskio", Series: "total",
			A: disk.ReadBPS, B: disk.WriteBPS, C: disk.ReadIOPS, D: disk.WriteIOPS,
		})
	} else {
		disk.State = StateError
		disk.Message = err.Error()
		errs = append(errs, "磁盘: "+err.Error())
	}

	if cpu != nil {
		samples = append(samples, ingest.Sample{TS: ts, Kind: "cpu", Series: "total", A: cpu})
	}
	s.mu.Lock()
	s.snap.CPU = cpu
	s.snap.Memory = mem
	if cpu != nil {
		s.snap.CPU = cpu
		s.snap.CPUHistory = appendHistory(s.snap.CPUHistory, *cpu)
	}
	if mem != nil {
		s.snap.Memory = mem
		s.snap.MemHistory = appendHistory(s.snap.MemHistory, mem.Percent)
	}
	s.snap.Disk = disk
	if disk.ReadBPS != nil {
		s.snap.ReadHistory = appendHistory(s.snap.ReadHistory, *disk.ReadBPS)
	}
	if disk.WriteBPS != nil {
		s.snap.WriteHistory = appendHistory(s.snap.WriteHistory, *disk.WriteBPS)
	}
	s.snap.TS = ts
	switch {
	case sysErr != nil || (mem == nil && s.opts.Enabled(settings.Memory)):
		s.snap.MetricState = StateError
	case cpu == nil && !hadCPU:
		s.snap.MetricState = StatePending
	case cpu == nil || disk.State == StateError:
		s.snap.MetricState = StatePartial
	default:
		s.snap.MetricState = StateOK
	}
	if len(errs) > 0 {
		s.snap.MetricError = strings.Join(errs, "；")
	} else {
		s.snap.MetricError = ""
	}
	s.mu.Unlock()

	if err := s.write(ctx, ts, samples, nil, nil); err != nil {
		s.mu.Lock()
		s.snap.MetricState = StateError
		s.snap.MetricError = appendMessage(s.snap.MetricError, "本地数据库写入失败")
		s.mu.Unlock()
		s.opts.Logger.Warn("desktop metric store failed", "error", err.Error())
	}
}

func (s *Sampler) collectProcesses(ctx context.Context) {
	if !s.opts.Enabled(settings.CPU) && !s.opts.Enabled(settings.Memory) {
		s.mu.Lock()
		s.snap.Processes = nil
		s.snap.Tree = Tree{}
		s.snap.ProcessState = StateUnsupported
		s.mu.Unlock()
		return
	}
	now := s.opts.Now()
	procs, next, err := sampleProcesses(now, s.procPrev)
	if err != nil {
		s.mu.Lock()
		s.snap.ProcessState = StateError
		s.snap.ProcessError = err.Error()
		s.mu.Unlock()
		s.opts.Logger.Warn("desktop process sample failed", "error", err.Error())
		return
	}
	s.procPrev = next
	tree := BuildTree(procs)
	ts := now.UnixMilli()

	s.mu.Lock()
	s.snap.Processes = procs
	s.snap.Tree = tree
	s.snap.TS = ts
	s.snap.ProcessState = StateOK
	s.snap.ProcessError = ""
	s.mu.Unlock()

}

func (s *Sampler) collectRTT(ctx context.Context) {
	var targets []string
	if s.opts.Enabled(settings.Latency) {
		targets = normalizeTargets(s.opts.Targets())
	}
	now := s.opts.Now()
	ts := now.UnixMilli()
	if len(targets) == 0 {
		s.mu.Lock()
		s.snap.RTT = nil
		s.snap.RTTState = StateNoTarget
		s.snap.RTTError = "未配置 RTT 目标"
		s.snap.TS = ts
		s.mu.Unlock()
		return
	}

	type result struct {
		index int
		value RTTTarget
	}
	ch := make(chan result, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(idx int, t string) {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			ch <- result{index: idx, value: probeTarget(probeCtx, t, now)}
		}(i, target)
	}
	wg.Wait()
	close(ch)
	byIndex := make([]RTTTarget, len(targets))
	seen := make([]bool, len(targets))
	for r := range ch {
		byIndex[r.index] = r.value
		seen[r.index] = true
	}
	results := make([]RTTTarget, 0, len(targets))
	var samples []ingest.Sample
	bad := 0
	for i := range byIndex {
		if !seen[i] {
			byIndex[i] = RTTTarget{Target: targets[i], Display: displayTarget(targets[i]), State: StateError, Message: "没有返回探测结果", TS: ts}
		}
		r := byIndex[i]
		results = append(results, r)
		if r.State != StateOK && r.State != StatePartial {
			bad++
		}
		if r.Loss != nil {
			samples = append(samples, ingest.Sample{
				TS: ts, Kind: "rtt", Series: r.Target, A: r.AvgMS, B: r.MaxMS, C: r.Loss,
			})
		}
	}
	state := StateOK
	if bad > 0 {
		state = StatePartial
	}
	if bad == len(results) {
		state = StateError
	}
	s.mu.Lock()
	s.snap.RTT = results
	s.snap.RTTState = state
	s.snap.RTTError = ""
	s.snap.TS = ts
	s.mu.Unlock()
	if err := s.write(ctx, ts, samples, nil, nil); err != nil {
		s.mu.Lock()
		s.snap.RTTState = StatePartial
		s.snap.RTTError = "RTT 已探测，但本地数据库写入失败"
		s.mu.Unlock()
		s.opts.Logger.Warn("desktop rtt store failed", "error", err.Error())
	}
}

func (s *Sampler) write(parent context.Context, ts int64, samples []ingest.Sample, procs []ingest.ProcessSample, tree []store.ProcessTreeRow) error {
	if s.opts.Store == nil || (len(samples) == 0 && len(procs) == 0 && tree == nil) {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	return s.opts.Store.InsertDesktopSnapshot(ctx, ts, samples, procs, tree)
}

func appendHistory(list []float64, v float64) []float64 {
	list = append(list, v)
	if len(list) > historyLen {
		list = list[len(list)-historyLen:]
	}
	return list
}

func appendMessage(existing, msg string) string {
	if existing == "" {
		return msg
	}
	if strings.Contains(existing, msg) {
		return existing
	}
	return existing + "；" + msg
}

func normalizeTargets(in []string) []string {
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
