//go:build windows

package desktop

import (
	"fmt"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/winmetrics"
)

// Popup geometry in DIP at 96 DPI (from the spec table): 336×326 window,
// 20 DIP margins, 40 DIP title row, four 39 DIP readout rows, a 40 DIP
// primary button and a single secondary action below it.
const (
	popupWDIP   = 336
	popupHDIP   = 326
	popupMargin = 20
	popupTitleH = 40
	popupRowH   = 39
	popupBtnH   = 40
	popupLinkH  = 30
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
	Width, Height int32
	DPI           uint32
	ContentH      int32
	Title         rect
	Gear          rect
	Rows          [4]rect
	AnalyzeBtn    rect
	ProcessLink   rect
	LinkIcon      rect
	LinkText      rect
}

func popupLayoutFor(w, h, dpi int32) popupLayout {
	s := func(v int) int32 { return scale(v, uint32(dpi)) }
	l := popupLayout{Width: w, Height: h, DPI: uint32(dpi)}
	m := s(popupMargin)
	l.Title = rect{m, s(20), w - m, s(20) + s(popupTitleH)}
	gearSize := s(28)
	l.Gear = rect{w - m - gearSize, s(20) + (s(popupTitleH)-gearSize)/2, w - m, s(20) + (s(popupTitleH)+gearSize)/2}
	y := s(20) + s(popupTitleH) + s(4)
	for i := range l.Rows {
		l.Rows[i] = rect{m, y, w - m, y + s(popupRowH)}
		y += s(popupRowH)
	}
	y += s(8)
	l.AnalyzeBtn = rect{m, y, w - m, y + s(popupBtnH)}
	y += s(popupBtnH) + s(4)
	l.ProcessLink = rect{m, y, w - m, y + s(popupLinkH)}
	// The chevron and text are centered together; measure the text at paint
	// time and place the icon left of it.
	l.ContentH = s(popupHDIP)
	return l
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
		pt := screenToClient(a.panel, cursorPos())
		delta := int16(wparam >> 16)
		if a.popupScrollMax() > 0 {
			a.popupWheelRem += int(delta)
			lines := a.popupWheelRem / 120 * 3
			a.popupWheelRem -= lines / 3 * 120
			if lines != 0 {
				a.popupScroll += int32(lines)
				if a.popupScroll < 0 {
					a.popupScroll = 0
				}
				if max := a.popupScrollMax(); a.popupScroll > max {
					a.popupScroll = max
				}
				invalidate(a.panel)
			}
			_ = pt
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
		if a.panelFocus < 0 {
			a.panelFocus = 0
		} else {
			a.panelFocus = (a.panelFocus + 1) % n
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

// paintPopupInto draws the four shared readouts, one primary action and one
// secondary action. Nothing else lives on the default page.
func (a *App) paintPopupInto(b *windowBuffer) {
	dpi := b.dpi
	f := a.fontsFor(dpi)
	t := a.theme
	fillRect(b.hdc, rect{0, 0, b.w, b.h}, t.bg())
	l := popupLayoutFor(b.w, b.h, int32(dpi))
	scroll := a.popupScroll
	if max := a.popupScrollMax(); scroll > max {
		scroll = max
	}
	shift := func(r rect) rect { return rect{r.Left, r.Top - scroll, r.Right, r.Bottom - scroll} }
	snap := a.source.Latest()
	targets := a.cfg.Get().RTTTargets
	nowMS := a.nowMS()
	stale := metricStale(snap, nowMS)

	// Title: waveform icon at the left edge of the title row, then the
	// name; settings gear at the right.
	iconBox := shift(l.Title)
	iconBox.Right = iconBox.Left + scale(24, dpi)
	drawIcon(b.hdc, "pulse", t.accent(), iconRect(iconBox, scale(20, dpi)))
	nameR := shift(l.Title)
	nameR.Left += scale(30, dpi)
	drawText(b.hdc, "DeskPulse Agent", nameR, f.Title, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
	gearFocus := a.popupFocusRect(paSettings, l)
	drawIcon(b.hdc, "settings", t.muted(), iconRect(shift(l.Gear), scale(18, dpi)))
	if gearFocus {
		a.focusBorder(b, shift(l.Gear), dpi)
	}

	values := [4]struct {
		label string
		text  string
		color uint32
	}{
		{"CPU", fmtPercentInt(snap.CPU), t.text()},
		{"内存", "—", t.text()},
		{"", "", t.text()},
		{"延迟", fmtLatency(snap, targets), t.status(firstRTTState(snap, targets))},
	}
	if snap.Memory != nil {
		values[1].text = fmt.Sprintf("%.0f%%", snap.Memory.Percent)
	}
	if stale {
		values[0].color = t.muted()
		values[1].color = t.muted()
	}
	if latencyStale(snap, targets, nowMS) {
		values[3].color = t.muted()
	}
	for i, v := range values {
		if i == 2 {
			continue // disk row is laid out below with measured fit
		}
		r := shift(l.Rows[i])
		drawText(b.hdc, v.label, r, f.Body, t.muted(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		vFont := f.valueFont(v.text)
		valueW, _ := measureText(b.hdc, v.text, vFont)
		vr := r
		vr.Right -= scale(2, dpi)
		drawText(b.hdc, v.text, vr, vFont, v.color, dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
		// 上次值 hint in muted small text left of the stale reading.
		if (stale && i != 3) || (i == 3 && latencyStale(snap, targets, nowMS)) {
			hr := vr
			hr.Right -= int32(valueW) + scale(6, dpi)
			drawText(b.hdc, "上次值", hr, f.Small, t.muted(), dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
		}
		if i < len(values)-1 {
			line := rect{r.Left, r.Bottom, r.Right, r.Bottom + 1}
			fillRect(b.hdc, line, t.line())
		}
	}

	// Disk row: choose a copy that fits the real widths with ≥8 DIP
	// between label and value — degraded text, large rates and missing
	// sides all degrade by copy, never by overlaying or clipping.
	rowColor := t.text()
	if stale {
		rowColor = t.muted()
	}
	errText := ""
	if snap.Disk.State == winmetrics.StateError {
		errText = "磁盘计数不可用"
		rowColor = t.amber()
	}
	m := scale(popupMargin, dpi)
	avail := b.w - 2*m
	disk := chooseDiskRow(
		func(s string) int32 { w, _ := measureText(b.hdc, s, f.Small); return int32(w) },
		func(s string) int32 { w, _ := measureText(b.hdc, s, f.valueFont(s)); return int32(w) },
		avail, scale(8, dpi),
		snap.Disk.ReadBPS, snap.Disk.WriteBPS, errText,
	)
	r2 := shift(l.Rows[2])
	if disk.Label != "" {
		drawText(b.hdc, disk.Label, r2, f.Body, t.muted(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
	}
	diskFont := f.valueFont(disk.Value)
	diskW, _ := measureText(b.hdc, disk.Value, diskFont)
	vr2 := r2
	vr2.Right -= scale(2, dpi)
	drawText(b.hdc, disk.Value, vr2, diskFont, rowColor, dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
	if stale {
		hr := vr2
		hr.Right -= int32(diskW) + scale(6, dpi)
		drawText(b.hdc, "上次值", hr, f.Small, t.muted(), dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
	}
	line2 := rect{r2.Left, r2.Bottom, r2.Right, r2.Bottom + 1}
	fillRect(b.hdc, line2, t.line())

	// Primary action: AI 分析.
	btn := shift(l.AnalyzeBtn)
	btnFocus := a.popupFocusRect(paAnalyze, l)
	fill := t.accent()
	if btnFocus {
		fill = t.accent()
	}
	roundRect(b.hdc, btn, scale(8, dpi), fill, fill)
	drawText(b.hdc, "AI 分析", btn, f.Body, t.accentText(), dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
	if btnFocus {
		inner := rect{btn.Left - 2, btn.Top - 2, btn.Right + 2, btn.Bottom + 2}
		frame := rect{inner.Left - 1, inner.Top - 1, inner.Right, inner.Top}
		fillRect(b.hdc, frame, t.text())
		frame = rect{inner.Left - 1, inner.Bottom - 1, inner.Right, inner.Bottom}
		fillRect(b.hdc, frame, t.text())
		frame = rect{inner.Left - 1, inner.Top, inner.Left, inner.Bottom}
		fillRect(b.hdc, frame, t.text())
		frame = rect{inner.Right - 1, inner.Top, inner.Right, inner.Bottom}
		fillRect(b.hdc, frame, t.text())
	}

	// Secondary action: 查看进程 with the chevron after the text.
	link := shift(l.ProcessLink)
	textW, _ := measureText(b.hdc, "查看进程", f.Body)
	iconW := scale(12, dpi)
	total := int32(textW) + scale(6, dpi) + iconW
	cx := (link.Left + link.Right) / 2
	l.LinkText = rect{cx - total/2, link.Top, cx - total/2 + int32(textW), link.Bottom}
	l.LinkIcon = rect{l.LinkText.Right + scale(6, dpi), link.Top + (link.Bottom-link.Top-iconW)/2, l.LinkText.Right + scale(6, dpi) + iconW, link.Top + (link.Bottom-link.Top+iconW)/2}
	linkFocus := a.popupFocusRect(paProcess, l)
	color := t.muted()
	if linkFocus {
		color = t.text()
	}
	drawIcon(b.hdc, "chevron-right", color, l.LinkIcon)
	drawText(b.hdc, "查看进程", l.LinkText, f.Body, color, dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
	if linkFocus {
		a.focusBorder(b, rect{cx - total/2 - scale(6, dpi), link.Top, cx + total/2 + scale(6, dpi), link.Bottom}, dpi)
	}

	// Hit rects are recomputed together with the frame they belong to.
	a.panelHits = []popupHit{
		{Rect: l.Gear, Action: paSettings},
		{Rect: l.AnalyzeBtn, Action: paAnalyze},
		{Rect: rect{cx - total/2 - scale(6, dpi), link.Top, cx + total/2 + scale(6, dpi), link.Bottom}, Action: paProcess},
	}
}

func (a *App) popupFocusRect(act popupAction, l popupLayout) bool {
	if a.panelFocus < 0 || a.panelFocus >= len(a.panelHits) {
		return false
	}
	return a.panelHits[a.panelFocus].Action == act
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
