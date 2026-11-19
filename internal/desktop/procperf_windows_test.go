//go:build windows

package desktop

import (
	"runtime"
	"sort"
	"testing"
	"time"
	"unsafe"
)

// TestProcPagePerf500Rows exercises the real native draw path. Cold paint
// includes initialization of Windows fonts and GDI objects; loaded rendering
// and interactions have separate limits as specified in the validation plan.
//   - every interaction class (keyboard, wheel, expand/collapse, snapshot
//     refresh) runs ≥20 times on a 500-row list; input-to-repaint p95 ≤ 50ms;
//   - refresh rounds interleave a click/selection/scroll so the anchor and
//     selection behavior is exercised while the snapshot changes;
//   - two latency boundaries are reported separately:
//     (a) 窗口过程内部: the handler + full buffered paint + BitBlt, called
//     directly (deterministic, excludes OS message-queue latency);
//     (b) 真实输入到渲染: a real WM_KEYDOWN posted to the window and pumped
//     through GetMessage/DispatchMessage until the repaint counter
//     increments — includes queue and scheduling latency;
//   - cold first paint ≤1.5s, loaded paint ≤120ms;
//   - idle: ≥10 seconds with zero model rebuilds and zero repaints.
func TestProcPagePerf500Rows(t *testing.T) {
	if testing.Short() {
		t.Skip("perf test skipped in -short")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initDesktop()
	if err := registerClass(moduleHandle()); err != nil {
		t.Log("class already registered:", err)
	}

	ts := time.Now().UnixMilli()
	base := perfTree(500, ts)
	a := modelTestApp(t, base)
	a.instance = moduleHandle()
	// The real message pump dispatches through the global activeApp; the test
	// must register itself or posted input never reaches handleProcKey.
	oldActive := activeApp
	activeApp = a
	defer func() { activeApp = oldActive }()

	hwnd, err := a.createProcPage()
	if err != nil {
		t.Skipf("cannot create process page in test session: %v", err)
	}
	defer destroyWindow(hwnd)
	moveWindow(hwnd, 8, 8, scale(procWDIP, 96), scale(procHDIP, 96))
	a.procUI = hwnd

	// First open → first paint.
	openStart := time.Now()
	a.refreshProcModel(true)
	a.paintProcPage()
	firstPaint := time.Since(openStart)
	if firstPaint > 1500*time.Millisecond {
		t.Errorf("cold first paint = %v, want ≤1.5s", firstPaint)
	}
	t.Logf("cold first paint: %v", firstPaint)
	loadedStart := time.Now()
	a.paintProcPage()
	if loadedPaint := time.Since(loadedStart); loadedPaint > 120*time.Millisecond {
		t.Errorf("loaded paint = %v, want ≤120ms", loadedPaint)
	}

	// Expand all parents so all 500 rows participate.
	for i := 0; i < len(a.procModel.Rows); i++ {
		if a.procModel.Rows[i].HasKids && !a.procModel.Rows[i].Expanded {
			a.toggleProcRow(i)
		}
	}
	if got := len(a.procModel.Rows); got != 500 {
		t.Fatalf("expanded rows = %d, want 500", got)
	}

	const perClass = 20
	type sample struct {
		kind string
		d    time.Duration
	}
	var samples []sample
	record := func(kind string, fn func()) {
		start := time.Now()
		fn()
		samples = append(samples, sample{kind, time.Since(start)})
	}
	wheelOnce := func() {
		a.procWheelRem += 120
		rows := a.procWheelRem / 120
		a.procWheelRem -= rows * 120
		if rows != 0 {
			a.procScroll += rows
			a.clampProcScroll()
		}
		a.paintProcPage()
	}
	refreshSource := func(i int) {
		a.source = &fixedSource{snap: perfTree(500, ts+int64(i+1)*10000)}
		a.refreshProcModel(false)
	}

	// (a) 窗口过程内部: ≥20 per class. Refresh rounds interleave a real
	// interaction (click/selection change/scroll) against the fresh snapshot
	// before the repaint, mirroring "刷新时点击、选中、滚动".
	for i := 0; i < perClass; i++ {
		record("keydown", func() { a.handleProcKey(0x28); a.paintProcPage() })
		record("wheel", func() { wheelOnce() })
		record("toggle", func() {
			idx := a.selectedProcIndex()
			if idx < 0 {
				idx = 0
			}
			if a.procModel.Rows[idx].HasKids {
				a.toggleProcRow(idx)
			} else {
				a.toggleProcRow(0)
			}
			a.paintProcPage()
		})
		record("refresh+interact", func() {
			refreshSource(i)
			switch i % 3 {
			case 0: // click a row of the fresh snapshot
				a.selectProcRow(minInt2(len(a.procModel.Rows)-1, a.procScroll+2))
			case 1: // move the selection across the reorder
				a.handleProcKey(0x28)
			case 2: // scroll against the fresh order
				a.procScroll += 3
				a.clampProcScroll()
			}
			a.paintProcPage()
		})
	}

	if len(samples) < perClass*4 {
		t.Fatalf("expected %d samples, got %d", perClass*4, len(samples))
	}
	durations := make([]time.Duration, 0, len(samples))
	for _, s := range samples {
		durations = append(durations, s.d)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[len(durations)*95/100]
	if p95 > 50*time.Millisecond {
		t.Errorf("(a) 窗口过程内部 input-to-repaint p95 = %v, want ≤50ms", p95)
	}
	var total time.Duration
	perKind := map[string]time.Duration{}
	count := map[string]int{}
	for _, s := range samples {
		total += s.d
		perKind[s.kind] += s.d
		count[s.kind]++
	}
	t.Logf("(a) 窗口过程内部: p95=%v mean=%v over %d interactions", p95, total/time.Duration(len(samples)), len(samples))
	for _, k := range []string{"keydown", "wheel", "toggle", "refresh+interact"} {
		t.Logf("(a)   %s: n=%d mean=%v", k, count[k], perKind[k]/time.Duration(count[k]))
	}

	// (b) 真实输入到渲染: post real WM_KEYDOWN messages to the window and
	// pump the message queue (non-blocking PeekMessage + sleep) until the
	// repaint counter moves. The window must be VISIBLE — Windows never
	// delivers WM_PAINT to hidden windows — and the selection starts at the
	// top so every alternating Down/Up key changes it and repaints. This
	// includes queue and scheduler latency; it is reported, not gated.
	const realInputRounds = 20
	showWindow(hwnd, swShowNoActivate)
	a.selectProcRow(0)
	a.paintProcPage()
	var realSamples []time.Duration
	before := a.procRepaints
	for i := 0; i < realInputRounds; i++ {
		vk := uintptr(0x28) // Down
		if i%2 == 1 {
			vk = 0x26 // Up
		}
		start := time.Now()
		if r1, _, _ := procPostMessageW.Call(hwnd, uintptr(wmKeyDown), vk, 0); r1 == 0 {
			t.Fatal("PostMessage failed")
		}
		deadline := time.Now().Add(time.Second)
		for a.procRepaints == before && time.Now().Before(deadline) {
			var m msg
			r1, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1) // PM_REMOVE
			if r1 != 0 {
				procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
				procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
			} else {
				time.Sleep(2 * time.Millisecond)
			}
		}
		if a.procRepaints == before {
			t.Fatal("real input produced no repaint within 1s")
		}
		before = a.procRepaints
		realSamples = append(realSamples, time.Since(start))
	}
	sort.Slice(realSamples, func(i, j int) bool { return realSamples[i] < realSamples[j] })
	realP95 := realSamples[len(realSamples)*95/100]
	t.Logf("(b) 真实输入到渲染: p95=%v mean=%v over %d posted keydowns (includes queue+scheduling)", realP95, mean(realSamples), len(realSamples))

	// Idle: ≥10 seconds with an unchanged snapshot must neither rebuild the
	// tree nor repaint.
	rebuilds, repaints := a.procRebuilds, a.procRepaints
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if a.refreshProcModel(false) {
			t.Fatal("idle refresh rebuilt the model")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if a.procRebuilds != rebuilds || a.procRepaints != repaints {
		t.Fatalf("idle activity: rebuilds %d→%d repaints %d→%d", rebuilds, a.procRebuilds, repaints, a.procRepaints)
	}
	t.Logf("idle 10s: rebuilds=%d repaints=%d (no growth)", a.procRebuilds, a.procRepaints)
}

func mean(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range ds {
		total += d
	}
	return total / time.Duration(len(ds))
}
