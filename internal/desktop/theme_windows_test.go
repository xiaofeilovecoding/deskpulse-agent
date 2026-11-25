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

// Exercise the native input and paint paths together in a work area short
// enough that the action row can be reached only by scrolling.
func TestScrolledOverviewActions(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a, err := newApp(Options{DataDir: t.TempDir(), Synthetic: true})
	if err != nil {
		t.Fatal(err)
	}
	defer unregisterDesktopTestClass(t)
	activeApp = a
	defer a.shutdown()
	if err := a.createWindows(); err != nil {
		t.Fatal(err)
	}
	for _, dpi := range []uint32{96, 120, 144, 192} {
		a.captureDPI, a.panelDPI = dpi, dpi
		w, h := scale(popupWDIP, dpi), scale(240, dpi)
		moveWindow(a.panel, -32000, -32000, w, h)
		a.popupScroll, a.panelFocus = 0, -1
		a.renderWindow(a.panel, a.paintPopupInto)
		a.handlePanel(wmMouseWheel, uintptr(uint32(0xff88)<<16), 0) // one downward notch
		if a.popupScroll <= 0 {
			t.Fatalf("dpi %d: wheel did not scroll down", dpi)
		}
		a.renderWindow(a.panel, a.paintPopupInto)
		a.handlePopupKey(0x09)
		a.renderWindow(a.panel, a.paintPopupInto)
		a.handlePopupKey(0x09)
		a.renderWindow(a.panel, a.paintPopupInto)
		if a.panelHits[a.panelFocus].Action != paAnalyze {
			t.Fatalf("dpi %d: keyboard lost the primary action", dpi)
		}
		y := scale(288, dpi) - a.popupScroll
		if y < 0 || y >= h {
			t.Fatalf("dpi %d: focused action is outside the client", dpi)
		}
		for _, tc := range []struct {
			x      int
			action popupAction
		}{{24, paAnalyze}, {312, paProcess}} {
			matches := 0
			for _, hit := range a.panelHits {
				if pointIn(scale(tc.x, dpi), y, hit.Rect) {
					if hit.Action != tc.action {
						t.Fatalf("dpi %d: wrong action at %d", dpi, tc.x)
					}
					matches++
				}
			}
			if matches != 1 {
				t.Fatalf("dpi %d: button edge at %d has %d targets", dpi, tc.x, matches)
			}
		}
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
