//go:build windows

package desktop

import "github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"

type palette struct {
	base, surface, border       uint32
	foreground, secondary       uint32
	primary, onPrimary, caution uint32
}

var lightPalette = palette{
	base: rgb(255, 255, 255), surface: rgb(247, 249, 249), border: rgb(217, 225, 224),
	foreground: rgb(26, 36, 35), secondary: rgb(82, 99, 96),
	primary: rgb(32, 94, 83), onPrimary: rgb(255, 255, 255), caution: rgb(133, 74, 15),
}

var darkPalette = palette{
	base: rgb(25, 30, 30), surface: rgb(34, 42, 42), border: rgb(63, 75, 75),
	foreground: rgb(238, 243, 241), secondary: rgb(168, 185, 181),
	primary: rgb(156, 222, 203), onPrimary: rgb(24, 49, 43), caution: rgb(242, 195, 125),
}

var contrastPalette = palette{
	base: rgb(0, 0, 0), surface: rgb(0, 0, 0), border: rgb(255, 255, 255),
	foreground: rgb(255, 255, 255), secondary: rgb(230, 230, 230),
	primary: rgb(255, 255, 255), onPrimary: rgb(0, 0, 0), caution: rgb(255, 255, 128),
}

type uiTheme struct {
	Dark         bool
	HighContrast bool
}

func (t uiTheme) colors() *palette {
	if t.HighContrast {
		return &contrastPalette
	}
	if t.Dark {
		return &darkPalette
	}
	return &lightPalette
}

func (t uiTheme) bg() uint32         { return t.colors().base }
func (t uiTheme) panel() uint32      { return t.colors().surface }
func (t uiTheme) line() uint32       { return t.colors().border }
func (t uiTheme) text() uint32       { return t.colors().foreground }
func (t uiTheme) muted() uint32      { return t.colors().secondary }
func (t uiTheme) accent() uint32     { return t.colors().primary }
func (t uiTheme) accentText() uint32 { return t.colors().onPrimary }
func (t uiTheme) amber() uint32      { return t.colors().caution }

func (t uiTheme) status(state string) uint32 {
	switch state {
	case "ok":
		return t.text()
	case "pending", "no_target", "no_data", "":
		return t.muted()
	default:
		return t.amber()
	}
}

func (t uiTheme) focusWidth(dpi uint32) int32 { return scale(2, dpi) }

func (a *App) appearanceAction() string {
	if a.theme.Dark {
		return a.tr("切换到浅色模式", "Switch to light mode")
	}
	return a.tr("切换到暗色模式", "Switch to dark mode")
}

func (a *App) toggleAppearance() {
	cfg := a.cfg.Get()
	cfg.Appearance = settings.AppearanceDark
	if a.theme.Dark {
		cfg.Appearance = settings.AppearanceLight
	}
	next := a.theme
	next.Dark = cfg.Appearance == settings.AppearanceDark
	brush, _, _ := procCreateSolidBrush.Call(uintptr(next.panel()))
	if brush == 0 {
		return
	}
	if err := a.cfg.Save(cfg); err != nil {
		procDeleteObject.Call(brush)
		ShowError(err.Error())
		return
	}
	previous := a.surfaceBrush
	a.theme, a.surfaceBrush = next, brush
	if previous != 0 {
		procDeleteObject.Call(previous)
	}
	if a.settingsAppearance != 0 {
		setText(a.settingsAppearance, a.appearanceAction())
		if a.settingsTips != 0 {
			procSendMessageW.Call(a.settingsTips, ttmPop, 0, 0)
		}
	}
	if a.ball != 0 {
		a.paintBall()
	}
	if a.hoverShown {
		a.paintLayered(a.hover, a.paintHoverInto)
	}
	for _, hwnd := range []uintptr{a.settingsUI, a.analysisUI, a.panel, a.procUI, a.historyUI} {
		if hwnd != 0 {
			invalidate(hwnd)
		}
	}
	for _, child := range a.settingsChildren() {
		if child != 0 {
			invalidate(child)
		}
	}
	if a.historyList != 0 {
		invalidate(a.historyList)
		invalidate(a.historyStatus)
	}
}
