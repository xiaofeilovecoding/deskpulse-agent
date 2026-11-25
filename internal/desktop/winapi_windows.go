//go:build windows

package desktop

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	msimg32  = syscall.NewLazyDLL("msimg32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")

	procInitCommonControls = comctl32.NewProc("InitCommonControls")

	procRegisterClassExW       = user32.NewProc("RegisterClassExW")
	procCreateWindowExW        = user32.NewProc("CreateWindowExW")
	procDefWindowProcW         = user32.NewProc("DefWindowProcW")
	procDestroyWindow          = user32.NewProc("DestroyWindow")
	procShowWindow             = user32.NewProc("ShowWindow")
	procUpdateWindow           = user32.NewProc("UpdateWindow")
	procGetMessageW            = user32.NewProc("GetMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessageW       = user32.NewProc("DispatchMessageW")
	procPostQuitMessage        = user32.NewProc("PostQuitMessage")
	procMoveWindow             = user32.NewProc("MoveWindow")
	procGetClientRect          = user32.NewProc("GetClientRect")
	procGetWindowRect          = user32.NewProc("GetWindowRect")
	procSetWindowRgn           = user32.NewProc("SetWindowRgn")
	procInvalidateRect         = user32.NewProc("InvalidateRect")
	procSetTimer               = user32.NewProc("SetTimer")
	procKillTimer              = user32.NewProc("KillTimer")
	procBeginPaint             = user32.NewProc("BeginPaint")
	procEndPaint               = user32.NewProc("EndPaint")
	procFillRect               = user32.NewProc("FillRect")
	procSetWindowTextW         = user32.NewProc("SetWindowTextW")
	procGetWindowTextW         = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW   = user32.NewProc("GetWindowTextLengthW")
	procSetFocus               = user32.NewProc("SetFocus")
	procGetKeyState            = user32.NewProc("GetKeyState")
	procGetFocus               = user32.NewProc("GetFocus")
	procLoadCursorW            = user32.NewProc("LoadCursorW")
	procLoadIconW              = user32.NewProc("LoadIconW")
	procMonitorFromWindow      = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW        = user32.NewProc("GetMonitorInfoW")
	procSystemParametersInfoW  = user32.NewProc("SystemParametersInfoW")
	procSetProcessDpiAwareness = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDpiForWindow        = user32.NewProc("GetDpiForWindow")
	procGetDpiForSystem        = user32.NewProc("GetDpiForSystem")
	procTrackMouseEvent        = user32.NewProc("TrackMouseEvent")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procPostMessageW           = user32.NewProc("PostMessageW")
	procPeekMessageW           = user32.NewProc("PeekMessageW")
	procSendMessageW           = user32.NewProc("SendMessageW")
	procMessageBoxW            = user32.NewProc("MessageBoxW")
	procFindWindowW            = user32.NewProc("FindWindowW")
	procRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	procSetWindowPos           = user32.NewProc("SetWindowPos")
	procIsWindowVisible        = user32.NewProc("IsWindowVisible")
	procIsDialogMessageW       = user32.NewProc("IsDialogMessageW")
	procCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	procAppendMenuW            = user32.NewProc("AppendMenuW")
	procTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	procDestroyMenu            = user32.NewProc("DestroyMenu")
	procGetCursorPos           = user32.NewProc("GetCursorPos")
	procSetCursorPos           = user32.NewProc("SetCursorPos")
	procSetCapture             = user32.NewProc("SetCapture")
	procReleaseCapture         = user32.NewProc("ReleaseCapture")
	procGetCapture             = user32.NewProc("GetCapture")
	procScreenToClient         = user32.NewProc("ScreenToClient")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procCreateRoundRectRgn     = gdi32.NewProc("CreateRoundRectRgn")
	procEllipse                = gdi32.NewProc("Ellipse")
	procRoundRect              = gdi32.NewProc("RoundRect")
	procPolyline               = gdi32.NewProc("Polyline")
	procSetBkMode              = gdi32.NewProc("SetBkMode")
	procSetTextColor           = gdi32.NewProc("SetTextColor")
	procSetBkColor             = gdi32.NewProc("SetBkColor")
	procDrawTextW              = user32.NewProc("DrawTextW")
	procCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	procCreatePen              = gdi32.NewProc("CreatePen")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procSaveDC                 = gdi32.NewProc("SaveDC")
	procRestoreDC              = gdi32.NewProc("RestoreDC")
	procIntersectClipRect      = gdi32.NewProc("IntersectClipRect")
	procCreateFontW            = gdi32.NewProc("CreateFontW")
	procShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
	procCreateMutexW           = kernel32.NewProc("CreateMutexW")
	procGetLastError           = kernel32.NewProc("GetLastError")
	procCloseHandle            = kernel32.NewProc("CloseHandle")
	procGlobalAlloc            = kernel32.NewProc("GlobalAlloc")
	procGlobalLock             = kernel32.NewProc("GlobalLock")
	procGlobalUnlock           = kernel32.NewProc("GlobalUnlock")
	procRtlMoveMemory          = kernel32.NewProc("RtlMoveMemory")

	procCreateCompatibleDC         = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC                   = gdi32.NewProc("DeleteDC")
	procCreateDIBSection           = gdi32.NewProc("CreateDIBSection")
	procBitBlt                     = gdi32.NewProc("BitBlt")
	procGdiFlush                   = gdi32.NewProc("GdiFlush")
	procAlphaBlend                 = msimg32.NewProc("AlphaBlend")
	procGetDC                      = user32.NewProc("GetDC")
	procReleaseDC                  = user32.NewProc("ReleaseDC")
	procPrintWindow                = user32.NewProc("PrintWindow")
	procGetWindowLongW             = user32.NewProc("GetWindowLongW")
	procGetClassNameW              = user32.NewProc("GetClassNameW")
	procSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	procUpdateLayeredWindow        = user32.NewProc("UpdateLayeredWindow")
	procValidateRect               = user32.NewProc("ValidateRect")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
)

// Common Win32 constants used by this desktop implementation.
const (
	wmDestroy         = 0x0002
	wmSize            = 0x0005
	wmMove            = 0x0003
	wmSetText         = 0x000C
	wmGetText         = 0x000D
	wmGetTextLength   = 0x000E
	wmPaint           = 0x000F
	wmClose           = 0x0010
	wmEraseBkgnd      = 0x0014
	wmKeyDown         = 0x0100
	wmCommand         = 0x0111
	wmTimer           = 0x0113
	wmMouseMove       = 0x0200
	wmLButtonDown     = 0x0201
	wmLButtonUp       = 0x0202
	wmRButtonUp       = 0x0205
	wmMouseActivate   = 0x0021
	wmMouseWheel      = 0x020A
	wmMouseLeave      = 0x02A3
	wmDpiChanged      = 0x02E0
	wmCtlColorEdit    = 0x0133
	wmCtlColorListBox = 0x0134
	wmCtlColorBtn     = 0x0135
	wmCtlColorStatic  = 0x0138
	wmApp             = 0x8000
	wmAppShow         = wmApp + 1
	wmAppTray         = wmApp + 2
	wmAppPulse        = wmApp + 12
	wmAppCache        = wmApp + 13
	wmSetFont         = 0x0030
	wmSetTextRaw      = 0x000C
	emSetSel          = 0x00B1

	wsPopup        = 0x80000000
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsClipChildren = 0x02000000
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsMinimize     = 0x00020000
	wsVScroll      = 0x00200000
	wsTabStop      = 0x00010000
	wsBorder       = 0x00800000
	wsOverlapped   = 0x00000000

	esMultiline   = 0x0004
	esAutoVScroll = 0x0040
	esAutoHScroll = 0x0080
	esReadOnly    = 0x0800
	esNumber      = 0x2000
	esPassword    = 0x0020
	esLeft        = 0x0000

	bsPush = 0x0000

	lbsNotify           = 0x00000001
	lbsNoIntegralHeight = 0x00000100

	wsExTopmost       = 0x00000008
	wsExToolWindow    = 0x00000080
	wsExAppWindow     = 0x00040000
	wsExNoActivate    = 0x08000000
	wsExComposited    = 0x02000000
	wsExControlParent = 0x00010000

	csHRedraw = 0x0002
	csVRedraw = 0x0001

	swHide           = 0
	swShow           = 5
	swShowNoActivate = 4

	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020
	swpShowWindow   = 0x0040

	hwndTop     = 0
	hwndTopmost = ^uintptr(0)

	dtLeft        = 0x0000
	dtCenter      = 0x0001
	dtRight       = 0x0002
	dtVCenter     = 0x0004
	dtWordBreak   = 0x0010
	dtSingleLine  = 0x0020
	dtNoPrefix    = 0x0800
	dtEndEllipsis = 0x8000
	dtNoClip      = 0x0100
	dtCalcrect    = 0x0400

	transparent    = 1
	fwNormal       = 400
	fwSemiBold     = 600
	fwBold         = 700
	cleartype      = 5
	defaultCharset = 1
	defaultPitch   = 0
	ffDontCare     = 0

	tmeLeave = 0x00000002

	monitorDefaultToNearest = 2
	spiGetWorkArea          = 0x0030
	smCXScreen              = 0
	smCYScreen              = 1

	idcArrow       = 32512
	idiApplication = 32512

	nimAdd     = 0x00000000
	nimDelete  = 0x00000002
	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	notifyID   = 1

	mfString       = 0x00000000
	mfSeparator    = 0x00000800
	mfChecked      = 0x00000008
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	mbOK              = 0x00000000
	mbIconError       = 0x00000010
	mbIconInformation = 0x00000040
	mbSetForeground   = 0x00010000

	wsExNoParentNotify = 0x00000004
	maNoActivate       = 3

	// v0.3 additions: buffered painting, layered fade-in, owner-draw buttons,
	// clipboard access and system accessibility queries.
	wmActivate      = 0x0006
	waInactive      = 0
	wmDrawItem      = 0x002B
	wmContextMenu   = 0x007B
	wmCtlColorList  = 0x0134
	odtButton       = 2
	odsSelected     = 0x0001
	odsFocus        = 0x0010
	odsDisabled     = 0x0004
	bsOwnerDraw     = 0x0000000B
	bmClick         = 0x00F5
	wsExLayered     = 0x00080000
	lwaAlpha        = 0x00000002
	ulwAlpha        = 0x00000002
	emSetCueBanner  = 0x1501
	lbGetItemRect   = 0x0198
	lbItemFromPoint = 0x01A9

	acsOver             = 0
	acSrcAlpha          = 1
	gmemMoveable        = 0x0002
	cfUnicodeText       = 13
	srCCopy             = 0x00CC0022
	smRemoteSession     = 0x1000
	pwRenderFullContent = 0x00000002
	gwlStyle            = ^uintptr(15) // GWL_STYLE = -16
	wmPrint             = 0x0317
	prfNonClient        = 0x00000002
	prfClient           = 0x00000004
	prfEraseBkgnd       = 0x00000008

	spiGetHighContrast        = 0x0042
	hcfHighContrastOn         = 0x00000001
	spiGetClientAreaAnimation = 0x1042

	biRGB        = 0
	dibRGBPixels = 0
)

var (
	windowProcCallback = syscall.NewCallback(windowProc)
	classAtom          uintptr
	activeApp          *App
)

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type point struct {
	X int32
	Y int32
}

type msg struct {
	HWND    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   syscall.Handle
	Icon       syscall.Handle
	Cursor     syscall.Handle
	Background syscall.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     syscall.Handle
}

type paintStruct struct {
	HDC       uintptr
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type trackMouseEvent struct {
	Size      uint32
	Flags     uint32
	HwndTrack uintptr
	HoverTime uint32
}

type notifyIconData struct {
	Size             uint32
	Wnd              uintptr
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUID             [16]byte
	BalloonIcon      uintptr
}

func initDesktop() {
	// Per-monitor DPI awareness v2. The constant is a pseudo-handle (-4).
	procSetProcessDpiAwareness.Call(^uintptr(3))
}

func systemDPI() uint32 {
	r1, _, _ := procGetDpiForSystem.Call()
	if r1 == 0 {
		return 96
	}
	return uint32(r1)
}

func dpiForWindow(hwnd uintptr) uint32 {
	r1, _, _ := procGetDpiForWindow.Call(hwnd)
	if r1 == 0 {
		return systemDPI()
	}
	return uint32(r1)
}

func scale(v int, dpi uint32) int32 {
	if dpi == 0 {
		dpi = 96
	}
	return int32((v*int(dpi) + 48) / 96)
}

func registerClass(instance syscall.Handle) error {
	name, err := syscall.UTF16PtrFromString(windowClassName)
	if err != nil {
		return err
	}
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	wc := wndClassEx{
		Style:     csHRedraw | csVRedraw,
		WndProc:   windowProcCallback,
		Instance:  instance,
		Cursor:    syscall.Handle(cursor),
		ClassName: name,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	atom, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		return fmt.Errorf("RegisterClassExW: %w", callError(e))
	}
	classAtom = atom
	return nil
}

func createWindow(exStyle uint32, title string, style uint32, x, y, w, h int32, parent uintptr, id uintptr, instance syscall.Handle, param uintptr) (uintptr, error) {
	classPtr, err := syscall.UTF16PtrFromString(windowClassName)
	if err != nil {
		return 0, err
	}
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return 0, err
	}
	r1, _, e := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(classPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, id, uintptr(instance), param,
	)
	if r1 == 0 {
		return 0, fmt.Errorf("CreateWindowExW(%s): %w", title, callError(e))
	}
	return r1, nil
}

func createControl(class, text string, style, exStyle uint32, x, y, w, h int32, parent uintptr, id int32, instance syscall.Handle) (uintptr, error) {
	classPtr, err := syscall.UTF16PtrFromString(class)
	if err != nil {
		return 0, err
	}
	textPtr, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return 0, err
	}
	r1, _, e := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(classPtr)),
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, uintptr(id), uintptr(instance), 0,
	)
	if r1 == 0 {
		return 0, fmt.Errorf("create %s control: %w", class, callError(e))
	}
	return r1, nil
}

func defWindowProc(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	r1, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wparam, lparam)
	return r1
}

func callError(e error) error {
	if e == nil {
		code, _, _ := procGetLastError.Call()
		if code == 0 {
			return fmt.Errorf("Win32 call failed")
		}
		return fmt.Errorf("Win32 error %d", code)
	}
	if errno, ok := e.(syscall.Errno); ok && errno == 0 {
		code, _, _ := procGetLastError.Call()
		if code == 0 {
			return fmt.Errorf("Win32 call failed")
		}
		return fmt.Errorf("Win32 error %d", code)
	}
	return e
}

func moduleHandle() syscall.Handle {
	r1, _, _ := procGetModuleHandleW.Call(0)
	return syscall.Handle(r1)
}

func showWindow(hwnd uintptr, cmd int32) {
	procShowWindow.Call(hwnd, uintptr(cmd))
}

func destroyWindow(hwnd uintptr) {
	if hwnd != 0 {
		procDestroyWindow.Call(hwnd)
	}
}

func invalidate(hwnd uintptr) {
	procInvalidateRect.Call(hwnd, 0, 0)
}

func setText(hwnd uintptr, text string) {
	p, _ := syscall.UTF16PtrFromString(text)
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(p)))
}

func getText(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, int(n)+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func clientRect(hwnd uintptr) rect {
	var r rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func paintBackground(hwnd uintptr, color uint32) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	fillRect(hdc, clientRect(hwnd), color)
}

func windowRect(hwnd uintptr) rect {
	var r rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func moveWindow(hwnd uintptr, x, y, w, h int32) {
	procMoveWindow.Call(hwnd, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 1)
}

func setWindowPos(hwnd uintptr, after uintptr, x, y, w, h int32, flags uint32) {
	procSetWindowPos.Call(hwnd, after, uintptr(x), uintptr(y), uintptr(w), uintptr(h), uintptr(flags))
}

func isVisible(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	r1, _, _ := procIsWindowVisible.Call(hwnd)
	return r1 != 0
}

func setCapture(hwnd uintptr) { procSetCapture.Call(hwnd) }
func releaseCapture()         { procReleaseCapture.Call() }

func cursorPos() point {
	var p point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return p
}

func screenToClient(hwnd uintptr, p point) point {
	procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&p)))
	return p
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func setFocus(hwnd uintptr) { procSetFocus.Call(hwnd) }

func getFocus() uintptr {
	r1, _, _ := procGetFocus.Call()
	return r1
}

func setTimer(hwnd uintptr, id uintptr, ms uint32) {
	procSetTimer.Call(hwnd, id, uintptr(ms), 0)
}

func killTimer(hwnd uintptr, id uintptr) {
	procKillTimer.Call(hwnd, id)
}

func rgb(r, g, b byte) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

func fillRect(hdc uintptr, r rect, color uint32) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(color))
	if brush != 0 {
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), brush)
		procDeleteObject.Call(brush)
	}
}

func drawText(hdc uintptr, text string, r rect, font uintptr, color uint32, flags uint32) {
	if text == "" {
		return
	}
	p, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	oldFont, _, _ := procSelectObject.Call(hdc, font)
	procSetTextColor.Call(hdc, uintptr(color))
	procSetBkMode.Call(hdc, transparent)
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(p)), ^uintptr(0), uintptr(unsafe.Pointer(&r)), uintptr(flags))
	if oldFont != 0 {
		procSelectObject.Call(hdc, oldFont)
	}
}

func makeFont(dpi uint32, sizeDIP int, weight int, face string) uintptr {
	facePtr, _ := syscall.UTF16PtrFromString(face)
	h := -int32(float64(sizeDIP) * float64(dpi) / 96.0)
	r1, _, _ := procCreateFontW.Call(
		uintptr(h), 0, 0, 0,
		uintptr(weight),
		0, 0, 0,
		defaultCharset,
		0, 0, cleartype,
		uintptr(defaultPitch|ffDontCare),
		uintptr(unsafe.Pointer(facePtr)),
	)
	return r1
}

func deleteFont(font uintptr) {
	if font != 0 {
		procDeleteObject.Call(font)
	}
}

func line(hdc uintptr, points []point, color uint32, width int32) {
	if len(points) < 2 {
		return
	}
	pen, _, _ := procCreatePen.Call(0, uintptr(width), uintptr(color))
	if pen == 0 {
		return
	}
	old, _, _ := procSelectObject.Call(hdc, pen)
	procPolyline.Call(hdc, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
	if old != 0 {
		procSelectObject.Call(hdc, old)
	}
	procDeleteObject.Call(pen)
}

func roundRect(hdc uintptr, r rect, radius int32, fill, border uint32) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(fill))
	pen, _, _ := procCreatePen.Call(0, 1, uintptr(border))
	oldB, _, _ := procSelectObject.Call(hdc, brush)
	oldP, _, _ := procSelectObject.Call(hdc, pen)
	procRoundRect.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom), uintptr(radius), uintptr(radius))
	if oldB != 0 {
		procSelectObject.Call(hdc, oldB)
	}
	if oldP != 0 {
		procSelectObject.Call(hdc, oldP)
	}
	procDeleteObject.Call(brush)
	procDeleteObject.Call(pen)
}

func ellipse(hdc uintptr, r rect, fill, border uint32) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(fill))
	pen, _, _ := procCreatePen.Call(0, 2, uintptr(border))
	oldB, _, _ := procSelectObject.Call(hdc, brush)
	oldP, _, _ := procSelectObject.Call(hdc, pen)
	procEllipse.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
	if oldB != 0 {
		procSelectObject.Call(hdc, oldB)
	}
	if oldP != 0 {
		procSelectObject.Call(hdc, oldP)
	}
	procDeleteObject.Call(brush)
	procDeleteObject.Call(pen)
}

func setRoundRegion(hwnd uintptr, width, height, radius int32) {
	rgn, _, _ := procCreateRoundRectRgn.Call(0, 0, uintptr(width+1), uintptr(height+1), uintptr(radius), uintptr(radius))
	if rgn != 0 {
		procSetWindowRgn.Call(hwnd, rgn, 1)
	}
}

func workArea(hwnd uintptr) rect {
	var mi monitorInfo
	mi.Size = uint32(unsafe.Sizeof(mi))
	mon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if mon != 0 {
		if r1, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r1 != 0 {
			return mi.Work
		}
	}
	var r rect
	if r1, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); r1 != 0 {
		return r
	}
	return rect{0, 0, 1920, 1080}
}

func copyUTF16(dst []uint16, s string) {
	u := syscall.StringToUTF16(s)
	if len(u) > 0 {
		u = u[:len(u)-1]
	}
	n := copy(dst, u)
	if n < len(dst) {
		dst[n] = 0
	}
}

func showMessage(title, text string, flags uint32) {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), uintptr(flags))
}

// bitmapInfoHeader is the 32bpp top-down DIB header used by the per-window
// paint buffers and the icon rasterizer.
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32 // negative for top-down rows
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

// memCopy transfers memory between native addresses and Go buffers without
// any uintptr→pointer conversion (go vet clean).
func memCopy(dst, src, n uintptr) {
	if n == 0 {
		return
	}
	syscall.SyscallN(procRtlMoveMemory.Addr(), dst, src, n)
}

func goPtr[T any](p *T) uintptr {
	return uintptr(unsafe.Pointer(p))
}

// createBufferDC builds a compatible DC with a 32bpp top-down DIB selected
// into it. bits is the DIB bits address; use memCopy to move pixels in and
// out, never convert it to a Go pointer.
func createBufferDC(w, h int32) (hdc, bmp, old, bits uintptr) {
	var bi bitmapInfo
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.Width = w
	bi.Header.Height = -h // top-down
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	bi.Header.Compression = biRGB
	compat, _, _ := procCreateCompatibleDC.Call(0)
	if compat == 0 {
		return 0, 0, 0, 0
	}
	bmp, _, _ = procCreateDIBSection.Call(compat, uintptr(unsafe.Pointer(&bi)), uintptr(dibRGBPixels), uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		procDeleteDC.Call(compat)
		return 0, 0, 0, 0
	}
	old, _, _ = procSelectObject.Call(compat, bmp)
	return compat, bmp, old, bits
}

func releaseBufferDC(hdc, bmp, old uintptr) {
	if old != 0 {
		procSelectObject.Call(hdc, old)
	}
	if bmp != 0 {
		procDeleteObject.Call(bmp)
	}
	if hdc != 0 {
		procDeleteDC.Call(hdc)
	}
}

func blit(hdcDest uintptr, r rect, hdcSrc uintptr) {
	procBitBlt.Call(hdcDest, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), hdcSrc, 0, 0, uintptr(srCCopy))
}

// blendIcon copies a premultiplied 32bpp source DIB onto dest with per-pixel
// alpha. The source DC must have the icon bitmap selected; AC_SRC_ALPHA is
// required or fully transparent pixels render as black squares.
func blendIcon(hdcDest uintptr, x, y, w, h int32, hdcSrc uintptr) {
	var bf blendFunc
	bf.BlendOp = acsOver
	bf.SourceConstantAlpha = 255
	bf.AlphaFormat = acSrcAlpha
	procAlphaBlend.Call(
		hdcDest, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		hdcSrc, 0, 0, uintptr(w), uintptr(h), uintptr(*(*uint32)(unsafe.Pointer(&bf))),
	)
}

type blendFunc struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

// clipboardSetText copies text to the clipboard as CF_UNICODETEXT. Pixel
// moves go through RtlMoveMemory; the locked pointer stays a native address.
func clipboardSetText(text string) bool {
	if text == "" {
		return false
	}
	if r1, _, _ := procOpenClipboard.Call(0); r1 == 0 {
		return false
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	u, err := syscall.UTF16FromString(text)
	if err != nil {
		return false
	}
	size := uintptr(len(u)) * 2
	h, _, _ := procGlobalAlloc.Call(uintptr(gmemMoveable), size)
	if h == 0 {
		return false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return false
	}
	memCopy(p, goPtr(&u[0]), size)
	procGlobalUnlock.Call(h)
	r1, _, _ := procSetClipboardData.Call(uintptr(cfUnicodeText), h)
	return r1 != 0
}

// highContrastOn reports the system accessibility high-contrast mode.
func highContrastOn() bool {
	var hc struct {
		Size          uint32
		Flags         uint32
		DefaultScheme [256]uint16
	}
	hc.Size = uint32(unsafe.Sizeof(hc))
	r1, _, _ := procSystemParametersInfoW.Call(uintptr(spiGetHighContrast), uintptr(hc.Size), uintptr(unsafe.Pointer(&hc)), 0)
	return r1 != 0 && hc.Flags&hcfHighContrastOn != 0
}

// clientAreaAnimation reports whether "Show animations in Windows" is enabled.
// When the query fails, animations are assumed enabled.
func clientAreaAnimation() bool {
	var on int32 = 1
	r1, _, _ := procSystemParametersInfoW.Call(uintptr(spiGetClientAreaAnimation), 0, uintptr(unsafe.Pointer(&on)), 0)
	if r1 == 0 {
		return true
	}
	return on != 0
}

func remoteSession() bool {
	r1, _, _ := procGetSystemMetrics.Call(uintptr(smRemoteSession))
	return r1 != 0
}

// animationsAllowed is false when the OS or session asks for instant
// transitions (reduce-animation setting or a remote session).
func animationsAllowed() bool {
	return clientAreaAnimation() && !remoteSession()
}

func setCueBanner(hwnd uintptr, text string) {
	p, _ := syscall.UTF16PtrFromString(text)
	procSendMessageW.Call(hwnd, uintptr(emSetCueBanner), 1, uintptr(unsafe.Pointer(p)))
}
