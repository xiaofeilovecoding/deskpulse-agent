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
	Configured bool                   `json:"configured"`
	Source     string                 `json:"source"`
	Model      string                 `json:"model"`
	BaseURL    string                 `json:"base_url"`
	Provider   settings.ModelProvider `json:"provider"`
	Error      string                 `json:"error,omitempty"`
}

// Preview is the exact bounded scope that would be sent.
type Preview struct {
	Summary       Summary `json:"summary"`
	WireJSON      string  `json:"wire_json"`
	EvidenceCount int     `json:"evidence_count"`
	AliasCount    int     `json:"alias_count"`
}

// PreparedAnalysis freezes the anonymous snapshot and destination shown in
// the confirmation dialog. A later sample cannot join an approved request.
type PreparedAnalysis struct {
	preview  Preview
	aliases  map[string]string
	profile  settings.ModelProfile
	preset   string
	language string
}

func (p PreparedAnalysis) Destination() settings.ModelProfile { return p.profile }
func (p PreparedAnalysis) EvidenceCount() int                 { return p.preview.EvidenceCount }
func (p PreparedAnalysis) WireJSON() string                   { return p.preview.WireJSON }
func (p PreparedAnalysis) Seconds() int                       { return p.preview.Summary.WindowSeconds }

// Options configures the diagnostic service.
type Options struct {
	Store       *store.Store
	BaseURL     string
	Model       func() string
	Profile     func() settings.ModelProfile
	Credentials func(settings.ModelProfile) KeyStorage
	EnvKey      func() string
	Target      func() string
	Targets     func() []string
	DPAPIKey    func() (string, error)
	DPAPIStore  func(string) error
	DPAPIClear  func() error
	HTTPClient  *http.Client
	Timeout     time.Duration
	Now         func() time.Time
	Logger      *slog.Logger
}

type KeyStorage interface {
	Get() (string, error)
	Set(string) error
	Clear() error
}

type modelConfiguration struct {
	profile  settings.ModelProfile
	client   *Client
	getKey   func() (string, error)
	saveKey  func(string) error
	clearKey func() error
	envKey   func() string
}

// Service owns session-only credentials and at most one active analysis.
type Service struct {
	store       *store.Store
	client      *Client
	model       func() string
	profile     func() settings.ModelProfile
	credentials func(settings.ModelProfile) KeyStorage
	timeout     time.Duration
	httpClient  *http.Client
	envKey      func() string
	target      func() string
	targets     func() []string
	dpapiKey    func() (string, error)
	dpapiStore  func(string) error
	dpapiClear  func() error
	now         func() time.Time
	log         *slog.Logger

	mu      sync.Mutex
	session map[string]string
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
		store:       opts.Store,
		client:      client,
		model:       opts.Model,
		profile:     opts.Profile,
		credentials: opts.Credentials,
		timeout:     opts.Timeout,
		httpClient:  opts.HTTPClient,
		envKey:      opts.EnvKey,
		target:      opts.Target,
		targets:     opts.Targets,
		dpapiKey:    opts.DPAPIKey,
		dpapiStore:  opts.DPAPIStore,
		dpapiClear:  opts.DPAPIClear,
		now:         opts.Now,
		log:         opts.Logger,
		active:      make(map[string]context.CancelFunc),
		session:     make(map[string]string),
	}
}

func (s *Service) configuration(draft *settings.ModelProfile) modelConfiguration {
	config := modelConfiguration{
		profile: settings.ModelProfile{Provider: settings.DeepSeek, BaseURL: s.client.BaseURL, Model: s.model()},
		client:  s.client, getKey: s.dpapiKey, saveKey: s.dpapiStore, clearKey: s.dpapiClear, envKey: s.envKey,
	}
	if s.profile == nil {
		return config
	}
	profile := s.profile()
	if draft != nil {
		profile = *draft
	}
	config.profile = profile
	config.client = newProfileClient(profile, s.timeout)
	if s.httpClient != nil {
		config.client.HTTP.Transport = s.httpClient.Transport
	}
	if profile.Provider == settings.DeepSeek && profile.BaseURL == settings.DeepSeek.DefaultURL() {
		return config
	}
	config.envKey = func() string { return "" }
	if s.credentials != nil {
		binding := s.credentials(profile)
		config.getKey, config.saveKey, config.clearKey = binding.Get, binding.Set, binding.Clear
	} else {
		config.getKey = func() (string, error) { return "", os.ErrNotExist }
		config.saveKey = func(string) error { return errors.New("凭据存储未配置") }
		config.clearKey = func() error { return nil }
	}
	return config
}

// Status exposes configuration presence only. It never returns a key or a
// key fragment. A missing credentials file is "not configured", not an error.
func (s *Service) Status() KeyStatus {
	return s.status(s.configuration(nil))
}

func (s *Service) StatusFor(profile settings.ModelProfile) KeyStatus {
	return s.status(s.configuration(&profile))
}

func (s *Service) status(config modelConfiguration) KeyStatus {
	s.mu.Lock()
	session := s.session[config.profile.CredentialScope()]
	s.mu.Unlock()
	source := "none"
	configured := false
	status := KeyStatus{Model: config.profile.Model, BaseURL: config.profile.BaseURL, Provider: config.profile.Provider}
	if session != "" {
		source = "session"
		configured = true
	} else if key, err := config.getKey(); err != nil && !errors.Is(err, os.ErrNotExist) {
		source = "dpapi_error"
		status.Error = "读取 DPAPI 凭据失败"
	} else if strings.TrimSpace(key) != "" {
		source = "dpapi"
		configured = true
	} else if strings.TrimSpace(config.envKey()) != "" {
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
	scope := s.configuration(nil).profile.CredentialScope()
	s.mu.Lock()
	s.session[scope] = key
	s.mu.Unlock()
	return nil
}

// ClearSessionKey removes the in-memory override.
func (s *Service) ClearSessionKey() {
	scope := s.configuration(nil).profile.CredentialScope()
	s.mu.Lock()
	delete(s.session, scope)
	s.mu.Unlock()
}

// SetPersistentKey stores a key through the DPAPI callback and clears the
// in-memory override. The service never returns the key.
func (s *Service) SetPersistentKey(key string) error {
	return s.setPersistentKey(s.configuration(nil), key)
}

func (s *Service) SetPersistentKeyFor(profile settings.ModelProfile, key string) error {
	return s.setPersistentKey(s.configuration(&profile), key)
}

func (s *Service) setPersistentKey(config modelConfiguration, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return s.clearPersistentKey(config)
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if err := config.saveKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.session, config.profile.CredentialScope())
	s.mu.Unlock()
	return nil
}

// ClearPersistentKey removes the DPAPI-protected key. Environment fallback is
// intentionally left intact.
func (s *Service) ClearPersistentKey() error {
	return s.clearPersistentKey(s.configuration(nil))
}

func (s *Service) ClearPersistentKeyFor(profile settings.ModelProfile) error {
	return s.clearPersistentKey(s.configuration(&profile))
}

func (s *Service) clearPersistentKey(config modelConfiguration) error {
	if err := config.clearKey(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.session, config.profile.CredentialScope())
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

func (s *Service) currentKey(config modelConfiguration) (key, source string) {
	s.mu.Lock()
	session := s.session[config.profile.CredentialScope()]
	s.mu.Unlock()
	if session != "" {
		return session, "session"
	}
	if k, err := config.getKey(); err == nil && strings.TrimSpace(k) != "" {
		return strings.TrimSpace(k), "dpapi"
	}
	return strings.TrimSpace(config.envKey()), "environment"
}

// TestConnection runs a lightweight authenticated request.
func (s *Service) TestConnection(ctx context.Context) error {
	return s.testConnection(ctx, s.configuration(nil), "")
}

// TestConnectionWithKey tests a candidate key without persisting it. An empty
// candidate tests the currently configured key.
func (s *Service) TestConnectionWithKey(ctx context.Context, key string) error {
	return s.testConnection(ctx, s.configuration(nil), key)
}

func (s *Service) TestConnectionFor(ctx context.Context, profile settings.ModelProfile, key string) error {
	return s.testConnection(ctx, s.configuration(&profile), key)
}

func (s *Service) testConnection(ctx context.Context, config modelConfiguration, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		key, _ = s.currentKey(config)
	}
	if err := validateKey(key); err != nil {
		return err
	}
	return config.client.TestConnection(ctx, key)
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

func (s *Service) PreparePreset(ctx context.Context, seconds int, presetID, language string) (PreparedAnalysis, error) {
	preset, err := normalizePreset(presetID)
	if err != nil {
		return PreparedAnalysis{}, err
	}
	profile := s.configuration(nil).profile
	summary, aliases, err := BuildDesktopSummary(ctx, s.store, seconds, false, s.now(), s.targets())
	if err != nil {
		return PreparedAnalysis{}, err
	}
	preview, err := previewOf(summary, aliases)
	if err != nil {
		return PreparedAnalysis{}, err
	}
	if len(preview.WireJSON) > 24<<10 {
		return PreparedAnalysis{}, &APIError{Code: "summary_too_large", Message: "诊断摘要超过发送上限"}
	}
	return PreparedAnalysis{preview: preview, aliases: aliases, profile: profile, preset: preset, language: language}, nil
}

func (s *Service) StartPrepared(prepared PreparedAnalysis) (string, error) {
	if prepared.EvidenceCount() == 0 {
		return "", errors.New("没有可发送的证据")
	}
	return s.startWithPrepared(prepared.Seconds(), 0, false, true, prepared.preset, &prepared)
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
	return s.startWithPrepared(seconds, minutes, includeProcessNames, desktop, preset, nil)
}

func (s *Service) startWithPrepared(seconds, minutes int, includeProcessNames, desktop bool, preset string, prepared *PreparedAnalysis) (string, error) {
	config := s.configuration(nil)
	if prepared != nil && config.profile != prepared.profile {
		return "", &APIError{Code: "model_changed", Message: "模型或地址已更改，请重新确认"}
	}
	key, _ := s.currentKey(config)
	if key == "" {
		return "", config.client.providerError(ErrNoKey)
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
	model := config.profile.Model
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
	go s.run(runCtx, id, seconds, minutes, includeProcessNames, desktop, preset, model, key, config.client, prepared)
	return id, nil
}

func (s *Service) run(ctx context.Context, id string, seconds, minutes int, include, desktop bool, preset, model, key string, client *Client, prepared *PreparedAnalysis) {
	start := s.now()
	defer func() {
		s.mu.Lock()
		delete(s.active, id)
		s.mu.Unlock()
	}()

	var summary Summary
	var aliases map[string]string
	var err error
	if prepared != nil {
		summary, aliases = prepared.preview.Summary, prepared.aliases
	} else if desktop {
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
	var wire []byte
	if prepared != nil {
		wire = []byte(prepared.WireJSON())
	} else {
		wire, err = json.Marshal(summary)
		if err != nil {
			s.finish(id, "error", "summary_failed", "无法构建本地诊断摘要", "")
			return
		}
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
		if prepared != nil {
			system = desktopPrompt(prepared.language)
		}
		user = PresetUserMessage(p.Task, wire)
		preset = p.ID
	}
	raw, err := client.Chat(ctx, key, model, system, user)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			s.finish(id, "canceled", "canceled", "诊断已取消", "")
			return
		}
		code := CodeOf(err)
		msg := "模型请求失败"
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
		invalid := client.providerError(ErrInvalid)
		s.finish(id, "error", CodeOf(invalid), invalid.Error(), "")
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
