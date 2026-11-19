package ingest

import (
	"context"
	"errors"
	"math"
	"testing"
)

func fptr(v float64) *float64 { return &v }

func TestParsePayloadFormats(t *testing.T) {
	single := []byte(`{"name":"cpu","tags":{"cpu":"total"},"fields":{"usage_idle":90},"timestamp":1700000000000}`)
	raws, err := ParsePayload(single)
	if err != nil || len(raws) != 1 || raws[0].Name != "cpu" {
		t.Fatalf("single object: %v raws=%d", err, len(raws))
	}

	batch := []byte(`{"metrics":[{"name":"cpu","fields":{"usage_idle":80},"timestamp":1700000000000},{"name":"mem","fields":{"used":10,"total":100},"timestamp":1700000001000}]}`)
	raws, err = ParsePayload(batch)
	if err != nil || len(raws) != 2 {
		t.Fatalf("metrics array: %v raws=%d", err, len(raws))
	}

	nd := []byte(`{"name":"cpu","fields":{"usage_idle":80},"timestamp":1700000000000}
{"name":"mem","fields":{"used":10,"total":100},"timestamp":1700000001000}`)
	raws, err = ParsePayload(nd)
	if err != nil || len(raws) != 2 {
		t.Fatalf("ndjson: %v raws=%d", err, len(raws))
	}

	if _, err := ParsePayload(nil); err == nil {
		t.Fatal("empty payload should fail")
	}
	if _, err := ParsePayload([]byte(`{"not":"a metric"}`)); err == nil {
		t.Fatal("object without name/fields should fail")
	}
}

func TestNormalizeMetricUnitsAndCounters(t *testing.T) {
	const sampleAt int64 = 1_700_000_005_000
	loader := func() (map[string]CounterState, error) {
		return map[string]CounterState{
			"PhysicalDrive0": {TS: sampleAt - 5000, ReadBytes: 1000, WriteBytes: 500, Reads: 10, Writes: 5},
		}, nil
	}
	n := NewNormalizer(4, loader)
	raws := []RawMetric{
		{Name: "cpu", Tags: map[string]string{"cpu": "total"}, Fields: map[string]any{"usage_idle": 75.0}, Timestamp: sampleAt},
		{Name: "mem", Fields: map[string]any{"used": 256.0, "total": 1024.0}, Timestamp: sampleAt},
		{Name: "disk", Tags: map[string]string{"path": "C:"}, Fields: map[string]any{"used": 400.0, "total": 1000.0}, Timestamp: sampleAt},
		{Name: "diskio", Tags: map[string]string{"name": "PhysicalDrive0"}, Fields: map[string]any{
			"read_bytes": 6000.0, "write_bytes": 2500.0, "reads": 20.0, "writes": 15.0,
		}, Timestamp: sampleAt},
		{Name: "procstat", Tags: map[string]string{"process_name": "chrome"}, Fields: map[string]any{
			"pid": 42.0, "cpu_usage": 80.0, "memory_rss": 123456.0, "cmdline": "must-not-be-used",
		}, Timestamp: sampleAt},
		{Name: "ping", Tags: map[string]string{"url": "10.0.0.1"}, Fields: map[string]any{
			"average_response_ms": 12.5, "maximum_response_ms": 20.0, "percent_packet_loss": 0.0, "packets_received": 3.0,
		}, Timestamp: sampleAt},
	}
	batch, err := n.Normalize(context.Background(), raws)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Samples) != 5 || len(batch.Processes) != 1 {
		t.Fatalf("samples=%d processes=%d", len(batch.Samples), len(batch.Processes))
	}
	byKind := map[string]Sample{}
	for _, s := range batch.Samples {
		byKind[s.Kind] = s
	}
	if cpu := byKind["cpu"]; cpu.A == nil || math.Abs(*cpu.A-25) > 0.001 {
		t.Fatalf("cpu = %#v", cpu.A)
	}
	if mem := byKind["mem"]; mem.A == nil || *mem.A != 256 || mem.B == nil || *mem.B != 1024 || mem.C == nil || math.Abs(*mem.C-25) > 0.001 {
		t.Fatalf("mem = %#v", mem)
	}
	if disk := byKind["disk"]; disk.Series != "C:" || disk.C == nil || math.Abs(*disk.C-40) > 0.001 {
		t.Fatalf("disk = %#v", disk)
	}
	io := byKind["diskio"]
	if io.A == nil || math.Abs(*io.A-1000) > 0.001 || io.B == nil || math.Abs(*io.B-400) > 0.001 {
		t.Fatalf("diskio bytes/s = %v %v", io.A, io.B)
	}
	if io.C == nil || math.Abs(*io.C-2) > 0.001 || io.D == nil || math.Abs(*io.D-2) > 0.001 {
		t.Fatalf("diskio iops = %v %v", io.C, io.D)
	}
	if rtt := byKind["rtt"]; rtt.A == nil || *rtt.A != 12.5 || rtt.B == nil || *rtt.B != 20 || rtt.C == nil || *rtt.C != 0 {
		t.Fatalf("rtt = %#v", rtt)
	}
	p := batch.Processes[0]
	if p.Name != "chrome" || p.PID != 42 || p.CPU == nil || *p.CPU != 20 || p.WorkingSet == nil || *p.WorkingSet != 123456 {
		t.Fatalf("process = %#v", p)
	}
	if _, ok := batch.Counters["PhysicalDrive0"]; !ok {
		t.Fatal("counter state must be returned for persistence")
	}
}

func TestNormalizeFirstCounterAndResetAreNull(t *testing.T) {
	n := NewNormalizer(1, nil)
	first, err := n.Normalize(context.Background(), []RawMetric{{
		Name: "diskio", Tags: map[string]string{"name": "d0"},
		Fields:    map[string]any{"read_bytes": 1000.0, "write_bytes": 500.0, "reads": 10.0, "writes": 5.0},
		Timestamp: 1000,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Samples) != 1 || first.Samples[0].A != nil || first.Samples[0].B != nil {
		t.Fatalf("first counter sample must be null: %#v", first.Samples)
	}
	reset, err := n.Normalize(context.Background(), []RawMetric{{
		Name: "diskio", Tags: map[string]string{"name": "d0"},
		Fields:    map[string]any{"read_bytes": 10.0, "write_bytes": 5.0, "reads": 1.0, "writes": 1.0},
		Timestamp: 2000,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reset.Samples) != 1 || reset.Samples[0].A != nil || reset.Samples[0].B != nil {
		t.Fatalf("counter reset must be null: %#v", reset.Samples)
	}
	if len(reset.Warnings) == 0 {
		t.Fatal("counter reset must be visible as a warning")
	}
}

func TestNormalizeRTTTotalLossAndMissing(t *testing.T) {
	n := NewNormalizer(1, nil)
	batch, err := n.Normalize(context.Background(), []RawMetric{{
		Name: "ping", Tags: map[string]string{"url": "host"},
		Fields:    map[string]any{"percent_packet_loss": 100.0, "packets_received": 0.0},
		Timestamp: 5000,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Samples) != 1 {
		t.Fatalf("samples=%d", len(batch.Samples))
	}
	s := batch.Samples[0]
	if s.A != nil || s.B != nil || s.C == nil || *s.C != 100 {
		t.Fatalf("total loss must keep loss and null latency: %#v", s)
	}
}

func TestLoaderErrorSurfaces(t *testing.T) {
	n := NewNormalizer(1, func() (map[string]CounterState, error) {
		return nil, errors.New("db down")
	})
	if _, err := n.Normalize(context.Background(), []RawMetric{{Name: "cpu", Fields: map[string]any{"usage_idle": 10.0}, Timestamp: 1000}}); err == nil {
		t.Fatal("loader error must surface")
	}
}

func TestTimestampUnits(t *testing.T) {
	cases := map[int64]int64{
		1700000000:          1700000000000,
		1700000000000:       1700000000000,
		1700000000000000:    1700000000000,
		1700000000000000000: 1700000000000,
	}
	for in, want := range cases {
		if got := TimestampMS(in); got != want {
			t.Fatalf("TimestampMS(%d)=%d want %d", in, got, want)
		}
	}
}

func TestNumberRejectsNaN(t *testing.T) {
	if _, ok := Number(math.NaN()); ok {
		t.Fatal("NaN must not be a number")
	}
	if v, ok := Number("12.5"); !ok || v != 12.5 {
		t.Fatal("string number should parse")
	}
}
