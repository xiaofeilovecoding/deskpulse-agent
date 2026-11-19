//go:build windows

package desktop

import (
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
)

func TestThemeDefaultsToLightAndTogglePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	a := &App{cfg: settings.New(path)}
	defer func() {
		if a.surfaceBrush != 0 {
			procDeleteObject.Call(a.surfaceBrush)
		}
	}()
	if a.cfg.Get().Appearance != settings.AppearanceLight || a.theme.bg() != rgb(255, 255, 255) {
		t.Fatal("fresh installations must start with a white background")
	}
	a.toggleAppearance()
	saved, err := settings.Load(path)
	if err != nil || saved.Get().Appearance != settings.AppearanceDark || !a.theme.Dark {
		t.Fatalf("dark appearance did not persist: %v", err)
	}
	a.toggleAppearance()
	saved, err = settings.Load(path)
	if err != nil || saved.Get().Appearance != settings.AppearanceLight || a.theme.Dark {
		t.Fatalf("switching back to light did not persist: %v", err)
	}
}

func TestSettingsHeaderNativeControls(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a, err := newApp(Options{DataDir: t.TempDir(), Synthetic: true})
	if err != nil {
		t.Fatal(err)
	}
	defer unregisterDesktopTestClass(t)
	activeApp = a
	defer a.shutdown()
	if err := a.createSettingsUI(); err != nil {
		t.Fatal(err)
	}
	moveWindow(a.settingsUI, -32000, -32000, 480, 420)
	showWindow(a.settingsUI, swShowNoActivate)
	a.applySettingsTab()
	const ttmGetToolCount = 0x0400 + 13
	count, _, err := procSendMessageW.Call(a.settingsTips, ttmGetToolCount, 0, 0)
	if a.settingsTips == 0 || count != 2 {
		t.Fatalf("tooltips: window=%d count=%d error=%v", a.settingsTips, count, err)
	}

	tooltip := func(button uintptr) string {
		const ttmGetTextW = 0x0400 + 56
		var text [80]uint16
		var pinned runtime.Pinner
		pinned.Pin(&text[0])
		defer pinned.Unpin()
		info := settingsToolInfo{Parent: a.settingsUI, ID: button, TextPointer: uintptr(unsafe.Pointer(&text[0]))}
		info.Size = uint32(unsafe.Sizeof(info))
		procSendMessageW.Call(a.settingsTips, ttmGetTextW, uintptr(len(text)), uintptr(unsafe.Pointer(&info)))
		return syscall.UTF16ToString(text[:])
	}
	assertThemeLabel := func() {
		t.Helper()
		label := a.appearanceAction()
		if getText(a.settingsAppearance) != label || tooltip(a.settingsAppearance) != label {
			t.Fatalf("theme action name and tooltip must both be %q, got name %q and tooltip %q", label,
				getText(a.settingsAppearance), tooltip(a.settingsAppearance))
		}
	}
	assertThemeLabel()
	procSendMessageW.Call(a.settingsAppearance, bmClick, 0, 0)
	if !a.theme.Dark {
		t.Fatal("native theme button did not change appearance")
	}
	assertThemeLabel()
	procSendMessageW.Call(a.settingsTabButtons[tabNetwork], wmKeyDown, 0x20, 0)
	procSendMessageW.Call(a.settingsTabButtons[tabNetwork], 0x0101, 0x20, 0) // WM_KEYUP
	if a.settingsTab != tabNetwork || !isVisible(a.settingsTarget) || isVisible(a.settingsKey) {
		t.Fatal("keyboard Space did not switch settings sections")
	}
	if label := tooltip(a.settingsClose); label == "" || label != getText(a.settingsClose) {
		t.Fatalf("close tooltip %q does not match its native title", label)
	}
	procSendMessageW.Call(a.settingsClose, bmClick, 0, 0)
	if isVisible(a.settingsUI) {
		t.Fatal("native close button did not hide settings")
	}
}

func unregisterDesktopTestClass(t *testing.T) {
	t.Helper()
	name, err := syscall.UTF16PtrFromString(windowClassName)
	if err != nil {
		t.Fatal(err)
	}
	removed, _, nativeErr := user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(name)), uintptr(moduleHandle()))
	if removed == 0 {
		t.Errorf("unregister test window class: %v", nativeErr)
		return
	}
	classAtom = 0
}
