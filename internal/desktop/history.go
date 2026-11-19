//go:build windows

package desktop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	idHistoryList   = 3001
	idHistoryExport = 3002
	idHistoryReload = 3003
	idHistoryClose  = 3004
	idHistoryStatus = 3005
)

func (a *App) openHistory() {
	if a.historyUI == 0 {
		if err := a.createHistoryUI(); err != nil {
			a.logger.Warn("create history window failed", "error", err.Error())
			return
		}
	}
	a.refreshHistory()
	a.positionHistory()
	showWindow(a.historyUI, swShow)
	procSetForegroundWindow.Call(a.historyUI)
}

func (a *App) createHistoryUI() error {
	dpi := dpiForWindow(a.ball)
	if dpi == 0 {
		dpi = systemDPI()
	}
	w, h := scale(640, dpi), scale(470, dpi)
	work := workArea(a.ball)
	x := work.Left + (work.Right-work.Left-w)/2
	y := work.Top + (work.Bottom-work.Top-h)/2
	hwnd, err := createWindow(
		wsExToolWindow|wsExTopmost|wsExControlParent,
		"DeskPulse Agent 诊断历史",
		wsCaption|wsSysMenu|wsMinimize|wsClipChildren,
		x, y, w, h,
		0, 0, a.instance, 0,
	)
	if err != nil {
		return err
	}
	a.historyUI = hwnd
	s := func(v int) int32 { return scale(v, dpi) }
	f := a.fontsFor(dpi)
	add := func(class, text string, style uint32, x, y, w, h int32, id int32) uintptr {
		ctl, _ := createControl(class, text, style, 0, s(int(x)), s(int(y)), s(int(w)), s(int(h)), hwnd, id, a.instance)
		procSendMessageW.Call(ctl, wmSetFont, f.Small, 1)
		return ctl
	}
	a.historyList = add("LISTBOX", "", wsChild|wsVisible|wsBorder|wsVScroll|wsTabStop|lbsNotify|lbsNoIntegralHeight, 20, 20, 588, 300, idHistoryList)
	a.historyStatus = add("STATIC", "", wsChild|wsVisible, 20, 330, 588, 48, idHistoryStatus)
	add("BUTTON", "导出 JSON", wsChild|wsVisible|wsTabStop|bsPush, 20, 382, 100, 30, idHistoryExport)
	add("BUTTON", "刷新", wsChild|wsVisible|wsTabStop|bsPush, 128, 382, 74, 30, idHistoryReload)
	add("BUTTON", "关闭", wsChild|wsVisible|wsTabStop|bsPush, 210, 382, 74, 30, idHistoryClose)
	return nil
}

func (a *App) positionHistory() {
	if a.historyUI == 0 {
		return
	}
	r := windowRect(a.historyUI)
	work := workArea(a.ball)
	if r.Right-r.Left > work.Right-work.Left {
		r.Right = r.Left + (work.Right - work.Left)
	}
	if r.Bottom-r.Top > work.Bottom-work.Top {
		r.Bottom = r.Top + (work.Bottom - work.Top)
	}
	x := work.Left + (work.Right-work.Left-(r.Right-r.Left))/2
	y := work.Top + (work.Bottom-work.Top-(r.Bottom-r.Top))/2
	moveWindow(a.historyUI, x, y, r.Right-r.Left, r.Bottom-r.Top)
}

func (a *App) refreshHistory() {
	if a.historyUI == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	items, err := a.diag.List(ctx, 30)
	if err != nil {
		setText(a.historyStatus, "读取历史失败："+err.Error())
		return
	}
	a.historyItems = items
	procSendMessageW.Call(a.historyList, lbResetContent, 0, 0)
	for _, d := range items {
		window := fmtWindow(d.WindowSeconds)
		if d.WindowSeconds == 0 && d.WindowMinutes > 0 {
			window = fmtWindow(d.WindowMinutes * 60)
		}
		line := fmt.Sprintf("%s  %s  %s  %s", fmtTime(d.CreatedAt), window, diagStatusText(d.Status), d.ID)
		addListString(a.historyList, line)
	}
	if len(items) == 0 {
		setText(a.historyStatus, "暂无诊断历史。")
	} else {
		setText(a.historyStatus, fmt.Sprintf("共 %d 条最近诊断。导出只写本地 JSON，不包含 API Key。", len(items)))
	}
}

func diagStatusText(status string) string {
	switch status {
	case "completed":
		return "完成"
	case "running":
		return "进行中"
	case "canceled":
		return "已取消"
	case "interrupted":
		return "已中断"
	case "error":
		return "失败"
	default:
		return status
	}
}

func (a *App) handleHistory(message uint32, wparam, lparam uintptr) (bool, uintptr) {
	switch message {
	case wmPaint:
		paintBackground(a.historyUI, colorBg)
		return true, 0
	case wmClose:
		showWindow(a.historyUI, swHide)
		return true, 0
	case wmCommand:
		id := int(uint16(wparam & 0xffff))
		switch id {
		case idHistoryClose, 2: // IDCANCEL
			showWindow(a.historyUI, swHide)
		case idHistoryReload:
			a.refreshHistory()
		case idHistoryExport:
			a.exportHistory()
		}
		return true, 0
	case wmCtlColorEdit, wmCtlColorStatic, wmCtlColorListBox, wmCtlColorBtn:
		hdc := wparam
		procSetTextColor.Call(hdc, uintptr(colorText))
		procSetBkColor.Call(hdc, uintptr(colorPanel))
		return true, a.darkBrush
	case wmEraseBkgnd:
		return true, 1
	}
	return false, 0
}

func (a *App) exportHistory() {
	sel, _, _ := procSendMessageW.Call(a.historyList, lbGetCurSel, 0, 0)
	i := int(int32(sel))
	if i < 0 || i >= len(a.historyItems) {
		setText(a.historyStatus, "请选择一条诊断记录。")
		return
	}
	d := a.historyItems[i]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := a.diag.Export(ctx, d.ID)
	if err != nil {
		setText(a.historyStatus, "导出失败："+err.Error())
		return
	}
	dir := filepath.Join(a.dataDir, "exports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		setText(a.historyStatus, "创建导出目录失败："+err.Error())
		return
	}
	path := filepath.Join(dir, "deskpulse-diagnosis-"+d.ID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		setText(a.historyStatus, "写入导出文件失败："+err.Error())
		return
	}
	setText(a.historyStatus, "已导出："+path)
}
