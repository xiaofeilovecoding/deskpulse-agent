//go:build windows

package desktop

import (
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/winmetrics"
)

// Fixed DIP footprint shared by the anchor, capture and scroll paths.
const (
	popupWDIP   = 336
	popupHDIP   = 326
	popupMargin = 20
)

type popupAction int

const (
	paNone popupAction = iota
	paSettings
	paAnalyze
	paProcess
)

type popupHit struct {
	Rect   rect
	Action popupAction
}

type popupLayout struct {
	Title, Gear, Instrument, Disk, Latency, Analyze, Processes rect
}

// scroll is a physical-pixel offset. Drawing and hit testing share these rectangles.
func popupLayoutFor(w int32, dpi uint32, scroll int32) popupLayout {
	s := func(v int) int32 { return scale(v, dpi) }
	m := s(popupMargin)
	row := func(top, bottom int) rect { return rect{m, s(top) - scroll, w - m, s(bottom) - scroll} }
	title := row(14, 54)
	gear := row(18, 50)
	gear.Left = gear.Right - s(32)
	primary, secondary := row(268, 308), row(268, 308)
	primary.Right = primary.Left + (primary.Right-primary.Left-s(8))*55/100
	secondary.Left = primary.Right + s(8)
	return popupLayout{Title: title, Gear: gear, Instrument: row(66, 170),
		Disk: row(180, 218), Latency: row(219, 257), Analyze: primary, Processes: secondary}
}

func (a *App) createPanel() (uintptr, error) {
	dpi := systemDPI()
	w, h := scale(popupWDIP, dpi), scale(popupHDIP, dpi)
	hwnd, err := createWindow(
		wsExToolWindow|wsExTopmost|wsExLayered,
		"DeskPulse Agent 概览",
		wsPopup,
		0, 0, w, h,
		0, 0, a.instance, 0,
	)
	if err != nil {
		return 0, err
	}
	a.panel = hwnd
	a.panelDPI = dpi
	setRoundRegion(hwnd, w, h, scale(10, dpi))
	return hwnd, nil
}

func (a *App) popupVisible() bool {
	return a.panel != 0 && isVisible(a.panel)
}

// openPopup shows the popup anchored at the ball with a one-shot 120ms
// fade-and-shift. Reduced-animation sessions skip straight to the final state.
func (a *App) openPopup() {
	if a.panel == 0 {
		return
	}
	a.hideHover()
	a.requestGPU(true)
	a.applyPopupFrame()
	a.popupScroll = 0
	a.panelFocus = -1
	invalidate(a.panel)
	// The popup must be the active window: Esc works, and clicking outside
	// raises WM_ACTIVATE/ WA_INACTIVE which closes it.
	if a.animAllowed() {
		a.popupAnimStep = 0
		setLayeredAlpha(a.panel, 0)
		showWindow(a.panel, swShow)
		procSetForegroundWindow.Call(a.panel)
		setFocus(a.panel)
		a.applyPopupAlpha()
		setTimer(a.panel, timerPopupAnim, 16)
	} else {
		setLayeredAlpha(a.panel, 255)
		showWindow(a.panel, swShow)
		procSetForegroundWindow.Call(a.panel)
		setFocus(a.panel)
	}
	setTimer(a.panel, timerPopupTick, 1000)
}

// animAllowed honors the system reduce-animation setting and remote sessions.
func (a *App) animAllowed() bool { return animationsAllowed() }

func (a *App) applyPopupFrame() {
	dpi := dpiForWindow(a.ball)
	if dpi == 0 {
		dpi = systemDPI()
	}
	w, h := scale(popupWDIP, dpi), scale(popupHDIP, dpi)
	work := workArea(a.ball)
	if maxH := work.Bottom - work.Top - scale(8, dpi); h > maxH {
		h = maxH // tiny work areas scroll the whole window content
	}
	x, y, _ := a.anchoredRect(w, h, dpi)
	moveWindow(a.panel, x, y, w, h)
	setRoundRegion(a.panel, w, h, scale(10, dpi))
}

func (a *App) applyPopupAlpha() {
	// 120ms total, five 16ms steps: fade in while shifting up 4 DIP.
	const steps = 7
	step := a.popupAnimStep
	alpha := uintptr(255 * (step + 1) / steps)
	if alpha > 255 {
		alpha = 255
	}
	setLayeredAlpha(a.panel, uint32(alpha))
	dpi := dpiForWindow(a.panel)
	invalidate(a.panel)
	_ = dpi
}

func setLayeredAlpha(hwnd uintptr, alpha uint32) {
	procSetLayeredWindowAttributes.Call(hwnd, 0, uintptr(alpha), uintptr(lwaAlpha))
}

func (a *App) closePopup() {
	if a.panel == 0 {
		return
	}
	killTimer(a.panel, timerPopupAnim)
	killTimer(a.panel, timerPopupTick)
	showWindow(a.panel, swHide)
	a.requestGPU(a.hoverShown)
}

func (a *App) togglePopup() {
	if a.popupVisible() {
		a.closePopup()
		return
	}
	a.openPopup()
}

func (a *App) handlePanel(message uint32, wparam, lparam uintptr) (bool, uintptr) {
	switch message {
	case wmPaint:
		a.paintPopup()
		return true, 0
	case wmEraseBkgnd:
		return true, 1
	case wmTimer:
		switch int(wparam) {
		case timerPopupTick:
			a.pollAI()
			invalidate(a.panel)
		case timerPopupAnim:
			a.popupAnimStep++
			if a.popupAnimStep >= 7 {
				killTimer(a.panel, timerPopupAnim)
				setLayeredAlpha(a.panel, 255)
				a.popupAnimStep = 0
				invalidate(a.panel)
			} else {
				a.applyPopupAlpha()
			}
		}
		return true, 0
	case wmActivate:
		// Clicking outside closes the popup. Opening a sub page hides the
		// popup first, so its own activation never triggers this path.
		if uint32(wparam)&0xffff == waInactive && a.popupVisible() {
			a.closePopup()
		}
		return true, 0
	case wmClose:
		a.closePopup()
		return true, 0
	case wmKeyDown:
		a.handlePopupKey(wparam)
		return true, 0
	case wmLButtonDown:
		if hwnd := getFocus(); hwnd == 0 || hwnd == a.panel {
			setFocus(a.panel)
		}
		return true, 0
	case wmLButtonUp:
		x, y := lparamPoint(lparam)
		a.handlePopupClick(x, y)
		return true, 0
	case wmMouseWheel:
		if limit := a.popupScrollMax(); limit > 0 {
			a.popupWheelRem += int(int16(wparam >> 16))
			steps := a.popupWheelRem / 120
			a.popupWheelRem %= 120
			a.popupScroll = max(0, min(limit, a.popupScroll-int32(steps)*scale(32, a.panelDPI)))
			invalidate(a.panel)
		}
		return true, 0
	case wmDpiChanged:
		dpi := uint32(wparam & 0xffff)
		if dpi != 0 {
			a.panelDPI = dpi
		}
		a.applyPopupFrame()
		invalidate(a.panel)
		return true, 0
	}
	return false, 0
}

func (a *App) popupScrollMax() int32 {
	b := a.buffers[a.panel]
	if b == nil {
		return 0
	}
	content := scale(popupHDIP, b.dpi)
	if b.h >= content {
		return 0
	}
	return content - b.h
}

func (a *App) handlePopupKey(vk uintptr) {
	if vk == 0x1B { // Escape
		a.closePopup()
		return
	}
	n := len(a.panelHits)
	if n == 0 {
		return
	}
	switch vk {
	case 0x09: // Tab
		keyState, _, _ := procGetKeyState.Call(0x10) // VK_SHIFT
		if keyState&0x8000 != 0 {
			a.panelFocus = (max(0, a.panelFocus) + n - 1) % n
		} else if a.panelFocus < 0 {
			a.panelFocus = 0
		} else {
			a.panelFocus = (a.panelFocus + 1) % n
		}
		r := a.panelHits[a.panelFocus].Rect
		height := clientRect(a.panel).Bottom
		pad := scale(3, a.panelDPI)
		if r.Top < pad {
			a.popupScroll = max(0, a.popupScroll+r.Top-pad)
		} else if r.Bottom > height-pad {
			a.popupScroll = min(a.popupScrollMax(), a.popupScroll+r.Bottom-height+pad)
		}
		invalidate(a.panel)
	case 0x0D, 0x20: // Enter/Space
		if a.panelFocus >= 0 && a.panelFocus < n {
			a.doPopupAction(a.panelHits[a.panelFocus].Action)
		}
	}
}

func (a *App) handlePopupClick(x, y int32) {
	for _, h := range a.panelHits {
		if pointIn(x, y, h.Rect) {
			a.doPopupAction(h.Action)
			return
		}
	}
}

func (a *App) doPopupAction(act popupAction) {
	switch act {
	case paSettings:
		a.closePopup()
		a.openSettings()
	case paAnalyze:
		a.closePopup()
		a.openLocalDiagnosis()
	case paProcess:
		a.closePopup()
		a.openProcPage()
	}
}

// paintPopup draws the four shared readouts, one primary action and one
// secondary action. Nothing else lives on the default page.
func (a *App) paintPopup() {
	a.paintWindow(a.panel, a.paintPopupInto)
}

func (a *App) paintPopupInto(b *windowBuffer) {
	dpi, t := b.dpi, a.theme
	f := a.fontsFor(dpi)
	s := func(v int) int32 { return scale(v, dpi) }
	l := popupLayoutFor(b.w, dpi, min(a.popupScroll, a.popupScrollMax()))
	a.panelHits = []popupHit{{l.Gear, paSettings}, {l.Analyze, paAnalyze}, {l.Processes, paProcess}}
	snap, cfg := a.source.Latest(), a.cfg.Get()
	now := a.nowMS()
	stale := metricStale(snap, now)
	const left = dtLeft | dtVCenter | dtSingleLine | dtNoPrefix
	const right = dtRight | dtVCenter | dtSingleLine | dtNoPrefix | dtEndEllipsis
	fillRect(b.hdc, rect{0, 0, b.w, b.h}, t.bg())

	mark := rect{l.Title.Left, l.Title.Top + s(3), l.Title.Left + s(34), l.Title.Top + s(37)}
	roundRect(b.hdc, mark, s(13), t.colors().instrument, t.colors().grid)
	drawIcon(b.hdc, "pulse", t.colors().cpuTrace, iconRect(mark, s(22)))
	name := rect{mark.Right + s(10), l.Title.Top, l.Gear.Left - s(8), l.Title.Top + s(23)}
	drawText(b.hdc, "DeskPulse", name, f.Title, t.text(), left)
	subtitle := rect{name.Left, name.Bottom, name.Right, l.Title.Bottom}
	drawText(b.hdc, a.tr("系统概览", "System overview"), subtitle, f.Small, t.muted(), left)
	if snap.GPU != nil && cfg.DisabledMetrics&settings.GPU == 0 && !stale {
		gpu := rect{l.Title.Left + s(172), l.Title.Top, l.Gear.Left - s(10), l.Title.Bottom}
		drawText(b.hdc, "GPU "+fmtPercentInt(snap.GPU), gpu, f.Small, t.muted(), right)
	}
	roundRect(b.hdc, l.Gear, s(10), t.panel(), t.line())
	drawIcon(b.hdc, "settings", t.muted(), iconRect(l.Gear, s(17)))

	colors := t.colors()
	card := l.Instrument
	roundRect(b.hdc, card, s(20), colors.instrument, colors.grid)
	mid := (card.Left + card.Right) / 2
	fillRect(b.hdc, rect{mid, card.Top + s(14), mid + s(1), card.Bottom - s(14)}, colors.grid)
	var memory *float64
	if snap.Memory != nil {
		memory = &snap.Memory.Percent
	}
	for _, metric := range []struct {
		x, end  int32
		label   string
		value   *float64
		samples []float64
		ink     uint32
	}{
		{card.Left, mid, "CPU", snap.CPU, snap.CPUHistory, colors.cpuTrace},
		{mid, card.Right, a.tr("内存", "Memory"), memory, snap.MemHistory, colors.memoryTrace},
	} {
		x, end := metric.x+s(15), metric.end-s(15)
		header := rect{x, card.Top + s(8), end, card.Top + s(30)}
		drawText(b.hdc, metric.label, header, f.Small, metric.ink, left)
		if stale && metric.value != nil {
			drawText(b.hdc, a.tr("上次值", "Last value"), header, f.Small, colors.onInstrument, right)
		}
		value := rect{x, card.Top + s(26), end, card.Top + s(68)}
		drawText(b.hdc, fmtPercentInt(metric.value), value, f.Display, colors.onInstrument, left)
		plot := rect{x, card.Top + s(68), end, card.Bottom - s(13)}
		fillRect(b.hdc, rect{plot.Left, plot.Bottom, plot.Right, plot.Bottom + s(1)}, colors.grid)
		if metric.value != nil {
			drawLoadHistory(b.hdc, plot, metric.samples, metric.ink, dpi)
		}
	}

	errText := ""
	diskInk := t.text()
	if snap.Disk.State == winmetrics.StateError {
		errText, diskInk = a.tr("计数不可用", "Counters unavailable"), t.amber()
	}
	latencyOld := latencyStale(snap, cfg.RTTTargets, now)
	// Put stale hints on a separate baseline so long rates and translated labels still fit.
	detail := func(r rect, label, value string, ink uint32, old bool) {
		labelR := r
		if old && value != "—" {
			labelR.Bottom -= s(13)
			hint := rect{r.Left, labelR.Bottom - s(2), r.Right, r.Bottom}
			drawText(b.hdc, a.tr("上次值", "Last value"), hint, f.Small, t.muted(), left)
		}
		drawText(b.hdc, label, labelR, f.Body, t.muted(), left)
		labelW, _ := measureText(b.hdc, label, f.Body)
		vr := r
		if label != "" {
			vr.Left += labelW + s(8)
		}
		drawText(b.hdc, value, vr, f.valueFont(value), ink, right)
	}
	disk := chooseDiskRow(a.tr, func(v string) int32 { w, _ := measureText(b.hdc, v, f.Body); return w },
		func(v string) int32 { w, _ := measureText(b.hdc, v, f.valueFont(v)); return w },
		l.Disk.Right-l.Disk.Left, s(8), snap.Disk.ReadBPS, snap.Disk.WriteBPS, errText)
	detail(l.Disk, disk.Label, disk.Value, diskInk, stale)
	fillRect(b.hdc, rect{l.Disk.Left, l.Disk.Bottom, l.Disk.Right, l.Disk.Bottom + s(1)}, t.line())
	latency := fmtLatency(a.tr, snap, cfg.RTTTargets)
	if len(cfg.RTTTargets) == 0 {
		latency = a.tr("未设置测点", "No target set")
	}
	detail(l.Latency, a.tr("延迟", "Latency"), latency, t.status(firstRTTState(snap, cfg.RTTTargets)), latencyOld)

	roundRect(b.hdc, l.Analyze, s(14), t.accent(), t.accent())
	drawActionLabel(b.hdc, l.Analyze, a.tr("本机诊断", "Local diagnosis"), "pulse", f.Body, t.accentText(), dpi)
	roundRect(b.hdc, l.Processes, s(14), t.panel(), t.line())
	drawActionLabel(b.hdc, l.Processes, a.tr("查看进程", "Processes"), "", f.Body, t.text(), dpi)
	if a.panelFocus >= 0 && a.panelFocus < len(a.panelHits) {
		a.focusBorder(b, a.panelHits[a.panelFocus].Rect, dpi)
	}
}

// A fixed percentage scale makes the two histories comparable. During warm-up
// the first samples enter from the right rather than stretching across the chart.
func drawLoadHistory(hdc uintptr, area rect, history []float64, ink uint32, dpi uint32) {
	const slots = 20
	recent := history[max(0, len(history)-slots):]
	if len(recent) < 2 {
		return
	}
	var points [slots]point
	for i, value := range recent {
		x := area.Right - 1 - int32(len(recent)-1-i)*(area.Right-area.Left-1)/(slots-1)
		y := area.Bottom - int32(max(0, min(100, value))*float64(area.Bottom-area.Top)/100)
		points[i] = point{x, y}
	}
	line(hdc, points[:len(recent)], ink, max(1, scale(2, dpi)))
}

func pointIn(x, y int32, r rect) bool {
	return x >= r.Left && x < r.Right && y >= r.Top && y < r.Bottom
}

func lparamPoint(lparam uintptr) (int32, int32) {
	x := int32(int16(uint16(lparam & 0xffff)))
	y := int32(int16(uint16((lparam >> 16) & 0xffff)))
	return x, y
}

// focusBorder draws a 2 DIP accessible focus outline just outside r.
func (a *App) focusBorder(b *windowBuffer, r rect, dpi uint32) {
	t := a.theme
	w := scale(2, dpi)
	top := rect{r.Left - w, r.Top - w, r.Right + w, r.Top}
	bottom := rect{r.Left - w, r.Bottom, r.Right + w, r.Bottom + w}
	left := rect{r.Left - w, r.Top, r.Left, r.Bottom}
	right := rect{r.Right, r.Top, r.Right + w, r.Bottom}
	for _, fr := range []rect{top, bottom, left, right} {
		fillRect(b.hdc, fr, t.accent())
	}
}
