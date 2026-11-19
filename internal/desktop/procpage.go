//go:build windows

package desktop

import (
	"fmt"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/winmetrics"
)

// Process page geometry in DIP (spec: ~504×420, header/footer, 34 DIP rows,
// 16 DIP indent per level, display indent capped at 5 levels).
const (
	procWDIP      = 504
	procHDIP      = 420
	procMargin    = 20
	procTitleH    = 40
	procHeaderH   = 32
	procRowH      = 34
	procIndentDIP = 16
	procMaxIndent = 5
	procMemColW   = 92
	procCPUColW   = 76
)

// procKey is the stable row identity across snapshots: PID plus process
// creation time, so PID reuse can never map an old row onto a new process.
type procKey struct {
	PID   int32
	Start int64
}

type procRow struct {
	Key      procKey
	Depth    int
	Name     string
	Children int
	HasKids  bool
	Expanded bool
	CPU      *float64
	WS       *float64
}

// procModel is the immutable cached row list the paint path reads. It is
// rebuilt only when the process snapshot version, the sort mode or the
// expansion revision changes — never during idle repaints.
type procModel struct {
	Rows   []procRow
	ProcTS int64
	SortBy string // "cpu" | "mem"
	Expand uint64
	Total  int
}

type procHeader int

const (
	phSortCPU procHeader = iota
	phSortMem
	procFocusCount
)

func (a *App) createProcPage() (uintptr, error) {
	dpi := systemDPI()
	w, h := scale(procWDIP, dpi), scale(procHDIP, dpi)
	hwnd, err := createWindow(
		wsExToolWindow|wsExTopmost,
		"DeskPulse Agent 进程",
		wsPopup,
		0, 0, w, h,
		0, 0, a.instance, 0,
	)
	if err != nil {
		return 0, err
	}
	a.procUI = hwnd
	setRoundRegion(hwnd, w, h, scale(10, dpi))
	return hwnd, nil
}

func (a *App) openProcPage() {
	if a.procUI == 0 {
		hwnd, err := a.createProcPage()
		if err != nil {
			a.logger.Warn("create process page failed", "error", err.Error())
			return
		}
		_ = hwnd
	}
	// First open builds the layout and model; hidden rebuilds never happen.
	a.refreshProcModel(true)
	a.applyProcFrame()
	a.procFocus = -1
	showWindow(a.procUI, swShow)
	procSetForegroundWindow.Call(a.procUI)
	setFocus(a.procUI)
	invalidate(a.procUI)
	setTimer(a.procUI, timerProcTick, 1000)
}

func (a *App) closeProcPage() {
	if a.procUI != 0 {
		killTimer(a.procUI, timerProcTick)
		showWindow(a.procUI, swHide)
	}
}

func (a *App) applyProcFrame() {
	dpi := dpiForWindow(a.ball)
	if dpi == 0 {
		dpi = systemDPI()
	}
	w, h := scale(procWDIP, dpi), scale(procHDIP, dpi)
	x, y, _ := a.anchoredRect(w, h, dpi)
	moveWindow(a.procUI, x, y, w, h)
	setRoundRegion(a.procUI, w, h, scale(10, dpi))
}

func (a *App) handleProcPage(message uint32, wparam, lparam uintptr) (bool, uintptr) {
	switch message {
	case wmPaint:
		a.paintProcPage()
		return true, 0
	case wmEraseBkgnd:
		return true, 1
	case wmTimer:
		if int(wparam) == timerProcTick {
			// Refresh sorting and rows from the newest snapshot; repaint only
			// when something changed.
			if a.refreshProcModel(false) {
				invalidate(a.procUI)
			}
		}
		return true, 0
	case wmClose:
		a.closeProcPage()
		return true, 0
	case wmKeyDown:
		a.handleProcKey(wparam)
		return true, 0
	case wmLButtonDown:
		if hwnd := getFocus(); hwnd == 0 || hwnd == a.procUI {
			setFocus(a.procUI)
		}
		return true, 0
	case wmLButtonUp:
		x, y := lparamPoint(lparam)
		a.handleProcClick(x, y)
		return true, 0
	case wmMouseWheel:
		delta := int16(wparam >> 16)
		a.procWheelRem += int(delta)
		// Keep the sub-notch remainder; smooth wheels never lose precision.
		rows := a.procWheelRem / 120 * 3
		a.procWheelRem -= rows / 3 * 120
		if rows != 0 {
			a.procScroll += rows
			a.clampProcScroll()
			invalidate(a.procUI)
		}
		return true, 0
	case wmDpiChanged:
		a.applyProcFrame()
		a.procModel = nil // rebuild with new metrics on next paint/tick
		invalidate(a.procUI)
		return true, 0
	}
	return false, 0
}

func (a *App) clampProcScroll() {
	m := a.procModel
	if m == nil {
		a.procScroll = 0
		return
	}
	visible := a.procVisibleRows()
	max := len(m.Rows) - visible
	if max < 0 {
		max = 0
	}
	if a.procScroll > max {
		a.procScroll = max
	}
	if a.procScroll < 0 {
		a.procScroll = 0
	}
}

func (a *App) procVisibleRows() int {
	dpi := a.procDPIFor()
	var winH int32 = scale(procHDIP, dpi)
	if b := a.buffers[a.procUI]; b != nil {
		dpi = b.dpi
		winH = b.h
	}
	// Match the paint layout: title, header, 4 DIP gap and the footer strip.
	area := winH - scale(procTitleH, dpi) - scale(procHeaderH, dpi) - scale(4, dpi) - scale(30, dpi)
	h := area / scale(procRowH, dpi)
	if h < 1 {
		h = 1
	}
	return int(h)
}

// procDPIFor returns the DPI the page actually paints with, preferring the
// paint buffer so hit rects and layout never mix DPIs (see dpiForAnalysis).
func (a *App) procDPIFor() uint32 {
	if b := a.buffers[a.procUI]; b != nil && b.dpi != 0 {
		return b.dpi
	}
	if a.captureDPI != 0 {
		return a.captureDPI
	}
	if a.procUI != 0 {
		if dpi := dpiForWindow(a.procUI); dpi != 0 {
			return dpi
		}
	}
	return systemDPI()
}

// refreshProcModel rebuilds the row list when the snapshot version, sort mode
// or expansion revision changed. It returns true when the model changed.
// force=true is used for the first open; the paint path never rebuilds.
func (a *App) refreshProcModel(force bool) bool {
	snap := a.source.Latest()
	if !force && a.procModel != nil && a.procModel.ProcTS == snap.TS &&
		a.procModel.SortBy == a.procSortBy && a.procModel.Expand == a.procExpandRev {
		return false
	}
	topKey, ok := a.procTopRowKey()
	m := buildProcModel(snap, a.procSortBy, a.procExpanded, a.procExpandRev)
	a.procModel = &m
	a.procRebuilds++
	if ok {
		// Keep the scroll anchored to the same process across snapshot
		// reordering; clamp when it disappeared.
		if idx := findProcRow(m.Rows, topKey); idx >= 0 {
			a.procScroll = idx
		}
	}
	a.clampProcScroll()
	return true
}

func (a *App) procTopRowKey() (procKey, bool) {
	if a.procModel == nil || a.procScroll < 0 || a.procScroll >= len(a.procModel.Rows) {
		return procKey{}, false
	}
	return a.procModel.Rows[a.procScroll].Key, true
}

func findProcRow(rows []procRow, key procKey) int {
	for i := range rows {
		if rows[i].Key == key {
			return i
		}
	}
	return -1
}

// buildProcModel walks the tree depth-first with the requested sort order.
// Rows beyond the display indent cap keep their full depth for expansion but
// are only drawn with capped indentation.
func buildProcModel(snap winmetrics.Snapshot, sortBy string, expanded map[procKey]bool, expandRev uint64) procModel {
	m := procModel{ProcTS: snap.TS, SortBy: sortBy, Expand: expandRev, Total: snap.Tree.Total}
	isExpanded := func(key procKey, hasKids bool) bool {
		if !hasKids {
			return false
		}
		return expanded[key] // absent = collapsed (default)
	}
	var walk func(nodes []*winmetrics.Node, depth int)
	walk = func(nodes []*winmetrics.Node, depth int) {
		sorted := sortNodeList(nodes, sortBy)
		for _, n := range sorted {
			key := procKey{PID: n.Proc.PID, Start: n.Proc.StartMS}
			hasKids := len(n.Children) > 0
			row := procRow{
				Key:      key,
				Depth:    depth,
				Name:     n.Proc.Name,
				Children: len(n.Children),
				HasKids:  hasKids,
				Expanded: isExpanded(key, hasKids),
			}
			if hasKids {
				row.CPU, row.WS = n.TotalCPU, n.TotalWS
			} else {
				row.CPU, row.WS = n.Proc.CPU, n.Proc.WorkingSet
			}
			m.Rows = append(m.Rows, row)
			if row.Expanded {
				walk(n.Children, depth+1)
			}
		}
	}
	walk(snap.Tree.Roots, 0)
	if len(snap.Tree.Orphans) > 0 {
		walk(snap.Tree.Orphans, 0)
	}
	return m
}

// sortNodeList returns a copy sorted by the requested column, highest first.
// The underlying tree nodes are never mutated.
func sortNodeList(nodes []*winmetrics.Node, sortBy string) []*winmetrics.Node {
	out := append([]*winmetrics.Node(nil), nodes...)
	less := func(a, b *winmetrics.Node) bool {
		av, bv := nodeSortValue(a, sortBy), nodeSortValue(b, sortBy)
		if av != bv {
			return av > bv
		}
		if a.Proc.Name != b.Proc.Name {
			return a.Proc.Name < b.Proc.Name
		}
		return a.Proc.PID < b.Proc.PID
	}
	// Insertion sort keeps equal keys stable without importing sort twice.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && less(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func nodeSortValue(n *winmetrics.Node, sortBy string) float64 {
	if sortBy == "mem" {
		if n.TotalWS != nil {
			return *n.TotalWS
		}
		if n.Proc.WorkingSet != nil {
			return *n.Proc.WorkingSet
		}
		return -1
	}
	if n.TotalCPU != nil {
		return *n.TotalCPU
	}
	if n.Proc.CPU != nil {
		return *n.Proc.CPU
	}
	return -1
}

func (a *App) toggleProcRow(row int) {
	if a.procModel == nil || row < 0 || row >= len(a.procModel.Rows) {
		return
	}
	r := a.procModel.Rows[row]
	if !r.HasKids {
		return
	}
	if a.procExpanded[r.Key] {
		delete(a.procExpanded, r.Key)
	} else {
		a.procExpanded[r.Key] = true
	}
	a.procExpandRev++
	a.refreshProcModel(true)
	invalidate(a.procUI)
}

func (a *App) selectProcRow(row int) {
	if a.procModel == nil || row < 0 || row >= len(a.procModel.Rows) {
		return
	}
	a.procSelected = a.procModel.Rows[row].Key
	a.procSelectedSet = true
	a.ensureProcRowVisible(row)
	invalidate(a.procUI)
}

func (a *App) ensureProcRowVisible(row int) {
	visible := a.procVisibleRows()
	if row < a.procScroll {
		a.procScroll = row
	}
	if row >= a.procScroll+visible {
		a.procScroll = row - visible + 1
	}
	a.clampProcScroll()
}

func (a *App) selectedProcIndex() int {
	if a.procModel == nil || !a.procSelectedSet {
		return -1
	}
	return findProcRow(a.procModel.Rows, a.procSelected)
}

func (a *App) handleProcKey(vk uintptr) {
	if vk == 0x1B { // Escape
		a.closeProcPage()
		return
	}
	switch vk {
	case 0x09: // Tab cycles the sort header focus
		a.procFocus++
		if a.procFocus >= int(procFocusCount) {
			a.procFocus = -1
		}
		invalidate(a.procUI)
	case 0x0D, 0x20: // Enter/Space activate the focused header
		if a.procFocus == int(phSortCPU) {
			a.setProcSort("cpu")
		} else if a.procFocus == int(phSortMem) {
			a.setProcSort("mem")
		}
	case 0x26: // Up
		if idx := a.selectedProcIndex(); idx > 0 {
			a.selectProcRow(idx - 1)
		} else if idx < 0 && a.procModel != nil && len(a.procModel.Rows) > 0 {
			a.selectProcRow(0)
		}
	case 0x28: // Down
		if idx := a.selectedProcIndex(); idx >= 0 && idx+1 < len(a.procModel.Rows) {
			a.selectProcRow(idx + 1)
		} else if idx < 0 && a.procModel != nil && len(a.procModel.Rows) > 0 {
			a.selectProcRow(0)
		}
	case 0x25: // Left: collapse, or jump to parent when collapsed
		if idx := a.selectedProcIndex(); idx >= 0 {
			r := a.procModel.Rows[idx]
			if r.Expanded {
				a.toggleProcRow(idx)
			} else if parent, ok := a.procParentOf(idx); ok {
				a.selectProcRow(parent)
			}
		}
	case 0x27: // Right: expand
		if idx := a.selectedProcIndex(); idx >= 0 && a.procModel.Rows[idx].HasKids && !a.procModel.Rows[idx].Expanded {
			a.toggleProcRow(idx)
		}
	case 0x24: // Home
		if a.procModel != nil && len(a.procModel.Rows) > 0 {
			a.selectProcRow(0)
		}
	case 0x23: // End
		if a.procModel != nil && len(a.procModel.Rows) > 0 {
			a.selectProcRow(len(a.procModel.Rows) - 1)
		}
	case 0x21: // PageUp
		if idx := a.selectedProcIndex(); idx > 0 {
			a.selectProcRow(maxInt2(0, idx-a.procVisibleRows()))
		}
	case 0x22: // PageDown
		if idx := a.selectedProcIndex(); idx >= 0 {
			a.selectProcRow(minInt2(len(a.procModel.Rows)-1, idx+a.procVisibleRows()))
		}
	}
}

func maxInt2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// procParentOf finds the nearest ancestor row of rows[idx] via the stable
// depth chain inside the current row list.
func (a *App) procParentOf(idx int) (int, bool) {
	if a.procModel == nil || idx <= 0 || idx >= len(a.procModel.Rows) {
		return 0, false
	}
	depth := a.procModel.Rows[idx].Depth
	for i := idx - 1; i >= 0; i-- {
		if a.procModel.Rows[i].Depth < depth {
			return i, true
		}
	}
	return 0, false
}

func (a *App) setProcSort(mode string) {
	if a.procSortBy == mode {
		return
	}
	a.procSortBy = mode
	a.refreshProcModel(true)
	invalidate(a.procUI)
}

func (a *App) handleProcClick(x, y int32) {
	b := a.buffers[a.procUI]
	if b == nil {
		return
	}
	dpi := b.dpi
	m := scale(procMargin, dpi)
	closeBox := rect{b.w - m - scale(28, dpi), scale(12, dpi), b.w - m, scale(12, dpi) + scale(28, dpi)}
	if pointIn(x, y, closeBox) {
		a.closeProcPage()
		return
	}
	headerTop := scale(procTitleH, dpi)
	if y >= headerTop && y < headerTop+scale(procHeaderH, dpi) {
		m := scale(procMargin, dpi)
		memRight := b.w - m
		memLeft := memRight - scale(procMemColW, dpi)
		cpuRight := memLeft - scale(8, dpi)
		cpuLeft := cpuRight - scale(procCPUColW, dpi)
		if x >= cpuLeft-scale(6, dpi) && x < cpuRight {
			a.setProcSort("cpu")
			return
		}
		if x >= memLeft-scale(6, dpi) && x < memRight {
			a.setProcSort("mem")
			return
		}
		return
	}
	for _, h := range a.procHits {
		if pointIn(x, y, h.Rect) {
			if h.Chevron {
				a.toggleProcRow(h.Row)
			} else {
				a.selectProcRow(h.Row)
			}
			return
		}
	}
}

type procRowHit struct {
	Rect    rect
	Row     int
	Chevron bool
}

// paintProcPage draws only the visible rows from the cached model. It never
// rebuilds the tree.
func (a *App) paintProcPage() {
	a.paintWindow(a.procUI, a.paintProcPageInto)
}

// paintProcPageInto draws only the visible rows from the cached model. It
// never rebuilds the tree.
func (a *App) paintProcPageInto(b *windowBuffer) {
	a.procRepaints++
	dpi := b.dpi
	f := a.fontsFor(dpi)
	t := a.theme
	fillRect(b.hdc, rect{0, 0, b.w, b.h}, t.bg())
	m := scale(procMargin, dpi)

	// Title + close button.
	titleR := rect{m, scale(12, dpi), b.w - m, scale(12, dpi) + scale(procTitleH-12, dpi)}
	drawText(b.hdc, "进程", titleR, f.TitleCJK, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
	closeBox := rect{b.w - m - scale(28, dpi), scale(12, dpi), b.w - m, scale(12, dpi) + scale(28, dpi)}
	drawIcon(b.hdc, "dismiss", t.muted(), iconRect(closeBox, scale(16, dpi)))
	if a.procCloseFocus() {
		a.focusBorder(b, closeBox, dpi)
	}

	// Column header with the active sort indicator.
	headerTop := scale(procTitleH, dpi)
	header := rect{m, headerTop, b.w - m, headerTop + scale(procHeaderH, dpi)}
	memRight := b.w - m
	memLeft := memRight - scale(procMemColW, dpi)
	cpuRight := memLeft - scale(8, dpi)
	cpuLeft := cpuRight - scale(procCPUColW, dpi)
	drawText(b.hdc, "进程名称", rect{header.Left, header.Top, header.Right, header.Bottom}, f.Small, t.muted(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
	cpuColor, memColor := t.muted(), t.muted()
	if a.procSortBy == "mem" {
		memColor = t.text()
	} else {
		cpuColor = t.text()
	}
	drawText(b.hdc, "CPU", rect{cpuLeft, header.Top, cpuRight, header.Bottom}, f.Small, cpuColor, dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
	drawText(b.hdc, "内存", rect{memLeft, header.Top, memRight, header.Bottom}, f.Small, memColor, dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
	if a.procSortBy == "cpu" || a.procSortBy == "" {
		arrow := rect{cpuRight + scale(2, dpi), header.Top + (header.Bottom-header.Top-scale(10, dpi))/2, cpuRight + scale(2, dpi) + scale(10, dpi), 0}
		arrow.Bottom = arrow.Top + scale(10, dpi)
		drawIcon(b.hdc, "chevron-down", t.muted(), arrow)
	} else {
		arrow := rect{memRight + scale(2, dpi), header.Top + (header.Bottom-header.Top-scale(10, dpi))/2, memRight + scale(2, dpi) + scale(10, dpi), 0}
		arrow.Bottom = arrow.Top + scale(10, dpi)
		drawIcon(b.hdc, "chevron-down", t.muted(), arrow)
	}
	hdrFocus := a.procHeaderFocusRect(b, header)
	if a.procFocus == int(phSortCPU) || a.procFocus == int(phSortMem) {
		a.focusBorder(b, hdrFocus, dpi)
	}
	sep := rect{header.Left, header.Bottom, header.Right, header.Bottom + 1}
	fillRect(b.hdc, sep, t.line())

	a.procHits = a.procHits[:0]
	areaTop := header.Bottom + scale(4, dpi)
	areaBottom := b.h - scale(30, dpi)
	visible := int((areaBottom - areaTop) / scale(procRowH, dpi))
	if visible < 1 {
		visible = 1
	}
	if a.procModel == nil || len(a.procModel.Rows) == 0 {
		text := "等待进程快照"
		if a.procModel != nil {
			text = "暂无进程数据"
		}
		drawText(b.hdc, text, rect{m, areaTop, b.w - m, areaBottom}, f.Body, t.muted(), dtLeft|dtCenter|dtSingleLine|dtNoPrefix)
	} else {
		a.clampProcScroll()
		start := a.procScroll
		end := start + visible
		if end > len(a.procModel.Rows) {
			end = len(a.procModel.Rows)
		}
		selIdx := a.selectedProcIndex()
		rowH := scale(procRowH, dpi)
		indentStep := scale(procIndentDIP, dpi)
		chevW := scale(16, dpi)
		for i := start; i < end; i++ {
			row := a.procModel.Rows[i]
			y := areaTop + int32(i-start)*rowH
			rr := rect{m, y, b.w - m, y + rowH}
			if i == selIdx {
				fillRect(b.hdc, rr, t.panel())
			}
			indent := int32(row.Depth)
			if indent > procMaxIndent {
				indent = procMaxIndent
			}
			indentPx := indent * indentStep
			if row.HasKids {
				cbx := rect{rr.Left + indentPx, rr.Top + (rowH-chevW)/2, rr.Left + indentPx + chevW, rr.Top + (rowH+chevW)/2}
				if row.Expanded {
					drawIcon(b.hdc, "chevron-down", t.muted(), cbx)
				} else {
					drawIcon(b.hdc, "chevron-right", t.muted(), cbx)
				}
			}
			nameX := rr.Left + indentPx + scale(20, dpi)
			name := row.Name
			if row.HasKids {
				name = fmt.Sprintf("%s · %d 个子进程", row.Name, row.Children)
			}
			nameR := rect{nameX, rr.Top, cpuLeft - scale(8, dpi), rr.Bottom}
			drawText(b.hdc, name, nameR, f.Body, t.text(), dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis|dtNoPrefix)
			drawText(b.hdc, fmtPercentInt(row.CPU), rect{cpuLeft, rr.Top, cpuRight, rr.Bottom}, f.Micro, t.text(), dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
			drawText(b.hdc, fmtBytesShort(row.WS), rect{memLeft, rr.Top, memRight, rr.Bottom}, f.Micro, t.muted(), dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
			// The chevron zone is hit-tested before the row so a click on
			// the arrow never just selects.
			if row.HasKids {
				chevRect := rect{rr.Left + indentPx, rr.Top, rr.Left + indentPx + scale(20, dpi), rr.Bottom}
				a.procHits = append(a.procHits, procRowHit{Rect: chevRect, Row: i, Chevron: true})
			}
			a.procHits = append(a.procHits, procRowHit{Rect: rr, Row: i})
		}
		// Scrollbar track/thumb only when content overflows.
		if total := len(a.procModel.Rows); total > visible {
			track := rect{b.w - scale(3, dpi), areaTop, b.w, areaBottom}
			fillRect(b.hdc, track, t.bg())
			thumbH := int32(int64(track.Bottom-track.Top) * int64(visible) / int64(total))
			if thumbH < scale(12, dpi) {
				thumbH = scale(12, dpi)
			}
			span := (track.Bottom - track.Top) - thumbH
			var thumbY int32 = track.Top
			if maxScroll := total - visible; maxScroll > 0 {
				thumbY += int32(int64(span) * int64(a.procScroll) / int64(maxScroll))
			}
			fillRect(b.hdc, rect{track.Left, thumbY, track.Right, thumbY + thumbH}, t.line())
		}
	}

	footer := rect{m, b.h - scale(26, dpi), b.w - m, b.h - scale(8, dpi)}
	drawText(b.hdc, "Windows 当前用户权限内的进程", footer, f.Small, t.muted(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
}

func (a *App) procCloseFocus() bool { return false }

func (a *App) procHeaderFocusRect(b *windowBuffer, header rect) rect {
	dpi := b.dpi
	memRight := b.w - scale(procMargin, dpi)
	memLeft := memRight - scale(procMemColW, dpi)
	cpuRight := memLeft - scale(8, dpi)
	cpuLeft := cpuRight - scale(procCPUColW, dpi)
	if a.procFocus == int(phSortCPU) {
		return rect{cpuLeft, header.Top, cpuRight, header.Bottom}
	}
	return rect{memLeft, header.Top, memRight, header.Bottom}
}
