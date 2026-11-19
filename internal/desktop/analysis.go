//go:build windows

package desktop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/diagnose"
)

// The diagnosis keeps a fixed toolbar and a scrollable report surface.
const (
	analysisWDIP    = 504
	analysisHDIP    = 480
	analysisMargin  = 24
	analysisTitleH  = 46
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
	abEvidence
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
		a.tr("DeskPulse Agent AI 分析", "DeskPulse Agent AI diagnosis"),
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
		a.setAIState(aiUnconfigured, a.tr("请在设置中配置模型凭据", "Set up a model credential in Settings"))
		invalidate(a.analysisUI)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	preview, err := a.diag.PreparePreset(ctx, a.aiSeconds, a.aiPreset, a.tr("zh-CN", "en"))
	cancel()
	if err != nil || preview.EvidenceCount() == 0 {
		message := a.tr("正在采样，稍后重试", "Waiting for samples. Try again shortly.")
		if err != nil {
			message = a.tr("无法预览诊断摘要：", "Could not preview the diagnosis: ") + err.Error()
		}
		a.setAIState(aiSampling, message)
		invalidate(a.analysisUI)
		return
	}
	profile := preview.Destination()
	details := fmt.Sprintf(a.tr("接收方：%s\n模型：%s\n范围：最近 %d 秒、%d 项脱敏证据。\n\n%s\n\n发送本次诊断？",
		"Recipient: %s\nModel: %s\nScope: last %d seconds, %d anonymized findings.\n\n%s\n\nSend this diagnosis?"),
		profile.BaseURL, profile.Model, preview.Seconds(), preview.EvidenceCount(), preview.WireJSON())
	if len([]rune(details)) > 2400 {
		details = fmt.Sprintf(a.tr("接收方：%s\n模型：%s\n范围：最近 %d 秒、%d 项脱敏证据。包含指标汇总和进程别名。\n\n发送本次诊断？",
			"Recipient: %s\nModel: %s\nScope: last %d seconds, %d findings with aggregate metrics and process aliases.\n\nSend this diagnosis?"),
			profile.BaseURL, profile.Model, preview.Seconds(), preview.EvidenceCount())
	}
	if !a.confirm("DeskPulse Agent", details) {
		return
	}
	id, err := a.diag.StartPrepared(preview)
	if err != nil {
		if err == diagnose.ErrBusy {
			a.setAIState(aiFailed, a.tr("已有分析正在进行，请等待完成或取消", "A diagnosis is running. Wait for it or cancel it."))
		} else {
			a.setAIState(aiFailed, a.tr("无法开始分析：", "Could not start diagnosis: ")+err.Error())
		}
		invalidate(a.analysisUI)
		return
	}
	a.aiState = aiViewState{
		kind:    aiRunning,
		runID:   id,
		preset:  a.aiPreset,
		seconds: a.aiSeconds,
		status:  a.tr("分析中 · ", "Diagnosing · ") + a.localizedPreset(a.aiPreset) + " · " + a.localizedWindow(a.aiSeconds),
	}
	a.aiEvidenceExpanded = false
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
		a.aiState.status = a.tr("正在取消…", "Cancelling…")
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
			a.setAIState(aiFailed, a.tr("读取分析状态失败", "Could not read diagnosis status"))
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
			a.setAIState(aiFailed, a.tr("本地报告无法解析", "Could not read the local report"))
		} else {
			a.aiState.kind = aiCompleted
			a.aiState.status = a.tr("完成 · ", "Complete · ") + a.localizedPreset(report.Preset) + " · " + a.localizedWindow(max(report.WindowSeconds, report.WindowMinutes*60))
			a.aiState.report = &report
			a.aiCompletedID = d.ID
			a.aiState.runID = ""
			a.rebuildAnalysisLines(&report)
		}
	case "canceled":
		a.setAIState(aiCanceled, a.tr("已取消", "Cancelled"))
	default:
		msg := d.ErrorMessage
		if msg == "" {
			msg = d.ErrorCode
		}
		a.setAIState(aiFailed, a.tr("分析失败：", "Diagnosis failed: ")+msg)
	}
	invalidate(a.analysisUI)
}

// rebuildAnalysisLines pre-wraps the report into display lines for the
// current window width. Sections render in spec order and empty sections are
// omitted.
func (a *App) rebuildAnalysisLines(report *diagnose.Report) {
	dpi := a.dpiForAnalysis()
	width := clientRect(a.analysisUI).Right
	if width <= 0 {
		width = scale(analysisWDIP, dpi)
	}
	a.aiLinesDPI = dpi
	a.aiLinesWidth = width
	a.aiLines = a.analysisLines(report, width-scale(2*analysisMargin, dpi), dpi)
	a.aiScroll = 0
}

// analysisLines renders the validated report. Aliases are back-filled with
// real local names; this stays on the machine.
func (a *App) analysisLines(report *diagnose.Report, widthPx int32, dpi uint32) []resultLine {
	var out []resultLine
	add := func(text string, kind byte) { out = append(out, resultLine{Text: text, Kind: kind}) }
	add(a.tr("结论", "Summary"), 'h')
	add(backfillAliases(report.Summary, report.Aliases), 's')
	add("", 'g')
	add(a.tr("证据", "Evidence"), 'e')
	if len(report.Findings) > 0 {
		catalog := map[string]diagnose.Evidence{}
		for _, e := range report.Evidence {
			catalog[e.ID] = e
		}
		for _, f := range report.Findings {
			cause := backfillAliases(f.Cause, report.Aliases)
			if f.Unverified || len(f.EvidenceIDs) == 0 {
				add("· "+cause+a.tr("（待验证）", " (unverified)"), 'a')
				continue
			}
			add("· "+cause, 'b')
			if a.aiEvidenceExpanded {
				for _, id := range f.EvidenceIDs {
					if e, ok := catalog[id]; ok {
						add("  "+evidenceHumanLine(e, report.Aliases), 'm')
					}
				}
			}
		}
	} else {
		add(a.tr("没有支持具体归因的发现。", "No finding supports a specific cause."), 'm')
	}
	add("", 'g')
	add(a.tr("下一步", "Next steps"), 'h')
	if len(report.NextSteps) > 0 {
		for _, s := range report.NextSteps {
			add("· "+backfillAliases(s, report.Aliases), 'b')
		}
	} else {
		add(a.tr("卡顿重现时复测。", "Sample again when the slowdown recurs."), 'm')
	}
	add("", 'g')
	add(a.tr("数据局限", "Evidence gaps"), 'h')
	if len(report.DataGaps) > 0 {
		for _, s := range report.DataGaps {
			add("· "+s, 'a')
		}
	} else {
		add(a.tr("报告未标记额外数据缺口。", "The report flagged no additional evidence gaps."), 'm')
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
		if l.Kind == 'm' {
			font = f.Small
		} else if l.Kind == 's' {
			font = f.valueFont(l.Text)
		}
		w, _ := measureText(hdc, l.Text, font)
		if int32(w) <= widthPx {
			out = append(out, l)
			continue
		}
		runes := []rune(l.Text)
		for len(runes) != 0 {
			end := max(1, sort.Search(len(runes), func(i int) bool {
				w, _ := measureText(hdc, string(runes[:i+1]), font)
				return w > widthPx
			}))
			if end < len(runes) {
				for i := end - 1; i > end/2; i-- {
					if runes[i] == ' ' {
						end = i
						break
					}
				}
			}
			out = append(out, resultLine{Text: strings.TrimRight(string(runes[:end]), " "), Kind: l.Kind})
			runes = runes[end:]
			for len(runes) != 0 && runes[0] == ' ' {
				runes = runes[1:]
			}
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
	btns := a.analysisFocusButtons()
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
	for _, btn := range a.analysisFocusButtons() {
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
			a.aiState.status = a.tr("已复制到剪贴板 · ", "Copied · ") + a.aiState.status
		}
	case abOpenSettings:
		a.openSettings()
	case abPreset:
		a.showPresetMenu()
	case abEvidence:
		if a.aiState.report != nil {
			savedScroll := a.aiScroll
			a.aiEvidenceExpanded = !a.aiEvidenceExpanded
			a.rebuildAnalysisLines(a.aiState.report)
			a.aiScroll = min(savedScroll, a.aiScrollMax())
		}
	}
	invalidate(a.analysisUI)
}

func (a *App) analysisFocusButtons() []aiButton {
	buttons := a.aiButtonsFor()
	if a.aiEvidenceHit != (rect{}) {
		buttons = append(buttons, aiButton{ID: abEvidence, Rect: a.aiEvidenceHit})
	}
	return buttons
}

func (a *App) evidenceDisclosure(report *diagnose.Report) string {
	if report == nil {
		return ""
	}
	ids := make(map[string]struct{})
	for _, finding := range report.Findings {
		if !finding.Unverified {
			for _, id := range finding.EvidenceIDs {
				ids[id] = struct{}{}
			}
		}
	}
	if len(ids) == 0 {
		return ""
	}
	if a.aiEvidenceExpanded {
		return a.tr("收起读数", "Hide readings")
	}
	return fmt.Sprintf(a.tr("展开 %d 条读数", "Show readings (%d)"), len(ids))
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
		appendMenu(menu, uint32(flags), uintptr(100+i), a.localizedPreset(p.ID))
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
	appendMenu(menu, mfString, 200, a.tr("查看历史", "View history"))
	appendMenu(menu, mfString, 201, a.tr("导出当前报告", "Export this report"))
	appendMenu(menu, mfString, 202, a.tr("预览过期诊断缓存", "Review expired snapshots"))
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
	case 202:
		a.cacheAction(trayCacheClear)
	}
}

// exportCurrentReport writes the completed report as local JSON under the
// data directory. Keys are never exported.
func (a *App) exportCurrentReport() {
	if a.aiCompletedID == "" || a.aiState.report == nil {
		a.aiState.status = a.tr("还没有可导出的报告", "There is no report to export")
		invalidate(a.analysisUI)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := a.diag.Export(ctx, a.aiCompletedID)
	if err != nil {
		a.aiState.status = a.tr("导出失败：", "Export failed: ") + err.Error()
		invalidate(a.analysisUI)
		return
	}
	dir := filepath.Join(a.dataDir, "exports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		a.aiState.status = a.tr("创建导出目录失败：", "Could not create the export folder: ") + err.Error()
		invalidate(a.analysisUI)
		return
	}
	path := filepath.Join(dir, "deskpulse-diagnosis-"+a.aiCompletedID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		a.aiState.status = a.tr("写入导出文件失败：", "Could not write export: ") + err.Error()
		invalidate(a.analysisUI)
		return
	}
	a.aiState.status = a.tr("已导出：", "Exported: ") + filepath.Base(path)
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
	addBtn := func(id aiButtonID, label string, widthDIP int) {
		wpx := scale(widthDIP, dpi)
		x -= wpx
		out = append(out, aiButton{ID: id, Label: label, Rect: rect{x, btnTop, x + wpx, btnTop + btnH}})
		x -= scale(8, dpi)
	}
	s := a.aiState.kind
	switch s {
	case aiRunning:
		addBtn(abCancel, a.tr("取消", "Cancel"), 64)
	case aiCompleted:
		addBtn(abCopy, a.tr("复制", "Copy"), 64)
		addBtn(abRetry, a.tr("重新分析", "Run again"), 104)
	case aiCanceled, aiSampling, aiFailed:
		if s == aiFailed && a.aiFailedUnconfigured() {
			addBtn(abOpenSettings, a.tr("打开设置", "Open settings"), 114)
		}
		addBtn(abRetry, a.tr("重新分析", "Run again"), 104)
	case aiUnconfigured:
		addBtn(abOpenSettings, a.tr("打开设置", "Open settings"), 114)
	case aiIdle:
		addBtn(abRetry, a.tr("开始分析", "Analyze"), 104)
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
	a.aiEvidenceHit = rect{}

	// Title row.
	titleR := rect{m + scale(31, dpi), scale(12, dpi), b.w - m - scale(70, dpi), scale(analysisTitleH, dpi)}
	badge := rect{m, titleR.Top, m + scale(25, dpi), titleR.Bottom}
	drawIcon(b.hdc, "pulse", t.accent(), iconRect(badge, scale(21, dpi)))
	drawText(b.hdc, a.tr("AI 分析", "AI diagnosis"), titleR, f.TitleCJK, t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
	closeBox := rect{b.w - m - scale(28, dpi), scale(12, dpi), b.w - m, scale(12, dpi) + scale(28, dpi)}
	moreBox := rect{b.w - m - scale(28, dpi)*2 - scale(4, dpi), scale(12, dpi), b.w - m - scale(28, dpi) - scale(4, dpi), scale(12, dpi) + scale(28, dpi)}
	drawIcon(b.hdc, "more", t.muted(), iconRect(moreBox, scale(18, dpi)))
	drawIcon(b.hdc, "dismiss", t.muted(), iconRect(closeBox, scale(16, dpi)))

	// Preset row: selector button + window label on the right.
	presetTop := scale(analysisTitleH, dpi)
	presetBtn := rect{m, presetTop, m + scale(150, dpi), presetTop + scale(32, dpi)}
	roundRect(b.hdc, presetBtn, scale(8, dpi), t.panel(), t.line())
	label := a.localizedPreset(a.aiPreset)
	drawText(b.hdc, label, rect{presetBtn.Left + scale(12, dpi), presetBtn.Top, presetBtn.Right - scale(22, dpi), presetBtn.Bottom}, f.Body, t.text(), dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis|dtNoPrefix)
	chev := rect{presetBtn.Right - scale(20, dpi), presetBtn.Top + (presetBtn.Bottom-presetBtn.Top-scale(12, dpi))/2, presetBtn.Right - scale(8, dpi), 0}
	chev.Bottom = chev.Top + scale(12, dpi)
	drawIcon(b.hdc, "chevron-down", t.muted(), chev)
	secondsLabel := a.localizedWindow(a.aiSeconds)
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
	btns := a.aiButtonsFor()
	statusRight := b.w - m
	for _, btn := range btns {
		statusRight = min(statusRight, btn.Rect.Left-scale(12, dpi))
	}
	drawText(b.hdc, a.aiState.status, rect{m, statusTop, statusRight, statusBot}, f.Small, statusColor, dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis|dtNoPrefix)
	for i, btn := range btns {
		fill := t.panel()
		border := t.line()
		ink := t.text()
		if btn.ID == abRetry && (a.aiState.kind == aiIdle || a.aiState.kind == aiFailed) {
			fill, border, ink = t.accent(), t.accent(), t.accentText()
		}
		if a.aiFocus == i {
			border = t.accent()
		}
		roundRect(b.hdc, btn.Rect, scale(6, dpi), fill, border)
		icon := ""
		switch btn.ID {
		case abCopy:
			icon = "copy"
		case abRetry:
			icon = "refresh"
			if a.aiState.kind == aiIdle {
				icon = "sparkle"
			}
		case abOpenSettings:
			icon = "settings"
		}
		drawActionLabel(b.hdc, btn.Rect, btn.Label, icon, f.Small, ink, dpi)
	}
	fillRect(b.hdc, rect{m, statusBot, b.w - m, statusBot + scale(1, dpi)}, t.line())

	// Result area. Re-wrap cached lines if the paint DPI differs from the
	// DPI they were laid out at (monitor DPI change or capture override).
	if a.aiState.report != nil && (a.aiLinesDPI != dpi || a.aiLinesWidth != b.w) {
		a.rebuildAnalysisLines(a.aiState.report)
	}
	resTop := statusBot + scale(11, dpi)
	lineH := scale(analysisLineH, dpi)
	scroll := a.aiScroll
	if max := a.aiScrollMax(); scroll > max {
		scroll = max
	}
	if len(a.aiLines) == 0 {
		hint := ""
		switch a.aiState.kind {
		case aiIdle:
			hint = a.tr("分析开始前可以检查将要发送的摘要。", "Review the summary before it is sent.")
		case aiRunning:
			hint = a.tr("正在发送已确认的摘要…", "Sending the approved summary…")
		case aiFailed, aiUnconfigured:
			width, _ := measureText(b.hdc, a.aiState.status, f.Small)
			if width > statusRight-m {
				hint = a.aiState.status
			}
		}
		drawText(b.hdc, hint, rect{m, resTop + scale(12, dpi), b.w - m, b.h - scale(16, dpi)}, f.Body, t.muted(), dtLeft|dtWordBreak|dtNoPrefix)
		return
	}
	first := int(scroll / lineH)
	yoff := resTop - scroll + int32(first)*lineH
	saved, _, _ := procSaveDC.Call(b.hdc)
	if saved != 0 {
		procIntersectClipRect.Call(b.hdc, uintptr(m), uintptr(resTop), uintptr(b.w-m), uintptr(b.h))
		defer procRestoreDC.Call(b.hdc, saved)
	}
	for i := first; i < len(a.aiLines); i++ {
		if yoff+lineH > b.h {
			break
		}
		l := a.aiLines[i]
		r := rect{m, yoff, b.w - m, yoff + lineH}
		switch l.Kind {
		case 'h':
			drawText(b.hdc, l.Text, r, f.Small, t.muted(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		case 'e':
			drawText(b.hdc, l.Text, r, f.Small, t.muted(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
			if caption := a.evidenceDisclosure(a.aiState.report); caption != "" && r.Top >= resTop {
				width, _ := measureText(b.hdc, caption, f.Small)
				right := r.Right
				button := rect{right - width - scale(30, dpi), r.Top, right, r.Bottom}
				a.aiEvidenceHit = button
				if a.aiFocus == len(btns) {
					roundRect(b.hdc, button, scale(5, dpi), t.panel(), t.accent())
				}
				text := rect{button.Left + scale(7, dpi), button.Top, right - scale(15, dpi), button.Bottom}
				drawText(b.hdc, caption, text, f.Small, t.accent(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
				marker := rect{right - scale(13, dpi), button.Top, right - scale(2, dpi), button.Bottom}
				icon := "chevron-down"
				if a.aiEvidenceExpanded {
					icon = "chevron-up"
				}
				drawIcon(b.hdc, icon, t.accent(), iconRect(marker, scale(10, dpi)))
			}
		case 's':
			drawText(b.hdc, l.Text, r, f.valueFont(l.Text), t.text(), dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
		case 'g':
			fillRect(b.hdc, rect{r.Left, r.Top + lineH/2, r.Right, r.Top + lineH/2 + scale(1, dpi)}, t.line())
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
