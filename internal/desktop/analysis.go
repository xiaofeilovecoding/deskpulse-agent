//go:build windows

package desktop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/diagnose"
)

// Analysis page geometry in DIP (spec: ~480×420, sections 结论/证据/建议/数据局限).
const (
	analysisWDIP    = 480
	analysisHDIP    = 420
	analysisMargin  = 20
	analysisTitleH  = 40
	analysisPresetH = 36
	analysisStatusH = 40
)

// aiStateKind enumerates the analysis page states. Every state has explicit
// buttons so the result area never spins forever.
type aiStateKind int

const (
	aiIdle aiStateKind = iota
	aiRunning
	aiCompleted
	aiCanceled
	aiFailed
	aiUnconfigured
	aiSampling
)

type aiButtonID int

const (
	abNone aiButtonID = iota
	abCancel
	abRetry
	abCopy
	abOpenSettings
	abPreset
)

type aiButton struct {
	ID    aiButtonID
	Label string
	Rect  rect
}

// resultLine is one pre-wrapped line of the report view.
type resultLine struct {
	Text string
	Kind byte // 'h' header, 'b' body, 'm' muted, 'a' amber
}

func (a *App) createAnalysisPage() (uintptr, error) {
	dpi := systemDPI()
	w, h := scale(analysisWDIP, dpi), scale(analysisHDIP, dpi)
	hwnd, err := createWindow(
		wsExToolWindow|wsExTopmost,
		"DeskPulse Agent AI 分析",
		wsPopup,
		0, 0, w, h,
		0, 0, a.instance, 0,
	)
	if err != nil {
		return 0, err
	}
	a.analysisUI = hwnd
	setRoundRegion(hwnd, w, h, scale(10, dpi))
	return hwnd, nil
}

// openAnalysis opens the page anchored at the popup position. With autostart
// (the 主弹层 AI 分析 entry) it immediately begins a fresh overview analysis —
// a failed or canceled previous run does not swallow the click; only an
// already-running task is left alone.
func (a *App) openAnalysis(autostart bool) {
	if a.analysisUI == 0 {
		hwnd, err := a.createAnalysisPage()
		if err != nil {
			a.logger.Warn("create analysis page failed", "error", err.Error())
			return
		}
		_ = hwnd
	}
	a.applyAnalysisFrame()
	a.aiFocus = -1
	if autostart {
		// The popup entry always analyzes with the overview preset.
		a.aiPreset = diagnose.PresetOverview
	}
	showWindow(a.analysisUI, swShow)
	procSetForegroundWindow.Call(a.analysisUI)
	setFocus(a.analysisUI)
	setTimer(a.analysisUI, timerAIPoll, 500)
	if autostart && a.aiState.kind != aiRunning {
		a.startAnalysis()
	}
	invalidate(a.analysisUI)
}

func (a *App) closeAnalysis() {
	if a.analysisUI != 0 {
		killTimer(a.analysisUI, timerAIPoll)
		showWindow(a.analysisUI, swHide)
	}
}

func (a *App) applyAnalysisFrame() {
	dpi := dpiForWindow(a.ball)
	if dpi == 0 {
		dpi = systemDPI()
	}
	w, h := scale(analysisWDIP, dpi), scale(analysisHDIP, dpi)
	x, y, _ := a.anchoredRect(w, h, dpi)
	moveWindow(a.analysisUI, x, y, w, h)
	setRoundRegion(a.analysisUI, w, h, scale(10, dpi))
}

func (a *App) handleAnalysisPage(message uint32, wparam, lparam uintptr) (bool, uintptr) {
	switch message {
	case wmPaint:
		a.paintAnalysis()
		return true, 0
	case wmEraseBkgnd:
		return true, 1
	case wmTimer:
		if int(wparam) == timerAIPoll {
			a.pollAI()
		}
		return true, 0
	case wmClose:
		a.closeAnalysis()
		return true, 0
	case wmKeyDown:
		if wparam == 0x1B { // Escape
			a.closeAnalysis()
			return true, 0
		}
		a.handleAnalysisKey(wparam)
		return true, 0
	case wmLButtonDown:
		if hwnd := getFocus(); hwnd == 0 || hwnd == a.analysisUI {
			setFocus(a.analysisUI)
		}
		return true, 0
	case wmLButtonUp:
		x, y := lparamPoint(lparam)
		a.handleAnalysisClick(x, y)
		return true, 0
	case wmMouseWheel:
		delta := int16(wparam >> 16)
		a.aiWheelRem += int(delta)
		lines := a.aiWheelRem / 120 * 3
		a.aiWheelRem -= lines / 3 * 120
		if lines != 0 {
			a.aiScroll += int32(lines) * scale(analysisLineH, a.dpiForAnalysis())
			if a.aiScroll < 0 {
				a.aiScroll = 0
			}
			if max := a.aiScrollMax(); a.aiScroll > max {
				a.aiScroll = max
			}
			invalidate(a.analysisUI)
		}
		return true, 0
	case wmDpiChanged:
		a.applyAnalysisFrame()
		a.aiLinesDPI = 0 // re-wrap for the new metrics
		invalidate(a.analysisUI)
		return true, 0
	}
	return false, 0
}

// dpiForAnalysis returns the DPI the page actually paints with: the buffer's
// DPI when a paint buffer exists (it honors the capture override), then the
// capture override, then the window DPI. Buttons, hot rects and text layout
// must all use this one value or high-DPI layouts overlap.
func (a *App) dpiForAnalysis() uint32 {
	if b := a.buffers[a.analysisUI]; b != nil && b.dpi != 0 {
		return b.dpi
	}
	if a.captureDPI != 0 {
		return a.captureDPI
	}
	if a.analysisUI != 0 {
		if dpi := dpiForWindow(a.analysisUI); dpi != 0 {
			return dpi
		}
	}
	return systemDPI()
}

const analysisLineH = 22

// startAnalysis runs one analysis. A click lands in exactly one of three
// places: running, sampling (no data yet) or unconfigured (missing key).
func (a *App) startAnalysis() {
	if a.aiState.kind == aiRunning {
		return // double-click guard: one task at a time
	}
	st := a.diag.Status()
	if !st.Configured {
		a.setAIState(aiUnconfigured, "未配置 DeepSeek API Key")
		invalidate(a.analysisUI)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	preview, err := a.diag.PreviewSeconds(ctx, a.aiSeconds, false)
	cancel()
	if err != nil || len(preview.Summary.Evidence) == 0 {
		a.setAIState(aiSampling, "正在采样，稍后重试")
		invalidate(a.analysisUI)
		return
	}
	details := fmt.Sprintf("接收方：%s\n范围：最近 %d 秒、%d 项脱敏指标证据。\n\n%s\n\n发送本次诊断？", st.BaseURL, a.aiSeconds, preview.EvidenceCount, preview.WireJSON)
	if len([]rune(details)) > 2400 {
		details = fmt.Sprintf("接收方：%s\n范围：最近 %d 秒、%d 项脱敏指标证据。\n\n发送本次诊断？", st.BaseURL, a.aiSeconds, preview.EvidenceCount)
	}
	if !a.confirm("DeskPulse Agent", details) {
		return
	}
	id, err := a.diag.StartPreset(a.aiSeconds, a.aiPreset, false)
	if err != nil {
		if err == diagnose.ErrBusy {
			a.setAIState(aiFailed, "已有分析正在进行，请等待完成或取消")
		} else {
			a.setAIState(aiFailed, "无法开始分析："+err.Error())
		}
		invalidate(a.analysisUI)
		return
	}
	a.aiState = aiViewState{
		kind:    aiRunning,
		runID:   id,
		preset:  a.aiPreset,
		seconds: a.aiSeconds,
		status:  "分析中 · " + diagnose.PresetName(a.aiPreset) + " · " + fmtWindow(a.aiSeconds),
	}
	a.aiPollFails = 0
	invalidate(a.analysisUI)
}

func (a *App) setAIState(kind aiStateKind, status string) {
	a.aiState.kind = kind
	a.aiState.status = status
	a.aiState.runID = ""
}

func (a *App) cancelAnalysis() {
	if a.aiState.kind != aiRunning || a.aiState.runID == "" {
		return
	}
	if a.diag.Cancel(a.aiState.runID) {
		a.aiState.status = "正在取消…"
	}
	invalidate(a.analysisUI)
}

// pollAI follows the running task once per timer tick.
func (a *App) pollAI() {
	if a.aiState.kind != aiRunning || a.aiState.runID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d, err := a.diag.Get(ctx, a.aiState.runID)
	if err != nil {
		a.aiPollFails++
		if a.aiPollFails >= 3 {
			a.setAIState(aiFailed, "读取分析状态失败")
			invalidate(a.analysisUI)
		}
		return
	}
	switch d.Status {
	case "running":
		return
	case "completed":
		report, perr := diagnose.ParseStoredReport(d.ReportJSON)
		if perr != nil {
			a.setAIState(aiFailed, "本地报告无法解析")
		} else {
			a.aiState.kind = aiCompleted
			a.aiState.status = "完成 · " + diagnose.PresetName(report.Preset) + " · " + windowLabel(report)
			a.aiState.report = &report
			a.aiCompletedID = d.ID
			a.aiState.runID = ""
			a.rebuildAnalysisLines(&report)
		}
	case "canceled":
		a.setAIState(aiCanceled, "已取消")
	default:
		msg := d.ErrorMessage
		if msg == "" {
			msg = d.ErrorCode
		}
		a.setAIState(aiFailed, "分析失败："+msg)
	}
	invalidate(a.analysisUI)
}

// rebuildAnalysisLines pre-wraps the report into display lines for the
// current window width. Sections render in spec order and empty sections are
// omitted.
func (a *App) rebuildAnalysisLines(report *diagnose.Report) {
	dpi := a.dpiForAnalysis()
	a.aiLinesDPI = dpi
	a.aiLines = analysisLines(report, int32(scale(analysisWDIP, dpi)-scale(2*analysisMargin, dpi)), dpi)
	a.aiScroll = 0
}

// analysisLines renders the validated report. Aliases are back-filled with
// real local names; this stays on the machine.
func analysisLines(report *diagnose.Report, widthPx int32, dpi uint32) []resultLine {
	var out []resultLine
	add := func(text string, kind byte) { out = append(out, resultLine{Text: text, Kind: kind}) }
	add("结论", 'h')
	add(backfillAliases(report.Summary, report.Aliases), 'b')
	if len(report.Findings) > 0 {
		add("证据", 'h')
		catalog := map[string]diagnose.Evidence{}
		for _, e := range report.Evidence {
			catalog[e.ID] = e
		}
		for _, f := range report.Findings {
			cause := backfillAliases(f.Cause, report.Aliases)
			if f.Unverified || len(f.EvidenceIDs) == 0 {
				add("· "+cause+"（待验证）", 'a')
				continue
			}
			add("· "+cause, 'b')
			for _, id := range f.EvidenceIDs {
				if e, ok := catalog[id]; ok {
					add("  "+evidenceHumanLine(e, report.Aliases), 'm')
				}
			}
		}
	}
	if len(report.NextSteps) > 0 {
		add("建议", 'h')
		for _, s := range report.NextSteps {
			add("· "+backfillAliases(s, report.Aliases), 'b')
		}
	}
	if len(report.DataGaps) > 0 {
		add("数据局限", 'h')
		for _, s := range report.DataGaps {
			add("· "+s, 'a')
		}
	}
	return wrapLines(out, widthPx, dpi)
}

// wrapLines hard-wraps body lines at the content width using the body font.
func wrapLines(in []resultLine, widthPx int32, dpi uint32) []resultLine {
	f := newFontSet(dpi)
	defer f.delete()
	hdc, bmp, old, _ := createBufferDC(1, 1)
	if hdc == 0 {
		return in
	}
	defer releaseBufferDC(hdc, bmp, old)
	var out []resultLine
	for _, l := range in {
		font := f.Body
		if l.Kind == 'h' {
			font = f.Title
		}
		w, _ := measureText(hdc, l.Text, font)
		if int32(w) <= widthPx {
			out = append(out, l)
			continue
		}
		// Character-accurate wrapping for CJK: cut at width, breaking at the
		// last space when one is inside the final chunk.
		runes := []rune(l.Text)
		line := ""
		for i := 0; i < len(runes); i++ {
			cand := line + string(runes[i])
			cw, _ := measureText(hdc, cand, font)
			if int32(cw) > widthPx && line != "" {
				out = append(out, resultLine{Text: line, Kind: l.Kind})
				line = string(runes[i])
				continue
			}
			line = cand
		}
		if line != "" {
			out = append(out, resultLine{Text: line, Kind: l.Kind})
		}
	}
	return out
}

func (a *App) aiScrollMax() int32 {
	b := a.buffers[a.analysisUI]
	if b == nil || len(a.aiLines) == 0 {
		return 0
	}
	content := int32(len(a.aiLines)) * scale(analysisLineH, b.dpi)
	visible := b.h - scale(analysisTitleH+analysisPresetH+analysisStatusH, b.dpi) - scale(12, b.dpi)
	if content <= visible {
		return 0
	}
	return content - visible
}

func (a *App) handleAnalysisKey(vk uintptr) {
	btns := a.aiButtonsFor()
	n := len(btns)
	if n == 0 {
		return
	}
	switch vk {
	case 0x09: // Tab
		if a.aiFocus < 0 {
			a.aiFocus = 0
		} else {
			a.aiFocus = (a.aiFocus + 1) % n
		}
		invalidate(a.analysisUI)
	case 0x0D, 0x20: // Enter/Space
		if a.aiFocus >= 0 && a.aiFocus < n {
			a.doAIButton(btns[a.aiFocus].ID)
		}
	}
}

func (a *App) handleAnalysisClick(x, y int32) {
	b := a.buffers[a.analysisUI]
	if b == nil {
		return
	}
	m := scale(analysisMargin, b.dpi)
	closeBox := rect{b.w - m - scale(28, b.dpi), scale(12, b.dpi), b.w - m, scale(12, b.dpi) + scale(28, b.dpi)}
	if pointIn(x, y, closeBox) {
		a.closeAnalysis()
		return
	}
	moreBox := rect{b.w - m - scale(28, b.dpi)*2 - scale(4, b.dpi), scale(12, b.dpi), b.w - m - scale(28, b.dpi) - scale(4, b.dpi), scale(12, b.dpi) + scale(28, b.dpi)}
	if pointIn(x, y, moreBox) {
		a.showAIMoreMenu()
		return
	}
	for _, btn := range a.aiButtonsFor() {
		if btn.ID != abNone && pointIn(x, y, btn.Rect) {
			a.doAIButton(btn.ID)
			return
		}
	}
}

func (a *App) doAIButton(id aiButtonID) {
	switch id {
	case abCancel:
		a.cancelAnalysis()
	case abRetry:
		a.startAnalysis()
	case abCopy:
		if a.aiState.report != nil {
			clipboardSetText(reportText(*a.aiState.report))
			a.aiState.status = "已复制到剪贴板 · " + a.aiState.status
		}
	case abOpenSettings:
		a.openSettings()
	case abPreset:
		a.showPresetMenu()
	}
	invalidate(a.analysisUI)
}

func (a *App) showPresetMenu() {
	if a.analysisUI == 0 {
		return
	}
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	for i, p := range diagnose.Presets {
		flags := uintptr(mfString)
		if p.ID == a.aiPreset {
			flags |= mfChecked
		}
		appendMenu(menu, uint32(flags), uintptr(100+i), p.Name)
	}
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(a.analysisUI)
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, a.analysisUI, 0)
	procPostMessageW.Call(a.analysisUI, 0, 0, 0)
	idx := int(cmd) - 100
	if idx >= 0 && idx < len(diagnose.Presets) {
		// Switching a preset only updates the selection; the user explicitly
		// re-runs with 重新分析.
		a.aiPreset = diagnose.Presets[idx].ID
	}
}

func (a *App) showAIMoreMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendMenu(menu, mfString, 200, "查看历史")
	appendMenu(menu, mfString, 201, "导出当前报告")
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(a.analysisUI)
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, a.analysisUI, 0)
	procPostMessageW.Call(a.analysisUI, 0, 0, 0)
	switch cmd {
	case 200:
		a.openHistory()
	case 201:
		a.exportCurrentReport()
	}
}

// exportCurrentReport writes the completed report as local JSON under the
// data directory. Keys are never exported.
func (a *App) exportCurrentReport() {
	if a.aiCompletedID == "" || a.aiState.report == nil {
		a.aiState.status = "还没有可导出的报告"
		invalidate(a.analysisUI)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := a.diag.Export(ctx, a.aiCompletedID)
	if err != nil {
		a.aiState.status = "导出失败：" + err.Error()
		invalidate(a.analysisUI)
		return
	}
	dir := filepath.Join(a.dataDir, "exports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		a.aiState.status = "创建导出目录失败：" + err.Error()
		invalidate(a.analysisUI)
		return
	}
	path := filepath.Join(dir, "deskpulse-diagnosis-"+a.aiCompletedID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		a.aiState.status = "写入导出文件失败：" + err.Error()
		invalidate(a.analysisUI)
		return
	}
	a.aiState.status = "已导出：" + filepath.Base(path)
	invalidate(a.analysisUI)
}

// aiButtonsFor returns the visible action buttons for the current state.
func (a *App) aiButtonsFor() []aiButton {
	b := a.buffers[a.analysisUI]
	dpi := a.dpiForAnalysis()
	var w int32
	if b != nil {
		w = b.w
	} else {
		w = scale(analysisWDIP, dpi)
	}
	m := scale(analysisMargin, dpi)
	statusTop := scale(analysisTitleH+analysisPresetH, dpi)
	statusBot := statusTop + scale(analysisStatusH, dpi)
	btnH := scale(28, dpi)
	btnTop := statusTop + (statusBot-statusTop-btnH)/2
	x := w - m
	var out []aiButton
	addBtn := func(id aiButtonID, label string, widthDIP int, primary bool) {
		_ = primary
		wpx := scale(widthDIP, dpi)
		x -= wpx
		out = append(out, aiButton{ID: id, Label: label, Rect: rect{x, btnTop, x + wpx, btnTop + btnH}})
		x -= scale(8, dpi)
	}
	s := a.aiState.kind
	switch s {
	case aiRunning:
		addBtn(abCancel, "取消", 64, false)
	case aiCompleted:
		addBtn(abCopy, "复制", 64, false)
		addBtn(abRetry, "重新分析", 92, false)
	case aiCanceled, aiSampling, aiFailed:
		if s == aiFailed && a.aiFailedUnconfigured() {
			addBtn(abOpenSettings, "打开设置", 92, false)
		}
		addBtn(abRetry, "重新分析", 92, false)
	case aiUnconfigured:
		addBtn(abOpenSettings, "打开设置", 92, false)
	case aiIdle:
		addBtn(abRetry, "重新分析", 92, false)
	}
	return out
}

// aiFailedUnconfigured reports whether a failure was caused by a missing key
// so the page can offer 打开设置.
func (a *App) aiFailedUnconfigured() bool {
	return !a.diag.Status().Configured
}

func (a *App) paintAnalysis() {
	a.paintWindow(a.analysisUI, a.paintAnalysisInto)
}

func (a *App) paintAnalysisInto(b *windowBuffer) {
	dpi := b.dpi
	f := a.fontsFor(dpi)
	t := a.theme
	fillRect(b.hdc, rect{0, 0, b.w, b.h}, t.bg())
	m := scale(analysisMargin, dpi)

	// Title row.
	titleR := rect{m, scale(12, dpi), b.w - m - scale(70, dpi), scale(12, dpi) + scale(analysisTitleH-12, dpi)}
	drawText(b.hdc, "AI 分析", titleR, f.TitleCJK, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
	closeBox := rect{b.w - m - scale(28, dpi), scale(12, dpi), b.w - m, scale(12, dpi) + scale(28, dpi)}
	moreBox := rect{b.w - m - scale(28, dpi)*2 - scale(4, dpi), scale(12, dpi), b.w - m - scale(28, dpi) - scale(4, dpi), scale(12, dpi) + scale(28, dpi)}
	drawIcon(b.hdc, "more", t.muted(), iconRect(moreBox, scale(18, dpi)))
	drawIcon(b.hdc, "dismiss", t.muted(), iconRect(closeBox, scale(16, dpi)))

	// Preset row: selector button + window label on the right.
	presetTop := scale(analysisTitleH, dpi)
	presetBtn := rect{m, presetTop, m + scale(150, dpi), presetTop + scale(32, dpi)}
	roundRect(b.hdc, presetBtn, scale(8, dpi), t.panel(), t.line())
	label := diagnose.PresetName(a.aiPreset)
	drawText(b.hdc, label, rect{presetBtn.Left + scale(12, dpi), presetBtn.Top, presetBtn.Right - scale(22, dpi), presetBtn.Bottom}, f.Body, t.text(), dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis|dtNoPrefix)
	chev := rect{presetBtn.Right - scale(20, dpi), presetBtn.Top + (presetBtn.Bottom-presetBtn.Top-scale(12, dpi))/2, presetBtn.Right - scale(8, dpi), 0}
	chev.Bottom = chev.Top + scale(12, dpi)
	drawIcon(b.hdc, "chevron-down", t.muted(), chev)
	secondsLabel := fmtWindow(a.aiSeconds)
	drawText(b.hdc, secondsLabel, rect{b.w - m - scale(120, dpi), presetTop, b.w - m, presetTop + scale(32, dpi)}, f.Small, t.muted(), dtRight|dtVCenter|dtSingleLine|dtNoPrefix)

	// Status row with the state's action buttons.
	statusTop := presetTop + scale(analysisPresetH, dpi)
	statusBot := statusTop + scale(analysisStatusH, dpi)
	statusColor := t.muted()
	switch a.aiState.kind {
	case aiCompleted, aiRunning:
		statusColor = t.text()
	case aiFailed, aiUnconfigured, aiSampling:
		statusColor = t.amber()
	}
	drawText(b.hdc, a.aiState.status, rect{m, statusTop, b.w - m - scale(220, dpi), statusBot}, f.Small, statusColor, dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis|dtNoPrefix)
	btns := a.aiButtonsFor()
	for i, btn := range btns {
		fill := t.panel()
		border := t.line()
		if a.aiFocus == i {
			border = t.accent()
		}
		roundRect(b.hdc, btn.Rect, scale(6, dpi), fill, border)
		drawText(b.hdc, btn.Label, btn.Rect, f.Small, t.text(), dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
	}

	// Result area. Re-wrap cached lines if the paint DPI differs from the
	// DPI they were laid out at (monitor DPI change or capture override).
	if a.aiState.report != nil && a.aiLinesDPI != dpi {
		a.rebuildAnalysisLines(a.aiState.report)
	}
	resTop := statusBot + scale(4, dpi)
	lineH := scale(analysisLineH, dpi)
	scroll := a.aiScroll
	if max := a.aiScrollMax(); scroll > max {
		scroll = max
	}
	if len(a.aiLines) == 0 {
		hint := "点击「AI 分析」后，结论、证据、建议和数据局限会显示在这里。"
		if a.aiState.kind == aiRunning {
			hint = "正在准备本地摘要并发送…"
		}
		drawText(b.hdc, hint, rect{m, resTop + scale(16, dpi), b.w - m, resTop + scale(40, dpi)}, f.Body, t.muted(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		return
	}
	first := int(scroll / lineH)
	yoff := resTop - scroll + int32(first)*lineH
	for i := first; i < len(a.aiLines); i++ {
		if yoff > b.h {
			break
		}
		l := a.aiLines[i]
		r := rect{m, yoff, b.w - m, yoff + lineH}
		switch l.Kind {
		case 'h':
			drawText(b.hdc, l.Text, r, f.Body, t.accent(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		case 'a':
			drawText(b.hdc, l.Text, r, f.Body, t.amber(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		case 'm':
			drawText(b.hdc, l.Text, r, f.Small, t.muted(), dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis|dtNoPrefix)
		default:
			drawText(b.hdc, l.Text, r, f.Body, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		}
		yoff += lineH
	}
}
