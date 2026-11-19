//go:build windows

package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/credential"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/diagnose"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/incidents"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/logging"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/store"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/winmetrics"
)

// Version is the v0.3 desktop build version.
const Version = "0.1.0-preview"

// Options configures the desktop application.
type Options struct {
	DataDir    string
	BaseURL    string
	Webhook    string
	Version    string
	Location   *time.Location
	Synthetic  bool   // dev/capture: fixed synthetic snapshot source
	CaptureDir string // dev/capture: render every page to PNGs and exit
	CaptureDPI uint32 // dev/capture: force the layout DPI (0 = system)
}

// snapshotSource abstracts the metric provider behind the UI. The real
// sampler implements it; the capture mode injects a synthetic source.
type snapshotSource interface {
	Latest() winmetrics.Snapshot
}

// aiViewState is the single source of truth for the analysis page.
type aiViewState struct {
	kind    aiStateKind
	runID   string
	preset  string
	seconds int
	status  string
	report  *diagnose.Report
}

// App owns the native windows, in-process samplers and local services.
type App struct {
	opts     Options
	dataDir  string
	version  string
	started  time.Time
	logPath  string
	logger   *slog.Logger
	logW     *logging.RotatingWriter
	st       *store.Store
	cfg      *settings.Store
	cred     *credential.Store
	diag     *diagnose.Service
	sampler  *winmetrics.Sampler
	source   snapshotSource
	ctx      context.Context
	cancel   context.CancelFunc
	services sync.WaitGroup
	instance syscall.Handle

	theme      uiTheme
	buffers    map[uintptr]*windowBuffer
	fonts      map[uint32]*fontSet
	captureDPI uint32

	ball         uintptr
	hover        uintptr
	panel        uintptr
	procUI       uintptr
	analysisUI   uintptr
	settingsUI   uintptr
	historyUI    uintptr
	trayAdded    bool
	taskbarMsg   uint32
	surfaceBrush uintptr

	ballDPI      uint32
	ballSize     int32
	ballDrag     bool
	ballDragging bool
	dragStart    point
	dragOrigin   rect
	hoverShown   bool
	hoverStep    int
	hoverMask    settings.MetricMask
	hoverGPU     bool
	attention    bool

	panelDPI      uint32
	panelHits     []popupHit
	panelFocus    int
	popupScroll   int32
	popupWheelRem int
	popupAnimStep int

	procUIHandle    uintptr
	procModel       *procModel
	procScroll      int
	procWheelRem    int
	procExpanded    map[procKey]bool
	procExpandRev   uint64
	procSortBy      string
	procSelected    procKey
	procSelectedSet bool
	procHits        []procRowHit
	procFocus       int
	procRebuilds    int
	procRepaints    int

	aiState            aiViewState
	aiPreset           string
	aiSeconds          int
	aiFocus            int
	aiScroll           int32
	aiWheelRem         int
	aiLines            []resultLine
	aiLinesDPI         uint32
	aiLinesWidth       int32
	aiCompletedID      string
	aiPollFails        int
	aiEvidenceExpanded bool
	aiEvidenceHit      rect

	settingsTab           int
	settingsTabButtons    [len(settingsTabs)]uintptr
	settingsDPI           uint32
	settingsTargets       []string
	settingsTestText      string
	settingsTestOK        bool
	settingsSecErr        string
	settingsTargetErr     string
	settingsKeyErr        string
	settingsKey           uintptr
	settingsProvider      uintptr
	settingsProtocol      settings.ModelProvider
	settingsEndpoint      uintptr
	settingsModel         uintptr
	settingsRecovery      uintptr
	settingsAppearance    uintptr
	settingsClose         uintptr
	settingsTips          uintptr
	settingsKeyScope      string
	settingsDraftRevision uint64
	settingsInputFrames   map[uintptr]rect
	settingsSeconds       uintptr
	settingsList          uintptr
	settingsTarget        uintptr
	settingsTest          uintptr
	settingsClear         uintptr
	settingsAdd           uintptr
	settingsDelete        uintptr
	settingsPrimary       uintptr
	settingsSave          uintptr
	settingsCancel        uintptr

	historyList   uintptr
	historyStatus uintptr
	historyItems  []store.Diagnosis
	cacheBusy     bool
	cacheMu       sync.Mutex
	cacheResult   string
	cacheError    string

	testMu       sync.Mutex
	testBusy     bool
	testResult   string
	testOK       bool
	testRevision uint64

	closeOnce sync.Once
	closed    bool
}

// Run creates the application and enters the Win32 message loop.
func Run(opts Options) error {
	// Win32 windows and their message queue belong to the creating OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initDesktop()
	if opts.CaptureDir != "" {
		if !opts.Synthetic || opts.DataDir == "" {
			return fmt.Errorf("界面采样仅允许合成模式与指定的独立数据目录")
		}
	} else {
		already, err := ensureSingleInstance()
		if err != nil || already {
			return err
		}
	}
	a, err := newApp(opts)
	if err != nil {
		return err
	}
	activeApp = a
	if err := a.createWindows(); err != nil {
		a.shutdown()
		return err
	}
	if a.opts.CaptureDir != "" {
		err := a.runCapture()
		a.shutdown()
		return err
	}
	a.startServices()
	a.showBall()
	if err := a.addTray(); err != nil {
		a.logger.Warn("tray icon add failed", "error", err.Error())
	}
	a.logger.Info("DeskPulse desktop started", "version", a.version)
	if err := a.messageLoop(); err != nil {
		a.logger.Error("message loop failed", "error", err.Error())
		a.shutdown()
		return err
	}
	a.shutdown()
	return nil
}

func newApp(opts Options) (*App, error) {
	if opts.Version == "" {
		opts.Version = Version
	}
	dir, err := ResolveDataDir(opts.DataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o700); err != nil {
		return nil, fmt.Errorf("创建日志目录: %w", err)
	}
	logPath := filepath.Join(dir, "logs", "deskpulse-agent.log")
	logW, err := logging.New(logPath, 2<<20, 3)
	if err != nil {
		return nil, fmt.Errorf("打开日志: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(logW, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := settings.Load(filepath.Join(dir, "settings.json"))
	if err != nil {
		logger.Warn("settings load failed; using defaults", "error", err.Error())
		cfg = settings.New(filepath.Join(dir, "settings.json"))
	}
	st, err := store.Open(filepath.Join(dir, "deskpulse-agent.db"), store.Options{})
	if err != nil {
		logW.Close()
		return nil, fmt.Errorf("打开本地数据库: %w", err)
	}
	cred := credential.New(dir)

	baseURL := "https://api.deepseek.com"
	if opts.BaseURL != "" {
		u, parseErr := url.Parse(strings.TrimSpace(opts.BaseURL))
		if parseErr != nil || !opts.Synthetic || opts.DataDir == "" ||
			(u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
			st.Close()
			logW.Close()
			return nil, fmt.Errorf("测试接口仅允许合成模式与独立数据目录下的本机回环 IP")
		}
		baseURL = u.String()
	}
	if err := validateBaseURL(baseURL); err != nil {
		st.Close()
		logW.Close()
		return nil, err
	}
	var profile func() settings.ModelProfile
	if opts.BaseURL == "" {
		profile = func() settings.ModelProfile { return cfg.Get().ModelProfile() }
	}
	diag := diagnose.New(diagnose.Options{
		Store:   st,
		BaseURL: baseURL,
		Model:   func() string { return cfg.Get().DeepSeekModel },
		Profile: profile,
		Credentials: func(p settings.ModelProfile) diagnose.KeyStorage {
			return credential.ForEndpoint(dir, p.CredentialScope())
		},
		EnvKey:     func() string { return "" },
		Target:     func() string { return firstTarget(cfg.Get().RTTTargets) },
		Targets:    func() []string { return append([]string(nil), cfg.Get().RTTTargets...) },
		DPAPIKey:   cred.Get,
		DPAPIStore: cred.Set,
		DPAPIClear: cred.Clear,
		Timeout:    90 * time.Second,
		Logger:     logger,
	})
	_ = diag.MarkInterrupted(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	instance := moduleHandle()
	if err := registerClass(instance); err != nil {
		cancel()
		st.Close()
		logW.Close()
		return nil, err
	}
	theme := uiTheme{Dark: cfg.Get().Appearance == settings.AppearanceDark, HighContrast: highContrastOn()}
	brush, _, _ := procCreateSolidBrush.Call(uintptr(theme.panel()))
	taskbarName, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	taskbar, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(taskbarName)))

	a := &App{
		opts:         opts,
		dataDir:      dir,
		version:      opts.Version,
		started:      time.Now(),
		logPath:      logPath,
		logger:       logger,
		logW:         logW,
		st:           st,
		cfg:          cfg,
		cred:         cred,
		diag:         diag,
		ctx:          ctx,
		cancel:       cancel,
		instance:     instance,
		surfaceBrush: brush,
		taskbarMsg:   uint32(taskbar),
		theme:        theme,
		buffers:      make(map[uintptr]*windowBuffer),
		fonts:        make(map[uint32]*fontSet),
		procExpanded: make(map[procKey]bool),
		procSortBy:   "cpu",
		aiPreset:     diagnose.PresetOverview,
		aiSeconds:    cfg.Get().AIWindowSeconds,
		panelFocus:   -1,
		aiFocus:      -1,
		procFocus:    -1,
	}
	a.sampler = winmetrics.New(winmetrics.Options{
		Store:   st,
		Targets: func() []string { return append([]string(nil), cfg.Get().RTTTargets...) },
		Enabled: func(metric settings.MetricMask) bool { return cfg.Get().DisabledMetrics&metric == 0 },
		Logger:  logger,
	})
	a.source = a.sampler
	if opts.Synthetic {
		a.sampler = nil
		a.source = newSyntheticSource()
	}
	a.aiState = aiViewState{kind: aiIdle, preset: a.aiPreset, seconds: a.aiSeconds, status: a.tr("尚未分析", "Ready to diagnose")}
	return a, nil
}

func (a *App) createWindows() error {
	var err error
	if a.ball, err = a.createBall(); err != nil {
		return err
	}
	if a.hover, err = a.createHover(); err != nil {
		return err
	}
	if a.panel, err = a.createPanel(); err != nil {
		return err
	}
	return nil
}

func (a *App) startServices() {
	if a.sampler != nil {
		a.sampler.Start(a.ctx)
		a.services.Go(a.recordIncidents)
	}
	a.services.Go(a.maintenance)
}

func (a *App) requestGPU(visible bool) {
	if a.sampler != nil {
		a.sampler.SetGPUVisible(visible)
	}
}

func (a *App) recordIncidents() {
	recorder, err := incidents.Open(filepath.Join(a.dataDir, "incidents"), a.opts.Webhook)
	if err != nil {
		a.logger.Warn("incident recorder unavailable", "error", err.Error())
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	loggedFailure := false
	attention := false
	for {
		select {
		case <-a.ctx.Done():
			return
		case now := <-ticker.C:
			s := a.source.Latest()
			nextAttention := s.CPU != nil && sustainedHigh(s.CPUHistory, 90) ||
				s.Memory != nil && sustainedHigh(s.MemHistory, 90)
			if nextAttention != attention {
				attention = nextAttention
				value := uintptr(0)
				if attention {
					value = 1
				}
				procPostMessageW.Call(a.ball, wmAppPulse, value, 0)
			}
			var cpu, memory *float64
			if s.CPU != nil {
				value := *s.CPU / 100
				cpu = &value
			}
			if s.Memory != nil {
				value := s.Memory.Total - s.Memory.Used
				memory = &value
			}
			if err := recorder.Observe(now, cpu, memory); err != nil && !loggedFailure {
				a.logger.Warn("local incident write failed")
				loggedFailure = true
			}
			if err := recorder.Deliver(a.ctx, now); err != nil {
				a.logger.Warn("incident delivery deferred", "error", err.Error())
			}
		}
	}
}

func (a *App) maintenance() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	if err := a.st.Maintain(a.ctx, time.Now()); err != nil {
		a.logger.Warn("startup maintenance failed", "error", err.Error())
	}
	for {
		select {
		case <-a.ctx.Done():
			return
		case now := <-t.C:
			if err := a.st.Maintain(a.ctx, now); err != nil {
				a.logger.Warn("maintenance failed", "error", err.Error())
			}
		}
	}
}

func (a *App) messageLoop() error {
	var m msg
	for {
		r1, _, e := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r1) == -1 {
			return fmt.Errorf("GetMessageW: %w", callError(e))
		}
		if r1 == 0 {
			return nil
		}
		if a.settingsUI != 0 && isVisible(a.settingsUI) {
			if handled, _, _ := procIsDialogMessageW.Call(a.settingsUI, uintptr(unsafe.Pointer(&m))); handled != 0 {
				continue
			}
		}
		if a.historyUI != 0 && isVisible(a.historyUI) {
			if handled, _, _ := procIsDialogMessageW.Call(a.historyUI, uintptr(unsafe.Pointer(&m))); handled != 0 {
				continue
			}
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (a *App) shutdown() {
	a.closeOnce.Do(func() {
		a.closed = true
		a.cancel()
		a.services.Wait()
		if a.sampler != nil {
			a.sampler.Stop()
		}
		a.diag.StopAll()
		a.removeTray()
		destroyWindow(a.settingsTips)
		destroyWindow(a.settingsUI)
		destroyWindow(a.historyUI)
		destroyWindow(a.analysisUI)
		destroyWindow(a.procUI)
		destroyWindow(a.panel)
		destroyWindow(a.hover)
		destroyWindow(a.ball)
		if a.surfaceBrush != 0 {
			procDeleteObject.Call(a.surfaceBrush)
		}
		a.releaseBuffers()
		for _, f := range a.fonts {
			f.delete()
		}
		a.fonts = nil
		deleteIconCache()
		if err := a.st.Close(); err != nil {
			a.logger.Warn("store close failed", "error", err.Error())
		}
		a.logger.Info("cloudscope desktop stopped")
		a.logW.Close()
		activeApp = nil
	})
}

// handle dispatches messages for every top-level DeskPulse Agent window.
func (a *App) handle(hwnd uintptr, message uint32, wparam, lparam uintptr) (bool, uintptr) {
	if message == wmAppShow {
		a.openFromTray()
		return true, 0
	}
	switch hwnd {
	case a.ball:
		return a.handleBall(message, wparam, lparam)
	case a.hover:
		return a.handleHover(message, wparam, lparam)
	case a.panel:
		return a.handlePanel(message, wparam, lparam)
	case a.procUI:
		return a.handleProcPage(message, wparam, lparam)
	case a.analysisUI:
		return a.handleAnalysisPage(message, wparam, lparam)
	case a.settingsUI:
		return a.handleSettings(message, wparam, lparam)
	case a.historyUI:
		return a.handleHistory(message, wparam, lparam)
	}
	return false, 0
}

func (a *App) openFromTray() {
	// The tray entry stays usable when the ball is covered, and toggles the
	// popup just like the ball does. The popup anchors on the ball's monitor.
	if a.popupVisible() {
		a.closePopup()
		return
	}
	a.openPopup()
}

func (a *App) quit() {
	a.logger.Info("quit requested")
	procPostQuitMessage.Call(0)
}

func (a *App) nowMS() int64 {
	return time.Now().UnixMilli()
}

func ResolveDataDir(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("无法确定 LOCALAPPDATA: %w", err)
		}
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "DeskPulseAgent"), nil
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("DeepSeek 地址无效")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") {
		return nil
	}
	return fmt.Errorf("DeepSeek 地址必须是 HTTPS；仅本机回环地址允许 HTTP 测试")
}

func firstTarget(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// ShowError displays a native error dialog for the console-free GUI entrypoint.
func ShowError(text string) {
	showMessage("DeskPulse Agent", text, mbOK|mbIconError|mbSetForeground)
}

// ShowInfo displays a native information dialog.
func ShowInfo(text string) {
	showMessage("DeskPulse Agent", text, mbOK|mbIconInformation|mbSetForeground)
}
