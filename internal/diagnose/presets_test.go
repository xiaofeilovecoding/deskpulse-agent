package diagnose

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/ingest"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/store"
)

// presetStore seeds a 30-second synthetic window with CPU/mem/IO/RTT samples
// and process rows, matching the local simulation contract for v0.3.
func presetStore(t *testing.T, now time.Time, lowSample bool) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "deskpulse-agent.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ts := now.UnixMilli()
	var samples []ingest.Sample
	if !lowSample {
		for i := 29; i >= 0; i-- {
			t0 := ts - int64(i)*1000
			samples = append(samples,
				ingest.Sample{TS: t0, Kind: "cpu", Series: "total", A: dfp(76)},
				ingest.Sample{TS: t0, Kind: "mem", Series: "memory", A: dfp(4096), B: dfp(16384), C: dfp(61)},
				ingest.Sample{TS: t0, Kind: "diskio", Series: "total", A: dfp(2.4e6), B: dfp(0.8e6), C: dfp(120), D: dfp(40)},
				ingest.Sample{TS: t0, Kind: "rtt", Series: "www.baidu.com", A: dfp(19), B: dfp(23), C: dfp(0)},
			)
		}
	} else {
		samples = append(samples,
			ingest.Sample{TS: ts - 4000, Kind: "cpu", Series: "total", A: dfp(31)},
			ingest.Sample{TS: ts - 2000, Kind: "rtt", Series: "www.baidu.com", A: dfp(19), B: dfp(20), C: dfp(0)},
		)
	}
	procs := []ingest.ProcessSample{
		{TS: ts, Name: "chrome.exe", PID: 100, CPU: dfp(20), WorkingSet: dfp(1 << 30)},
		{TS: ts, Name: "explorer.exe", PID: 200, CPU: dfp(2), WorkingSet: dfp(200 << 20)},
	}
	if err := st.InsertDesktopSnapshot(context.Background(), ts, samples, procs, nil); err != nil {
		t.Fatal(err)
	}
	return st
}

func newPresetService(t *testing.T, st *store.Store, mockURL string, client *http.Client, key string) *Service {
	t.Helper()
	return New(Options{
		Store:      st,
		BaseURL:    mockURL,
		HTTPClient: client,
		EnvKey:     func() string { return key },
		Targets:    func() []string { return []string{"www.baidu.com", "10.0.0.1"} },
		Now:        func() time.Time { return time.Now() },
	})
}

func waitDiagnosis(t *testing.T, svc *Service, id string) store.Diagnosis {
	t.Helper()
	for i := 0; i < 200; i++ {
		d, err := svc.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != "running" {
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("diagnosis did not finish")
	return store.Diagnosis{}
}

const presetReply = `{"choices":[{"message":{"content":"{\"summary\":\"CPU 是目前最可能的瓶颈。\",\"findings\":[{\"cause\":\"CPU 持续高占用\",\"evidence_ids\":[\"cpu.avg\",\"nope.missing\"]}],\"next_steps\":[\"查看高占用应用\"],\"data_gaps\":[]}"},"finish_reason":"stop"}]}`

// chatRequestBody mirrors the outbound Chat Completions request.
type chatRequestBody struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func decodeChatBody(t *testing.T, raw string) chatRequestBody {
	t.Helper()
	var req chatRequestBody
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d", len(req.Messages))
	}
	return req
}

func TestPresetFlowAllFourPresets(t *testing.T) {
	now := time.Now()
	for _, preset := range Presets {
		t.Run(preset.ID, func(t *testing.T) {
			st := presetStore(t, now, false)
			var body atomic.Value
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				body.Store(string(b))
				_, _ = w.Write([]byte(presetReply))
			}))
			defer mock.Close()
			svc := newPresetService(t, st, mock.URL, mock.Client(), "sk-synthetic")
			id, err := svc.StartPreset(30, preset.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			d := waitDiagnosis(t, svc, id)
			if d.Status != "completed" {
				t.Fatalf("status=%s msg=%s", d.Status, d.ErrorMessage)
			}
			req := decodeChatBody(t, body.Load().(string))
			system, user := req.Messages[0].Content, req.Messages[1].Content
			if system != DesktopSystemPrompt || req.Messages[0].Role != "system" {
				t.Fatal("the fixed desktop diagnosis prompt was not sent as a system instruction")
			}
			if !strings.Contains(user, preset.Task) {
				t.Fatalf("preset task missing from user message: %s", user)
			}
			if !strings.Contains(user, `"window_seconds":30`) {
				t.Fatalf("summary window missing: %s", user)
			}
			// Desensitization: real targets, process names and key never sent.
			for _, forbidden := range []string{"www.baidu.com", "10.0.0.1", "chrome.exe", "sk-synthetic"} {
				if strings.Contains(user, forbidden) {
					t.Fatalf("request leaked %q", forbidden)
				}
			}
			// All configured targets are present as aliases.
			if !strings.Contains(user, `"alias":"R1"`) || !strings.Contains(user, `"alias":"R2"`) {
				t.Fatalf("RTT aliases missing: %s", user)
			}
			if !strings.Contains(user, "physical_disk_io") || !strings.Contains(user, "data_gaps") {
				t.Fatalf("IO/gaps missing: %s", user)
			}
			report, err := ParseStoredReport(d.ReportJSON)
			if err != nil {
				t.Fatal(err)
			}
			if report.Preset != preset.ID {
				t.Fatalf("report preset = %q, want %q", report.Preset, preset.ID)
			}
			if len(report.Findings) != 1 || report.Findings[0].Unverified {
				t.Fatalf("findings = %#v", report.Findings)
			}
			// The unknown evidence id is dropped and recorded as a data gap.
			if len(report.Findings[0].EvidenceIDs) != 1 || report.Findings[0].EvidenceIDs[0] != "cpu.avg" {
				t.Fatalf("evidence ids = %#v", report.Findings[0].EvidenceIDs)
			}
		})
	}
}

func TestPresetUnknownRejectedAndDefaultIsOverview(t *testing.T) {
	now := time.Now()
	st := presetStore(t, now, false)
	svc := newPresetService(t, st, "https://api.invalid", nil, "")
	if _, err := svc.StartPreset(30, "made-up", false); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("err = %v, want ErrUnknownPreset", err)
	}
	// A stored report without a preset field reads as the overview preset.
	raw := `{"id":"x","model":"m","created_at":1,"summary":"s","findings":[{"cause":"c","evidence_ids":[]}],"next_steps":["n"],"data_gaps":[]}`
	r, err := ParseStoredReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Preset != PresetOverview {
		t.Fatalf("legacy preset = %q", r.Preset)
	}
	if PresetName(r.Preset) != "综合诊断" || PresetName("slow-desktop") != "电脑变慢" {
		t.Fatal("preset display names wrong")
	}
}

func TestPresetLowSampleRecordsGaps(t *testing.T) {
	now := time.Now()
	st := presetStore(t, now, true)
	var body atomic.Value
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body.Store(string(b))
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"样本不足，需继续观察。\",\"findings\":[],\"next_steps\":[\"稍后重新分析\"],\"data_gaps\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer mock.Close()
	svc := newPresetService(t, st, mock.URL, mock.Client(), "sk-synthetic")
	id, err := svc.StartPreset(30, PresetSlowDesktop, false)
	if err != nil {
		t.Fatal(err)
	}
	d := waitDiagnosis(t, svc, id)
	if d.Status != "completed" {
		t.Fatalf("status=%s", d.Status)
	}
	req := decodeChatBody(t, body.Load().(string))
	if !strings.Contains(req.Messages[1].Content, Presets[1].Task) {
		t.Fatalf("task segment missing: %s", req.Messages[1].Content)
	}
	report, _ := ParseStoredReport(d.ReportJSON)
	if report.Preset != PresetSlowDesktop {
		t.Fatalf("preset = %q", report.Preset)
	}
	if len(report.DataGaps) == 0 {
		t.Fatal("low-sample window must record data gaps")
	}
}

func TestPresetMissingKeyFailsFast(t *testing.T) {
	now := time.Now()
	st := presetStore(t, now, false)
	svc := newPresetService(t, st, "https://api.invalid", nil, "")
	if _, err := svc.StartPreset(30, PresetOverview, false); !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
}

func TestPresetAuthFailureSurfacesCode(t *testing.T) {
	now := time.Now()
	st := presetStore(t, now, false)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mock.Close()
	svc := newPresetService(t, st, mock.URL, mock.Client(), "sk-bad")
	id, err := svc.StartPreset(30, PresetNetworkLag, false)
	if err != nil {
		t.Fatal(err)
	}
	d := waitDiagnosis(t, svc, id)
	if d.Status != "error" || d.ErrorCode != "deepseek_auth_failed" {
		t.Fatalf("status=%s code=%s", d.Status, d.ErrorCode)
	}
	if !strings.Contains(d.ErrorMessage, "认证") {
		t.Fatalf("message = %s", d.ErrorMessage)
	}
}

func TestPresetCancel(t *testing.T) {
	now := time.Now()
	st := presetStore(t, now, false)
	release := make(chan struct{})
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer mock.Close()
	svc := newPresetService(t, st, mock.URL, mock.Client(), "sk-synthetic")
	id, err := svc.StartPreset(30, PresetAppUsage, false)
	if err != nil {
		t.Fatal(err)
	}
	if !svc.Cancel(id) {
		t.Fatal("cancel returned false")
	}
	close(release)
	d := waitDiagnosis(t, svc, id)
	if d.Status != "canceled" {
		t.Fatalf("status=%s", d.Status)
	}
}
