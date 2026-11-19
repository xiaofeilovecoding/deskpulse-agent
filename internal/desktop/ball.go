//go:build windows

package desktop

import (
	"math"
	"unsafe"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/winmetrics"
)

// v0.3 timers. Pages only run timers while visible; the ball face is static
// and needs none.
const (
	timerBallHover = 1 // one-shot 300ms hover delay on the ball
	timerPopupTick = 2 // 1s popup refresh while visible
	timerProcTick  = 3 // 1s process-page refresh while visible
	timerAIPoll    = 4 // 500ms analysis poll while visible
	timerPopupAnim = 5 // 16ms popup open animation
	timerHoverTick = 6
	timerHoverExit = 7
	timerHoverAnim = 8
)

func (a *App) createBall() (uintptr, error) {
	dpi := systemDPI()
	size := scale(ballSizeDIP, dpi)
	hwnd, err := createWindow(
		wsExToolWindow|wsExTopmost|wsExNoActivate|wsExLayered,
		"DeskPulse Agent",
		wsPopup,
		0, 0, size, size,
		0, 0, a.instance, 0,
	)
	if err != nil {
		return 0, err
	}
	a.ball = hwnd
	a.ballDPI = dpi
	a.ballSize = size
	a.placeBallInitial()
	return hwnd, nil
}

const ballSizeDIP = 48

func (a *App) placeBallInitial() {
	work := workArea(a.ball)
	dpi := a.ballDPI
	if dpi == 0 {
		dpi = systemDPI()
	}
	a.ballDPI = dpi
	a.ballSize = scale(ballSizeDIP, dpi)
	pos := a.cfg.Get().BallPosition
	x, y := work.Right-a.ballSize-scale(16, dpi), work.Bottom-a.ballSize-scale(16, dpi)
	if pos.Set {
		x = scale(pos.X, dpi)
		y = scale(pos.Y, dpi)
	}
	x, y = clampBall(x, y, a.ballSize, work)
	moveWindow(a.ball, x, y, a.ballSize, a.ballSize)
}

func clampBall(x, y, size int32, work rect) (int32, int32) {
	if x < work.Left {
		x = work.Left
	}
	if y < work.Top {
		y = work.Top
	}
	if x+size > work.Right {
		x = work.Right - size
	}
	if y+size > work.Bottom {
		y = work.Bottom - size
	}
	return x, y
}

func (a *App) showBall() {
	if a.ball != 0 {
		a.paintBall()
		showWindow(a.ball, swShowNoActivate)
	}
}

func (a *App) saveBallPosition() {
	pos := windowRect(a.ball)
	dpi := a.ballDPI
	if dpi == 0 {
		dpi = 96
	}
	v := a.cfg.Get()
	v.BallPosition = settings.WindowPosition{
		X:   int(int64(pos.Left) * 96 / int64(dpi)),
		Y:   int(int64(pos.Top) * 96 / int64(dpi)),
		Set: true,
	}
	if err := a.cfg.Save(v); err != nil {
		a.logger.Warn("save ball position failed", "error", err.Error())
	}
}

func (a *App) handleBall(message uint32, wparam, lparam uintptr) (bool, uintptr) {
	switch message {
	case wmPaint:
		a.paintBall()
		return true, 0
	case wmEraseBkgnd:
		return true, 1
	case wmTimer:
		switch int(wparam) {
		case timerBallHover:
			// 300ms dwell reached: show the quick view.
			killTimer(a.ball, timerBallHover)
			a.showHover()
		}
		return true, 0
	case wmMouseMove:
		if a.ballDrag {
			a.hideHover()
			pt := cursorPos()
			dx, dy := pt.X-a.dragStart.X, pt.Y-a.dragStart.Y
			// Moving more than 6 DIP turns the press into a move; releasing
			// then only saves the position and never opens the popup.
			if !a.ballDragging && (abs32(dx) > scale(6, a.ballDPI) || abs32(dy) > scale(6, a.ballDPI)) {
				a.ballDragging = true
			}
			if a.ballDragging {
				work := workArea(a.ball)
				x, y := clampBall(a.dragOrigin.Left+dx, a.dragOrigin.Top+dy, a.ballSize, work)
				moveWindow(a.ball, x, y, a.ballSize, a.ballSize)
			}
			return true, 0
		}
		if !a.hoverShown {
			setTimer(a.ball, timerBallHover, 140)
		}
		var tme trackMouseEvent
		tme.Size = uint32(unsafe.Sizeof(tme))
		tme.Flags = tmeLeave
		tme.HwndTrack = a.ball
		procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
		return true, 0
	case wmLButtonDown:
		a.ballDrag = true
		a.ballDragging = false
		a.dragStart = cursorPos()
		a.dragOrigin = windowRect(a.ball)
		killTimer(a.ball, timerBallHover)
		a.hideHover()
		setCapture(a.ball)
		return true, 0
	case wmLButtonUp:
		releaseCapture()
		wasDrag := a.ballDragging
		a.ballDrag = false
		a.ballDragging = false
		if wasDrag {
			a.saveBallPosition()
			return true, 0
		}
		// A plain click toggles the main popup (from the quick view too).
		a.togglePopup()
		return true, 0
	case wmRButtonUp:
		a.showTrayMenu(a.ball)
		return true, 0
	case wmMouseLeave:
		killTimer(a.ball, timerBallHover)
		if !a.ballDrag && a.hoverShown {
			setTimer(a.hover, timerHoverExit, 250)
		}
		return true, 0
	case wmMouseActivate:
		return true, maNoActivate
	case wmClose:
		// The tray menu is the explicit quit path; an accidental Alt+F4 on the
		// ball must not leave the process without its tray icon.
		return true, 0
	case wmDpiChanged:
		dpi := uint32(wparam & 0xffff)
		if dpi == 0 {
			dpi = systemDPI()
		}
		a.onDPIChanged(dpi)
		a.ballDPI = dpi
		a.ballSize = scale(ballSizeDIP, dpi)
		pos := windowRect(a.ball)
		work := workArea(a.ball)
		x, y := clampBall(pos.Left, pos.Top, a.ballSize, work)
		moveWindow(a.ball, x, y, a.ballSize, a.ballSize)
		a.paintBall()
		return true, 0
	case wmAppTray:
		a.handleTrayCallback(lparam)
		return true, 0
	case wmAppPulse:
		if attention := wparam != 0; attention != a.attention {
			a.attention = attention
			a.paintBall()
		}
		return true, 0
	case wmAppCache:
		a.finishCacheWork()
		return true, 0
	}
	if a.taskbarMsg != 0 && message == a.taskbarMsg {
		// Explorer restarted and discarded every notification icon. NIM_ADD
		// must run again even though our local flag still says it was added.
		a.trayAdded = false
		_ = a.addTray()
		return true, 0
	}
	return false, 0
}

// onDPIChanged rebuilds every DPI-derived resource: buffers and icon bitmaps
// are re-created on the next paint, fonts are re-rasterized per DPI.
func (a *App) onDPIChanged(dpi uint32) {
	deleteIconCache()
	_ = dpi
}

func (a *App) paintBall() {
	a.paintLayered(a.ball, a.paintBallInto)
}

// paintBallInto draws the ball face into a buffer (paint path and capture).
func (a *App) paintBallInto(b *windowBuffer) {
	dpi := b.dpi
	fillRect(b.hdc, rect{0, 0, b.w, b.h}, a.theme.panel())
	cx := float64(b.w) / 2
	cy := float64(b.h) / 2
	r := min(cx, cy) - float64(scale(1, dpi))
	pixelScale := float64(dpi) / 96
	if a.attention {
		procGdiFlush.Call()
		if b.bits != 0 && len(b.pix) > 0 {
			memCopy(goPtr(&b.pix[0]), b.bits, uintptr(len(b.pix)))
			a.drawAttentionDot(b, cx+r*.58, cy-r*.58, pixelScale)
			b.upload()
		}
	}
	box := iconRect(rect{0, 0, b.w, b.h}, scale(29, dpi))
	drawIcon(b.hdc, "pulse", a.theme.accent(), box)
	b.finishPill(r, pixelScale, a.theme.muted())
}

func (a *App) drawAttentionDot(b *windowBuffer, x, y, scale float64) {
	swCircle(b.pix, b.w, b.h, x, y, 3.4*scale, a.theme.panel())
	swCircle(b.pix, b.w, b.h, x, y, 2.1*scale, a.theme.amber())
}

// anchoredRect positions a w×h page near the ball inside the ball's monitor
// work area, flipping up/left to stay on screen.
func (a *App) anchoredRect(w, h int32, dpi uint32) (int32, int32, rect) {
	work := workArea(a.ball)
	ball := windowRect(a.ball)
	y := ball.Top - h - scale(8, dpi)
	if y < work.Top {
		y = ball.Bottom + scale(8, dpi)
	}
	if y+h > work.Bottom {
		y = work.Bottom - h
	}
	if y < work.Top {
		y = work.Top
	}
	x := ball.Right - w
	if x < work.Left {
		x = ball.Left
	}
	if x+w > work.Right {
		x = work.Right - w
	}
	if x < work.Left {
		x = work.Left
	}
	return x, y, work
}

// createHover builds the floating metrics capsule.
func (a *App) createHover() (uintptr, error) {
	dpi := systemDPI()
	w, h := scale(hoverWDIP, dpi), scale(hoverHDIP, dpi)
	hwnd, err := createWindow(
		wsExToolWindow|wsExTopmost|wsExNoActivate|wsExLayered,
		"DeskPulse Agent 指标",
		wsPopup,
		0, 0, w, h,
		0, 0, a.instance, 0,
	)
	if err != nil {
		return 0, err
	}
	a.hover = hwnd
	return hwnd, nil
}

const (
	hoverHDIP      = 54
	hoverActionDIP = 56
	hoverBaseWDIP  = 72
	hoverMetricDIP = 66
	hoverWDIP      = hoverBaseWDIP + 4*hoverMetricDIP
)

var capsuleMetrics = [...]settings.MetricMask{settings.CPU, settings.Memory, settings.Disk, settings.Latency, settings.GPU}

func (a *App) metricsForCapsule() []settings.MetricMask {
	var visible []settings.MetricMask
	for _, metric := range capsuleMetrics {
		if a.hoverMask&metric == 0 && (metric != settings.GPU || a.hoverGPU) {
			visible = append(visible, metric)
		}
	}
	return visible
}

func (a *App) showHover() {
	if a.hoverShown || a.hover == 0 || a.ballDrag || a.popupVisible() {
		return
	}
	cfg := a.cfg.Get()
	a.hoverMask = cfg.HiddenMetrics | cfg.DisabledMetrics
	a.hoverGPU = a.source.Latest().GPUAvailable
	if len(a.metricsForCapsule()) == 0 {
		return
	}
	a.requestGPU(true)
	a.hoverStep = 10
	if animationsAllowed() {
		a.hoverStep = 0
	}
	a.positionHover()
	showWindow(a.hover, swShowNoActivate)
	a.hoverShown = true
	setTimer(a.hover, timerHoverTick, 5000)
	if a.hoverStep < 10 {
		setTimer(a.hover, timerHoverAnim, 15)
	}
	var track trackMouseEvent
	track.Size = uint32(unsafe.Sizeof(track))
	track.Flags = tmeLeave
	track.HwndTrack = a.hover
	procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&track)))
}

func (a *App) hideHover() {
	if !a.hoverShown {
		return
	}
	for _, timer := range []uintptr{timerHoverTick, timerHoverExit, timerHoverAnim} {
		killTimer(a.hover, timer)
	}
	showWindow(a.hover, swHide)
	a.hoverShown = false
	a.requestGPU(a.popupVisible())
}

func (a *App) capsuleOpensRight() bool {
	ball, work := windowRect(a.ball), workArea(a.ball)
	return ball.Left+ball.Right < work.Left+work.Right
}

func (a *App) positionHover() {
	dpi := dpiForWindow(a.ball)
	ball, work := windowRect(a.ball), workArea(a.ball)
	h := scale(hoverHDIP, dpi)
	fullWidth := scale(hoverBaseWDIP+hoverMetricDIP*len(a.metricsForCapsule()), dpi)
	if fullWidth > work.Right-work.Left {
		fullWidth = work.Right - work.Left
	}
	t := float64(a.hoverStep) / 10
	width := h + int32(float64(fullWidth-h)*(1-math.Pow(1-t, 3)))
	anchor := (ball.Left + ball.Right) / 2
	x := anchor + h/2 - width
	if a.capsuleOpensRight() {
		x = anchor - h/2
	}
	x = max(work.Left, min(x, work.Right-width))
	y := max(work.Top, min((ball.Top+ball.Bottom-h)/2, work.Bottom-h))
	moveWindow(a.hover, x, y, width, h)
	a.paintLayered(a.hover, a.paintHoverInto)
}

func (a *App) handleHover(message uint32, wparam, lparam uintptr) (bool, uintptr) {
	switch message {
	case wmPaint:
		a.paintLayered(a.hover, a.paintHoverInto)
		return true, 0
	case wmEraseBkgnd:
		return true, 1
	case wmTimer:
		switch wparam {
		case timerHoverTick:
			a.paintLayered(a.hover, a.paintHoverInto)
		case timerHoverExit:
			killTimer(a.hover, timerHoverExit)
			p, r := cursorPos(), windowRect(a.hover)
			if p.X < r.Left || p.X >= r.Right || p.Y < r.Top || p.Y >= r.Bottom {
				a.hideHover()
			}
		case timerHoverAnim:
			a.hoverStep++
			a.positionHover()
			if a.hoverStep >= 10 {
				killTimer(a.hover, timerHoverAnim)
			}
		}
		return true, 0
	case wmMouseMove:
		killTimer(a.hover, timerHoverExit)
		var track trackMouseEvent
		track.Size = uint32(unsafe.Sizeof(track))
		track.Flags = tmeLeave
		track.HwndTrack = a.hover
		procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&track)))
		return true, 0
	case wmMouseLeave:
		setTimer(a.hover, timerHoverExit, 250)
		return true, 0
	case wmLButtonUp:
		p := screenToClient(a.hover, cursorPos())
		b := clientRect(a.hover)
		edge := scale(hoverActionDIP, dpiForWindow(a.hover))
		agent := (!a.capsuleOpensRight() && p.X >= b.Right-edge) || (a.capsuleOpensRight() && p.X < edge)
		a.hideHover()
		if agent {
			a.openLocalDiagnosis()
		} else {
			a.openPopup()
		}
		return true, 0
	case wmRButtonUp:
		a.hideHover()
		a.showTrayMenu(a.ball)
		return true, 0
	case wmMouseActivate:
		return true, maNoActivate
	}
	return false, 0
}

func (a *App) paintHoverInto(b *windowBuffer) {
	t, dpi := a.theme, b.dpi
	f := a.fontsFor(dpi)
	fillRect(b.hdc, rect{0, 0, b.w, b.h}, t.panel())
	nativeScale := float64(dpi) / 96
	defer b.finishPill(float64(b.h)/2, nativeScale, t.muted())
	start, end, actionX := scale(7, dpi), b.w-scale(hoverActionDIP, dpi), b.w-b.h/2
	if a.capsuleOpensRight() {
		start, end, actionX = scale(hoverActionDIP, dpi), b.w-scale(7, dpi), b.h/2
	}
	seam := end
	if a.capsuleOpensRight() {
		seam = start
	}
	if end > start {
		line(b.hdc, []point{{seam, scale(12, dpi)}, {seam, b.h - scale(12, dpi)}}, t.line(), max(1, scale(1, dpi)))
	}
	iconSize := scale(23, dpi)
	actionY := (b.h - iconSize) / 2
	if end-start >= scale(40, dpi) {
		actionY = scale(5, dpi)
		drawText(b.hdc, a.tr("诊断", "Agent"), rect{actionX - b.h/2, scale(29, dpi), actionX + b.h/2, b.h - scale(3, dpi)}, f.Body, t.text(), dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
	}
	drawIcon(b.hdc, "pulse", t.accent(), rect{actionX - iconSize/2, actionY, actionX + iconSize/2, actionY + iconSize})
	mask := a.metricsForCapsule()
	if len(mask) == 0 || end-start < scale(40*len(mask), dpi) {
		return
	}
	snap := a.source.Latest()
	radius := scale(11, dpi)
	cy := scale(17, dpi)
	var rings [3]ringGlyph
	nrings := 0
	for i, key := range mask {
		left := start + int32(i)*(end-start)/int32(len(mask))
		right := start + int32(i+1)*(end-start)/int32(len(mask))
		cx := (left + right) / 2
		var label string
		switch key {
		case settings.CPU:
			label = "CPU"
			rings[nrings] = ringGlyph{cx, cy, snap.CPU, t.accent()}
			nrings++
		case settings.Memory:
			label = a.tr("内存", "RAM")
			var pct *float64
			if snap.Memory != nil {
				pct = &snap.Memory.Percent
			}
			rings[nrings] = ringGlyph{cx, cy, pct, t.accent()}
			nrings++
		case settings.GPU:
			label = "GPU"
			rings[nrings] = ringGlyph{cx, cy, snap.GPU, t.accent()}
			nrings++
		case settings.Disk:
			label = "I/O"
			if snap.Disk.ReadBPS == nil && snap.Disk.WriteBPS == nil {
				drawUnknown(b.hdc, cx, cy, dpi, t.muted())
			} else {
				line(b.hdc, []point{{cx - radius, cy}, {cx + radius, cy}}, t.muted(), max(1, scale(1, dpi)))
				capsuleBars(b.hdc, cx, cy, radius, max(2, scale(2, dpi)), t.accent(), snap.ReadHistory, snap.WriteHistory)
			}
		case settings.Latency:
			label = a.tr("延迟", "RTT")
			targets := a.cfg.Get().RTTTargets
			if a.opts.Synthetic && len(targets) == 0 && len(snap.RTT) > 0 {
				targets = []string{snap.RTT[0].Target}
			}
			state := firstRTTState(snap, targets)
			if state == winmetrics.StateOK || state == winmetrics.StatePartial {
				x1, y1 := scale(4, dpi), scale(2, dpi)
				line(b.hdc, []point{{cx - radius, cy + 2*y1}, {cx - x1, cy}, {cx, cy + y1}, {cx + x1, cy - 2*y1}, {cx + radius, cy - y1}}, t.accent(), max(2, scale(2, dpi)))
			} else if state != "" && state != winmetrics.StateNoTarget && state != winmetrics.StatePending {
				drawUnknown(b.hdc, cx, cy, dpi, t.amber())
			} else {
				drawUnknown(b.hdc, cx, cy, dpi, t.muted())
			}
		}
		drawText(b.hdc, label, rect{left, scale(29, dpi), right, b.h - scale(3, dpi)}, f.Body, t.text(), dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
	}
	if (nrings != 0 || a.attention) && len(b.pix) > 0 && b.bits != 0 {
		procGdiFlush.Call()
		memCopy(goPtr(&b.pix[0]), b.bits, uintptr(len(b.pix)))
		for _, glyph := range rings[:nrings] {
			drawRingGlyph(b, glyph, float64(radius), nativeScale, t.muted())
		}
		if a.attention {
			d := float64(scale(10, dpi))
			a.drawAttentionDot(b, float64(actionX)+d, float64(actionY)+d/2, nativeScale)
		}
		b.upload()
	}
}

func drawUnknown(hdc uintptr, cx, cy int32, dpi uint32, color uint32) {
	outer, inner := scale(8, dpi), scale(3, dpi)
	line(hdc, []point{{cx - outer, cy}, {cx - inner, cy}}, color, max(1, scale(1, dpi)))
	line(hdc, []point{{cx + inner, cy}, {cx + outer, cy}}, color, max(1, scale(1, dpi)))
}

type ringGlyph struct {
	x, y   int32
	value  *float64
	accent uint32
}

func drawRingGlyph(b *windowBuffer, glyph ringGlyph, radius, scale float64, base uint32) {
	window := int32(math.Ceil(radius + 2*scale))
	for y := max(0, glyph.y-window); y < min(b.h, glyph.y+window+1); y++ {
		for x := max(0, glyph.x-window); x < min(b.w, glyph.x+window+1); x++ {
			dx, dy := float64(x-glyph.x)+.5, float64(y-glyph.y)+.5
			distance := math.Abs(math.Hypot(dx, dy) - radius)
			theta := math.Atan2(dx, -dy) / (2 * math.Pi)
			if theta < 0 {
				theta++
			}
			color, halfWidth := base, .66*scale
			if glyph.value == nil {
				if int(theta*12)%2 != 0 {
					continue
				}
			} else if theta < max(0, min(100, *glyph.value))/100 {
				color, halfWidth = glyph.accent, 1.18*scale
			}
			alpha := max(0, min(1, halfWidth-distance+.5))
			if alpha == 0 {
				continue
			}
			offset := int((y*b.w + x) * 4)
			for i, component := range []byte{byte(color >> 16), byte(color >> 8), byte(color)} {
				b.pix[offset+i] = byte(float64(component)*alpha + float64(b.pix[offset+i])*(1-alpha))
			}
			b.pix[offset+3] = 255
		}
	}
}

func capsuleBars(hdc uintptr, x, y, r, thickness int32, accent uint32, read, write []float64) {
	peak := float64(512 * 1024)
	for _, seq := range [][]float64{read, write} {
		for _, sample := range seq[max(0, len(seq)-5):] {
			peak = max(peak, sample)
		}
	}
	for i, seq := range [][]float64{read, write} {
		seq = seq[max(0, len(seq)-5):]
		for j, sample := range seq {
			height := int32(float64(r) * sample / peak)
			if height <= 0 {
				continue
			}
			at := x - r + 2*r*int32(j)/5
			if i == 0 {
				line(hdc, []point{{at, y - 1}, {at, y - 1 - height}}, accent, thickness)
			} else {
				line(hdc, []point{{at, y + 1}, {at, y + 1 + height}}, rgb(158, 193, 237), thickness)
			}
		}
	}
}

// firstRTTState exposes the first target's state for coloring.
func firstRTTState(snap winmetrics.Snapshot, targets []string) string {
	for i := range snap.RTT {
		if equalFoldASCII(snap.RTT[i].Target, targets0(targets)) {
			return snap.RTT[i].State
		}
	}
	if snap.RTTState == winmetrics.StateNoTarget {
		return winmetrics.StateNoTarget
	}
	return ""
}

func targets0(t []string) string {
	if len(t) == 0 {
		return ""
	}
	return t[0]
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}
