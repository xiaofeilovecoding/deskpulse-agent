//go:build windows

package desktop

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/contracttest"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/winmetrics"
)

func TestMetricsExportMatchesPublicContract(t *testing.T) {
	validate := contracttest.Validator(t, "metrics-v1")
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cpu, read := 75.0, 12345.0
	sample := winmetrics.Snapshot{
		TS:        now.UnixMilli(),
		CPU:       &cpu,
		Memory:    &winmetrics.Memory{Total: 1024, Used: 512},
		Disk:      winmetrics.DiskRate{ReadBPS: &read},
		Processes: []winmetrics.ProcessInfo{{Name: "private-process-fixture", PID: 42}},
		RTT:       []winmetrics.RTTTarget{{Target: "private-rtt-fixture.invalid"}},
	}
	payload, err := exportSnapshot(sample)
	if err != nil {
		t.Fatal(err)
	}
	if err := validate(payload); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-process-fixture", "private-rtt-fixture", `"pid"`, `"target"`} {
		if bytes.Contains(payload, []byte(private)) {
			t.Errorf("unexpected private field or value %q in the export", private)
		}
	}
	var snapshot struct {
		CapturedAt time.Time      `json:"captured_at"`
		Metrics    []exportMetric `json:"metrics"`
	}
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.CapturedAt.Equal(now) {
		t.Fatalf("capture time is %v, want %v", snapshot.CapturedAt, now)
	}
	index := slices.IndexFunc(snapshot.Metrics, func(metric exportMetric) bool { return metric.ID == "cpu.used_ratio" })
	if index < 0 || snapshot.Metrics[index].Value == nil || *snapshot.Metrics[index].Value != .75 {
		t.Fatal("CPU percentage was not exported as a ratio")
	}
	index = slices.IndexFunc(snapshot.Metrics, func(metric exportMetric) bool { return metric.ID == "disk.write_bytes_per_sec" })
	if index < 0 || snapshot.Metrics[index].Quality != "unknown" || snapshot.Metrics[index].Value != nil {
		t.Fatal("missing disk samples must remain unknown with a null value")
	}
}
