package diagnose

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/store"
)

// KeyStatus is safe to return to the UI: it contains no key material.
type KeyStatus struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source"`
	Model      string `json:"model"`
	BaseURL    string `json:"base_url"`
	Error      string `json:"error,omitempty"`
}

// Preview is the exact bounded scope that would be sent.
type Preview struct {
	Summary       Summary `json:"summary"`
	WireJSON      string  `json:"wire_json"`
	EvidenceCount int     `json:"evidence_count"`
	AliasCount    int     `json:"alias_count"`
}

// Options configures the diagnostic service.
type Options struct {
	Store      *store.Store
	BaseURL    string
	Model      func() string
	EnvKey     func() string
	Target     func() string
	Targets    func() []string
	DPAPIKey   func() (string, error)
	DPAPIStore func(string) error
	DPAPIClear func() error
	HTTPClient *http.Client
	Timeout    time.Duration
	Now        func() time.Time
	Logger     *slog.Logger
}

// Service owns session-only credentials and at most one active analysis.
type Service struct {
	store      *store.Store
	client     *Client
	model      func() string
	envKey     func() string
	target     func() string
	targets    func() []string
	dpapiKey   func() (string, error)
	dpapiStore func(string) error
	dpapiClear func() error
	now        func() time.Time
	log        *slog.Logger

	mu      sync.Mutex
	session string
	active  map[string]context.CancelFunc
}

// New builds a service. The HTTP client is injectable for httptest.
func New(opts Options) *Service {
	if opts.Model == nil {
		opts.Model = func() string { return settings.DefaultModel }
	}
	if opts.EnvKey == nil {
		opts.EnvKey = func() string { return os.Getenv("DEEPSEEK_API_KEY") }
	}
	if opts.Target == nil {
		opts.Target = func() string { return "" }
	}
	if opts.Targets == nil {
		opts.Targets = func() []string {
			if t := strings.TrimSpace(opts.Target()); t != "" {
				return []string{t}
			}
			return nil
		}
	}
	if opts.DPAPIKey == nil {
		opts.DPAPIKey = func() (string, error) { return "", nil }
	}
	if opts.DPAPIStore == nil {
		opts.DPAPIStore = func(string) error { return errors.New("DPAPI 存储未配置") }
	}
	if opts.DPAPIClear == nil {
		opts.DPAPIClear = func() error { return nil }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	client := NewClient(opts.BaseURL, opts.Timeout)
	if opts.HTTPClient != nil {
		client.HTTP = opts.HTTPClient
	}
	return &Service{
		store:      opts.Store,
		client:     client,
		model:      opts.Model,
		envKey:     opts.EnvKey,
		target:     opts.Target,
		targets:    opts.Targets,
		dpapiKey:   opts.DPAPIKey,
		dpapiStore: opts.DPAPIStore,
		dpapiClear: opts.DPAPIClear,
		now:        opts.Now,
		log:        opts.Logger,
		active:     make(map[string]context.CancelFunc),
	}
}

// Status exposes configuration presence only. It never returns a key or a
// key fragment. A missing credentials file is "not configured", not an error.
func (s *Service) Status() KeyStatus {
	s.mu.Lock()
	session := s.session
	s.mu.Unlock()
	source := "none"
	configured := false
	status := KeyStatus{Model: s.model(), BaseURL: s.client.BaseURL}
	if session != "" {
		source = "session"
		configured = true
	} else if key, err := s.dpapiKey(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			source = "none"
		} else {
			source = "dpapi_error"
			status.Error = "读取 DPAPI 凭据失败"
		}
	} else if strings.TrimSpace(key) != "" {
		source = "dpapi"
		configured = true
	} else if strings.TrimSpace(s.envKey()) != "" {
		source = "environment"
		configured = true
	}
	status.Configured = configured
	status.Source = source
	return status
}

// SetSessionKey stores a key only in this process's memory. An empty key
// clears it. It is never written to disk or logged.
func (s *Service) SetSessionKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		s.ClearSessionKey()
		return nil
	}
	if err := validateKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	s.session = key
	s.mu.Unlock()
	return nil
}

// ClearSessionKey removes the in-memory override.
func (s *Service) ClearSessionKey() {
	s.mu.Lock()
	s.session = ""
	s.mu.Unlock()
}

// SetPersistentKey stores a key through the DPAPI callback and clears the
// in-memory override. The service never returns the key.
func (s *Service) SetPersistentKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return s.ClearPersistentKey()
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if err := s.dpapiStore(key); err != nil {
		return err
	}
	s.mu.Lock()
	s.session = ""
	s.mu.Unlock()
	return nil
}

// ClearPersistentKey removes the DPAPI-protected key. Environment fallback is
// intentionally left intact.
func (s *Service) ClearPersistentKey() error {
	if err := s.dpapiClear(); err != nil {
		return err
	}
	s.mu.Lock()
	s.session = ""
	s.mu.Unlock()
	return nil
}

func validateKey(key string) error {
	if len(key) > 512 {
		return errors.New("API Key 过长")
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return errors.New("API Key 含非法控制字符")
		}
	}
	return nil
}

func (s *Service) currentKey() (key, source string) {
	s.mu.Lock()
	session := s.session
	s.mu.Unlock()
	if session != "" {
		return session, "session"
	}
	if k, err := s.dpapiKey(); err == nil && strings.TrimSpace(k) != "" {
		return strings.TrimSpace(k), "dpapi"
	}
	return strings.TrimSpace(s.envKey()), "environment"
}

// TestConnection runs a lightweight authenticated request.
func (s *Service) TestConnection(ctx context.Context) error {
	key, _ := s.currentKey()
	if key == "" {
		return ErrNoKey
	}
	return s.client.TestConnection(ctx, key)
}

// TestConnectionWithKey tests a candidate key without persisting it. An empty
// candidate tests the currently configured key.
func (s *Service) TestConnectionWithKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return s.TestConnection(ctx)
	}
	if err := validateKey(key); err != nil {
		return err
	}
	return s.client.TestConnection(ctx, key)
}

// Preview builds the exact summary that would be sent, without sending it.
func (s *Service) Preview(ctx context.Context, minutes int, includeProcessNames bool) (Preview, error) {
	summary, aliases, err := BuildSummary(ctx, s.store, minutes, includeProcessNames, s.now(), strings.TrimSpace(s.target()))
	if err != nil {
		return Preview{}, err
	}
	return previewOf(summary, aliases)
}

// PreviewSeconds builds the v0.2 multi-target, second-window summary.
func (s *Service) PreviewSeconds(ctx context.Context, seconds int, includeProcessNames bool) (Preview, error) {
	summary, aliases, err := BuildDesktopSummary(ctx, s.store, seconds, includeProcessNames, s.now(), s.targets())
	if err != nil {
		return Preview{}, err
	}
	return previewOf(summary, aliases)
}

func previewOf(summary Summary, aliases map[string]string) (Preview, error) {
	wire, err := json.Marshal(summary)
	if err != nil {
		return Preview{}, err
	}
	return Preview{Summary: summary, WireJSON: string(wire), EvidenceCount: len(summary.Evidence), AliasCount: len(aliases)}, nil
}

// Start creates a v0.1-compatible minute analysis.
func (s *Service) Start(minutes int, includeProcessNames bool) (string, error) {
	if minutes != 5 && minutes != 15 && minutes != 60 {
		return "", errors.New("诊断窗口仅支持 5、15 或 60 分钟")
	}
	return s.start(minutes*60, minutes, includeProcessNames, false, "")
}

// StartSeconds creates a v0.2-style second-window analysis; since v0.3 it runs
// as the overview preset.
func (s *Service) StartSeconds(seconds int, includeProcessNames bool) (string, error) {
	return s.StartPreset(seconds, PresetOverview, includeProcessNames)
}

// StartPreset creates a v0.3 analysis for a validated preset over any
// 15–3600 second window.
func (s *Service) StartPreset(seconds int, presetID string, includeProcessNames bool) (string, error) {
	if seconds < 15 || seconds > 3600 {
		return "", errors.New("诊断窗口必须在 15–3600 秒之间")
	}
	preset, err := normalizePreset(presetID)
	if err != nil {
		return "", err
	}
	return s.start(seconds, 0, includeProcessNames, true, preset)
}

func (s *Service) start(seconds, minutes int, includeProcessNames, desktop bool, preset string) (string, error) {
	key, _ := s.currentKey()
	if key == "" {
		return "", ErrNoKey
	}
	s.mu.Lock()
	if len(s.active) > 0 {
		s.mu.Unlock()
		return "", ErrBusy
	}
	id, err := newID()
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.active[id] = cancel
	model := s.model()
	s.mu.Unlock()

	now := s.now()
	record := store.Diagnosis{
		ID:                  id,
		CreatedAt:           now.UnixMilli(),
		UpdatedAt:           now.UnixMilli(),
		WindowMinutes:       minutes,
		WindowSeconds:       seconds,
		IncludeProcessNames: includeProcessNames,
		Model:               model,
		Status:              "running",
	}
	ctx, cancelDB := context.WithTimeout(context.Background(), 5*time.Second)
	err = s.store.CreateDiagnosis(ctx, record)
	cancelDB()
	if err != nil {
		s.mu.Lock()
		delete(s.active, id)
		s.mu.Unlock()
		cancel()
		return "", err
	}
	go s.run(runCtx, id, seconds, minutes, includeProcessNames, desktop, preset, model, key)
	return id, nil
}

func (s *Service) run(ctx context.Context, id string, seconds, minutes int, include, desktop bool, preset, model, key string) {
	start := s.now()
	defer func() {
		s.mu.Lock()
		delete(s.active, id)
		s.mu.Unlock()
	}()

	var summary Summary
	var aliases map[string]string
	var err error
	if desktop {
		summary, aliases, err = BuildDesktopSummary(ctx, s.store, seconds, include, s.now(), s.targets())
	} else {
		summary, aliases, err = BuildSummary(ctx, s.store, minutes, include, s.now(), strings.TrimSpace(s.target()))
	}
	if err != nil {
		if ctx.Err() != nil {
			s.finish(id, "canceled", "canceled", "诊断已取消", "")
			return
		}
		s.finish(id, "error", "summary_failed", "无法构建本地诊断摘要", "")
		return
	}
	wire, err := json.Marshal(summary)
	if err != nil {
		s.finish(id, "error", "summary_failed", "无法构建本地诊断摘要", "")
		return
	}
	if len(wire) > 24<<10 {
		s.finish(id, "error", "summary_too_large", "诊断摘要超过发送上限", "")
		return
	}
	catalog := catalogFor(summary.Evidence)
	system, user := ModelPrompt(), UserPrompt(wire)
	if desktop {
		p, err := LookupPreset(preset)
		if err != nil {
			s.finish(id, "error", "invalid_preset", "诊断预设无效", "")
			return
		}
		system = DesktopSystemPrompt
		user = PresetUserMessage(p.Task, wire)
		preset = p.ID
	}
	raw, err := s.client.Chat(ctx, key, model, system, user)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			s.finish(id, "canceled", "canceled", "诊断已取消", "")
			return
		}
		code := CodeOf(err)
		msg := "DeepSeek 请求失败"
		var api *APIError
		if errors.As(err, &api) {
			msg = api.Message
		}
		if s.log != nil {
			s.log.Info("diagnosis request failed", "code", code)
		}
		s.finish(id, "error", code, msg, "")
		return
	}
	report, err := BuildReport(raw, catalog, aliases, ReportMeta{
		ID:                  id,
		Model:               model,
		CreatedAt:           start.UnixMilli(),
		WindowMinutes:       minutes,
		WindowSeconds:       seconds,
		IncludeProcessNames: include,
		Preset:              preset,
		DurationMS:          s.now().Sub(start).Milliseconds(),
	}, summary.DataGaps)
	if err != nil {
		s.finish(id, "error", "deepseek_invalid_response", ErrInvalid.Message, "")
		return
	}
	reportJSON, err := MarshalReport(report)
	if err != nil {
		s.finish(id, "error", "report_failed", "无法保存诊断报告", "")
		return
	}
	s.finish(id, "completed", "", "", reportJSON)
}

func (s *Service) finish(id, status, code, message, reportJSON string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.UpdateDiagnosis(ctx, id, status, code, message, reportJSON, s.now()); err != nil && s.log != nil {
		s.log.Warn("update diagnosis failed", "id", id, "error", err.Error())
	}
}

// Cancel stops a running analysis. It returns false when nothing is running
// under that ID.
func (s *Service) Cancel(id string) bool {
	s.mu.Lock()
	cancel := s.active[id]
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// StopAll cancels active analyses during application shutdown.
func (s *Service) StopAll() {
	s.mu.Lock()
	active := make([]context.CancelFunc, 0, len(s.active))
	for _, c := range s.active {
		active = append(active, c)
	}
	s.mu.Unlock()
	for _, c := range active {
		c()
	}
}

// Get retrieves a diagnosis record.
func (s *Service) Get(ctx context.Context, id string) (store.Diagnosis, error) {
	return s.store.GetDiagnosis(ctx, id)
}

// List retrieves recent diagnosis metadata.
func (s *Service) List(ctx context.Context, limit int) ([]store.Diagnosis, error) {
	return s.store.ListDiagnoses(ctx, limit)
}

// Export produces a local, pretty-printed JSON document for a completed
// analysis. Reports are the only thing exported; keys are never included.
func (s *Service) Export(ctx context.Context, id string) ([]byte, error) {
	d, err := s.store.GetDiagnosis(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.Status != "completed" {
		return nil, fmt.Errorf("分析尚未完成，无法导出")
	}
	report, err := ParseStoredReport(d.ReportJSON)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"exported_by": "DeskPulse Agent v0.2",
		"exported_at": s.now().UTC().Format(time.RFC3339),
		"diagnosis":   d,
		"report":      report,
	}
	return json.MarshalIndent(payload, "", "  ")
}

// MarkInterrupted marks stale running rows at startup.
func (s *Service) MarkInterrupted(ctx context.Context) error {
	return s.store.MarkInterrupted(ctx, s.now())
}

func catalogFor(ev []Evidence) map[string]Evidence {
	out := make(map[string]Evidence, len(ev))
	for _, e := range ev {
		out[e.ID] = e
	}
	return out
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate diagnosis id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
