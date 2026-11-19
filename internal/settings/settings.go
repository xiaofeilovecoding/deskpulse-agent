// Package settings persists non-secret DeskPulse Agent configuration. API keys are
// intentionally not part of this file; v0.2 stores them through DPAPI in the
// credential package.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	DefaultModel    = "deepseek-flash"
	AppearanceLight = "light"
	AppearanceDark  = "dark"
	// DefaultAIWindowSeconds is the v0.2 one-click diagnosis window.
	DefaultAIWindowSeconds = 30
	// MaxRTTTargets is the v0.2 product limit.
	MaxRTTTargets = 8
)

// WindowPosition is the persisted, DPI-independent top-left of the bubble.
// Set distinguishes the default corner from a deliberate (0,0) position.
type WindowPosition struct {
	X   int  `json:"x"`
	Y   int  `json:"y"`
	Set bool `json:"set"`
}

// Settings is the on-disk, non-secret configuration. RTTTarget is retained as
// a v0.1 compatibility mirror of RTTTargets[0]; v0.2 reads and writes
// RTTTargets.
type Settings struct {
	SchemaVersion   int            `json:"schema_version"`
	RTTTargets      []string       `json:"rtt_targets"`
	RTTTarget       string         `json:"rtt_target,omitempty"`
	AIWindowSeconds int            `json:"ai_window_seconds"`
	DeepSeekModel   string         `json:"deepseek_model"`
	AI              *ModelProfile  `json:"ai,omitempty"`
	RecoveryFolder  string         `json:"recovery_folder,omitempty"`
	BallPosition    WindowPosition `json:"ball_position"`
	HiddenMetrics   MetricMask     `json:"hidden_metrics,omitempty"`
	DisabledMetrics MetricMask     `json:"disabled_metrics,omitempty"`
	Language        string         `json:"language,omitempty"`
	Appearance      string         `json:"appearance,omitempty"`
}

type MetricMask uint8

const (
	CPU MetricMask = 1 << iota
	Memory
	Disk
	Latency
	GPU
)

// Store is a small concurrency-safe settings file store.
type Store struct {
	mu   sync.RWMutex
	path string
	val  Settings
}

// Default keeps network probing off until a target is configured.
func Default() Settings {
	return Settings{
		SchemaVersion:   2,
		AIWindowSeconds: DefaultAIWindowSeconds,
		DeepSeekModel:   DefaultModel,
		Appearance:      AppearanceLight,
	}
}

// New returns an in-memory store with defaults.
func New(path string) *Store {
	return &Store{path: path, val: Default()}
}

// Load reads settings.json. A missing file yields defaults and is not an error.
// v0.1's single rtt_target field is migrated to the ordered list.
func Load(path string) (*Store, error) {
	s := &Store{path: path, val: Default()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	var raw struct {
		SchemaVersion   int            `json:"schema_version"`
		RTTTargets      *[]string      `json:"rtt_targets"`
		RTTTarget       string         `json:"rtt_target"`
		AIWindowSeconds int            `json:"ai_window_seconds"`
		DeepSeekModel   string         `json:"deepseek_model"`
		AI              *ModelProfile  `json:"ai"`
		RecoveryFolder  string         `json:"recovery_folder"`
		BallPosition    WindowPosition `json:"ball_position"`
		HiddenMetrics   MetricMask     `json:"hidden_metrics"`
		DisabledMetrics MetricMask     `json:"disabled_metrics"`
		Language        string         `json:"language"`
		Appearance      string         `json:"appearance"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse settings: %w", err)
	}
	v := Settings{
		SchemaVersion:   raw.SchemaVersion,
		RTTTarget:       strings.TrimSpace(raw.RTTTarget),
		AIWindowSeconds: raw.AIWindowSeconds,
		DeepSeekModel:   strings.TrimSpace(raw.DeepSeekModel),
		AI:              raw.AI,
		RecoveryFolder:  strings.TrimSpace(raw.RecoveryFolder),
		BallPosition:    raw.BallPosition,
		HiddenMetrics:   raw.HiddenMetrics,
		DisabledMetrics: raw.DisabledMetrics,
		Language:        raw.Language,
		Appearance:      normalizeAppearance(raw.Appearance),
	}
	if v.SchemaVersion == 0 {
		v.SchemaVersion = 1
	}
	if raw.RTTTargets != nil {
		v.RTTTargets = append([]string(nil), (*raw.RTTTargets)...)
	} else if v.RTTTarget != "" {
		v.RTTTargets = []string{v.RTTTarget}

	}
	targets, err := NormalizeTargets(v.RTTTargets)
	if err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	v.RTTTargets = targets
	if v.DeepSeekModel == "" {
		v.DeepSeekModel = DefaultModel
	}
	if err := ValidateModel(v.DeepSeekModel); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	if err := v.normalizeAI(); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	if v.AIWindowSeconds == 0 {
		v.AIWindowSeconds = DefaultAIWindowSeconds
	}
	if err := ValidateAIWindow(v.AIWindowSeconds); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	v.SchemaVersion = 2
	if err := ValidateRecoveryFolder(v.RecoveryFolder); err != nil {
		return nil, err
	}
	if len(v.RTTTargets) > 0 {
		v.RTTTarget = v.RTTTargets[0]
	} else {
		v.RTTTarget = ""
	}
	s.val = v
	return s, nil
}

// Get returns a deep copy so callers cannot mutate the stored slice.
func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.val)
}

func (s *Store) Language() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.val.Language
}

// Save validates and atomically replaces the file. An empty target list is
// valid and remains empty (the UI shows "待配置").
func (s *Store) Save(v Settings) error {
	if len(v.RTTTargets) == 0 && v.SchemaVersion < 2 && strings.TrimSpace(v.RTTTarget) != "" {
		v.RTTTargets = []string{strings.TrimSpace(v.RTTTarget)}
	}
	targets, err := NormalizeTargets(v.RTTTargets)
	if err != nil {
		return err
	}
	v.SchemaVersion = 2
	v.RTTTargets = targets
	v.Appearance = normalizeAppearance(v.Appearance)
	v.RecoveryFolder = strings.TrimSpace(v.RecoveryFolder)
	if err := ValidateRecoveryFolder(v.RecoveryFolder); err != nil {
		return err
	}
	v.DeepSeekModel = strings.TrimSpace(v.DeepSeekModel)
	if v.DeepSeekModel == "" {
		v.DeepSeekModel = DefaultModel
	}
	if err := ValidateModel(v.DeepSeekModel); err != nil {
		return err
	}
	if err := v.normalizeAI(); err != nil {
		return err
	}
	if v.AIWindowSeconds == 0 {
		v.AIWindowSeconds = DefaultAIWindowSeconds
	}
	if err := ValidateAIWindow(v.AIWindowSeconds); err != nil {
		return err
	}
	if len(targets) > 0 {
		v.RTTTarget = targets[0]
	} else {
		v.RTTTarget = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeAtomic(s.path, v); err != nil {
		return err
	}
	s.val = clone(v)
	return nil
}

// ValidateModel accepts simple model names and namespaced router identifiers.
func ValidateModel(model string) error {
	if model == "" {
		return errors.New("模型名称不能为空")
	}
	if len(model) > 128 {
		return errors.New("模型名称过长")
	}
	for _, r := range model {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-_./:", r) {
			continue
		}
		return errors.New("模型名称含不支持的字符")
	}
	return nil
}

// ValidateAIWindow accepts 15 seconds through one hour.
func ValidateAIWindow(seconds int) error {
	if seconds < 15 || seconds > 3600 {
		return errors.New("AI 分析窗口必须在 15–3600 秒之间")
	}
	return nil
}

// NormalizeTargets trims, de-duplicates case-insensitively and validates 0–8
// hostnames/IPs. URLs, ports, paths and whitespace are rejected.
func NormalizeTargets(in []string) ([]string, error) {
	if len(in) > MaxRTTTargets {
		return nil, fmt.Errorf("RTT 目标最多 %d 个", MaxRTTTargets)
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		if err := ValidateTarget(t); err != nil {
			return nil, err
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out, nil
}

// ValidateTarget accepts a hostname or IP literal only. It deliberately has no
// scheme, port, path or query syntax.
func ValidateTarget(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("RTT 目标不能为空")
	}
	if len(target) > 253 {
		return errors.New("RTT 目标过长")
	}
	if strings.Contains(target, "://") || strings.ContainsAny(target, "/\\?#@") {
		return errors.New("RTT 目标只能是主机名或 IP，不能是 URL")
	}
	for _, r := range target {
		if r <= 0x20 || r == 0x7f {
			return errors.New("RTT 目标不能包含空白或控制字符")
		}
	}
	if ip := net.ParseIP(target); ip != nil {
		return nil
	}
	if strings.Contains(target, ":") {
		return errors.New("RTT 目标不能带端口；IPv6 地址请使用合法字面量")
	}
	if len(target) > 0 && target[len(target)-1] == '.' {
		target = target[:len(target)-1]
	}
	labels := strings.Split(target, ".")
	if len(labels) > 127 {
		return errors.New("RTT 主机名过长")
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return errors.New("RTT 主机名标签长度无效")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("RTT 主机名标签不能以连字符开头或结尾")
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				continue
			}
			// Allow IDN labels; net.LookupIP applies the platform resolver.
			if r < 0x80 {
				return errors.New("RTT 主机名包含非法字符")
			}
		}
	}
	return nil
}

// DisplayName returns the local UI label for a target. The model never receives
// the hostname; it sees the report alias generated by the diagnostic package.
func DisplayName(target string) string {
	if strings.EqualFold(strings.TrimSpace(target), "www.baidu.com") {
		return "百度"
	}
	return target
}

// MoveTarget moves items[from] to to (0-based) and returns a new slice.
func MoveTarget(items []string, from, to int) []string {
	if from < 0 || from >= len(items) || to < 0 || to >= len(items) || from == to {
		return append([]string(nil), items...)
	}
	out := append([]string(nil), items...)
	v := out[from]
	out = append(out[:from], out[from+1:]...)
	out = append(out, "")
	copy(out[to+1:], out[to:])
	out[to] = v
	return out
}

func clone(v Settings) Settings {
	v.RTTTargets = append([]string(nil), v.RTTTargets...)
	if v.AI != nil {
		profile := *v.AI
		v.AI = &profile
	}
	return v
}

func normalizeAppearance(value string) string {
	if value == AppearanceDark {
		return AppearanceDark
	}
	return AppearanceLight
}

func ValidateRecoveryFolder(folder string) error {
	if folder == "" {
		return nil
	}
	if !filepath.IsAbs(folder) || len(folder) > 1024 || strings.HasPrefix(folder, `\\`) ||
		strings.ContainsAny(folder, "\x00\r\n") {
		return errors.New("恢复目录须为本机绝对路径")
	}
	return nil
}

func (v *Settings) normalizeAI() error {
	if v.AI == nil {
		return nil
	}
	profile, err := v.AI.Normalize()
	if err != nil {
		return err
	}
	v.AI = &profile
	if profile.Provider == DeepSeek && profile.BaseURL == DeepSeek.DefaultURL() {
		v.DeepSeekModel = profile.Model
	}
	return nil
}

// ModelProfile returns a normalized profile. Older files retain their
// original DeepSeek model and existing DPAPI key.
func (v Settings) ModelProfile() ModelProfile {
	if v.AI != nil {
		return *v.AI
	}
	return ModelProfile{Provider: DeepSeek, BaseURL: DeepSeek.DefaultURL(), Model: v.DeepSeekModel}
}

func writeAtomic(path string, v Settings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}
