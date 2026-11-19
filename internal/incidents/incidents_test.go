package incidents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistAndDeliverIncidentAndRecovery(t *testing.T) {
	var received []Event
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil || e.EventID != r.Header.Get("Idempotency-Key") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received = append(received, e)
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
	reopened, err := Open(dir, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	reopened.client = server.Client()
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
	if len(received) != 2 || received[0].Kind != "opened" || received[1].Kind != "recovered" ||
		received[0].IncidentID != received[1].IncidentID || received[0].EventID == received[1].EventID {
		t.Fatalf("unexpected event sequence: %+v", received)
	}
	if st, err := os.Stat(filepath.Join(dir, "events-v1.ndjson")); err != nil || st.Size() == 0 {
		t.Fatalf("local journal: %v", err)
	}
}
