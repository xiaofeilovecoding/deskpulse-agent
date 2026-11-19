package winmetrics

import "math"

type cpuCounters struct {
	idle   uint64
	kernel uint64
	user   uint64
	valid  bool
}

// cpuPercentDelta computes whole-machine CPU utilization from cumulative
// GetSystemTimes counters. Kernel time includes idle time. Any counter reset or
// zero interval yields nil rather than a fabricated zero.
func cpuPercentDelta(cur, prev cpuCounters) *float64 {
	if !cur.valid || !prev.valid {
		return nil
	}
	if cur.idle < prev.idle || cur.kernel < prev.kernel || cur.user < prev.user {
		return nil
	}
	idle := cur.idle - prev.idle
	total := (cur.kernel - prev.kernel) + (cur.user - prev.user)
	if total == 0 || idle > total {
		return nil
	}
	v := float64(total-idle) / float64(total) * 100
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return &v
}

// rateFromCounter returns (cur-prev)/dt when the counters are monotonic and
// the interval is plausible. First points, resets and long gaps are nil.
func rateFromCounter(cur, prev float64, dtSeconds float64) *float64 {
	if dtSeconds <= 0 || dtSeconds > 300 || cur < prev {
		return nil
	}
	v := (cur - prev) / dtSeconds
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil
	}
	return &v
}

// processCPUPercent normalizes per-process CPU time to whole-machine percent.
// deltaCPU100ns is the process kernel+user delta; elapsed is wall time.
func processCPUPercent(deltaCPU100ns, elapsedSeconds float64, logicalCores int) *float64 {
	if elapsedSeconds <= 0 || deltaCPU100ns < 0 || logicalCores < 1 {
		return nil
	}
	v := deltaCPU100ns / 1e7 / elapsedSeconds / float64(logicalCores) * 100
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil
	}
	if v > 100 {
		v = 100
	}
	return &v
}

func lossPercent(sent, recv int) *float64 {
	if sent <= 0 {
		return nil
	}
	if recv < 0 {
		recv = 0
	}
	if recv > sent {
		recv = sent
	}
	v := float64(sent-recv) / float64(sent) * 100
	return &v
}

func averageMS(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	out := sum / float64(len(values))
	return &out
}

func maxMS(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	m := values[0]
	for _, v := range values[1:] {
		if v > m {
			m = v
		}
	}
	return &m
}
