package diagnose

import (
	"context"
	"encoding/json"
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

func recentStore(t *testing.T, now time.Time) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "deskpulse-agent.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ts := now.UnixMilli()
	var samples []ingest.Sample
	for i := 4; i >= 0; i-- {
		t0 := ts - int64(i)*5000
		samples = append(samples,
			ingest.Sample{TS: t0, Kind: "cpu", Series: "total", A: dfp(18)},
			ingest.Sample{TS: t0, Kind: "mem", Series: "memory", A: dfp(1024), B: dfp(4096), C: dfp(25)},
			ingest.Sample{TS: t0, Kind: "rtt", Series: "www.baidu.com", A: dfp(24), B: dfp(30), C: dfp(0)},
			ingest.Sample{TS: t0, Kind: "rtt", Series: "10.0.0.1", A: dfp(3), B: dfp(4), C: dfp(0)},
		)
	}
	if err := st.InsertDesktopSnapshot(context.Background(), ts, samples, []ingest.ProcessSample{
		{TS: ts, Name: "chrome.exe", PID: 100, CPU: dfp(20), WorkingSet: dfp(1 << 30)},
	}, nil); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestServiceStartSeconds30AndDPAPIStatus(t *testing.T) {
	now := time.Now()
	st := recentStore(t, now)
	var body atomic.Value
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body.Store(string(b))
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"窗口内指标平稳。\",\"findings\":[],\"next_steps\":[\"继续观察\"],\"data_gaps\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer mock.Close()
	var stored atomic.Value
	svc := New(Options{
		Store:      st,
		BaseURL:    mock.URL,
		HTTPClient: mock.Client(),
		EnvKey:     func() string { return "" },
		Targets:    func() []string { return []string{"www.baidu.com", "10.0.0.1"} },
		DPAPIKey:   func() (string, error) { v, _ := stored.Load().(string); return v, nil },
		DPAPIStore: func(key string) error { stored.Store(key); return nil },
		DPAPIClear: func() error { stored.Store(""); return nil },
		Now:        func() time.Time { return now },
	})
	if err := svc.SetPersistentKey("sk-dpapi-synthetic"); err != nil {
		t.Fatal(err)
	}
	stt := svc.Status()
	if !stt.Configured || stt.Source != "dpapi" {
		t.Fatalf("status = %#v", stt)
	}
	statusJSON, _ := json.Marshal(stt)
	if strings.Contains(string(statusJSON), "sk-dpapi-synthetic") {
		t.Fatal("key in status")
	}
	id, err := svc.StartSeconds(30, false)
	if err != nil {
		t.Fatal(err)
	}
	var got store.Diagnosis
	for i := 0; i < 100; i++ {
		got, err = svc.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Status != "completed" {
		t.Fatalf("status = %s err=%s", got.Status, got.ErrorMessage)
	}
	if got.WindowSeconds != 30 || !strings.Contains(got.ReportJSON, `"window_seconds":30`) {
		t.Fatalf("record/report window = %d / %s", got.WindowSeconds, got.ReportJSON)
	}
	raw := body.Load().(string)
	for _, forbidden := range []string{"www.baidu.com", "10.0.0.1", "chrome.exe", "sk-dpapi-synthetic"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("outbound body leaked %q: %s", forbidden, raw)
		}
	}
	if strings.Contains(got.ReportJSON, "sk-dpapi-synthetic") {
		t.Fatal("key in report")
	}
}
