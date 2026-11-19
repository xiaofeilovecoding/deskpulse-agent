//go:build windows

package desktop

import (
	"syscall"
	"unsafe"
)

const (
	wmNotify        = 0x004E
	ttmAddToolW     = 0x0400 + 50
	ttmPop          = 0x0400 + 28
	ttnGetDispInfoW = -530
	ttfIDIsHwnd     = 0x0001
	ttfSubclass     = 0x0010
	ttsAlwaysTip    = 0x0001
	ttsNoPrefix     = 0x0002
)

// TOOLINFOW V2 size is required by the in-box version 5 common controls.
type settingsToolInfo struct {
	Size, Flags uint32
	Parent, ID  uintptr
	Bounds      rect
	Instance    uintptr
	TextPointer uintptr
	Data        uintptr
}

type settingsTipNotice struct {
	Header struct {
		From, ID uintptr
		Code     int32
	}
	TextPointer uintptr
	Text        [80]uint16
	Instance    uintptr
	Flags       uint32
	Data        uintptr
}

func (a *App) installSettingsTips() {
	procInitCommonControls.Call()
	tip, err := createControl("tooltips_class32", "", wsPopup|ttsAlwaysTip|ttsNoPrefix, wsExTopmost,
		0, 0, 0, 0, a.settingsUI, 0, a.instance)
	if err != nil {
		a.logger.Warn("create settings tooltips", "error", err.Error())
		return
	}
	a.settingsTips = tip
	for _, button := range []uintptr{a.settingsAppearance, a.settingsClose} {
		info := settingsToolInfo{Flags: ttfIDIsHwnd | ttfSubclass, Parent: a.settingsUI, ID: button, TextPointer: ^uintptr(0)}
		info.Size = uint32(unsafe.Sizeof(info))
		added, _, _ := procSendMessageW.Call(tip, ttmAddToolW, 0, uintptr(unsafe.Pointer(&info)))
		if added == 0 {
			a.logger.Warn("register settings tooltip", "control", childControlID(button))
		}
	}
}

func (a *App) settingsTipText(address uintptr) bool {
	if address == 0 || a.settingsTips == 0 {
		return false
	}
	var notice settingsTipNotice
	syscall.SyscallN(procRtlMoveMemory.Addr(), uintptr(unsafe.Pointer(&notice)), address, unsafe.Sizeof(notice.Header))
	if notice.Header.From != a.settingsTips || notice.Header.Code != ttnGetDispInfoW {
		return false
	}
	syscall.SyscallN(procRtlMoveMemory.Addr(), uintptr(unsafe.Pointer(&notice)), address, unsafe.Sizeof(notice))
	clear(notice.Text[:])
	label, _ := syscall.UTF16FromString(getText(notice.Header.ID))
	copy(notice.Text[:len(notice.Text)-1], label)
	// Use the notification's own text buffer so no Go pointer escapes to Win32.
	notice.TextPointer = address + unsafe.Offsetof(notice.Text)
	syscall.SyscallN(procRtlMoveMemory.Addr(), address, uintptr(unsafe.Pointer(&notice)), unsafe.Sizeof(notice))
	return true
}

func (a *App) paintSettingsHeaderButton(hdc uintptr, r rect, id int, focus, down bool) {
	t := a.theme
	fill, border, ink := t.bg(), t.bg(), t.muted()
	icon := "dismiss"
	if id == idAppearance {
		fill, border, ink = t.panel(), t.line(), t.text()
		icon = "moon"
		if t.Dark {
			icon = "sun"
		}
	}
	if down {
		fill = t.line()
	}
	if focus {
		border, ink = t.accent(), t.accent()
	}
	dpi := a.settingsDPI
	roundRect(hdc, r, scale(7, dpi), fill, border)
	drawIcon(hdc, icon, ink, iconRect(r, scale(18, dpi)))
}

func (a *App) paintSettingsTabButton(hdc uintptr, r rect, index int, focus, down bool) {
	dpi, t := a.settingsDPI, a.theme
	fillRect(hdc, r, t.bg())
	inner := rect{r.Left, r.Top + scale(3, dpi), r.Right, r.Bottom - scale(4, dpi)}
	if focus || down {
		roundRect(hdc, inner, scale(6, dpi), t.panel(), t.accent())
	}
	label := settingsTabs[index]
	ink := tabColor(t, index == a.settingsTab)
	drawActionLabel(hdc, inner, a.tr(label.zh, label.en), label.icon, a.fontsFor(dpi).Small, ink, dpi)
	if index == a.settingsTab {
		fillRect(hdc, rect{r.Left + scale(8, dpi), r.Bottom - scale(2, dpi), r.Right - scale(8, dpi), r.Bottom}, t.accent())
	}
}
