package incidents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/contracttest"
)

func TestPersistAndDeliverIncidentAndRecovery(t *testing.T) {
	received := make(chan Event, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil || e.EventID != r.Header.Get("Idempotency-Key") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- e
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dir := t.TempDir()
	r, err := Open(dir, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cpu := .99
	for range 18 {
		now = now.Add(5 * time.Second)
		if err := r.Observe(now, &cpu, nil); err != nil {
			t.Fatal(err)
		}
	}
	reopened := openForTest(t, dir, server)
	if err := reopened.Deliver(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	cpu = .3
	for range 9 {
		now = now.Add(5 * time.Second)
		if err := reopened.Observe(now, &cpu, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := reopened.Deliver(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 {
		t.Fatalf("expected two events, got %d", len(received))
	}
	opened, recovered := <-received, <-received
	if opened.Kind != "opened" || recovered.Kind != "recovered" ||
		opened.IncidentID != recovered.IncidentID || opened.EventID == recovered.EventID {
		t.Fatalf("unexpected event sequence: %+v, %+v", opened, recovered)
	}
	if st, err := os.Stat(filepath.Join(dir, "events-v1.ndjson")); err != nil || st.Size() == 0 {
		t.Fatalf("local journal: %v", err)
	}
}

func TestIncidentsRemainLocalByDefault(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	raiseCPUAlert(t, r, now)
	if err := r.Deliver(context.Background(), now); err != nil || len(r.state.Outbox) != 0 {
		t.Fatalf("default configuration queued or sent an incident: %v", err)
	}
	journal, err := os.ReadFile(filepath.Join(dir, "events-v1.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if err := contracttest.Validator(t, "incident-v1")(journal); err != nil {
		t.Fatalf("local event does not conform to the incident contract: %v", err)
	}
}

func TestWebhookRateLimitSurvivesRestart(t *testing.T) {
	validate := contracttest.Validator(t, "incident-v1")
	type request struct{ key, payload string }
	requests := make(chan request, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || validate(body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var event Event
		if err := json.Unmarshal(body, &event); err != nil || event.EventID != r.Header.Get("Idempotency-Key") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		status := http.StatusNoContent
		if len(requests) == 0 {
			w.Header().Set("Retry-After", "45")
			status = http.StatusTooManyRequests
		}
		requests <- request{event.EventID, string(body)}
		w.WriteHeader(status)
	}))
	defer server.Close()

	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	first := openForTest(t, dir, server)
	raiseCPUAlert(t, first, now)
	if err := first.Deliver(ctx, now); err == nil || err.Error() != "event endpoint returned HTTP 429" {
		t.Fatalf("first attempt must receive a rate-limit response, got %v", err)
	}
	second := openForTest(t, dir, server)
	if err := second.Deliver(ctx, now.Add(44*time.Second)); err != nil || len(requests) != 1 {
		t.Fatalf("restart bypassed Retry-After: requests=%d, error=%v", len(requests), err)
	}
	if err := second.Deliver(ctx, now.Add(45*time.Second)); err != nil || len(requests) != 2 {
		t.Fatalf("retry failed: requests=%d, error=%v", len(requests), err)
	}
	if before, after := <-requests, <-requests; before != after {
		t.Fatal("retry changed the event, including its idempotency key")
	}
	third := openForTest(t, dir, server)
	if err := third.Deliver(ctx, now.Add(time.Minute)); err != nil || len(requests) != 0 {
		t.Fatalf("acknowledged event was resent: requests=%d, error=%v", len(requests), err)
	}
}

func TestWebhookRetriesAfterAcknowledgementIsLost(t *testing.T) {
	var mu sync.Mutex
	accepted := make(map[string]string)
	attempts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		key := r.Header.Get("Idempotency-Key")
		if err != nil || key == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if previous, found := accepted[key]; found {
			if previous != string(body) {
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		accepted[key] = string(body)
		// The receiver saved the event, but the response never reached the sender.
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn.Close()
	}))
	defer server.Close()

	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	r := openForTest(t, dir, server)
	raiseCPUAlert(t, r, now)
	ctx := context.Background()
	if err := r.Deliver(ctx, now); err == nil || err.Error() != "event endpoint unavailable" {
		t.Fatalf("expected a lost acknowledgement, got %v", err)
	}
	reopened := openForTest(t, dir, server)
	if err := reopened.Deliver(ctx, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(reopened.state.Outbox) != 0 {
		t.Fatal("the retry acknowledgement did not clear the queue")
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 || len(accepted) != 1 {
		t.Fatalf("expected one accepted event across two attempts, got %d events and %d attempts", len(accepted), attempts)
	}
}

func TestWebhookDoesNotFollowRedirectOrMoveQueuedEvents(t *testing.T) {
	counts := make(chan string, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts <- r.URL.Path
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	r := openForTest(t, dir, server)
	raiseCPUAlert(t, r, now)
	if err := r.Deliver(ctx, now); err == nil || len(counts) != 1 || <-counts != "/" {
		t.Fatalf("redirect or request was not handled as expected: %v", err)
	}
	changed, err := Open(dir, server.URL+"/other")
	if err != nil {
		t.Fatal(err)
	}
	changed.client.Transport = server.Client().Transport
	if !changed.state.NextAttempt.IsZero() || changed.state.Failures != 0 || len(changed.state.Outbox) != 0 {
		t.Fatal("retry state or queued events carried over to the new destination")
	}
	if err := changed.Deliver(ctx, now.Add(time.Hour)); err != nil || len(counts) != 0 {
		t.Fatalf("old queued event was sent to the new endpoint: %v", err)
	}
}

func TestQueuedEventExpiresDuringBackoff(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	r, err := Open(dir, "https://receiver.example/ingest")
	if err != nil {
		t.Fatal(err)
	}
	raiseCPUAlert(t, r, now)
	r.state.NextAttempt = now.Add(eventLifetime + 7*time.Hour)
	if err := r.Deliver(context.Background(), now.Add(eventLifetime+time.Second)); err != nil {
		t.Fatal(err)
	}
	loaded, err := Open(dir, r.target)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.state.Outbox) != 0 || !loaded.state.NextAttempt.Equal(r.state.NextAttempt) {
		t.Fatal("expired event was retained or backoff was cleared")
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"0", 0},
		{" 30 ", 30 * time.Second},
		{"invalid", 10 * time.Second},
		{"9223372036854775807", eventLifetime},
		{now.Add(time.Minute).Format(http.TimeFormat), time.Minute},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{now.Add(72 * time.Hour).Format(http.TimeFormat), eventLifetime},
	} {
		if delay := retryAfter(tc.value, now, 10*time.Second); delay != tc.want {
			t.Errorf("Retry-After %q: got %v, want %v", tc.value, delay, tc.want)
		}
	}
}

func openForTest(t *testing.T, dir string, server *httptest.Server) *Recorder {
	t.Helper()
	r, err := Open(dir, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	r.client.Transport = server.Client().Transport
	return r
}

func raiseCPUAlert(t *testing.T, r *Recorder, now time.Time) {
	t.Helper()
	cpu := .99
	for i := 17; i >= 0; i-- {
		if err := r.Observe(now.Add(-time.Duration(i)*5*time.Second), &cpu, nil); err != nil {
			t.Fatal(err)
		}
	}
}
