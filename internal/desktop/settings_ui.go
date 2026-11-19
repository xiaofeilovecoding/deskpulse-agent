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

	tabDeepSeek = 0
	tabNetwork  = 1

	lbAddString    = 0x0180
	lbResetContent = 0x0184
	lbSetCurSel    = 0x0186
	lbGetCurSel    = 0x0188
	lbGetCount     = 0x018B
)

func (a *App) openSettings() {
	if a.settingsUI == 0 {
		if err := a.createSettingsUI(); err != nil {
			a.logger.Warn("create settings window failed", "error", err.Error())
			showMessage("DeskPulse Agent", "打开设置失败："+err.Error(), mbOK|mbIconError)
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
	a.positionSettings()
	showWindow(a.settingsUI, swShow)
	procSetForegroundWindow.Call(a.settingsUI)
	setFocus(a.settingsKey)
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
		wsPopup,
		x, y, w, h,
		0, 0, a.instance, 0,
	)
	if err != nil {
		return err
	}
	a.settingsUI = hwnd
	setRoundRegion(hwnd, w, h, scale(10, dpi))
	a.settingsDPI = dpi

	a.settingsKey, err = createControl(
		"EDIT", "",
		wsChild|wsVisible|wsTabStop|wsBorder|esPassword|esAutoHScroll|esLeft,
		0, 0, 0, 0, 0, hwnd, idAPIKey, a.instance,
	)
	if err != nil {
		return err
	}
	a.settingsSeconds, err = createControl(
		"EDIT", "",
		wsChild|wsVisible|wsTabStop|wsBorder|esNumber|esLeft,
		0, 0, 0, 0, 0, hwnd, idSeconds, a.instance,
	)
	if err != nil {
		return err
	}
	a.settingsList, err = createControl(
		"LISTBOX", "",
		wsChild|wsVisible|wsTabStop|wsBorder|wsVScroll|lbsNotify|lbsNoIntegralHeight,
		0, 0, 0, 0, 0, hwnd, idTargetList, a.instance,
	)
	if err != nil {
		return err
	}
	a.settingsTarget, err = createControl(
		"EDIT", "",
		wsChild|wsVisible|wsTabStop|wsBorder|esAutoHScroll|esLeft,
		0, 0, 0, 0, 0, hwnd, idTargetEdit, a.instance,
	)
	if err != nil {
		return err
	}
	button := func(label string, id int32, btnW int) (uintptr, error) {
		// All children start visible; applySettingsTab hides the inactive
		// tab's controls. The footer 取消/保存 are tab-independent and must
		// always be present in the real window (and in WM_PRINT captures).
		return createControl(
			"BUTTON", label,
			wsChild|wsVisible|wsTabStop|bsOwnerDraw,
			0, 0, 0, 0, 0, hwnd, id, a.instance,
		)
	}
	if a.settingsTest, err = button("测试连接", idTest, 92); err != nil {
		return err
	}
	if a.settingsClear, err = button("清除 Key", idClearKey, 92); err != nil {
		return err
	}
	if a.settingsAdd, err = button("添加", idAdd, 64); err != nil {
		return err
	}
	if a.settingsDelete, err = button("删除", idDelete, 64); err != nil {
		return err
	}
	if a.settingsPrimary, err = button("设为主要", idPrimary, 92); err != nil {
		return err
	}
	if a.settingsSave, err = button("保存", idSave, 92); err != nil {
		return err
	}
	if a.settingsCancel, err = button("取消", idCancel, 80); err != nil {
		return err
	}

	setCueBanner(a.settingsKey, "留空以保留当前配置")
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
	w := scale(settingsWDIP, dpi)
	m := s(settingsMargin)
	contentW := w - 2*m
	btnH := s(32)

	keyW := contentW - s(92) - s(8)
	moveWindow(a.settingsKey, m, s(134), keyW, s(36))
	moveWindow(a.settingsTest, m+keyW+s(8), s(134), s(92), btnH)
	moveWindow(a.settingsClear, m+keyW+s(8), s(134)+btnH+s(8), s(92), btnH)
	moveWindow(a.settingsSeconds, w-m-s(64)-s(30), s(230), s(64), s(32))
	listH := s(150)
	moveWindow(a.settingsList, m, s(140), contentW, listH)
	moveWindow(a.settingsTarget, m, s(140)+listH+s(10), contentW-s(64)-s(8), s(30))
	moveWindow(a.settingsAdd, w-m-s(64), s(140)+listH+s(10), s(64), s(30))
	moveWindow(a.settingsDelete, m, s(140)+listH+s(10)+s(38), s(64), btnH)
	moveWindow(a.settingsPrimary, m+s(64)+s(8), s(140)+listH+s(10)+s(38), s(92), btnH)
	btnY := s(settingsHDIP - settingsFooterH + 10)
	moveWindow(a.settingsCancel, w-m-s(80)-s(8)-s(92), btnY, s(80), btnH)
	moveWindow(a.settingsSave, w-m-s(92), btnY, s(92), btnH)
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
	net := !deep
	show(a.settingsKey, deep)
	show(a.settingsTest, deep)
	show(a.settingsClear, deep)
	show(a.settingsSeconds, deep)
	show(a.settingsList, net)
	show(a.settingsTarget, net)
	show(a.settingsAdd, net)
	show(a.settingsDelete, net)
	show(a.settingsPrimary, net)
	// 取消/保存 stay visible on both tabs.
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
			label = t + "（主要）"
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
		a.onSettingsCommand(id)
		return true, 0
	case wmDrawItem:
		// The message carries a DRAWITEMSTRUCT pointer; copy it into a local
		// value through RtlMoveMemory (no uintptr→pointer conversion).
		var dis drawItemStruct
		memCopy(goPtr((*byte)(unsafe.Pointer(&dis))), lparam, unsafe.Sizeof(dis))
		a.paintOwnerDrawButton(&dis)
		return true, 1
	case wmAppTestDone:
		a.finishKeyTest()
		return true, 0
	case wmCtlColorEdit, wmCtlColorStatic, wmCtlColorListBox, wmCtlColorBtn:
		hdc := wparam
		procSetTextColor.Call(hdc, uintptr(colorText))
		procSetBkColor.Call(hdc, uintptr(colorPanel))
		return true, a.darkBrush
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
		b := a.buffers[a.settingsUI]
		if b == nil {
			return true, 0
		}
		dpi := b.dpi
		m := scale(settingsMargin, dpi)
		closeBox := rect{b.w - m - scale(28, dpi) + scale(4, dpi), scale(12, dpi), b.w - m + scale(4, dpi), scale(12, dpi) + scale(28, dpi)}
		if pointIn(x, y, closeBox) {
			a.closeSettings()
			return true, 0
		}
		tabY := scale(settingsTitleH, dpi)
		if y >= tabY && y < tabY+scale(settingsTabH, dpi) {
			tab2X := m + scale(110, dpi)
			if x >= m && x < m+scale(100, dpi) && a.settingsTab != tabDeepSeek {
				a.settingsTab = tabDeepSeek
				a.applySettingsTab()
			} else if x >= tab2X && x < tab2X+scale(100, dpi) && a.settingsTab != tabNetwork {
				a.settingsTab = tabNetwork
				a.applySettingsTab()
			}
		}
		return true, 0
	case wmDpiChanged:
		a.settingsDPI = systemDPI()
		a.applySettingsFrame()
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
	m := scale(settingsMargin, dpi)
	w := b.w

	// Title + close.
	titleR := rect{m, scale(12, dpi), w - m - scale(40, dpi), scale(12, dpi) + scale(settingsTitleH-12, dpi)}
	drawText(b.hdc, "设置", titleR, f.TitleCJK, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
	closeBox := rect{w - m - scale(28, dpi) + scale(4, dpi), scale(12, dpi), w - m + scale(4, dpi), scale(12, dpi) + scale(28, dpi)}
	drawIcon(b.hdc, "dismiss", t.muted(), iconRect(closeBox, scale(16, dpi)))

	// Tabs.
	tabY := scale(settingsTitleH, dpi)
	tab2X := m + scale(110, dpi)
	active := a.settingsTab == tabDeepSeek
	drawText(b.hdc, "DeepSeek", rect{m, tabY, m + scale(100, dpi), tabY + scale(settingsTabH, dpi)}, f.Body,
		tabColor(t, active), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
	if active {
		fillRect(b.hdc, rect{m, tabY + scale(settingsTabH, dpi) - 2, m + scale(76, dpi), tabY + scale(settingsTabH, dpi)}, t.accent())
	}
	active = a.settingsTab == tabNetwork
	drawText(b.hdc, "网络", rect{tab2X, tabY, tab2X + scale(100, dpi), tabY + scale(settingsTabH, dpi)}, f.Body,
		tabColor(t, active), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
	if active {
		fillRect(b.hdc, rect{tab2X, tabY + scale(settingsTabH, dpi) - 2, tab2X + scale(40, dpi), tabY + scale(settingsTabH, dpi)}, t.accent())
	}
	fillRect(b.hdc, rect{m, tabY + scale(settingsTabH, dpi), w - m, tabY + scale(settingsTabH, dpi) + 1}, t.line())

	contentTop := tabY + scale(settingsTabH, dpi) + scale(12, dpi)
	if a.settingsTab == tabDeepSeek {
		// API Key label + configured status.
		drawText(b.hdc, "API Key", rect{m, contentTop, w - m, contentTop + scale(24, dpi)}, f.Body, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		st := a.diag.Status()
		statusText := keySourceText(st.Source)
		if st.Error != "" {
			statusText = st.Error
		}
		drawText(b.hdc, statusText, rect{m, contentTop, w - m, contentTop + scale(24, dpi)}, f.Small, keyStatusColor(t, st.Configured), dtRight|dtVCenter|dtSingleLine|dtNoPrefix)
		// Field hints and inline errors.
		y := scale(176, dpi)
		drawText(b.hdc, a.settingsTestText, rect{m, y, w - m, y + scale(20, dpi)}, f.Small, testResultColor(t, a.settingsTestOK), dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis|dtNoPrefix)
		y = scale(206, dpi)
		drawText(b.hdc, "分析范围", rect{m, y, w - m, y + scale(24, dpi)}, f.Body, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		drawText(b.hdc, "秒", rect{w - m - scale(26, dpi), scale(230, dpi), w - m, scale(230, dpi) + scale(32, dpi)}, f.Body, t.muted(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		drawText(b.hdc, "分析最近这段时间的监测数据。", rect{m, y + scale(28, dpi), w - m, y + scale(48, dpi)}, f.Small, t.muted(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		if a.settingsSecErr != "" {
			drawText(b.hdc, a.settingsSecErr, rect{m, y + scale(52, dpi), w - m, y + scale(70, dpi)}, f.Small, t.amber(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		}
		if a.settingsKeyErr != "" {
			drawText(b.hdc, a.settingsKeyErr, rect{m, y - scale(10, dpi), w - m, y + scale(8, dpi)}, f.Small, t.amber(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		}
	} else {
		// Network tab: list hint and inline error.
		drawText(b.hdc, "RTT 目标（最多 8 个，第一个为主要目标）", rect{m, contentTop, w - m, contentTop + scale(24, dpi)}, f.Body, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		if a.settingsTargetErr != "" {
			drawText(b.hdc, a.settingsTargetErr, rect{m, scale(140, dpi) + scale(150, dpi) + scale(10, dpi) + scale(34, dpi), w - m, scale(140, dpi) + scale(150, dpi) + scale(10, dpi) + scale(52, dpi)}, f.Small, t.amber(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		}
	}
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
func buttonFaceFor(id int) (string, bool) {
	switch id {
	case idTest:
		return "测试连接", false
	case idClearKey:
		return "清除 Key", false
	case idAdd:
		return "添加", false
	case idDelete:
		return "删除", false
	case idPrimary:
		return "设为主要", false
	case idSave:
		return "保存", true
	case idCancel:
		return "取消", false
	}
	return "", false
}

// paintButtonFace is the single owner-draw button renderer used by both live
// WM_DRAWITEM handling and the capture compositor.
func (a *App) paintButtonFace(hdc uintptr, r rect, id int, focus, down bool) {
	label, primary := buttonFaceFor(id)
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
		fill = colorHoverRow
	}
	radius := scale(6, a.settingsDPI)
	roundRect(hdc, r, radius, fill, border)
	drawText(hdc, label, r, f.Body, text, dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
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
		a.testDeepSeek()
	case idClearKey:
		a.clearStoredKey()
	case idCancel, 2: // IDCANCEL from Esc
		a.closeSettings()
	}
}

func (a *App) addSettingsTarget() {
	t := strings.TrimSpace(getText(a.settingsTarget))
	if t == "" {
		a.settingsTargetErr = "目标不能为空。"
		invalidate(a.settingsUI)
		return
	}
	if err := settings.ValidateTarget(t); err != nil {
		a.settingsTargetErr = err.Error()
		invalidate(a.settingsUI)
		return
	}
	if len(a.settingsTargets) >= settings.MaxRTTTargets {
		a.settingsTargetErr = "最多 8 个 RTT 目标。"
		invalidate(a.settingsUI)
		return
	}
	for _, old := range a.settingsTargets {
		if strings.EqualFold(old, t) {
			a.settingsTargetErr = "目标已存在。"
			invalidate(a.settingsUI)
			return
		}
	}
	a.settingsTargets = append(a.settingsTargets, t)
	setText(a.settingsTarget, "")
	a.settingsTargetErr = ""
	a.refreshSettingsList()
	a.settingsTestText = "已添加；点击保存后生效。"
	a.settingsTestOK = true
	invalidate(a.settingsUI)
}

func (a *App) deleteSettingsTarget() {
	sel, _, _ := procSendMessageW.Call(a.settingsList, lbGetCurSel, 0, 0)
	i := int(int32(sel))
	if i < 0 || i >= len(a.settingsTargets) {
		a.settingsTargetErr = "请选择一个目标。"
		invalidate(a.settingsUI)
		return
	}
	a.settingsTargets = append(a.settingsTargets[:i], a.settingsTargets[i+1:]...)
	a.refreshSettingsList()
	a.settingsTargetErr = ""
	a.settingsTestText = "已删除；点击保存后生效。"
	a.settingsTestOK = true
	invalidate(a.settingsUI)
}

// makeTargetPrimary moves the selected draft row to the head of the list.
func (a *App) makeTargetPrimary() {
	sel, _, _ := procSendMessageW.Call(a.settingsList, lbGetCurSel, 0, 0)
	i := int(int32(sel))
	if i <= 0 || i >= len(a.settingsTargets) {
		if i == 0 {
			a.settingsTargetErr = "该目标已经是主要目标。"
		} else {
			a.settingsTargetErr = "请选择一个目标。"
		}
		invalidate(a.settingsUI)
		return
	}
	a.settingsTargets = settings.MoveTarget(a.settingsTargets, i, 0)
	a.refreshSettingsList()
	a.settingsTargetErr = ""
	a.settingsTestText = "已设为主要目标；点击保存后生效。"
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
	appendMenu(menu, mfString, 300, "设为主要目标")
	appendMenu(menu, mfString, 301, "删除")
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(a.settingsUI)
	// Select the row under the cursor before acting on it.
	clientPt := pt
	screenToClient(a.settingsList, clientPt)
	lp := uintptr(uint32(uint16(clientPt.X&0xffff)) | uint32(uint16(clientPt.Y&0xffff))<<16)
	idx, _, _ := procSendMessageW.Call(a.settingsList, lbItemFromPoint, 0, lp)
	_ = idx
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
	if err := a.diag.ClearPersistentKey(); err != nil {
		a.settingsKeyErr = "清除 Key 失败：" + err.Error()
		a.settingsTestOK = false
		a.settingsTestText = ""
		invalidate(a.settingsUI)
		return
	}
	a.settingsKeyErr = ""
	a.settingsTestText = "已清除 DPAPI 保存的 Key；环境变量回退不受影响。"
	a.settingsTestOK = true
	invalidate(a.settingsUI)
}

func (a *App) saveSettingsUI() {
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
	if err := a.cfg.Save(v); err != nil {
		a.settingsKeyErr = "保存设置失败：" + err.Error()
		invalidate(a.settingsUI)
		return
	}
	key := strings.TrimSpace(getText(a.settingsKey))
	if key != "" {
		if err := a.diag.SetPersistentKey(key); err != nil {
			// Drafts survive a failed key save; the user can retry.
			a.settingsKeyErr = "设置已保存，但 Key 保存失败：" + err.Error()
			a.settingsTestOK = false
			invalidate(a.settingsUI)
			return
		}
		setText(a.settingsKey, "")
	}
	a.aiSeconds = seconds
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

func (a *App) testDeepSeek() {
	a.testMu.Lock()
	if a.testBusy {
		a.testMu.Unlock()
		return
	}
	a.testBusy = true
	a.testMu.Unlock()
	key := strings.TrimSpace(getText(a.settingsKey))
	a.settingsTestText = "正在测试 DeepSeek 连接…"
	a.settingsTestOK = true
	invalidate(a.settingsUI)
	window := a.settingsUI
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
		defer cancel()
		err := a.diag.TestConnectionWithKey(ctx, key)
		a.testMu.Lock()
		a.testBusy = false
		a.testOK = err == nil
		if err != nil {
			a.testResult = err.Error()
		} else {
			a.testResult = "连接成功。"
		}
		a.testMu.Unlock()
		procPostMessageW.Call(window, wmAppTestDone, 0, 0)
	}()
}

func (a *App) finishKeyTest() {
	a.testMu.Lock()
	ok := a.testOK
	result := a.testResult
	a.testMu.Unlock()
	// The test result lives in its own status line and never overwrites the
	// key draft, the seconds draft or the target drafts.
	a.settingsTestOK = ok
	if ok {
		a.settingsTestText = "测试连接成功。" + keySourceText(a.diag.Status().Source)
	} else {
		a.settingsTestText = "测试连接失败：" + result
	}
	invalidate(a.settingsUI)
}
