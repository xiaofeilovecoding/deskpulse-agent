//go:build windows

package desktop

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
)

const (
	trayOpenCmd      = 1
	traySettingsCmd  = 2
	trayQuitCmd      = 3
	trayAgent        = 4
	trayExport       = 5
	trayCacheStage   = 6
	trayCacheClear   = 7
	trayCacheRestore = 8
	trayLanguage     = 9
	trayHiddenBase   = 20
	trayDisabledBase = 30
)

func (a *App) addTray() error {
	if a.ball == 0 {
		return fmt.Errorf("tray owner window is not ready")
	}
	icon, _, _ := procLoadIconW.Call(0, idiApplication)
	nid := notifyIconData{
		Wnd:             a.ball,
		ID:              notifyID,
		Flags:           nifMessage | nifIcon | nifTip,
		CallbackMessage: wmAppTray,
		Icon:            icon,
	}
	nid.Size = uint32(unsafe.Sizeof(nid))
	copyUTF16(nid.Tip[:], "DeskPulse Agent v"+a.version)
	r1, _, e := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if r1 == 0 {
		return fmt.Errorf("Shell_NotifyIconW(NIM_ADD): %w", callError(e))
	}
	a.trayAdded = true
	return nil
}

func (a *App) removeTray() {
	if !a.trayAdded || a.ball == 0 {
		return
	}
	var nid notifyIconData
	nid.Size = uint32(unsafe.Sizeof(nid))
	nid.Wnd = a.ball
	nid.ID = notifyID
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	a.trayAdded = false
}

func (a *App) showTrayMenu(owner uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendMenu(menu, mfString, trayOpenCmd, a.tr("打开监测", "Open monitor"))
	appendMenu(menu, mfString, trayAgent, a.tr("本地诊断", "Local diagnosis"))
	appendMenu(menu, mfString, traySettingsCmd, a.tr("网络与 AI 设置", "Network and AI settings"))
	appendMenu(menu, mfString, trayExport, a.tr("导出只读指标", "Export read-only metrics"))
	appendMenu(menu, mfString, trayCacheStage, a.tr("缓存本机快照", "Cache local snapshot"))
	appendMenu(menu, mfString, trayCacheClear, a.tr("预览过期缓存", "Review expired cache"))
	appendMenu(menu, mfString, trayCacheRestore, a.tr("恢复缓存归档", "Restore cached snapshots"))
	cfg := a.cfg.Get()
	for _, group := range []struct {
		title string
		base  uintptr
		mask  settings.MetricMask
	}{
		{a.tr("悬浮条显示", "Show on floating monitor"), trayHiddenBase, cfg.HiddenMetrics},
		{a.tr("指标采集", "Collect metrics"), trayDisabledBase, cfg.DisabledMetrics},
	} {
		sub, _, _ := procCreatePopupMenu.Call()
		if sub == 0 {
			continue
		}
		for i, label := range []string{"CPU", a.tr("内存", "RAM"), "I/O", a.tr("延迟", "RTT")} {
			flags := uint32(mfString)
			if group.mask&capsuleMetrics[i] == 0 {
				flags |= 0x8 // MF_CHECKED
			}
			appendMenu(sub, flags, group.base+uintptr(i), label)
		}
		appendMenu(menu, 0x10, sub, group.title) // MF_POPUP
	}
	appendMenu(menu, mfString, trayLanguage, a.tr("托盘和本地诊断换成英文", "Tray and local diagnosis in Chinese"))
	appendMenu(menu, mfSeparator, 0, "")
	appendMenu(menu, mfString, trayQuitCmd, a.tr("退出", "Quit"))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(owner)
	cmd, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmReturnCmd|tpmRightButton,
		uintptr(pt.X), uintptr(pt.Y), 0,
		owner, 0,
	)
	procPostMessageW.Call(owner, 0, 0, 0)
	switch cmd {
	case trayOpenCmd:
		a.openFromTray()
	case traySettingsCmd:
		a.openSettings()
	case trayAgent:
		a.openLocalDiagnosis()
	case trayExport:
		a.exportMetrics()
	case trayCacheStage, trayCacheClear, trayCacheRestore:
		a.cacheAction(cmd)
	case trayLanguage:
		cfg.Language = a.tr("en", "zh-CN")
		if err := a.cfg.Save(cfg); err != nil {
			ShowError(err.Error())
		}
	case trayQuitCmd:
		a.quit()
	default:
		switch {
		case cmd >= trayHiddenBase && cmd < trayHiddenBase+uintptr(len(capsuleMetrics)):
			cfg.HiddenMetrics ^= capsuleMetrics[cmd-trayHiddenBase]
		case cmd >= trayDisabledBase && cmd < trayDisabledBase+uintptr(len(capsuleMetrics)):
			cfg.DisabledMetrics ^= capsuleMetrics[cmd-trayDisabledBase]
		default:
			return
		}
		if err := a.cfg.Save(cfg); err != nil {
			ShowError(err.Error())
		}
	}
}

func appendMenu(menu uintptr, flags uint32, id uintptr, text string) {
	p, _ := syscall.UTF16PtrFromString(text)
	procAppendMenuW.Call(menu, uintptr(flags), id, uintptr(unsafe.Pointer(p)))
}

func (a *App) handleTrayCallback(lparam uintptr) bool {
	switch uint32(lparam) {
	case wmLButtonUp, wmLButtonDown:
		a.openFromTray()
		return true
	case wmRButtonUp:
		owner := a.ball
		if a.panel != 0 && isVisible(a.panel) {
			owner = a.panel
		}
		a.showTrayMenu(owner)
		return true
	}
	return false
}
