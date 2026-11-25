package desktop

import (
	"strings"
	"testing"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/diagnose"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/winmetrics"
)

func testChinese(zh, en string) string { return zh }

func TestReportCopyText(t *testing.T) {
	r := diagnose.Report{
		Summary:       "CPU 短时升高。",
		WindowSeconds: 30,
		Model:         "deepseek-flash",
		Preset:        diagnose.PresetOverview,
		Findings: []diagnose.Finding{
			{Cause: "进程 P01 占用高", EvidenceIDs: []string{"cpu.peak"}},
			{Cause: "无证据猜测", Unverified: true},
		},
		NextSteps: []string{"观察进程"},
		DataGaps:  []string{"样本较少"},
		Evidence:  []diagnose.Evidence{{ID: "cpu.peak", Label: "CPU 峰值", Value: 90.0}},
		Aliases:   map[string]string{"P01": "chrome.exe"},
	}
	text := reportText(r)
	for _, want := range []string{"预设：综合诊断", "最近 30 秒", "结论", "证据", "建议", "数据局限", "chrome.exe", "待验证", "观察进程", "样本较少"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "<script") || strings.Contains(text, "<html") {
		t.Fatalf("report contains markup: %s", text)
	}
}

// TestFmtDiskPair verifies the shared-unit rule: one decimal unit covers both
// sides, 0 or 1 decimal, — for a missing side.
func TestFmtDiskPair(t *testing.T) {
	read := 2.4e6
	write := 0.8e6
	if got := fmtDiskPair(testChinese, &read, &write); got != "读 2.4 · 写 0.8 MB/s" {
		t.Fatalf("pair = %q", got)
	}
	if got := fmtDiskPair(testChinese, &read, nil); got != "读 2.4 · 写 — MB/s" {
		t.Fatalf("read-only = %q", got)
	}
	if got := fmtDiskPair(testChinese, nil, nil); got != "—" {
		t.Fatalf("empty = %q", got)
	}
	zero := 0.0
	if got := fmtDiskPair(testChinese, &zero, &zero); got != "读 0 · 写 0 B/s" {
		t.Fatalf("zero = %q", got)
	}
	big := 3.62e9
	small := 1024.0
	if got := fmtDiskPair(testChinese, &big, &small); got != "读 3.6 · 写 0.0 GB/s" {
		t.Fatalf("mixed units = %q", got)
	}
}

// TestFmtLatency covers the first-target rule: normal ms reading, timeout
// text, missing sample dash.
func TestFmtLatency(t *testing.T) {
	avg := 19.0
	ok := winmetrics.Snapshot{
		RTT:      []winmetrics.RTTTarget{{Target: "www.baidu.com", State: winmetrics.StateOK, AvgMS: &avg}},
		RTTState: winmetrics.StateOK,
	}
	if got := fmtLatency(testChinese, ok, []string{"www.baidu.com"}); got != "19 ms" {
		t.Fatalf("latency = %q", got)
	}
	if got := fmtLatency(testChinese, ok, []string{"other.invalid"}); got != "—" {
		t.Fatalf("missing target = %q", got)
	}
	unreachable := winmetrics.Snapshot{
		RTT:      []winmetrics.RTTTarget{{Target: "www.baidu.com", State: winmetrics.StateUnreachable}},
		RTTState: winmetrics.StateError,
	}
	if got := fmtLatency(testChinese, unreachable, []string{"www.baidu.com"}); got != "超时" {
		t.Fatalf("timeout = %q", got)
	}
	if got := fmtLatency(testChinese, ok, nil); got != "—" {
		t.Fatalf("no targets = %q", got)
	}
}

// TestStaleHints covers the 15s metric / 45s RTT staleness windows.
func TestStaleHints(t *testing.T) {
	now := time.Now().UnixMilli()
	fresh := winmetrics.Snapshot{TS: now - 5*1000}
	if metricStale(fresh, now) {
		t.Fatal("fresh metric marked stale")
	}
	old := winmetrics.Snapshot{TS: now - 16*1000}
	if !metricStale(old, now) {
		t.Fatal("old metric not marked stale")
	}
	rtt := 19.0
	freshRTT := winmetrics.Snapshot{RTT: []winmetrics.RTTTarget{{Target: "a", TS: now - 40*1000, AvgMS: &rtt}}}
	if latencyStale(freshRTT, []string{"a"}, now) {
		t.Fatal("fresh RTT marked stale")
	}
	oldRTT := winmetrics.Snapshot{RTT: []winmetrics.RTTTarget{{Target: "a", TS: now - 46*1000, AvgMS: &rtt}}}
	if !latencyStale(oldRTT, []string{"a"}, now) {
		t.Fatal("old RTT not marked stale")
	}
}

func TestFormatting(t *testing.T) {
	if got := fmtWindow(30); got != "最近 30 秒" {
		t.Fatalf("window = %q", got)
	}
	if got := fmtWindow(300); got != "最近 5 分钟" {
		t.Fatalf("window = %q", got)
	}
	v := 1.2e9
	if got := fmtBytesShort(&v); got != "1.2 GB" {
		t.Fatalf("bytes short = %q", got)
	}
	w := 208e6
	if got := fmtBytesShort(&w); got != "208 MB" {
		t.Fatalf("bytes short integer = %q", got)
	}
	cpu := 24.0
	if got := fmtPercentInt(&cpu); got != "24%" {
		t.Fatalf("percent int = %q", got)
	}
	small := 0.4
	if got := fmtPercentInt(&small); got != "<1%" {
		t.Fatalf("sub-1%% percent = %q", got)
	}
	zero := 0.0
	if got := fmtPercentInt(&zero); got != "0%" {
		t.Fatalf("zero percent = %q", got)
	}
}

// TestBackfillAliases verifies alias back-fill never leaks into the model
// path: it is display-only.
func TestBackfillAliases(t *testing.T) {
	got := backfillAliases("进程 P01 与 P010 占用高", map[string]string{"P01": "chrome.exe", "P010": "teams.exe"})
	// Longest aliases first so P010 does not get corrupted by P01.
	want := "进程 chrome.exe 与 teams.exe 占用高"
	if got != want {
		t.Fatalf("backfill = %q, want %q", got, want)
	}
}

// stubMeasurer estimates text width: CJK runes cost 2 units, others 1.
func stubMeasurer() (func(string) int32, func(string) int32) {
	width := func(s string) int32 {
		w := int32(0)
		for _, r := range s {
			if r >= 0x2E80 && r <= 0x9FFF {
				w += 2
			} else {
				w++
			}
		}
		return w
	}
	return func(s string) int32 { return width(s) }, func(s string) int32 { return width(s) }
}

// TestChooseDiskRowDegradation verifies the measured fallback chain: full
// design copy → compact value → compact label → value alone, with the
// required gap, and never an empty value.
func TestChooseDiskRowDegradation(t *testing.T) {
	labelW, valueW := stubMeasurer()
	read, write := 2.4e6, 0.8e6

	// Wide enough: the design copy.
	copy := chooseDiskRow(testChinese, labelW, valueW, 60, 8, &read, &write, "")
	if copy.Label != "磁盘 I/O" || copy.Value != "读 2.4 · 写 0.8 MB/s" {
		t.Fatalf("wide = %q/%q", copy.Label, copy.Value)
	}
	// Narrow: compact value under the standard label.
	copy = chooseDiskRow(testChinese, labelW, valueW, 35, 8, &read, &write, "")
	if copy.Label != "磁盘 I/O" || copy.Value != "2.4 / 0.8 MB/s" {
		t.Fatalf("narrow = %q/%q", copy.Label, copy.Value)
	}
	// Narrower: compact label too.
	copy = chooseDiskRow(testChinese, labelW, valueW, 29, 8, &read, &write, "")
	if copy.Label != "读 / 写" || copy.Value != "2.4 / 0.8 MB/s" {
		t.Fatalf("narrower = %q/%q", copy.Label, copy.Value)
	}
	// Extremely narrow: value alone, never clipped.
	copy = chooseDiskRow(testChinese, labelW, valueW, 21, 8, &read, &write, "")
	if copy.Label != "" || copy.Value == "" {
		t.Fatalf("extreme = %q/%q", copy.Label, copy.Value)
	}
	// Degraded disk counters replace the value.
	copy = chooseDiskRow(testChinese, labelW, valueW, 60, 8, nil, nil, "磁盘计数不可用")
	if copy.Label != "磁盘 I/O" || copy.Value != "磁盘计数不可用" {
		t.Fatalf("error = %q/%q", copy.Label, copy.Value)
	}
	// Missing sides keep their — placeholders.
	copy = chooseDiskRow(testChinese, labelW, valueW, 60, 8, &read, nil, "")
	if copy.Value != "读 2.4 · 写 — MB/s" {
		t.Fatalf("missing side = %q", copy.Value)
	}
}

func TestFmtDiskPairCompact(t *testing.T) {
	read, write := 2.4e6, 0.8e6
	if got := fmtDiskPairCompact(&read, &write); got != "2.4 / 0.8 MB/s" {
		t.Fatalf("compact = %q", got)
	}
	if got := fmtDiskPairCompact(&read, nil); got != "2.4 / — MB/s" {
		t.Fatalf("read-only = %q", got)
	}
	if got := fmtDiskPairCompact(nil, nil); got != "—" {
		t.Fatalf("empty = %q", got)
	}
}

// TestPopupDiskErrorTextFits guards the popup degraded copy against the
// label collision across the acceptance DPI range.
func TestPopupDiskErrorTextFits(t *testing.T) {
	errText := "磁盘计数不可用"
	for _, dpi := range []uint32{96, 120, 144, 192} {
		m := scale(popupMargin, dpi)
		avail := scale(popupWDIP, dpi) - 2*m
		gap := scale(8, dpi)
		// Stub width: CJK runes ≈ font DIP each, Latin ≈ 0.55 DIP.
		textW := int32(0)
		for _, r := range errText {
			if r >= 0x2E80 && r <= 0x9FFF {
				textW += scale(1, dpi)
			} else {
				textW += scale(1, dpi) / 2
			}
		}
		labelW := int32(0)
		for _, r := range "磁盘 I/O" {
			if r >= 0x2E80 && r <= 0x9FFF {
				labelW += scale(1, dpi)
			} else {
				labelW += scale(1, dpi) / 2
			}
		}
		if labelW+gap+textW > avail {
			t.Fatalf("dpi %d: disk error text collides with label (label %d + gap %d + text %d > avail %d)", dpi, labelW, gap, textW, avail)
		}
	}
}

func TestEnglishOverviewUnitsAndProbeState(t *testing.T) {
	english := func(zh, en string) string { return en }
	read, write := 2.4e6, 0.8e6
	width := func(v string) int32 { return int32(len(v)) }
	row := chooseDiskRow(english, width, width, 52, 8, &read, &write, "")
	if row.Label != "Disk I/O" || row.Value != "Read 2.4 · Write 0.8 MB/s" {
		t.Fatalf("disk row: %+v", row)
	}
	snap := winmetrics.Snapshot{RTT: []winmetrics.RTTTarget{{Target: "example.invalid", State: winmetrics.StateUnreachable}}}
	if got := fmtLatency(english, snap, []string{"example.invalid"}); got != "Timeout" {
		t.Fatalf("latency: %q", got)
	}
}
