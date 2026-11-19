//go:build windows

package desktop

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/diagnose"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/winmetrics"
)

// v0.3 readout formats shared by the hover quick view and the popup. Rules
// from desktop-v0.3.md: CPU/memory round to integer; disk read/write share one
// common unit with 0 or 1 decimal; latency uses the first RTT target; no
// sample is 「—」, timeout shows 「超时」, zero only when a real sample said 0.

const (
	staleMetricSeconds = 15
	staleRTTSeconds    = 45
)

func fmtPercentInt(v *float64) string {
	if v == nil {
		return "—"
	}
	// A nonzero sub-1% reading shows <1% instead of a misleading 0%.
	if *v > 0 && *v < 1 {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", *v)
}

// fmtDiskPairCompact is the compact single-line disk copy for narrow rows.
// Same read/write semantics as fmtDiskPair (common unit covering both sides,
// 0 or 1 decimal, — for a missing side), rendered as "2.4 / 0.8 MB/s".
func fmtDiskPairCompact(read, write *float64) string {
	if read == nil && write == nil {
		return "—"
	}
	max := 0.0
	if read != nil && *read > max {
		max = *read
	}
	if write != nil && *write > max {
		max = *write
	}
	unit, factor := rateUnit(max)
	return rateValue(read, factor) + " / " + rateValue(write, factor) + " " + unit
}

// DiskRowCopy is one chosen disk-row rendering that fits the available width
// with at least gapPX between label and value — never overlaid or clipped.
type DiskRowCopy struct {
	Label string
	Value string
}

// chooseDiskRow picks the first copy that fits, in priority order: the full
// design copy → compact value under the standard label → compact label and
// value → the value alone. measureLabel uses the small font, measureValue the
// readout font of the calling page. errText (non-empty on degraded states)
// replaces the value.
func chooseDiskRow(measureLabel func(string) int32, measureValue func(string) int32, avail, gapPX int32, read, write *float64, errText string) DiskRowCopy {
	if errText != "" {
		if measureLabel("磁盘 I/O")+gapPX+measureValue(errText) <= avail {
			return DiskRowCopy{Label: "磁盘 I/O", Value: errText}
		}
		return DiskRowCopy{Label: "", Value: errText}
	}
	full := fmtDiskPair(read, write)
	compact := fmtDiskPairCompact(read, write)
	for _, c := range []DiskRowCopy{
		{Label: "磁盘 I/O", Value: full},
		{Label: "磁盘 I/O", Value: compact},
		{Label: "读 / 写", Value: compact},
		{Label: "", Value: compact},
		{Label: "", Value: full},
	} {
		// The gap only applies between a label and its value.
		need := measureValue(c.Value)
		if c.Label != "" {
			need += measureLabel(c.Label) + gapPX
		}
		if need <= avail {
			return c
		}
	}
	// Pathological widths: keep the value readable and never clipped.
	return DiskRowCopy{Label: "", Value: compact}
}

// fmtDiskPair renders "读 2.4 · 写 0.8 MB/s" choosing one decimal unit that
// covers both sides (the design sample uses MB/s). A missing side shows —;
// both missing shows a single —.
func fmtDiskPair(read, write *float64) string {
	if read == nil && write == nil {
		return "—"
	}
	// Pick the unit from the larger of the two magnitudes.
	max := 0.0
	if read != nil && *read > max {
		max = *read
	}
	if write != nil && *write > max {
		max = *write
	}
	unit, factor := rateUnit(max)
	return "读 " + rateValue(read, factor) + " · 写 " + rateValue(write, factor) + " " + unit
}

func rateUnit(maxBPS float64) (string, float64) {
	if maxBPS < 1000 {
		return "B/s", 1
	}
	units := []string{"KB/s", "MB/s", "GB/s", "TB/s", "PB/s"}
	v := maxBPS
	factor := 1000.0
	for _, u := range units {
		v /= 1000
		if v < 1000 || u == units[len(units)-1] {
			return u, factor
		}
		factor *= 1000
	}
	return "PB/s", factor
}

func rateValue(v *float64, factor float64) string {
	if v == nil {
		return "—"
	}
	x := *v / factor
	if x == 0 {
		// 有采样且确实为零给出 0，不是占位符。
		return "0"
	}
	if x >= 10 {
		return fmt.Sprintf("%.0f", x)
	}
	return fmt.Sprintf("%.1f", x)
}

// fmtLatency renders the first RTT target per spec. Missing samples are —,
// unreachable targets show their short state text.
func fmtLatency(snap winmetrics.Snapshot, targets []string) string {
	if len(targets) == 0 || snap.RTTState == winmetrics.StateNoTarget {
		return "—"
	}
	var t *winmetrics.RTTTarget
	for i := range snap.RTT {
		if strings.EqualFold(snap.RTT[i].Target, targets[0]) {
			t = &snap.RTT[i]
			break
		}
	}
	if t == nil {
		return "—"
	}
	switch t.State {
	case winmetrics.StateOK, winmetrics.StatePartial:
		if t.AvgMS != nil {
			return fmt.Sprintf("%.0f ms", *t.AvgMS)
		}
		return "—"
	case winmetrics.StateUnreachable:
		return "超时"
	case winmetrics.StateDNSError:
		return "DNS 错误"
	case winmetrics.StatePermissionError:
		return "权限受限"
	case winmetrics.StateUnsupported:
		return "IPv6 不支持"
	case winmetrics.StatePending:
		return "—"
	default:
		if t.Message != "" {
			return t.Message
		}
		return "探测失败"
	}
}

// latencyStale reports whether the first target's reading is older than the
// 45-second RTT staleness window.
func latencyStale(snap winmetrics.Snapshot, targets []string, nowMS int64) bool {
	if len(targets) == 0 {
		return false
	}
	for i := range snap.RTT {
		if strings.EqualFold(snap.RTT[i].Target, targets[0]) {
			return snap.RTT[i].TS > 0 && nowMS-snap.RTT[i].TS > staleRTTSeconds*1000
		}
	}
	return false
}

// metricStale reports whether all metric readings are older than 15 seconds.
func metricStale(snap winmetrics.Snapshot, nowMS int64) bool {
	return snap.TS > 0 && nowMS-snap.TS > staleMetricSeconds*1000
}

// fmtWindow keeps the v0.2 wording for windows in seconds.
func fmtWindow(seconds int) string {
	if seconds <= 0 {
		return "最近 30 秒"
	}
	if seconds%60 == 0 && seconds >= 60 {
		return fmt.Sprintf("最近 %d 分钟", seconds/60)
	}
	return fmt.Sprintf("最近 %d 秒", seconds)
}

func fmtTime(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).Local().Format("01-02 15:04:05")
}

// fmtBytesShort is the process-page memory column (design sample: 1.2 GB).
// Decimal units match the design; whole numbers drop the decimal.
func fmtBytesShort(v *float64) string {
	if v == nil {
		return "—"
	}
	const unit = 1000.0
	val := *v
	units := []string{"B", "KB", "MB", "GB", "TB"}
	for _, u := range units {
		if val < unit || u == units[len(units)-1] {
			if val >= 100 || val == float64(int64(val)) {
				return fmt.Sprintf("%.0f %s", val, u)
			}
			return fmt.Sprintf("%.1f %s", val, u)
		}
		val /= unit
	}
	return fmt.Sprintf("%.1f PB", val)
}

// evidenceLine renders one evidence item for the machine-facing copy form:
// ID + label + raw value + unit. IDs stay out of the on-page 证据 section and
// remain in the exported JSON and validation layer.
func evidenceLine(e diagnose.Evidence) string {
	if e.Value == nil {
		return e.ID + " · " + e.Label
	}
	line := e.ID + " · " + e.Label + "：" + fmt.Sprint(e.Value)
	if e.Unit != "" {
		line += " " + e.Unit
	}
	return line
}

func numericOf(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case *float64:
		if x != nil {
			return *x, true
		}
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func trimNum(n float64) string {
	if n == float64(int64(n)) {
		return fmt.Sprintf("%.0f", n)
	}
	s := fmt.Sprintf("%.2f", n)
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}

// fmtEvidenceValue renders an evidence value with a human unit: 76%, 19 ms,
// 1.2 GB, 2.4 MB/s, 30 个样本.
func fmtEvidenceValue(v any, unit string) string {
	if s, ok := v.(string); ok {
		return s
	}
	n, ok := numericOf(v)
	if !ok {
		if v == nil {
			return "—"
		}
		return fmt.Sprint(v)
	}
	switch unit {
	case "percent":
		return trimNum(n) + "%"
	case "milliseconds":
		return trimNum(n) + " ms"
	case "samples":
		return fmt.Sprintf("%.0f 个样本", n)
	case "bytes":
		return fmtBytesShort(&n)
	case "bytes_per_second":
		u, f := rateUnit(n)
		return rateValue(&n, f) + " " + u
	default:
		return trimNum(n)
	}
}

// evidenceHumanLine renders one evidence item in plain Chinese for the page
// and the copy text: 「CPU 平均利用率：76%」. Internal IDs never appear here;
// aliases are back-filled for readability (display only).
func evidenceHumanLine(e diagnose.Evidence, aliases map[string]string) string {
	label := backfillAliases(e.Label, aliases)
	if e.Value == nil {
		return label
	}
	return label + "：" + fmtEvidenceValue(e.Value, e.Unit)
}

// reportText builds the plain-text copy/export form of a report. Aliases are
// back-filled to real local names; this text never leaves the machine via the
// model path and contains no key material.
func reportText(r diagnose.Report) string {
	var b strings.Builder
	b.WriteString("预设：" + diagnose.PresetName(r.Preset) + " · " + windowLabel(r) + "\n\n")
	b.WriteString("结论\n")
	b.WriteString(strings.TrimSpace(r.Summary))
	b.WriteString("\n\n证据\n")
	if len(r.Findings) == 0 {
		b.WriteString("（无）\n")
	} else {
		catalog := map[string]diagnose.Evidence{}
		for _, e := range r.Evidence {
			catalog[e.ID] = e
		}
		for i, f := range r.Findings {
			cause := backfillAliases(f.Cause, r.Aliases)
			mark := ""
			if f.Unverified || len(f.EvidenceIDs) == 0 {
				mark = "（待验证）"
			}
			b.WriteString(fmt.Sprintf("%d. %s%s\n", i+1, cause, mark))
			for _, id := range f.EvidenceIDs {
				if e, ok := catalog[id]; ok {
					b.WriteString("   " + evidenceHumanLine(e, r.Aliases) + "\n")
				}
			}
		}
	}
	b.WriteString("\n建议\n")
	if len(r.NextSteps) == 0 {
		b.WriteString("（无）\n")
	}
	for _, s := range r.NextSteps {
		b.WriteString("- " + backfillAliases(s, r.Aliases) + "\n")
	}
	b.WriteString("\n数据局限\n")
	if len(r.DataGaps) == 0 {
		b.WriteString("（无）\n")
	}
	for _, s := range r.DataGaps {
		b.WriteString("- " + s + "\n")
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("模型 %s · 耗时 %d ms", r.Model, r.DurationMS))
	return b.String()
}

func windowLabel(r diagnose.Report) string {
	if r.WindowSeconds > 0 {
		return fmtWindow(r.WindowSeconds)
	}
	return fmtWindow(r.WindowMinutes * 60)
}

// backfillAliases replaces task-stable aliases with real local names for
// display and copy only.
func backfillAliases(text string, aliases map[string]string) string {
	if len(aliases) == 0 {
		return text
	}
	keys := make([]string, 0, len(aliases))
	for k := range aliases {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		text = strings.ReplaceAll(text, k, aliases[k])
	}
	return text
}

func keySourceText(source string) string {
	switch source {
	case "dpapi":
		return "已配置 · DPAPI"
	case "environment":
		return "已配置 · 环境变量"
	case "session":
		return "已配置 · 本次进程"
	case "dpapi_error":
		return "DPAPI 凭据读取失败"
	default:
		return "未配置"
	}
}
