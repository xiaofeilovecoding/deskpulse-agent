package diagnose

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/ingest"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/store"
)

func TestBuildDesktopSummary30sMultiRTTNoCapacity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "deskpulse-agent.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	ts := now.UnixMilli()
	var samples []ingest.Sample
	for i := 4; i >= 0; i-- {
		t0 := ts - int64(i)*5000
		samples = append(samples,
			ingest.Sample{TS: t0, Kind: "cpu", Series: "total", A: dfp(10 + float64(5-i))},
			ingest.Sample{TS: t0, Kind: "mem", Series: "memory", A: dfp(1024), B: dfp(4096), C: dfp(25)},
			ingest.Sample{TS: t0, Kind: "diskio", Series: "total", A: dfp(2048), B: dfp(1024), C: dfp(4), D: dfp(2)},
			ingest.Sample{TS: t0, Kind: "rtt", Series: "www.baidu.com", A: dfp(24), B: dfp(30), C: dfp(0)},
			ingest.Sample{TS: t0, Kind: "rtt", Series: "10.0.0.1", A: dfp(3), B: dfp(4), C: dfp(0)},
			ingest.Sample{TS: t0, Kind: "rtt", Series: "old-host", A: dfp(180), B: dfp(200), C: dfp(0)},
		)
	}
	if err := st.InsertDesktopSnapshot(context.Background(), ts, samples, []ingest.ProcessSample{
		{TS: ts, Name: "chrome.exe", PID: 100, CPU: dfp(30), WorkingSet: dfp(1 << 30)},
	}, nil); err != nil {
		t.Fatal(err)
	}
	sum, aliases, err := BuildDesktopSummary(context.Background(), st, 30, false, now, []string{"www.baidu.com", "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if sum.WindowSeconds != 30 || sum.WindowMinutes != 0 {
		t.Fatalf("window = %d/%d", sum.WindowMinutes, sum.WindowSeconds)
	}
	if sum.RTT != nil || len(sum.RTTTargets) != 2 {
		t.Fatalf("rtt = %#v targets=%#v", sum.RTT, sum.RTTTargets)
	}
	if aliases["R1"] != "www.baidu.com" || aliases["R2"] != "10.0.0.1" {
		t.Fatalf("aliases = %#v", aliases)
	}
	if !hasEvidence(sum.Evidence, "rtt.R1.avg") || !hasEvidence(sum.Evidence, "rtt.R2.loss") {
		t.Fatalf("RT evidence missing: %#v", sum.Evidence)
	}
	if len(sum.Disks) != 0 {
		t.Fatalf("logical disk capacity leaked: %#v", sum.Disks)
	}
	raw, _ := json.Marshal(sum)
	text := string(raw)
	for _, forbidden := range []string{"www.baidu.com", "10.0.0.1", "old-host", "logical_disks", "chrome.exe"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("desktop summary leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"window_seconds":30`) {
		t.Fatalf("window_seconds missing: %s", text)
	}

	if _, _, err := BuildDesktopSummary(context.Background(), st, 10, false, now, nil); err == nil {
		t.Fatal("10 second window accepted")
	}
}
