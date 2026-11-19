//go:build windows

package desktop

import (
	"testing"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/winmetrics"
)

// perfTree builds a deterministic process snapshot with nRows rows across
// 50 parents so the process page can be exercised at the acceptance scale.
func perfTree(nRows int, ts int64) winmetrics.Snapshot {
	var rows []winmetrics.ProcessInfo
	parents := 50
	if nRows < parents {
		parents = nRows
	}
	perParent := (nRows - parents) / parents
	rest := (nRows - parents) - perParent*parents
	pid := int32(1)
	for p := 0; p < parents; p++ {
		kids := perParent
		if p < rest {
			kids++
		}
		cpu := float64(10 + p)
		ws := float64(100<<20) * float64(p+1)
		rows = append(rows, winmetrics.ProcessInfo{
			PID: pid, PPID: 0, Name: "parent.exe", StartMS: ts - 60000,
			CPU: &cpu, WorkingSet: &ws, TS: ts,
		})
		ppid := pid
		pid++
		for c := 0; c < kids; c++ {
			cc := float64(c + 1)
			cws := float64(10<<20) * float64(c+1)
			rows = append(rows, winmetrics.ProcessInfo{
				PID: pid, PPID: ppid, Name: "child.exe", StartMS: ts - 30000,
				CPU: &cc, WorkingSet: &cws, TS: ts,
			})
			pid++
		}
	}
	tree := winmetrics.BuildTree(rows)
	return winmetrics.Snapshot{
		TS: ts, Tree: tree, Processes: rows,
		ProcessState: winmetrics.StateOK,
		MetricState:  winmetrics.StateOK,
	}
}

func modelTestApp(t *testing.T, snap winmetrics.Snapshot) *App {
	t.Helper()
	return &App{
		source:       &fixedSource{snap: snap},
		theme:        uiTheme{},
		buffers:      map[uintptr]*windowBuffer{},
		fonts:        map[uint32]*fontSet{},
		procExpanded: map[procKey]bool{},
		procSortBy:   "cpu",
		procFocus:    -1,
		panelFocus:   -1,
		aiFocus:      -1,
	}
}

type fixedSource struct {
	snap winmetrics.Snapshot
}

func (s *fixedSource) Latest() winmetrics.Snapshot { return s.snap }

func TestProcModelDefaultCollapsed(t *testing.T) {
	ts := time.Now().UnixMilli()
	snap := perfTree(60, ts)
	a := modelTestApp(t, snap)
	if !a.refreshProcModel(true) {
		t.Fatal("first refresh must build the model")
	}
	if a.procModel == nil || len(a.procModel.Rows) == 0 {
		t.Fatal("no rows")
	}
	// Roots only, all collapsed by default.
	if len(a.procModel.Rows) != 50 {
		t.Fatalf("collapsed rows = %d, want 50 roots", len(a.procModel.Rows))
	}
	kids := 0
	for _, r := range a.procModel.Rows {
		if r.Expanded {
			t.Fatal("rows must default to collapsed")
		}
		if r.HasKids {
			kids++
		}
	}
	if kids != 10 {
		t.Fatalf("parents with children = %d, want 10", kids)
	}
}

func TestProcModelStableKeysAcrossPIDReuse(t *testing.T) {
	ts := time.Now().UnixMilli()
	snap := perfTree(20, ts)
	a := modelTestApp(t, snap)
	a.refreshProcModel(true)
	// PID reuse: same PID, later start time is a different identity.
	reused := snap.Tree.Roots[0].Proc
	reused.StartMS = ts + 1000
	cpu := 1.0
	ws := 10.0 * 1024 * 1024
	reused.CPU, reused.WorkingSet = &cpu, &ws
	snap2 := winmetrics.Snapshot{TS: ts + 2000, Tree: winmetrics.BuildTree([]winmetrics.ProcessInfo{reused}), ProcessState: winmetrics.StateOK}
	a.source = &fixedSource{snap: snap2}
	a.refreshProcModel(false)
	// The new identity starts collapsed; expansion never crosses identities.
	newKey := procKey{PID: reused.PID, Start: ts + 1000}
	if a.procExpanded[newKey] {
		t.Fatal("expansion must not cross process identity")
	}
	if got := a.procModel.Rows[0].Key; got != newKey {
		t.Fatalf("row key = %v, want %v", got, newKey)
	}
}

func TestProcModelSortSwitch(t *testing.T) {
	ts := time.Now().UnixMilli()
	snap := perfTree(40, ts)
	a := modelTestApp(t, snap)
	a.refreshProcModel(true)
	// CPU sort puts the highest-total parent first.
	if got := a.procModel.Rows[0].Key.PID; got == 0 {
		t.Fatal("no first row")
	}
	firstCPU := a.procModel.Rows[0]
	if firstCPU.CPU == nil || *firstCPU.CPU < 10 {
		t.Fatalf("cpu sort broken: %#v", firstCPU)
	}
	a.setProcSort("mem")
	if a.procModel.SortBy != "mem" {
		t.Fatal("sort not switched")
	}
	// The WS-highest parent (p=49) must come first under memory sort.
	if a.procModel.Rows[0].WS == nil {
		t.Fatal("no ws value")
	}
	if len(a.procModel.Rows) < 2 {
		t.Fatal("need rows")
	}
	for _, r := range a.procModel.Rows[1:] {
		if r.WS != nil && *r.WS > *a.procModel.Rows[0].WS {
			t.Fatalf("mem sort not descending: %#v vs %#v", a.procModel.Rows[0], r)
		}
	}
}

func TestProcModelScrollAnchorAndClamp(t *testing.T) {
	ts := time.Now().UnixMilli()
	snap := perfTree(100, ts)
	a := modelTestApp(t, snap)
	a.refreshProcModel(true)
	a.procScroll = 5
	topKey, ok := a.procTopRowKey()
	if !ok {
		t.Fatal("no top key")
	}
	// Next snapshot reorders children: the anchor keeps the same process on top.
	rows := append([]winmetrics.ProcessInfo(nil), snap.Processes...)
	for i := range rows {
		if rows[i].PPID != 0 {
			rows[i].CPU = floatPtr(int(rows[i].PID)%7 + 1)
		}
	}
	snap2 := winmetrics.Snapshot{TS: ts + 10000, Tree: winmetrics.BuildTree(rows), ProcessState: winmetrics.StateOK}
	a.source = &fixedSource{snap: snap2}
	a.refreshProcModel(false)
	if idx := findProcRow(a.procModel.Rows, topKey); idx != a.procScroll {
		// The anchored row should sit at the top after the refresh, clamped
		// to the valid range.
		t.Fatalf("anchor lost: idx=%d scroll=%d", idx, a.procScroll)
	}
	// Clamp: scroll beyond the end is impossible.
	a.procScroll = 100000
	a.clampProcScroll()
	visible := a.procVisibleRows()
	if max := len(a.procModel.Rows) - visible; a.procScroll != max && max >= 0 {
		t.Fatalf("scroll not clamped: %d (max %d)", a.procScroll, max)
	}
}

func floatPtr(v int) *float64 { f := float64(v); return &f }

func TestProcModelIdleDoesNotRebuild(t *testing.T) {
	ts := time.Now().UnixMilli()
	snap := perfTree(80, ts)
	a := modelTestApp(t, snap)
	a.refreshProcModel(true)
	rebuilds := a.procRebuilds
	// Idle ticks with an unchanged snapshot must not rebuild the tree.
	for i := 0; i < 10; i++ {
		if a.refreshProcModel(false) {
			t.Fatal("idle tick rebuilt the model")
		}
	}
	if a.procRebuilds != rebuilds {
		t.Fatalf("rebuilds changed: %d → %d", rebuilds, a.procRebuilds)
	}
}

func TestProcModelDeepHierarchyFullDepth(t *testing.T) {
	// A 7-level chain: rows deeper than the 5-level display cap still exist
	// and stay expandable.
	ts := time.Now().UnixMilli()
	var rows []winmetrics.ProcessInfo
	cpu := 1.0
	ws := 1 << 20
	for i := int32(1); i <= 8; i++ {
		ppid := i - 1
		if ppid == 0 {
			ppid = 0
		}
		c, w := cpu, float64(ws)
		rows = append(rows, winmetrics.ProcessInfo{PID: i, PPID: ppid, Name: "chain.exe", StartMS: ts - 1000*int64(9-i), CPU: &c, WorkingSet: &w, TS: ts})
	}
	snap := winmetrics.Snapshot{TS: ts, Tree: winmetrics.BuildTree(rows), ProcessState: winmetrics.StateOK}
	a := modelTestApp(t, snap)
	a.refreshProcModel(true)
	// Expand every level down to the last row.
	for guard := 0; guard < 12; guard++ {
		allExpanded := true
		for i, r := range a.procModel.Rows {
			if r.HasKids && !r.Expanded {
				a.toggleProcRow(i)
				allExpanded = false
				break
			}
		}
		if allExpanded {
			break
		}
	}
	if len(a.procModel.Rows) != 8 {
		t.Fatalf("deep rows = %d, want 8 (full hierarchy expandable)", len(a.procModel.Rows))
	}
	deepest := a.procModel.Rows[len(a.procModel.Rows)-1]
	if deepest.Depth != 7 {
		t.Fatalf("deepest depth = %d, want 7", deepest.Depth)
	}
}

func TestProcToggleParentClickOnlyWhenKids(t *testing.T) {
	ts := time.Now().UnixMilli()
	snap := perfTree(20, ts)
	a := modelTestApp(t, snap)
	a.refreshProcModel(true)
	leafIndex := -1
	for i, r := range a.procModel.Rows {
		if !r.HasKids {
			leafIndex = i
			break
		}
	}
	if leafIndex < 0 {
		t.Skip("no leaf rows")
	}
	before := a.procExpandRev
	a.toggleProcRow(leafIndex)
	if a.procExpandRev != before {
		t.Fatal("leaf rows must not toggle expansion")
	}
}
