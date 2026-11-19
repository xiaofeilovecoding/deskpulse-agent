// Package winmetrics samples Windows CPU, memory, physical disk I/O, the real
// process tree and multi-target ICMP RTT inside the DeskPulse Agent process. It does
// not start Telegraf, ping.exe or any other child process.
package winmetrics

import (
	"fmt"
	"time"
)

const (
	StatePending         = "pending"
	StateOK              = "ok"
	StatePartial         = "partial_loss"
	StateUnreachable     = "unreachable"
	StateDNSError        = "dns_error"
	StatePermissionError = "permission_error"
	StateUnsupported     = "unsupported"
	StateError           = "error"
	StateNoTarget        = "no_target"
)

// Memory is one RAM snapshot in bytes.
type Memory struct {
	Used    float64 `json:"used_bytes"`
	Total   float64 `json:"total_bytes"`
	Percent float64 `json:"used_percent"`
	TS      int64   `json:"ts"`
}

// DiskRate is aggregated physical-disk throughput. Nil means the first sample,
// a counter reset or an unreadable counter; it is never coerced to zero.
type DiskRate struct {
	ReadBPS   *float64 `json:"read_bytes_per_second"`
	WriteBPS  *float64 `json:"write_bytes_per_second"`
	ReadIOPS  *float64 `json:"read_iops"`
	WriteIOPS *float64 `json:"write_iops"`
	TS        int64    `json:"ts"`
	State     string   `json:"state"`
	Message   string   `json:"message,omitempty"`
}

// ProcessInfo is one real process row. StartMS disambiguates PID reuse.
type ProcessInfo struct {
	PID           int32    `json:"pid"`
	PPID          int32    `json:"ppid"`
	Name          string   `json:"name"`
	StartMS       int64    `json:"start_ms"`
	CPU           *float64 `json:"cpu_percent"`
	WorkingSet    *float64 `json:"working_set_bytes"`
	PrivateCommit *float64 `json:"private_commit_bytes,omitempty"`
	TS            int64    `json:"ts"`
}

// Node is one process-tree node. TotalCPU and TotalWS include the node and all
// descendants, each descendant counted exactly once.
type Node struct {
	Proc        ProcessInfo `json:"process"`
	Children    []*Node     `json:"children"`
	Orphan      bool        `json:"orphan"`
	Descendants int         `json:"descendants"`
	TotalCPU    *float64    `json:"total_cpu_percent"`
	TotalWS     *float64    `json:"total_working_set_bytes"`
}

// Tree groups real roots, orphans (parent absent or inaccessible) and provides
// flat rows for tests and diagnostics.
type Tree struct {
	Roots   []*Node `json:"roots"`
	Orphans []*Node `json:"orphans"`
	Total   int     `json:"total"`
}

// RTTTarget is one independent target probe result. Loss is nil when no probe
// could be sent (DNS/ICMP permission), and 100 when every sent packet was lost.
type RTTTarget struct {
	Target  string   `json:"target"`
	Display string   `json:"display"`
	State   string   `json:"state"`
	Message string   `json:"message,omitempty"`
	AvgMS   *float64 `json:"avg_ms"`
	MaxMS   *float64 `json:"max_ms"`
	Loss    *float64 `json:"loss_percent"`
	Sent    int      `json:"sent"`
	Recv    int      `json:"recv"`
	TS      int64    `json:"ts"`
}

// Snapshot is the immutable view consumed by the Win32 UI. Histories are
// bounded to the latest 60 samples for the compact sparklines.
type Snapshot struct {
	TS           int64         `json:"ts"`
	CPU          *float64      `json:"cpu_percent"`
	Memory       *Memory       `json:"memory"`
	Disk         DiskRate      `json:"disk"`
	Processes    []ProcessInfo `json:"processes"`
	Tree         Tree          `json:"tree"`
	RTT          []RTTTarget   `json:"rtt"`
	CPUHistory   []float64     `json:"cpu_history"`
	MemHistory   []float64     `json:"mem_history"`
	ReadHistory  []float64     `json:"read_history"`
	WriteHistory []float64     `json:"write_history"`
	MetricState  string        `json:"metric_state"`
	MetricError  string        `json:"metric_error,omitempty"`
	ProcessState string        `json:"process_state"`
	ProcessError string        `json:"process_error,omitempty"`
	RTTState     string        `json:"rtt_state"`
	RTTError     string        `json:"rtt_error,omitempty"`
}

const historyLen = 60

// HumanRate formats B/s with binary units and one decimal.
func HumanRate(v *float64) string {
	if v == nil {
		return "—"
	}
	return HumanBytes(*v) + "/s"
}

// HumanBytes formats a byte count with binary units.
func HumanBytes(v float64) string {
	if v < 0 {
		return "—"
	}
	const unit = 1024.0
	if v < unit {
		return fmt.Sprintf("%.0f B", v)
	}
	val := v
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	for _, u := range units {
		val /= unit
		if val < unit {
			return fmt.Sprintf("%.1f %s", val, u)
		}
	}
	return fmt.Sprintf("%.1f EiB", val/unit)
}

// HumanIOPS formats a rate with no decimal when large.
func HumanIOPS(v *float64) string {
	if v == nil {
		return "—"
	}
	if *v < 10 {
		return fmt.Sprintf("%.1f", *v)
	}
	return fmt.Sprintf("%.0f", *v)
}

// RTTText is the compact, unambiguous UI status for one target.
func RTTText(t RTTTarget) string {
	switch t.State {
	case StateOK, StatePartial:
		if t.AvgMS != nil {
			loss := 0.0
			if t.Loss != nil {
				loss = *t.Loss
			}
			return fmt.Sprintf("%s %.0f ms · %.0f%%", t.Display, *t.AvgMS, loss)
		}
	case StateDNSError:
		return t.Display + " DNS 错误"
	case StatePermissionError:
		return t.Display + " ICMP 权限受限"
	case StateUnreachable:
		return t.Display + " 超时/全丢包"
	case StateUnsupported:
		return t.Display + " IPv6 不支持"
	case StatePending:
		return t.Display + " 探测中"
	case StateError:
		return t.Display + " 探测失败"
	}
	return t.Display + " 待配置"
}

// FormatAge is a small status helper used by the panel.
func FormatAge(ts int64, now time.Time) string {
	if ts <= 0 {
		return "尚无样本"
	}
	d := now.Sub(time.UnixMilli(ts))
	if d < 0 {
		d = 0
	}
	switch {
	case d < 5*time.Second:
		return "刚刚更新"
	case d < time.Minute:
		return fmt.Sprintf("%d 秒前", int(d.Seconds()))
	default:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	}
}

func cloneSnapshot(s Snapshot) Snapshot {
	s.CPUHistory = append([]float64(nil), s.CPUHistory...)
	s.MemHistory = append([]float64(nil), s.MemHistory...)
	s.ReadHistory = append([]float64(nil), s.ReadHistory...)
	s.WriteHistory = append([]float64(nil), s.WriteHistory...)
	s.RTT = append([]RTTTarget(nil), s.RTT...)
	s.Processes = append([]ProcessInfo(nil), s.Processes...)
	// Nodes are never mutated after publication; copying the root slice is
	// sufficient and keeps the UI timer cheap.
	s.Tree.Roots = append([]*Node(nil), s.Tree.Roots...)
	s.Tree.Orphans = append([]*Node(nil), s.Tree.Orphans...)
	if s.Memory != nil {
		m := *s.Memory
		s.Memory = &m
	}
	if s.CPU != nil {
		v := *s.CPU
		s.CPU = &v
	}
	return s
}
