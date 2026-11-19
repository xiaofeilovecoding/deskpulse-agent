//go:build windows

package desktop

import (
	"syscall"
	"unsafe"
)

// windowBuffer is the per-window paint surface: a 32bpp top-down DIB at actual
// device pixels. All pages draw into this buffer and present once with BitBlt
// so the visible frame is never torn or partially composed. Software pixel
// work (the ball's anti-aliased circle) runs on pix and uploads via memCopy;
// the native bits address is only ever used as a syscall argument.
type windowBuffer struct {
	hdc, bmp, old uintptr
	bits          uintptr
	pix           []byte
	w, h          int32
	dpi           uint32
}

// bufferFor returns the window's current-size buffer, recreating it after a
// resize or DPI change. Buffers live in App.buffers, keyed by HWND.
func (a *App) bufferFor(hwnd uintptr) *windowBuffer {
	cr := clientRect(hwnd)
	dpi := dpiForWindow(hwnd)
	if dpi == 0 {
		dpi = systemDPI()
	}
	if a.captureDPI != 0 {
		dpi = a.captureDPI
	}
	if b := a.buffers[hwnd]; b != nil && b.w == cr.Right && b.h == cr.Bottom && b.dpi == dpi {
		return b
	}
	if b := a.buffers[hwnd]; b != nil {
		releaseBufferDC(b.hdc, b.bmp, b.old)
	}
	hdc, bmp, old, bits := createBufferDC(cr.Right, cr.Bottom)
	b := &windowBuffer{hdc: hdc, bmp: bmp, old: old, bits: bits, w: cr.Right, h: cr.Bottom, dpi: dpi}
	if n := int(cr.Right) * int(cr.Bottom) * 4; n > 0 {
		b.pix = make([]byte, n)
	}
	a.buffers[hwnd] = b
	return b
}

// upload copies the Go-side pixels onto the DIB (used after swCircle blends).
func (b *windowBuffer) upload() {
	if b.bits != 0 && len(b.pix) > 0 {
		memCopy(b.bits, goPtr(&b.pix[0]), uintptr(len(b.pix)))
	}
}

// download copies the DIB contents into a fresh Go buffer (capture path).
func (b *windowBuffer) download() []byte {
	if b.bits == 0 || len(b.pix) == 0 {
		return nil
	}
	out := make([]byte, len(b.pix))
	memCopy(goPtr(&out[0]), b.bits, uintptr(len(out)))
	return out
}

func (a *App) releaseBuffers() {
	for hwnd, b := range a.buffers {
		releaseBufferDC(b.hdc, b.bmp, b.old)
		delete(a.buffers, hwnd)
	}
}

// paintWindow renders one page through its buffer and presents it.
func (a *App) paintWindow(hwnd uintptr, draw func(b *windowBuffer)) {
	b := a.bufferFor(hwnd)
	if b == nil || b.hdc == 0 {
		return
	}
	draw(b)
	wdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&a.paintScratch)))
	if wdc != 0 {
		blit(wdc, rect{0, 0, b.w, b.h}, b.hdc)
		procEndPaint.Call(hwnd, wdc)
	}
}

// renderWindow runs the same draw path without a WM_PAINT cycle; used by the
// capture mode to export real program surfaces.
func (a *App) renderWindow(hwnd uintptr, draw func(b *windowBuffer)) *windowBuffer {
	b := a.bufferFor(hwnd)
	if b == nil || b.hdc == 0 {
		return nil
	}
	draw(b)
	return b
}

// fontSet is the per-DPI type ramp. Per the design: Latin titles and pure
// numeric readouts use Segoe UI (Semibold family), Chinese text uses
// Microsoft YaHei UI. Mixed-CJK readouts take the YaHei variant so CJK and
// Latin glyphs stay one size.
type fontSet struct {
	Title         uintptr // Segoe UI Semibold 17 — Latin titles (DeskPulse Agent)
	TitleCJK      uintptr // Microsoft YaHei UI 17 — CJK titles (AI 分析/进程/设置)
	Value         uintptr // Segoe UI Semibold 16 — Latin/numeric readouts
	ValueCJK      uintptr // Microsoft YaHei UI 16 — mixed-CJK readouts
	ValueSmall    uintptr // Segoe UI Semibold 14 — hover Latin/numeric
	ValueSmallCJK uintptr // Microsoft YaHei UI 14 — hover mixed-CJK
	Body          uintptr // Microsoft YaHei UI 14
	Small         uintptr // Microsoft YaHei UI 12
	Micro         uintptr // Segoe UI 12 — pure numeric columns
	DPI           uint32
}

const (
	faceLatin    = "Segoe UI Semibold"
	faceLatinReg = "Segoe UI"
	faceCJK      = "Microsoft YaHei UI"
)

func newFontSet(dpi uint32) *fontSet {
	return &fontSet{
		Title:         makeFont(dpi, 17, fwSemiBold, faceLatin),
		TitleCJK:      makeFont(dpi, 17, fwSemiBold, faceCJK),
		Value:         makeFont(dpi, 16, fwSemiBold, faceLatin),
		ValueCJK:      makeFont(dpi, 16, fwSemiBold, faceCJK),
		ValueSmall:    makeFont(dpi, 14, fwSemiBold, faceLatin),
		ValueSmallCJK: makeFont(dpi, 14, fwSemiBold, faceCJK),
		Body:          makeFont(dpi, 14, fwNormal, faceCJK),
		Small:         makeFont(dpi, 12, fwNormal, faceCJK),
		Micro:         makeFont(dpi, 12, fwNormal, faceLatinReg),
		DPI:           dpi,
	}
}

// hasCJK reports whether the string contains CJK ideographs, kana, Hangul or
// fullwidth punctuation, which require the YaHei variant for uniform glyphs.
func hasCJK(s string) bool {
	for _, r := range s {
		if (r >= 0x2E80 && r <= 0x9FFF) || (r >= 0xAC00 && r <= 0xD7AF) || (r >= 0xFF00 && r <= 0xFFEF) || (r >= 0x3000 && r <= 0x303F) {
			return true
		}
	}
	return false
}

// valueFont picks the readout font by content: CJK-containing strings take the
// YaHei variant, pure Latin/numeric strings take Segoe UI Semibold.
func (f *fontSet) valueFont(text string) uintptr {
	if hasCJK(text) {
		return f.ValueCJK
	}
	return f.Value
}

func (f *fontSet) valueSmallFont(text string) uintptr {
	if hasCJK(text) {
		return f.ValueSmallCJK
	}
	return f.ValueSmall
}

func (f *fontSet) delete() {
	deleteFont(f.Title)
	deleteFont(f.TitleCJK)
	deleteFont(f.Value)
	deleteFont(f.ValueCJK)
	deleteFont(f.ValueSmall)
	deleteFont(f.ValueSmallCJK)
	deleteFont(f.Body)
	deleteFont(f.Small)
	deleteFont(f.Micro)
}

// fontsFor caches font sets per DPI so pages share one ramp.
func (a *App) fontsFor(dpi uint32) *fontSet {
	if f := a.fonts[dpi]; f != nil {
		return f
	}
	f := newFontSet(dpi)
	a.fonts[dpi] = f
	return f
}

// measureText returns the single-line size of text in the given font.
func measureText(hdc uintptr, text string, font uintptr) (int32, int32) {
	if text == "" {
		return 0, 0
	}
	p, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return 0, 0
	}
	r := rect{0, 0, 0, 0}
	old, _, _ := procSelectObject.Call(hdc, font)
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(p)), ^uintptr(0), uintptr(unsafe.Pointer(&r)), uintptr(dtLeft|dtSingleLine|dtCalcrect|dtNoPrefix))
	if old != 0 {
		procSelectObject.Call(hdc, old)
	}
	return r.Right, r.Bottom
}

// drawTextIn is the themed wrapper for buffer DCs.
func drawTextIn(hdc uintptr, text string, r rect, font uintptr, color uint32, flags uint32) {
	drawText(hdc, text, r, font, color, flags)
}

// drawIconButton renders a Fluent icon centered in box, returning the rect
// actually used (for hit tests).
func iconRect(area rect, size int32) rect {
	w := area.Right - area.Left
	h := area.Bottom - area.Top
	if size > w {
		size = w
	}
	if size > h {
		size = h
	}
	return rect{
		Left:   area.Left + (w-size)/2,
		Top:    area.Top + (h-size)/2,
		Right:  area.Left + (w-size)/2 + size,
		Bottom: area.Top + (h-size)/2 + size,
	}
}
