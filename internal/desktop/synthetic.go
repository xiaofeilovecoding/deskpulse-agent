//go:build windows

package desktop

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/diagnose"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/winmetrics"
)

// syntheticSource feeds the UI with a fixed, design-shaped snapshot for
// screenshot capture and local testing. It never touches real sampling.
type syntheticSource struct {
	snap winmetrics.Snapshot
}

func (s *syntheticSource) Latest() winmetrics.Snapshot { return s.snap }

func newSyntheticSource() *syntheticSource {
	return &syntheticSource{snap: buildSyntheticSnapshot()}
}

// buildSyntheticSnapshot mirrors the numbers in docs/cloudscope/desktop-v0.3
// -design.png so captured pages can be compared against the design.
func buildSyntheticSnapshot() winmetrics.Snapshot {
	now := time.Now().UnixMilli()
	cpu := 24.0
	read, write := 2.4e6, 0.8e6
	rttAvg, rttMax := 19.0, 23.0
	loss := 0.0

	// Microsoft Edge: 18 children; the two named rows match the design.
	var edgeKids []*winmetrics.Node
	mkNode := func(name string, pid int32, ppid int32, cpu, ws float64) *winmetrics.Node {
		return &winmetrics.Node{
			Proc: winmetrics.ProcessInfo{
				PID: pid, PPID: ppid, Name: name, StartMS: now - 3600_000,
				CPU: &cpu, WorkingSet: &ws, TS: now,
			},
		}
	}
	edgeKids = append(edgeKids,
		mkNode("浏览器进程", 1001, 1000, 9, 405e6),
		mkNode("渲染进程", 1002, 1000, 6, 538e6),
	)
	edgeRestCPU := 18.0 - 9 - 6
	edgeRestWS := 1.2e9 - 405e6 - 538e6
	perCPU := edgeRestCPU / 16
	perWS := edgeRestWS / 16
	for i := 0; i < 16; i++ {
		edgeKids = append(edgeKids, mkNode("msedge.exe", int32(1100+i), 1000, perCPU, perWS))
	}
	edge := mkNode("Microsoft Edge", 1000, 0, 0, 0)
	edge.Children = edgeKids
	edgeCPU := 18.0
	edgeWS := 1.2e9
	edge.TotalCPU, edge.TotalWS = &edgeCPU, &edgeWS
	edge.Descendants = 18

	teams := mkNode("Microsoft Teams", 700, 0, 0, 0)
	var teamKids []*winmetrics.Node
	for i := 0; i < 6; i++ {
		teamKids = append(teamKids, mkNode("teams.exe", int32(1300+i), 700, 0.1, 40e6))
	}
	teams.Children = teamKids
	teamsCPU := 2.0
	teamsWS := 612e6
	teams.TotalCPU, teams.TotalWS = &teamsCPU, &teamsWS
	teams.Descendants = 6

	explorerCPU := 3.0
	explorerWS := 208e6
	explorer := mkNode("Windows 资源管理器", 500, 0, explorerCPU, explorerWS)
	csCPU := 0.4
	csWS := 30e6
	cloudscope := mkNode("DeskPulse Agent", 900, 0, csCPU, csWS)

	tree := winmetrics.Tree{
		Roots: []*winmetrics.Node{edge, explorer, teams, cloudscope},
		Total: 1 + 18 + 1 + 6 + 1,
	}
	memUsed := 0.61 * 16e9
	memTotal := 16e9
	return winmetrics.Snapshot{
		TS:     now,
		CPU:    &cpu,
		Memory: &winmetrics.Memory{Used: memUsed, Total: memTotal, Percent: 61, TS: now},
		Disk: winmetrics.DiskRate{
			ReadBPS: &read, WriteBPS: &write, TS: now, State: winmetrics.StateOK,
		},
		Processes: flattenTree(tree),
		Tree:      tree,
		RTT: []winmetrics.RTTTarget{{
			Target: "www.baidu.com", Display: "百度", State: winmetrics.StateOK,
			AvgMS: &rttAvg, MaxMS: &rttMax, Loss: &loss, Sent: 3, Recv: 3, TS: now,
		}},
		RTTState:     winmetrics.StateOK,
		MetricState:  winmetrics.StateOK,
		ProcessState: winmetrics.StateOK,
	}
}

func flattenTree(tree winmetrics.Tree) []winmetrics.ProcessInfo {
	var walk func(n *winmetrics.Node)
	out := []winmetrics.ProcessInfo{}
	walk = func(n *winmetrics.Node) {
		out = append(out, n.Proc)
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range tree.Roots {
		walk(r)
	}
	return out
}

// syntheticReport is the completed-report fixture for capture.
func syntheticReport() *diagnose.Report {
	cpuAvg, cpuPeak, procCPU := 76.0, 94.0, 52.0
	return &diagnose.Report{
		ID:            "capture0001",
		Model:         "deepseek-flash",
		CreatedAt:     time.Now().UnixMilli(),
		WindowSeconds: 30,
		Preset:        diagnose.PresetOverview,
		Summary:       "CPU 是目前最可能的瓶颈",
		Findings: []diagnose.Finding{
			{Cause: "CPU 持续高占用，平均 76%、峰值 94%", EvidenceIDs: []string{"cpu.avg", "cpu.peak", "proc.P01.cpu"}},
		},
		NextSteps: []string{"查看高占用应用，再测一次。"},
		DataGaps:  []string{"RTT 仅两条样本"},
		Aliases: map[string]string{
			"P01": "chrome.exe",
			"R1":  "www.baidu.com",
		},
		Evidence: []diagnose.Evidence{
			{ID: "cpu.avg", Label: "CPU 平均利用率", Value: cpuAvg, Unit: "percent"},
			{ID: "cpu.peak", Label: "CPU 峰值利用率", Value: cpuPeak, Unit: "percent"},
			{ID: "proc.P01.cpu", Label: "进程 P01 最大 CPU", Value: procCPU, Unit: "percent"},
		},
		DurationMS: 2600,
	}
}

// runCapture renders every page through the real paint path at the requested
// DPI values and writes PNGs, then the process exits. No window is shown and
// no message loop runs. Default set covers 100/125/150/200%.
func (a *App) runCapture() error {
	dpis := []uint32{a.opts.CaptureDPI}
	if a.opts.CaptureDPI == 0 {
		dpis = []uint32{96, 120, 144, 192}
	}
	for _, dpi := range dpis {
		if err := a.captureAt(dpi); err != nil {
			return err
		}
	}
	a.logger.Info("capture finished", "dir", a.opts.CaptureDir)
	fmt.Println("截图已输出到 " + a.opts.CaptureDir)
	return nil
}

func (a *App) captureAt(dpi uint32) error {
	dir := filepath.Join(a.opts.CaptureDir, fmt.Sprintf("%d%%", dpi*100/96))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	a.captureDPI = dpi
	size := func(w, h int32) (int32, int32) { return scale(int(w), dpi), scale(int(h), dpi) }
	resize := func(hwnd uintptr, wDIP, hDIP int32) rect {
		w, h := size(wDIP, hDIP)
		moveWindow(hwnd, 0, 0, w, h)
		return clientRect(hwnd)
	}
	shoot := func(hwnd uintptr, name string, draw func(b *windowBuffer)) error {
		b := a.renderWindow(hwnd, draw)
		if b == nil {
			return fmt.Errorf("render %s failed", name)
		}
		return saveBufferPNG(b, filepath.Join(dir, name))
	}

	// Ball + hover + popup.
	bw, bh := size(ballSizeDIP, ballSizeDIP)
	moveWindow(a.ball, 0, 0, bw, bh)
	if err := shoot(a.ball, "ball.png", a.paintBallBuffer); err != nil {
		return err
	}
	hw, hh := size(hoverWDIP, hoverHDIP)
	moveWindow(a.hover, 0, 0, hw, hh)
	setRoundRegion(a.hover, hw, hh, scale(10, dpi))
	if err := shoot(a.hover, "hover.png", a.paintHoverBuffer); err != nil {
		return err
	}
	pw, ph := size(popupWDIP, popupHDIP)
	moveWindow(a.panel, 0, 0, pw, ph)
	setRoundRegion(a.panel, pw, ph, scale(10, dpi))
	if err := shoot(a.panel, "popup.png", a.paintPopupBuffer); err != nil {
		return err
	}

	// Process page: default collapsed and expanded state. Every DPI starts
	// from a clean page — expansion, selection and scroll from a previous DPI
	// must never leak into the next set of captures.
	if a.procUI == 0 {
		if _, err := a.createProcPage(); err != nil {
			return err
		}
	}
	a.procExpanded = make(map[procKey]bool)
	a.procExpandRev++
	a.procScroll = 0
	a.procSelected = procKey{}
	a.procSelectedSet = false
	a.procWheelRem = 0
	a.procModel = nil
	resize(a.procUI, procWDIP, procHDIP)
	setRoundRegion(a.procUI, scale(procWDIP, dpi), scale(procHDIP, dpi), scale(10, dpi))
	a.refreshProcModel(true)
	if err := shoot(a.procUI, "process.png", a.paintProcPageBuffer); err != nil {
		return err
	}
	if edge := a.findSyntheticEdge(); edge.PID != 0 {
		a.procExpanded[edge] = true
		a.procExpandRev++
		a.refreshProcModel(true)
	}
	if err := shoot(a.procUI, "process-expanded.png", a.paintProcPageBuffer); err != nil {
		return err
	}

	// Settings: both tabs. The window is shown off-screen so the native
	// children (EDIT/LISTBOX) become really visible — hidden EDIT controls
	// print nothing — while nothing appears on the user's desktop.
	if a.settingsUI == 0 {
		if err := a.createSettingsUI(); err != nil {
			return err
		}
	}
	a.settingsDPI = dpi
	a.layoutSettingsControls()
	a.applyControlFonts()
	a.settingsTargets = []string{"example.invalid", "192.0.2.1"}
	a.settingsTargets = a.settingsTargets[:minInt2(len(a.settingsTargets), 4)]
	a.refreshSettingsList()
	procSendMessageW.Call(a.settingsList, lbSetCurSel, ^uintptr(0), 0)
	setText(a.settingsSeconds, "30")
	sw, sh := size(settingsWDIP, settingsHDIP)
	moveWindow(a.settingsUI, -32000, -32000, sw, sh)
	setRoundRegion(a.settingsUI, sw, sh, scale(10, dpi))
	showWindow(a.settingsUI, swShowNoActivate)
	a.settingsTab = tabDeepSeek
	a.applySettingsTab()
	shootSettings := func(name string) error {
		return shoot(a.settingsUI, name, func(bb *windowBuffer) {
			a.paintSettingsInto(bb)
			a.compositeChildren(a.settingsUI, bb, a.settingsChildren())
		})
	}
	if err := shootSettings("settings-deepseek.png"); err != nil {
		return err
	}
	a.settingsTab = tabNetwork
	a.applySettingsTab()
	if err := shootSettings("settings-network.png"); err != nil {
		return err
	}

	// Analysis: success, running, failed and sampling states.
	if a.analysisUI == 0 {
		if _, err := a.createAnalysisPage(); err != nil {
			return err
		}
	}
	resize(a.analysisUI, analysisWDIP, analysisHDIP)
	setRoundRegion(a.analysisUI, scale(analysisWDIP, dpi), scale(analysisHDIP, dpi), scale(10, dpi))
	report := syntheticReport()
	a.aiState = aiViewState{kind: aiCompleted, status: "完成 · 综合诊断 · 最近 30 秒", report: report}
	a.aiCompletedID = report.ID
	a.rebuildAnalysisLines(report)
	if err := shoot(a.analysisUI, "analysis-success.png", a.paintAnalysisBuffer); err != nil {
		return err
	}
	a.aiState = aiViewState{kind: aiRunning, status: "分析中 · 综合诊断 · 最近 30 秒"}
	a.aiLines = nil
	if err := shoot(a.analysisUI, "analysis-running.png", a.paintAnalysisBuffer); err != nil {
		return err
	}
	a.aiState = aiViewState{kind: aiFailed, status: "分析失败：DeepSeek 认证失败，请检查 API Key"}
	if err := shoot(a.analysisUI, "analysis-failed.png", a.paintAnalysisBuffer); err != nil {
		return err
	}
	a.aiState = aiViewState{kind: aiSampling, status: "正在采样，稍后重试"}
	if err := shoot(a.analysisUI, "analysis-nosamples.png", a.paintAnalysisBuffer); err != nil {
		return err
	}
	a.aiState = aiViewState{kind: aiIdle, status: "尚未分析"}
	a.captureDPI = 0
	return nil
}

func (a *App) findSyntheticEdge() procKey {
	return procKey{PID: 1000, Start: a.source.Latest().Tree.Roots[0].Proc.StartMS}
}

// paintXXXBuffer wrappers let the capture path reuse the page draw functions
// without a WM_PAINT cycle.
func (a *App) paintBallBuffer(b *windowBuffer) { a.paintBallInto(b) }

func (a *App) paintHoverBuffer(b *windowBuffer) { a.paintHoverInto(b) }

func (a *App) paintPopupBuffer(b *windowBuffer) { a.paintPopupInto(b) }

func (a *App) paintProcPageBuffer(b *windowBuffer) { a.paintProcPageInto(b) }

func (a *App) paintSettingsBuffer(b *windowBuffer) { a.paintSettingsInto(b) }

func (a *App) paintAnalysisBuffer(b *windowBuffer) { a.paintAnalysisInto(b) }

// compositeChildren asks each visible native child control to print itself
// onto the parent's buffer at its client position. EDIT/LISTBOX print via
// WM_PRINT; owner-draw BUTTONS cannot print themselves, so the capture draws
// them through the app's own button-face routine at their real rects.
func (a *App) compositeChildren(parent uintptr, b *windowBuffer, children []uintptr) {
	pRect := windowRect(parent)
	for _, ch := range children {
		if ch == 0 || !childStyleVisible(ch) {
			continue
		}
		cr := windowRect(ch)
		w, h := cr.Right-cr.Left, cr.Bottom-cr.Top
		if w <= 0 || h <= 0 {
			continue
		}
		if strings.EqualFold(childClassName(ch), "Button") {
			continue
		}
		chdc, cbmp, cold, _ := createBufferDC(w, h)
		if chdc == 0 {
			continue
		}
		procSendMessageW.Call(ch, uintptr(wmPrint), chdc, uintptr(prfClient|prfEraseBkgnd))
		offsetX := cr.Left - pRect.Left
		offsetY := cr.Top - pRect.Top
		procBitBlt.Call(b.hdc, uintptr(offsetX), uintptr(offsetY), uintptr(w), uintptr(h), chdc, 0, 0, uintptr(srCCopy))
		releaseBufferDC(chdc, cbmp, cold)
	}
	// Owner-draw buttons: paint through the app's own face routine.
	a.paintSettingsButtonsInto(parent, b, children)
}

// paintSettingsButtonsInto draws every visible owner-draw button with the
// same face used in live rendering.
func (a *App) paintSettingsButtonsInto(parent uintptr, b *windowBuffer, children []uintptr) {
	pRect := windowRect(parent)
	for _, ch := range children {
		if ch == 0 || !childStyleVisible(ch) {
			continue
		}
		if !strings.EqualFold(childClassName(ch), "Button") {
			continue
		}
		cr := windowRect(ch)
		r := rect{cr.Left - pRect.Left, cr.Top - pRect.Top, cr.Right - pRect.Left, cr.Bottom - pRect.Top}
		a.paintButtonFace(b.hdc, r, childControlID(ch), false, false)
	}
}

// childClassName returns the window class name of a control.
func childClassName(hwnd uintptr) string {
	var buf [64]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:n])
}

// childControlID reads the child's dialog control ID (bottom word of the
// menu handle slot via GetWindowLongPtr GWL_ID = -12).
func childControlID(hwnd uintptr) int {
	const gwlID = ^uintptr(11) // -12
	v, _, _ := procGetWindowLongW.Call(hwnd, uintptr(gwlID))
	return int(int32(v))
}

// childStyleVisible reports the child's own WS_VISIBLE bit, independent of
// ancestor visibility.
func childStyleVisible(hwnd uintptr) bool {
	style, _, _ := procGetWindowLongW.Call(hwnd, uintptr(gwlStyle))
	return style&uintptr(wsVisible) != 0
}

func (a *App) settingsChildren() []uintptr {
	return []uintptr{
		a.settingsKey, a.settingsSeconds, a.settingsList, a.settingsTarget,
		a.settingsTest, a.settingsClear, a.settingsAdd, a.settingsDelete,
		a.settingsPrimary, a.settingsSave, a.settingsCancel,
	}
}

// saveBufferPNG converts the 32bpp BGRX buffer to PNG.
func saveBufferPNG(b *windowBuffer, path string) error {
	w, h := int(b.w), int(b.h)
	raw := b.download()
	if len(raw) == 0 {
		return fmt.Errorf("empty buffer for %s", path)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			o := i
			img.Pix[o+0] = raw[i+2] // R
			img.Pix[o+1] = raw[i+1] // G
			img.Pix[o+2] = raw[i+0] // B
			img.Pix[o+3] = 255
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}
