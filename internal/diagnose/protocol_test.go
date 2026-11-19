package diagnose

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
)

func TestModelProtocols(t *testing.T) {
	for _, tc := range []struct {
		provider        settings.ModelProvider
		route, response string
	}{
		{settings.ChatAPI, "/chat/completions", `{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`},
		{settings.Responses, "/responses", `{"status":"completed","output":[{"type":"reasoning"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{"},{"type":"output_text","text":"}"}]}]}`},
		{settings.Messages, "/messages", `{"role":"assistant","stop_reason":"end_turn","content":[{"type":"thinking"},{"type":"text","text":"{}"}]}`},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			mock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1"+tc.route || r.Method != http.MethodPost {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req["model"] != "mock-model" || req["thinking"] != nil {
					t.Errorf("unexpected model or foreign options: %v", req)
				}
				if tc.provider == settings.Messages {
					if r.Header.Get("x-api-key") != "synthetic" || r.Header.Get("Authorization") != "" ||
						r.Header.Get("anthropic-version") != "2023-06-01" || req["system"] != "System" {
						t.Error("Messages authentication or prompt fields are wrong")
					}
				} else if r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("x-api-key") != "" {
					t.Error("unexpected key header")
				}
				if tc.provider == settings.Responses && (req["store"] != false || req["input"] != "Example JSON") {
					t.Error("Responses request must disable storage and send the supplied input")
				}
				_, _ = w.Write([]byte(tc.response))
			}))
			defer mock.Close()
			c := newProfileClient(settings.ModelProfile{Provider: tc.provider, BaseURL: mock.URL + "/v1"}, time.Second)
			c.HTTP.Transport = mock.Client().Transport
			got, err := c.Chat(context.Background(), "synthetic", "mock-model", "System", "Example JSON")
			if err != nil || string(got) != "{}" {
				t.Fatalf("output %s, err %v", got, err)
			}
		})
	}
}

func TestModelEndpointBindingAndRedirectRefusal(t *testing.T) {
	var calls atomic.Int32
	mock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/tenant-a/models" {
			http.Redirect(w, r, "/tenant-b/models", http.StatusTemporaryRedirect)
			return
		}
		t.Error("credential-bearing request escaped its endpoint")
	}))
	defer mock.Close()
	profile := settings.ModelProfile{Provider: settings.Messages, BaseURL: mock.URL + "/tenant-a", Model: "mock-model"}
	svc := New(Options{
		Profile:    func() settings.ModelProfile { return profile },
		HTTPClient: mock.Client(),
		EnvKey:     func() string { return "legacy-only" },
		DPAPIKey:   func() (string, error) { return "original-only", nil },
	})
	if svc.Status().Configured {
		t.Fatal("legacy key exposed to another provider")
	}
	if err := svc.SetSessionKey("synthetic"); err != nil {
		t.Fatal(err)
	}
	if err := svc.TestConnection(context.Background()); CodeOf(err) != "model_redirect_refused" {
		t.Fatalf("expected redirect refusal, got %v", err)
	}
	profile.BaseURL = mock.URL + "/tenant-b"
	if svc.Status().Configured || CodeOf(svc.TestConnection(context.Background())) != "model_not_configured" || calls.Load() != 1 {
		t.Fatal("switching endpoints reused a credential")
	}
}

func TestPreparedAnalysisFreezesApproval(t *testing.T) {
	now := time.Now()
	st := recentStore(t, now)
	request := make(chan string, 1)
	mock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body responsesRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		request <- body.Input
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"summary\":\"Stable\",\"findings\":[],\"next_steps\":[\"Observe again\"],\"data_gaps\":[]}"}]}]}`))
	}))
	defer mock.Close()
	profile := settings.ModelProfile{Provider: settings.Responses, BaseURL: mock.URL, Model: "fixture"}
	clock := now
	svc := New(Options{Store: st, Profile: func() settings.ModelProfile { return profile }, HTTPClient: mock.Client(), Now: func() time.Time { return clock }})
	if err := svc.SetSessionKey("synthetic"); err != nil {
		t.Fatal(err)
	}
	proof, err := svc.PreparePreset(context.Background(), 30, PresetOverview, "en")
	if err != nil {
		t.Fatal(err)
	}
	clock = now.Add(8 * time.Minute)
	id, err := svc.StartPrepared(proof)
	if err != nil {
		t.Fatal(err)
	}
	result := waitDiagnosis(t, svc, id)
	if result.Status != "completed" {
		t.Fatalf("status %s: %s", result.Status, result.ErrorMessage)
	}
	if msg := <-request; !strings.Contains(msg, proof.WireJSON()) || strings.Contains(msg, "chrome.exe") {
		t.Fatal("request differed from the approved anonymized snapshot")
	}
	profile.Model = "another-model"
	if _, err := svc.StartPrepared(proof); CodeOf(err) != "model_changed" {
		t.Fatalf("changed destination accepted: %v", err)
	}
}
