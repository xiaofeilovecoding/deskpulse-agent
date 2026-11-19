package diagnose

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/ingest"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/store"
)

func dfp(v float64) *float64 { return &v }

func testStoreWithData(t *testing.T, now time.Time) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "deskpulse-agent.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	from := now.Add(-15 * time.Minute).UnixMilli()
	var samples []ingest.Sample
	for i := 0; i < 10; i++ {
		ts := from + int64(i)*5000
		samples = append(samples,
			ingest.Sample{TS: ts, Kind: "cpu", Series: "total", A: dfp(float64(20 + i))},
			ingest.Sample{TS: ts, Kind: "mem", Series: "memory", A: dfp(512), B: dfp(2048), C: dfp(25)},
			ingest.Sample{TS: ts, Kind: "disk", Series: "C:", A: dfp(100), B: dfp(1000), C: dfp(10)},
			ingest.Sample{TS: ts, Kind: "diskio", Series: "PhysicalDrive0", A: dfp(2048), B: dfp(1024), C: dfp(4), D: dfp(2)},
		)
		samples = append(samples, ingest.Sample{TS: ts, Kind: "rtt", Series: "secret-host", A: dfp(12), B: dfp(20), C: dfp(0)})
	}
	procs := []ingest.ProcessSample{
		{TS: now.UnixMilli(), Name: "chrome.exe", PID: 100, CPU: dfp(55), WorkingSet: dfp(2 << 30)},
		{TS: now.UnixMilli(), Name: "explorer.exe", PID: 101, CPU: dfp(5), WorkingSet: dfp(256 << 20)},
	}
	if err := st.InsertBatch(context.Background(), ingest.Batch{Samples: samples, Processes: procs, ProcessTS: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestBuildSummaryPrivacyAndEvidence(t *testing.T) {
	now := time.Now()
	st := testStoreWithData(t, now)
	ctx := context.Background()
	sum, aliases, err := BuildSummary(ctx, st, 15, false, now, "secret-host")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(sum)
	text := string(data)
	for _, forbidden := range []string{"chrome.exe", "explorer.exe", "secret-host", "C:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("summary leaked %q: %s", forbidden, text)
		}
	}
	if aliases["P01"] != "chrome.exe" {
		t.Fatalf("aliases = %#v", aliases)
	}
	if sum.RTT == nil || sum.RTT.Status != "ok" || sum.RTT.AvgMS == nil {
		t.Fatalf("rtt = %#v", sum.RTT)
	}
	if len(sum.Evidence) == 0 {
		t.Fatal("evidence missing")
	}
	if !hasEvidence(sum.Evidence, "cpu.peak") || !hasEvidence(sum.Evidence, "proc.P01.cpu") {
		t.Fatalf("expected evidence ids missing: %#v", sum.Evidence)
	}

	sumNames, _, err := BuildSummary(ctx, st, 15, true, now, "secret-host")
	if err != nil {
		t.Fatal(err)
	}
	withNames, _ := json.Marshal(sumNames)
	if !strings.Contains(string(withNames), "chrome.exe") {
		t.Fatal("opted-in process name missing")
	}
	if strings.Contains(string(withNames), "secret-host") {
		t.Fatal("RTT target must never be sent")
	}
}

func hasEvidence(list []Evidence, id string) bool {
	for _, e := range list {
		if e.ID == id {
			return true
		}
	}
	return false
}

func TestBuildReportValidationAndEvidenceFiltering(t *testing.T) {
	catalog := map[string]Evidence{
		"cpu.peak": {ID: "cpu.peak", Label: "CPU 峰值", Value: 99.0, Unit: "percent"},
	}
	raw := []byte(`{"summary":"CPU 出现峰值。","findings":[{"cause":"可能有短时负载","evidence_ids":["cpu.peak","missing.id"]}],"next_steps":["观察"],"data_gaps":[]}`)
	r, err := BuildReport(raw, catalog, map[string]string{"P01": "chrome.exe"}, ReportMeta{ID: "id1", Model: "deepseek-flash", CreatedAt: 1, WindowMinutes: 15}, []string{"样本不足"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 || r.Findings[0].Unverified || len(r.Findings[0].EvidenceIDs) != 1 {
		t.Fatalf("findings = %#v", r.Findings)
	}
	if !contains(r.DataGaps, "部分模型引用未通过本地证据校验，已忽略") {
		t.Fatalf("unknown evidence gap missing: %#v", r.DataGaps)
	}
	if !contains(r.DataGaps, "样本不足") {
		t.Fatalf("summary gap missing: %#v", r.DataGaps)
	}
	fenced := append([]byte("```json\n"), raw...)
	fenced = append(fenced, []byte("\n```")...)
	if _, err := BuildReport(fenced, catalog, nil, ReportMeta{}, nil); err != nil {
		t.Fatalf("fenced JSON object should use the same validation: %v", err)
	}

	unverified := []byte(`{"summary":"证据不足。","findings":[{"cause":"可能原因","evidence_ids":["nope"]}],"next_steps":[],"data_gaps":["缺少数据"]}`)
	r, err = BuildReport(unverified, catalog, nil, ReportMeta{ID: "id2"}, nil)
	if err != nil || !r.Findings[0].Unverified {
		t.Fatalf("unverified: %#v %v", r.Findings, err)
	}

	for _, bad := range [][]byte{
		[]byte(``),
		[]byte(`not json`),
		[]byte(`{"summary":"","findings":[],"next_steps":[],"data_gaps":[]}`),
		[]byte(`{"summary":"ok","findings":[{"cause":"x","evidence_ids":[]}],"next_steps":[],"data_gaps":[]} {}`),
		[]byte(`{"summary":"ok","findings":[],"next_steps":["retry"],"data_gaps":null}`),
		[]byte(`{"summary":"ok","findings":[{"cause":"x","evidence_ids":null}],"next_steps":[],"data_gaps":[]}`),
		[]byte(`{"summary":"ok","findings":[],"next_steps":["retry"],"data_gaps":[],"command":"unexpected"}`),
		[]byte("```json\n```"),
	} {
		if _, err := BuildReport(bad, catalog, nil, ReportMeta{}, nil); err == nil {
			t.Fatalf("expected invalid report for %q", string(bad))
		}
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestClientHTTPOutcomes(t *testing.T) {
	ctx := context.Background()
	var sawAuth atomic.Bool
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer sk-test" {
			sawAuth.Store(true)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"ok\",\"findings\":[],\"next_steps\":[],\"data_gaps\":[\"x\"]}"},"finish_reason":"stop"}]}`))
	}))
	defer ok.Close()
	raw, err := NewClient(ok.URL, 2*time.Second).Chat(ctx, "sk-test", "deepseek-flash", "s", "u")
	if err != nil || !sawAuth.Load() || !strings.Contains(string(raw), "summary") {
		t.Fatalf("chat raw=%q err=%v auth=%v", raw, err, sawAuth.Load())
	}

	for status, want := range map[int]string{
		http.StatusUnauthorized:        ErrAuth.Code,
		http.StatusTooManyRequests:     ErrRate.Code,
		http.StatusInternalServerError: "deepseek_upstream_error",
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		_, err := NewClient(s.URL, time.Second).Chat(ctx, "sk-test", "deepseek-flash", "s", "u")
		if CodeOf(err) != want {
			t.Fatalf("status %d code=%s want=%s err=%v", status, CodeOf(err), want, err)
		}
		s.Close()
	}

	timeout := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer timeout.Close()
	if _, err := NewClient(timeout.URL, 40*time.Millisecond).Chat(ctx, "sk-test", "deepseek-flash", "s", "u"); CodeOf(err) != ErrTimeout.Code {
		t.Fatalf("timeout code=%s err=%v", CodeOf(err), err)
	}

	invalid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer invalid.Close()
	if _, err := NewClient(invalid.URL, time.Second).Chat(ctx, "sk-test", "deepseek-flash", "s", "u"); CodeOf(err) != ErrInvalid.Code {
		t.Fatalf("invalid code=%s err=%v", CodeOf(err), err)
	}

	truncated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"x\"}"},"finish_reason":"length"}]}`))
	}))
	defer truncated.Close()
	if _, err := NewClient(truncated.URL, time.Second).Chat(ctx, "sk-test", "deepseek-flash", "s", "u"); CodeOf(err) != ErrTruncated.Code {
		t.Fatalf("truncated code=%s err=%v", CodeOf(err), err)
	}
}

func TestClientRefusesCrossHostRedirect(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	_, err := NewClient(redirector.URL, time.Second).Chat(context.Background(), "sk-secret", "deepseek-flash", "s", "u")
	if CodeOf(err) != ErrRedirect.Code {
		t.Fatalf("redirect code=%s err=%v", CodeOf(err), err)
	}
	if leaked.Load() {
		t.Fatal("Authorization leaked to redirect target")
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Fatal("key leaked into error")
	}
}

func TestClientNoKeyAndTestConnection(t *testing.T) {
	ctx := context.Background()
	c := NewClient("", time.Second)
	if _, err := c.Chat(ctx, "", "deepseek-flash", "s", "u"); CodeOf(err) != ErrNoKey.Code {
		t.Fatalf("no key code=%s", CodeOf(err))
	}
	if err := c.TestConnection(ctx, ""); CodeOf(err) != ErrNoKey.Code {
		t.Fatalf("no key test code=%s", CodeOf(err))
	}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Fatalf("unexpected test request")
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer ok.Close()
	if err := NewClient(ok.URL, time.Second).TestConnection(ctx, "sk-test"); err != nil {
		t.Fatal(err)
	}
}

func TestServiceFlowAndCredentialPrivacy(t *testing.T) {
	now := time.Now()
	st := testStoreWithData(t, now)
	var seenBody atomic.Value
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody.Store(string(body))
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"CPU 峰值偏高。\",\"findings\":[{\"cause\":\"短时负载\",\"evidence_ids\":[\"cpu.peak\"]}],\"next_steps\":[\"观察进程\"],\"data_gaps\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer mock.Close()
	svc := New(Options{
		Store:      st,
		BaseURL:    mock.URL,
		HTTPClient: mock.Client(),
		EnvKey:     func() string { return "sk-secret-123" },
		Target:     func() string { return "secret-host" },
		Now:        func() time.Time { return now },
	})
	preview, err := svc.Preview(context.Background(), 15, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(preview.WireJSON, "secret-host") || strings.Contains(preview.WireJSON, "chrome.exe") {
		t.Fatalf("preview leaked private data: %s", preview.WireJSON)
	}
	id, err := svc.Start(15, false)
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
	if got.Status != "completed" || got.ReportJSON == "" {
		t.Fatalf("diagnosis = %#v err=%v", got, err)
	}
	if !strings.Contains(got.ReportJSON, "cpu.peak") {
		t.Fatalf("report missing evidence: %s", got.ReportJSON)
	}
	if strings.Contains(seenBody.Load().(string), "secret-host") || strings.Contains(seenBody.Load().(string), "chrome.exe") ||
		strings.Contains(seenBody.Load().(string), "PhysicalDrive0") || strings.Contains(seenBody.Load().(string), `"C:"`) {
		t.Fatalf("outbound body leaked private data: %s", seenBody.Load().(string))
	}
	dbBytes, err := os.ReadFile(st.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dbBytes), "sk-secret-123") {
		t.Fatal("API key persisted in SQLite")
	}
	status := svc.Status()
	if !status.Configured || status.Source != "environment" {
		t.Fatalf("status = %#v", status)
	}
	statusJSON, _ := json.Marshal(status)
	if strings.Contains(string(statusJSON), "sk-secret-123") {
		t.Fatal("key in status")
	}
	export, err := svc.Export(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(export), "sk-secret-123") {
		t.Fatal("key in export")
	}
}

func TestServiceSessionKeyAndBusyCancel(t *testing.T) {
	now := time.Now()
	st := testStoreWithData(t, now)
	block := make(chan struct{})
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"ok\",\"findings\":[],\"next_steps\":[],\"data_gaps\":[\"x\"]}"},"finish_reason":"stop"}]}`))
	}))
	defer mock.Close()
	svc := New(Options{Store: st, BaseURL: mock.URL, HTTPClient: mock.Client(), EnvKey: func() string { return "" }, Target: func() string { return "" }, Now: func() time.Time { return now }})
	if _, err := svc.Start(15, false); CodeOf(err) != ErrNoKey.Code {
		t.Fatalf("no key start code=%s", CodeOf(err))
	}
	if err := svc.SetSessionKey("sk-session"); err != nil {
		t.Fatal(err)
	}
	if status := svc.Status(); status.Source != "session" || !status.Configured {
		t.Fatalf("session status = %#v", status)
	}
	id, err := svc.Start(15, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(15, false); CodeOf(err) != ErrBusy.Code {
		t.Fatalf("busy code=%s", CodeOf(err))
	}
	if !svc.Cancel(id) {
		t.Fatal("cancel should find active analysis")
	}
	close(block)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, _ := svc.Get(context.Background(), id)
		if d.Status == "canceled" || d.Status == "completed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	svc.ClearSessionKey()
	if status := svc.Status(); status.Configured {
		t.Fatalf("session key should be cleared: %#v", status)
	}
}
