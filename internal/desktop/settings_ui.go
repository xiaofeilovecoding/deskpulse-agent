//go:build windows

package desktop

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
)

// Settings geometry in DIP (spec: ~480×420, DeepSeek/网络 tabs, fixed footer).
const (
	settingsWDIP    = 480
	settingsHDIP    = 420
	settingsMargin  = 24
	settingsTitleH  = 40
	settingsTabH    = 36
	settingsFooterH = 52
)

const (
	wmAppTestDone = wmApp + 10

	idTargetList = 2001
	idTargetEdit = 2002
	idAdd        = 2003
	idDelete     = 2004
	idPrimary    = 2005
	idSeconds    = 2006
	idAPIKey     = 2007
	idTest       = 2008
	idClearKey   = 2009
	idSave       = 2010
	idCancel     = 2011
	idProvider   = 2012
	idEndpoint   = 2013
	idModel      = 2014
	idRecovery   = 2015
	idAppearance = 2016
	idClose      = 2017
	idFirstTab   = 2020

	tabDeepSeek = 0
	tabNetwork  = 1
	tabRecovery = 2

	lbAddString    = 0x0180
	lbResetContent = 0x0184
	lbSetCurSel    = 0x0186
	lbGetCurSel    = 0x0188
	lbGetCount     = 0x018B
	lbnSetFocus    = 4
	lbnKillFocus   = 5

	enChange = 0x0300
	enFocus  = 0x0100
	enBlur   = 0x0200
)

var modelProviders = [...]settings.ModelProvider{settings.DeepSeek, settings.ChatAPI, settings.Responses, settings.Messages}

var settingsTabs = [...]struct{ zh, en, icon string }{
	{"模型", "Models", "sparkle"},
	{"网络", "Network", "network"},
	{"恢复", "Recovery", "refresh"},
}

func (a *App) openSettings() {
	if a.settingsUI == 0 {
		if err := a.createSettingsUI(); err != nil {
			a.logger.Warn("create settings window failed", "error", err.Error())
			showMessage("DeskPulse Agent", a.tr("打开设置失败：", "Could not open Settings: ")+err.Error(), mbOK|mbIconError)
			return
		}
	}
	a.settingsTab = tabDeepSeek
	a.settingsTargets = append([]string(nil), a.cfg.Get().RTTTargets...)
	a.settingsTestText = ""
	a.settingsSecErr = ""
	a.settingsTargetErr = ""
	a.settingsKeyErr = ""
	a.applySettingsTab()
	a.refreshSettingsList()
	setText(a.settingsSeconds, strconv.Itoa(a.aiSeconds))
	setText(a.settingsKey, "")
	setText(a.settingsRecovery, a.cfg.Get().RecoveryFolder)
	a.loadSettingsProfile(a.cfg.Get().ModelProfile())
	a.positionSettings()
	a.layoutSettingsControls()
	showWindow(a.settingsUI, swShow)
	procSetForegroundWindow.Call(a.settingsUI)
	setFocus(a.settingsProvider)
	invalidate(a.settingsUI)
}

func (a *App) createSettingsUI() error {
	dpi := dpiForWindow(a.ball)
	if dpi == 0 {
		dpi = systemDPI()
	}
	w, h := scale(settingsWDIP, dpi), scale(settingsHDIP, dpi)
	x, y, _ := a.anchoredRect(w, h, dpi)
	hwnd, err := createWindow(
		wsExToolWindow|wsExTopmost|wsExControlParent,
		"DeskPulse Agent 设置",
		wsPopup|wsClipChildren,
		x, y, w, h,
		0, 0, a.instance, 0,
	)
	if err != nil {
		return err
	}
	a.settingsUI = hwnd
	setRoundRegion(hwnd, w, h, scale(10, dpi))
	a.settingsDPI = dpi

	button := func(label string, id int32) (uintptr, error) {
		return createControl("BUTTON", label, wsChild|wsVisible|wsTabStop|bsOwnerDraw,
			0, 0, 0, 0, 0, hwnd, id, a.instance)
	}
	if a.settingsAppearance, err = button(a.appearanceAction(), idAppearance); err != nil {
		return err
	}
	if a.settingsClose, err = button(a.tr("关闭设置", "Close settings"), idClose); err != nil {
		return err
	}
	for i, tab := range settingsTabs {
		a.settingsTabButtons[i], err = button(a.tr(tab.zh, tab.en), idFirstTab+int32(i))
		if err != nil {
			return err
		}
	}
	a.settingsProvider, err = createControl(
		"BUTTON", "", wsChild|wsVisible|wsTabStop|bsOwnerDraw,
		0, 0, 0, 0, 0, hwnd, idProvider, a.instance,
	)
	if err != nil {
		return err
	}
	var aiFields = []struct {
		field *uintptr
		id    int32
	}{
		{&a.settingsModel, idModel},
		{&a.settingsEndpoint, idEndpoint},
		{&a.settingsRecovery, idRecovery},
	}
	for _, item := range aiFields {
		*item.field, err = createControl("EDIT", "", wsChild|wsVisible|wsTabStop|esAutoHScroll|esLeft,
			0, 0, 0, 0, 0, hwnd, item.id, a.instance)
		if err != nil {
			return err
		}
	}
	a.settingsKey, err = createControl(
		"EDIT", "",
		wsChild|wsVisible|wsTabStop|esPassword|esAutoHScroll|esLeft,
		0, 0, 0, 0, 0, hwnd, idAPIKey, a.instance,
	)
	if err != nil {
		return err
	}
	a.settingsSeconds, err = createControl(
		"EDIT", "",
		wsChild|wsVisible|wsTabStop|esNumber|esLeft,
		0, 0, 0, 0, 0, hwnd, idSeconds, a.instance,
	)
	if err != nil {
		return err
	}
	a.settingsList, err = createControl(
		"LISTBOX", "",
		wsChild|wsVisible|wsTabStop|wsVScroll|lbsNotify|lbsNoIntegralHeight,
		0, 0, 0, 0, 0, hwnd, idTargetList, a.instance,
	)
	if err != nil {
		return err
	}
	a.settingsTarget, err = createControl(
		"EDIT", "",
		wsChild|wsVisible|wsTabStop|esAutoHScroll|esLeft,
		0, 0, 0, 0, 0, hwnd, idTargetEdit, a.instance,
	)
	if err != nil {
		return err
	}
	if a.settingsTest, err = button(a.tr("测试连接", "Test connection"), idTest); err != nil {
		return err
	}
	if a.settingsClear, err = button(a.tr("清除 Key", "Clear key"), idClearKey); err != nil {
		return err
	}
	if a.settingsAdd, err = button(a.tr("添加", "Add"), idAdd); err != nil {
		return err
	}
	if a.settingsDelete, err = button(a.tr("删除", "Remove"), idDelete); err != nil {
		return err
	}
	if a.settingsPrimary, err = button(a.tr("设为主要", "Make primary"), idPrimary); err != nil {
		return err
	}
	if a.settingsSave, err = button(a.tr("保存", "Save"), idSave); err != nil {
		return err
	}
	if a.settingsCancel, err = button(a.tr("取消", "Cancel"), idCancel); err != nil {
		return err
	}

	a.installSettingsTips()
	setCueBanner(a.settingsKey, a.tr("留空以保留此接口的 Key", "Leave blank to keep this endpoint's key"))
	a.applyControlFonts()
	a.layoutSettingsControls()
	return nil
}

// applyControlFonts gives every child control the DPI-correct UI font.
func (a *App) applyControlFonts() {
	f := a.fontsFor(a.settingsDPI)
	for _, ctl := range a.settingsChildren() {
		if ctl != 0 {
			procSendMessageW.Call(ctl, wmSetFont, f.Small, 1)
		}
	}
}

// layoutSettingsControls positions the child controls for the current DPI.
func (a *App) layoutSettingsControls() {
	dpi := a.settingsDPI
	s := func(v int) int32 { return scale(v, dpi) }
	bounds := clientRect(a.settingsUI)
	w := bounds.Right
	m := s(settingsMargin)
	contentW := w - 2*m
	btnH := s(32)
	inputs := make(map[uintptr]rect, 6)
	placeInput := func(hwnd uintptr, x, y, width, height int32) {
		inputs[hwnd] = rect{x, y, x + width, y + height}
		padding, lineBox := s(9), min(s(18), height)
		moveWindow(hwnd, x+padding, y+(height-lineBox)/2, max(1, width-2*padding), lineBox)
	}

	half := (contentW - s(12)) / 2
	moveWindow(a.settingsProvider, m, s(112), half, s(28))
	placeInput(a.settingsModel, m+half+s(12), s(112), half, s(28))
	placeInput(a.settingsEndpoint, m, s(164), contentW, s(30))
	placeInput(a.settingsKey, m, s(224), contentW, s(30))
	moveWindow(a.settingsTest, m, s(266), s(120), btnH)
	moveWindow(a.settingsClear, m+s(128), s(266), s(92), btnH)
	placeInput(a.settingsSeconds, w-m-s(64)-s(30), s(266), s(64), s(32))
	placeInput(a.settingsRecovery, m, s(118), contentW, s(34))
	listH := s(150)
	listTop := s(117)
	inputs[a.settingsList] = rect{m, listTop, w - m, listTop + listH}
	moveWindow(a.settingsList, m+s(9), listTop+s(7), contentW-s(18), listH-s(14))
	placeInput(a.settingsTarget, m, s(277), contentW-s(72)-s(8), s(30))
	moveWindow(a.settingsAdd, w-m-s(72), s(277), s(72), s(30))
	moveWindow(a.settingsDelete, m, s(318), s(80), btnH)
	moveWindow(a.settingsPrimary, m+s(88), s(318), s(110), btnH)
	btnY := bounds.Bottom - s(settingsFooterH-10)
	moveWindow(a.settingsCancel, w-m-s(80)-s(8)-s(92), btnY, s(80), btnH)
	moveWindow(a.settingsSave, w-m-s(92), btnY, s(92), btnH)
	moveWindow(a.settingsAppearance, w-m-s(64), s(10), s(28), s(28))
	moveWindow(a.settingsClose, w-m-s(28), s(10), s(28), s(28))
	for i, control := range a.settingsTabButtons {
		moveWindow(control, m+s(96*i), s(settingsTitleH), s(88), s(settingsTabH))
	}
	a.settingsInputFrames = inputs
}

func (a *App) positionSettings() {
	if a.settingsUI == 0 {
		return
	}
	r := windowRect(a.settingsUI)
	work := workArea(a.ball)
	if r.Right-r.Left > work.Right-work.Left {
		r.Right = r.Left + (work.Right - work.Left)
	}
	if r.Bottom-r.Top > work.Bottom-work.Top {
		r.Bottom = r.Top + (work.Bottom - work.Top)
	}
	x := work.Left + (work.Right-work.Left-(r.Right-r.Left))/2
	y := work.Top + (work.Bottom-work.Top-(r.Bottom-r.Top))/2
	moveWindow(a.settingsUI, x, y, r.Right-r.Left, r.Bottom-r.Top)
}

func (a *App) applySettingsTab() {
	if a.settingsUI == 0 {
		return
	}
	show := func(hwnd uintptr, on bool) {
		if hwnd != 0 {
			if on {
				showWindow(hwnd, swShow)
			} else {
				showWindow(hwnd, swHide)
			}
		}
	}
	// All children are created visible (so the footer buttons and native
	// controls render and print); this routine hides the inactive tab's set.
	deep := a.settingsTab == tabDeepSeek
	net := a.settingsTab == tabNetwork
	show(a.settingsRecovery, a.settingsTab == tabRecovery)
	show(a.settingsKey, deep)
	show(a.settingsProvider, deep)
	show(a.settingsEndpoint, deep)
	show(a.settingsModel, deep)
	show(a.settingsTest, deep)
	show(a.settingsClear, deep)
	show(a.settingsSeconds, deep)
	show(a.settingsList, net)
	show(a.settingsTarget, net)
	show(a.settingsAdd, net)
	show(a.settingsDelete, net)
	show(a.settingsPrimary, net)
	for _, button := range a.settingsTabButtons {
		invalidate(button)
	}
	invalidate(a.settingsUI)
}

func (a *App) refreshSettingsList() {
	if a.settingsList == 0 {
		return
	}
	procSendMessageW.Call(a.settingsList, lbResetContent, 0, 0)
	for i, t := range a.settingsTargets {
		label := t
		if i == 0 {
			label = t + a.tr("（主要）", " (primary)")
		}
		addListString(a.settingsList, label)
	}
	if len(a.settingsTargets) > 0 {
		procSendMessageW.Call(a.settingsList, lbSetCurSel, 0, 0)
	}
}

func addListString(list uintptr, s string) {
	p, _ := syscall.UTF16PtrFromString(s)
	procSendMessageW.Call(list, lbAddString, 0, uintptr(unsafe.Pointer(p)))
}

func (a *App) handleSettings(message uint32, wparam, lparam uintptr) (bool, uintptr) {
	switch message {
	case wmPaint:
		a.paintSettings()
		return true, 0
	case wmEraseBkgnd:
		return true, 1
	case wmClose:
		a.closeSettings()
		return true, 0
	case wmCommand:
		id := int(uint16(wparam & 0xffff))
		notification := uint16(wparam >> 16)
		focusedEdit := a.settingsInputFrames[lparam] != (rect{}) && (notification == enFocus || notification == enBlur)
		focusedList := id == idTargetList && (notification == lbnSetFocus || notification == lbnKillFocus)
		if focusedEdit || focusedList {
			invalidate(a.settingsUI)
			return true, 0
		}
		if (id == idEndpoint || id == idModel || id == idAPIKey) && notification == enChange {
			a.onModelDraftChanged(id)
			return true, 0
		}
		a.onSettingsCommand(id)
		return true, 0
	case wmDrawItem:
		// The message carries a DRAWITEMSTRUCT pointer; copy it into a local
		// value through RtlMoveMemory (no uintptr→pointer conversion).
		var dis drawItemStruct
		memCopy(goPtr((*byte)(unsafe.Pointer(&dis))), lparam, unsafe.Sizeof(dis))
		a.paintOwnerDrawButton(&dis)
		return true, 1
	case wmNotify:
		if a.settingsTipText(lparam) {
			return true, 0
		}
	case wmAppTestDone:
		a.finishKeyTest()
		return true, 0
	case wmCtlColorEdit, wmCtlColorStatic, wmCtlColorListBox, wmCtlColorBtn:
		hdc := wparam
		procSetTextColor.Call(hdc, uintptr(a.theme.text()))
		procSetBkColor.Call(hdc, uintptr(a.theme.panel()))
		return true, a.surfaceBrush
	case wmContextMenu:
		// Right-click on the target list opens the row menu. wparam carries
		// the window under the cursor; lparam is a screen position.
		if uintptr(wparam) == a.settingsList {
			a.showPrimaryMenu()
			return true, 0
		}
		return false, 0
	case wmLButtonUp:
		x, y := lparamPoint(lparam)
		for _, field := range a.visibleSettingsInputs() {
			if pointIn(x, y, a.settingsInputFrames[field]) {
				setFocus(field)
				return true, 0
			}
		}
		return true, 0
	case wmDpiChanged:
		a.settingsDPI = uint32(wparam & 0xffff)
		if a.settingsDPI == 0 {
			a.settingsDPI = dpiForWindow(a.settingsUI)
		}
		a.applySettingsFrame()
		a.positionSettings()
		a.applyControlFonts()
		a.layoutSettingsControls()
		invalidate(a.settingsUI)
		return true, 0
	}
	return false, 0
}

func (a *App) applySettingsFrame() {
	dpi := dpiForWindow(a.settingsUI)
	w, h := scale(settingsWDIP, dpi), scale(settingsHDIP, dpi)
	r := windowRect(a.settingsUI)
	moveWindow(a.settingsUI, r.Left, r.Top, w, h)
	setRoundRegion(a.settingsUI, w, h, scale(10, dpi))
}

func (a *App) closeSettings() {
	if a.settingsUI != 0 {
		setText(a.settingsKey, "")
		showWindow(a.settingsUI, swHide)
	}
}

func (a *App) paintSettings() {
	a.paintWindow(a.settingsUI, a.paintSettingsInto)
}

// paintSettingsInto draws the frame, tabs, labels and inline errors; the
// EDIT, LISTBOX and BUTTON children render themselves on top.
func (a *App) paintSettingsInto(b *windowBuffer) {
	dpi := b.dpi
	f := a.fontsFor(dpi)
	t := a.theme
	fillRect(b.hdc, rect{0, 0, b.w, b.h}, t.bg())
	focus, _, _ := procGetFocus.Call()
	for _, field := range a.visibleSettingsInputs() {
		if frame, ok := a.settingsInputFrames[field]; ok {
			border := t.line()
			if focus == field {
				border = t.accent()
			}
			roundRect(b.hdc, frame, scale(6, dpi), t.panel(), border)
		}
	}
	m := scale(settingsMargin, dpi)
	w := b.w

	// The title shares its baseline with the native header buttons.
	titleR := rect{m, scale(10, dpi), w - m - scale(76, dpi), scale(38, dpi)}
	drawText(b.hdc, a.tr("设置", "Settings"), titleR, f.TitleCJK, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)

	tabBottom := scale(settingsTitleH, dpi) + scale(settingsTabH, dpi)
	fillRect(b.hdc, rect{m, tabBottom, w - m, tabBottom + 1}, t.line())

	contentTop := tabBottom + scale(12, dpi)
	if a.settingsTab == tabDeepSeek {
		a.paintModelSettings(b, m)
	} else if a.settingsTab == tabNetwork {
		// Network tab: list hint and inline error.
		drawText(b.hdc, a.tr("RTT 目标（最多 8 个，第一个为主要目标）", "RTT targets · first is primary · up to 8"), rect{m, contentTop, w - m, contentTop + scale(24, dpi)}, f.Body, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		if a.settingsTargetErr != "" {
			drawText(b.hdc, a.settingsTargetErr, rect{m + scale(207, dpi), scale(318, dpi), w - m, scale(350, dpi)}, f.Small, t.amber(), dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis|dtNoPrefix)
		}
	} else {
		line := a.tr("已存在的本机恢复目录", "Existing local recovery folder")
		drawText(b.hdc, line, rect{m, contentTop, w - m, contentTop + scale(26, dpi)}, f.Body, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		note := a.tr("留空使用同盘恢复。跨盘归档前会显示文件和备份位置，确认后再校验副本。",
			"Leave empty for same-volume recovery. Cross-volume recovery shows files and the backup path before confirmation.")
		drawText(b.hdc, note, rect{m, scale(167, dpi), w - m, scale(234, dpi)}, f.Body, t.muted(), dtLeft|dtWordBreak|dtNoPrefix)
		if a.settingsKeyErr != "" {
			drawText(b.hdc, a.settingsKeyErr, rect{m, scale(279, dpi), w - m, scale(325, dpi)}, f.Small, t.amber(), dtLeft|dtWordBreak|dtNoPrefix)
		}
	}
}

func (a *App) visibleSettingsInputs() []uintptr {
	switch a.settingsTab {
	case tabDeepSeek:
		return []uintptr{a.settingsModel, a.settingsEndpoint, a.settingsKey, a.settingsSeconds}
	case tabNetwork:
		return []uintptr{a.settingsList, a.settingsTarget}
	case tabRecovery:
		return []uintptr{a.settingsRecovery}
	}
	return nil
}

func (a *App) paintModelSettings(b *windowBuffer, margin int32) {
	dpi, theme := b.dpi, a.theme
	f := a.fontsFor(dpi)
	at := func(x int32, y, height int) rect {
		return rect{x, scale(y, dpi), b.w - margin, scale(y+height, dpi)}
	}
	const left = dtLeft | dtVCenter | dtSingleLine | dtEndEllipsis | dtNoPrefix
	label := func(text string, x int32, y int) {
		drawText(b.hdc, text, at(x, y, 22), f.Small, theme.text(), left)
	}
	label(a.tr("接口与预设", "API format and preset"), margin, 88)
	label(a.tr("模型名称", "Model ID"), (b.w+scale(12, dpi))/2, 88)
	label(a.tr("接口地址（HTTPS）", "API base URL (HTTPS)"), margin, 140)
	label("API Key", margin, 199)
	if p, err := a.settingsProfile(); err == nil {
		status := a.diag.StatusFor(p)
		text := a.tr("未保存凭据", "No saved credential")
		if status.Configured {
			text = a.tr("凭据已配置", "Credential configured")
		} else if status.Error != "" {
			text = a.tr("读取凭据失败", "Could not read the credential")
		}
		drawText(b.hdc, text, at(margin, 199, 22), f.Small, keyStatusColor(theme, status.Configured), dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
	}
	label(a.tr("分析时长", "Lookback"), b.w-margin-scale(166, dpi), 271)
	label(a.tr("秒", "sec"), b.w-margin-scale(26, dpi), 270)
	status, color := a.settingsTestText, testResultColor(theme, a.settingsTestOK)
	if a.settingsKeyErr != "" {
		status, color = a.settingsKeyErr, theme.amber()
	} else if a.settingsSecErr != "" {
		status, color = a.settingsSecErr, theme.amber()
	}
	drawText(b.hdc, status, at(margin, 304, 22), f.Small, color, left)
	drawText(b.hdc, a.tr("测试连接只验证凭据，不会发送监测数据。", "Connection testing checks access without sending monitoring data."),
		at(margin, 331, 22), f.Small, theme.muted(), left)
}

func tabColor(t uiTheme, active bool) uint32 {
	if active {
		return t.text()
	}
	return t.muted()
}

// keyStatusColor shows a configured key in the accent color so 「已配置 ·
// 环境变量」 reads at a glance.
func keyStatusColor(t uiTheme, configured bool) uint32 {
	if configured {
		return t.accent()
	}
	return t.muted()
}

func testResultColor(t uiTheme, ok bool) uint32 {
	if ok {
		return t.muted()
	}
	return t.amber()
}

type drawItemStruct struct {
	CtlType  uint32
	CtlID    uint32
	ItemID   uint32
	Action   uint32
	State    uint32
	Item     uintptr
	HWNDItem uintptr
	HDC      uintptr
	RcItem   rect
	ItemData uintptr
}

// paintOwnerDrawButton renders native owner-draw buttons with the v0.3 look.
func (a *App) paintOwnerDrawButton(dis *drawItemStruct) {
	if dis == nil || dis.CtlType != odtButton {
		return
	}
	a.paintButtonFace(dis.HDC, dis.RcItem, int(dis.CtlID), dis.State&odsFocus != 0, dis.State&odsSelected != 0)
}

// buttonFaceFor maps a settings control ID to its label and primary state.
func (a *App) buttonFaceFor(id int) (string, bool) {
	switch id {
	case idTest:
		return a.tr("测试连接", "Test connection"), false
	case idClearKey:
		return a.tr("清除 Key", "Clear key"), false
	case idAdd:
		return a.tr("添加", "Add"), false
	case idDelete:
		return a.tr("删除", "Remove"), false
	case idPrimary:
		return a.tr("设为主要", "Make primary"), false
	case idSave:
		return a.tr("保存", "Save"), true
	case idCancel:
		return a.tr("取消", "Cancel"), false
	case idProvider:
		return a.modelChoiceLabel(a.selectedModelProvider()), false
	}
	return "", false
}

// paintButtonFace is the single owner-draw button renderer used by both live
// WM_DRAWITEM handling and the capture compositor.
func (a *App) paintButtonFace(hdc uintptr, r rect, id int, focus, down bool) {
	if tab := id - idFirstTab; tab >= 0 && tab < len(settingsTabs) {
		a.paintSettingsTabButton(hdc, r, tab, focus, down)
		return
	}
	if id == idAppearance || id == idClose {
		a.paintSettingsHeaderButton(hdc, r, id, focus, down)
		return
	}
	label, primary := a.buttonFaceFor(id)
	if label == "" {
		return
	}
	f := a.fontsFor(a.settingsDPI)
	t := a.theme
	var fill, border, text uint32
	if primary {
		fill, border, text = t.accent(), t.accent(), t.accentText()
	} else {
		fill, border, text = t.panel(), t.line(), t.text()
	}
	if down {
		fill = t.line()
	}
	radius := scale(6, a.settingsDPI)
	roundRect(hdc, r, radius, fill, border)
	if id == idProvider {
		dpi := a.settingsDPI
		labelRect := rect{r.Left + scale(10, dpi), r.Top, r.Right - scale(25, dpi), r.Bottom}
		drawText(hdc, label, labelRect, f.Small, text, dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis|dtNoPrefix)
		marker := rect{r.Right - scale(24, dpi), r.Top, r.Right - scale(8, dpi), r.Bottom}
		drawIcon(hdc, "chevron-down", t.muted(), iconRect(marker, scale(12, dpi)))
	} else {
		drawText(hdc, label, r, f.Body, text, dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
	}
	if focus {
		w := scale(2, a.settingsDPI)
		accent := t.text()
		if !primary {
			accent = t.accent()
		}
		for _, fr := range []rect{
			{r.Left - w, r.Top - w, r.Right + w, r.Top},
			{r.Left - w, r.Bottom, r.Right + w, r.Bottom + w},
			{r.Left - w, r.Top, r.Left, r.Bottom},
			{r.Right, r.Top, r.Right + w, r.Bottom},
		} {
			fillRect(hdc, fr, accent)
		}
	}
}

func (a *App) onSettingsCommand(id int) {
	if tab := id - idFirstTab; tab >= 0 && tab < len(settingsTabs) {
		a.settingsTab = tab
		a.applySettingsTab()
		return
	}
	switch id {
	case idAdd:
		a.addSettingsTarget()
	case idDelete:
		a.deleteSettingsTarget()
	case idPrimary:
		a.makeTargetPrimary()
	case idSave:
		a.saveSettingsUI()
	case idTest:
		a.testModelConnection()
	case idClearKey:
		a.clearStoredKey()
	case idProvider:
		a.showModelChoiceMenu()
	case idAppearance:
		a.toggleAppearance()
	case idClose, idCancel, 2: // IDCANCEL from Esc
		a.closeSettings()
	}
}

func (a *App) selectedModelProvider() settings.ModelProvider {
	if a.settingsProtocol != "" {
		return a.settingsProtocol
	}
	return settings.DeepSeek
}

func (a *App) loadSettingsProfile(p settings.ModelProfile) {
	a.settingsProtocol = p.Provider
	setText(a.settingsProvider, a.modelChoiceLabel(p.Provider))
	setText(a.settingsEndpoint, p.BaseURL)
	setText(a.settingsModel, p.Model)
	a.onModelDraftChanged(idEndpoint)
}

func (a *App) modelChoiceLabel(p settings.ModelProvider) string {
	switch p {
	case settings.DeepSeek:
		return "DeepSeek · Chat Completions"
	case settings.ChatAPI:
		return "OpenAI · Chat Completions"
	case settings.Responses:
		return "OpenAI · Responses"
	case settings.Messages:
		return "Anthropic · Messages"
	}
	return ""
}

func (a *App) showModelChoiceMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	for i, p := range modelProviders {
		flags := uint32(mfString)
		if p == a.selectedModelProvider() {
			flags |= mfChecked
		}
		appendMenu(menu, flags, uintptr(i+1), a.modelChoiceLabel(p))
	}
	position := windowRect(a.settingsProvider)
	command, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd, uintptr(position.Left), uintptr(position.Bottom), 0, a.settingsUI, 0)
	if command > 0 && command <= uintptr(len(modelProviders)) {
		a.switchSettingsProvider(modelProviders[command-1])
	}
}

func (a *App) switchSettingsProvider(p settings.ModelProvider) {
	current := a.cfg.Get().ModelProfile()
	if p == current.Provider {
		a.loadSettingsProfile(current)
		return
	}
	model := ""
	if p == settings.DeepSeek {
		model = a.cfg.Get().DeepSeekModel
	}
	a.loadSettingsProfile(settings.ModelProfile{Provider: p, BaseURL: p.DefaultURL(), Model: model})
}

func (a *App) onModelDraftChanged(field int) {
	a.settingsTestText = ""
	a.settingsKeyErr = ""
	a.settingsDraftRevision++
	if field == idEndpoint {
		scope := string(a.selectedModelProvider()) + "\n" + strings.TrimSpace(getText(a.settingsEndpoint))
		if a.settingsKeyScope != scope {
			a.settingsKeyScope = scope
			setText(a.settingsKey, "")
		}
	}
	invalidate(a.settingsUI)
}

func (a *App) settingsProfile() (settings.ModelProfile, error) {
	return (settings.ModelProfile{
		Provider: a.selectedModelProvider(),
		BaseURL:  getText(a.settingsEndpoint),
		Model:    getText(a.settingsModel),
	}).Normalize()
}

func (a *App) addSettingsTarget() {
	t := strings.TrimSpace(getText(a.settingsTarget))
	if t == "" {
		a.settingsTargetErr = a.tr("目标不能为空。", "A target is required.")
		invalidate(a.settingsUI)
		return
	}
	if err := settings.ValidateTarget(t); err != nil {
		a.settingsTargetErr = err.Error()
		invalidate(a.settingsUI)
		return
	}
	if len(a.settingsTargets) >= settings.MaxRTTTargets {
		a.settingsTargetErr = a.tr("最多 8 个 RTT 目标。", "Up to eight RTT targets are supported.")
		invalidate(a.settingsUI)
		return
	}
	for _, old := range a.settingsTargets {
		if strings.EqualFold(old, t) {
			a.settingsTargetErr = a.tr("目标已存在。", "That target already exists.")
			invalidate(a.settingsUI)
			return
		}
	}
	a.settingsTargets = append(a.settingsTargets, t)
	setText(a.settingsTarget, "")
	a.settingsTargetErr = ""
	a.refreshSettingsList()
	a.settingsTestText = a.tr("已添加；点击保存后生效。", "Added. Save to apply.")
	a.settingsTestOK = true
	invalidate(a.settingsUI)
}

func (a *App) deleteSettingsTarget() {
	sel, _, _ := procSendMessageW.Call(a.settingsList, lbGetCurSel, 0, 0)
	i := int(int32(sel))
	if i < 0 || i >= len(a.settingsTargets) {
		a.settingsTargetErr = a.tr("请选择一个目标。", "Select a target.")
		invalidate(a.settingsUI)
		return
	}
	a.settingsTargets = append(a.settingsTargets[:i], a.settingsTargets[i+1:]...)
	a.refreshSettingsList()
	a.settingsTargetErr = ""
	a.settingsTestText = a.tr("已删除；点击保存后生效。", "Removed. Save to apply.")
	a.settingsTestOK = true
	invalidate(a.settingsUI)
}

// makeTargetPrimary moves the selected draft row to the head of the list.
func (a *App) makeTargetPrimary() {
	sel, _, _ := procSendMessageW.Call(a.settingsList, lbGetCurSel, 0, 0)
	i := int(int32(sel))
	if i <= 0 || i >= len(a.settingsTargets) {
		if i == 0 {
			a.settingsTargetErr = a.tr("该目标已经是主要目标。", "This is already the primary target.")
		} else {
			a.settingsTargetErr = a.tr("请选择一个目标。", "Select a target.")
		}
		invalidate(a.settingsUI)
		return
	}
	a.settingsTargets = settings.MoveTarget(a.settingsTargets, i, 0)
	a.refreshSettingsList()
	a.settingsTargetErr = ""
	a.settingsTestText = a.tr("已设为主要目标；点击保存后生效。", "Set as primary. Save to apply.")
	a.settingsTestOK = true
	invalidate(a.settingsUI)
}

// showPrimaryMenu is the row context menu (设为主要目标 / 删除).
func (a *App) showPrimaryMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendMenu(menu, mfString, 300, a.tr("设为主要目标", "Make primary"))
	appendMenu(menu, mfString, 301, a.tr("删除", "Remove"))
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(a.settingsUI)
	// Select the row under the cursor before acting on it.
	clientPt := screenToClient(a.settingsList, pt)
	lp := uintptr(uint32(uint16(clientPt.X&0xffff)) | uint32(uint16(clientPt.Y&0xffff))<<16)
	idx, _, _ := procSendMessageW.Call(a.settingsList, lbItemFromPoint, 0, lp)
	if uint16(idx>>16) == 0 && int(uint16(idx)) < len(a.settingsTargets) {
		procSendMessageW.Call(a.settingsList, lbSetCurSel, uintptr(uint16(idx)), 0)
	}
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, a.settingsUI, 0)
	procPostMessageW.Call(a.settingsUI, 0, 0, 0)
	switch cmd {
	case 300:
		a.makeTargetPrimary()
	case 301:
		a.deleteSettingsTarget()
	}
}

func (a *App) clearStoredKey() {
	profile, err := a.settingsProfile()
	if err != nil {
		a.settingsKeyErr = err.Error()
		invalidate(a.settingsUI)
		return
	}
	if err := a.diag.ClearPersistentKeyFor(profile); err != nil {
		a.settingsKeyErr = a.tr("清除 Key 失败：", "Could not clear the key: ") + err.Error()
		a.settingsTestOK = false
		a.settingsTestText = ""
		invalidate(a.settingsUI)
		return
	}
	a.settingsKeyErr = ""
	setText(a.settingsKey, "")
	a.settingsTestText = a.tr("已清除此接口保存的 Key。", "Cleared the saved key for this endpoint.")
	a.settingsTestOK = true
	invalidate(a.settingsUI)
}

func (a *App) saveSettingsUI() {
	profile, err := a.settingsProfile()
	if err != nil {
		a.settingsKeyErr = err.Error()
		a.settingsTab = tabDeepSeek
		a.applySettingsTab()
		return
	}
	seconds, err := parseSettingsSeconds(getText(a.settingsSeconds))
	if err != nil {
		a.settingsSecErr = err.Error()
		invalidate(a.settingsUI)
		return
	}
	a.settingsSecErr = ""
	if _, err := settings.NormalizeTargets(a.settingsTargets); err != nil {
		a.settingsTargetErr = err.Error()
		invalidate(a.settingsUI)
		return
	}
	v := a.cfg.Get()
	v.RTTTargets = append([]string(nil), a.settingsTargets...)
	v.AIWindowSeconds = seconds
	v.AI = &profile
	v.RecoveryFolder = strings.TrimSpace(getText(a.settingsRecovery))
	if err := settings.ValidateRecoveryFolder(v.RecoveryFolder); err != nil {
		a.settingsKeyErr = a.tr("请输入已存在目录的本机绝对路径。", "Enter the absolute path to an existing local folder.")
		a.settingsTab = tabRecovery
		a.applySettingsTab()
		return
	}
	if err := a.cfg.Save(v); err != nil {
		a.settingsKeyErr = a.tr("保存设置失败：", "Could not save settings: ") + err.Error()
		invalidate(a.settingsUI)
		return
	}
	a.aiSeconds = seconds
	key := strings.TrimSpace(getText(a.settingsKey))
	if key != "" {
		if err := a.diag.SetPersistentKeyFor(profile, key); err != nil {
			// Drafts survive a failed key save; the user can retry.
			a.settingsKeyErr = a.tr("设置已保存，但 Key 保存失败：", "Settings saved. Could not save the key: ") + err.Error()
			a.settingsTestOK = false
			invalidate(a.settingsUI)
			return
		}
		setText(a.settingsKey, "")
	}
	// Sampling and diagnosis configuration refresh only after a successful
	// save (the sampler reads targets through the live config getter).
	a.settingsTargets = append([]string(nil), v.RTTTargets...)
	a.closeSettings()
	invalidate(a.panel)
}

func parseSettingsSeconds(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, errors.New("分析秒数必须是整数")
	}
	if err := settings.ValidateAIWindow(n); err != nil {
		return 0, err
	}
	return n, nil
}

func (a *App) testModelConnection() {
	profile, err := a.settingsProfile()
	if err != nil {
		a.settingsKeyErr = err.Error()
		invalidate(a.settingsUI)
		return
	}
	a.testMu.Lock()
	if a.testBusy {
		a.testMu.Unlock()
		return
	}
	a.testBusy = true
	revision := a.settingsDraftRevision
	a.testMu.Unlock()
	key := strings.TrimSpace(getText(a.settingsKey))
	a.settingsTestText = a.tr("正在验证凭据…", "Checking credentials…")
	a.settingsTestOK = true
	invalidate(a.settingsUI)
	window := a.settingsUI
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
		defer cancel()
		err := a.diag.TestConnectionFor(ctx, profile, key)
		a.testMu.Lock()
		a.testBusy = false
		a.testOK = err == nil
		a.testRevision = revision
		if err != nil {
			a.testResult = err.Error()
		} else {
			a.testResult = ""
		}
		a.testMu.Unlock()
		procPostMessageW.Call(window, wmAppTestDone, 0, 0)
	}()
}

func (a *App) finishKeyTest() {
	a.testMu.Lock()
	ok := a.testOK
	result := a.testResult
	revision := a.testRevision
	a.testMu.Unlock()
	if revision != a.settingsDraftRevision {
		return
	}
	// The test result lives in its own status line and never overwrites the
	// key draft, the seconds draft or the target drafts.
	a.settingsTestOK = ok
	if ok {
		a.settingsTestText = a.tr("凭据验证成功。", "Credential check passed.")
	} else {
		a.settingsTestText = a.tr("连接失败：", "Connection failed: ") + result
	}
	invalidate(a.settingsUI)
}
