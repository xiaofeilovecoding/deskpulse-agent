package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/ingest"
)

func fp(v float64) *float64 { return &v }

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "deskpulse-agent.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestInsertWindowAndRestart(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	from := now - 5*60*1000
	batch := ingest.Batch{
		Samples: []ingest.Sample{
			{TS: from + 100, Kind: "cpu", Series: "total", A: fp(10)},
			{TS: from + 200, Kind: "cpu", Series: "total", A: fp(99)},
			{TS: from + 300, Kind: "cpu", Series: "total", A: fp(20)},
			{TS: from + 300, Kind: "mem", Series: "memory", A: fp(512), B: fp(2048), C: fp(25)},
			{TS: from + 300, Kind: "disk", Series: "C:", A: fp(100), B: fp(1000), C: fp(10)},
			{TS: from + 300, Kind: "diskio", Series: "PhysicalDrive0", A: fp(2048), B: fp(1024), C: fp(4), D: fp(2)},
			{TS: from + 300, Kind: "rtt", Series: "host", A: fp(12), B: fp(23), C: fp(0)},
			{TS: from + 300, Kind: "rtt", Series: "previous-host", A: fp(180), B: fp(220), C: fp(0)},
		},
		Processes: []ingest.ProcessSample{
			{TS: from + 300, Name: "chrome", PID: 10, CPU: fp(90), WorkingSet: fp(2 << 30)},
			{TS: from + 300, Name: "idle", PID: 20, CPU: fp(1), WorkingSet: fp(1 << 20)},
		},
		ProcessTS: from + 300,
		Counters: map[string]ingest.CounterState{
			"PhysicalDrive0": {TS: from + 300, ReadBytes: 1000, WriteBytes: 500, Reads: 10, Writes: 5},
		},
	}
	if err := st.InsertBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	win, err := st.QueryWindow(ctx, from, now, "host")
	if err != nil {
		t.Fatal(err)
	}
	if len(win.CPU) == 0 {
		t.Fatal("cpu points missing")
	}
	var peak float64
	for _, p := range win.CPU {
		if p.Max != nil && *p.Max > peak {
			peak = *p.Max
		}
	}
	if peak < 99 {
		t.Fatalf("peak not preserved: %v", peak)
	}
	if win.MemPercent == nil || *win.MemPercent != 25 {
		t.Fatalf("memory latest = %#v", win.MemPercent)
	}
	if len(win.Disks) != 1 || win.Disks[0].Series != "C:" {
		t.Fatalf("disks = %#v", win.Disks)
	}
	if len(win.DiskIO) != 1 || win.DiskIO[0].LastRead == nil || *win.DiskIO[0].LastRead != 2048 {
		t.Fatalf("io = %#v", win.DiskIO)
	}
	if len(win.RTT) != 1 || win.RTT[0].Avg == nil || *win.RTT[0].Avg != 12 {
		t.Fatalf("rtt = %#v", win.RTT)
	}

	procs, snapshot, err := st.QueryProcesses(ctx, "cpu", "", 200)
	if err != nil || len(procs) != 2 || snapshot == 0 || procs[0].Name != "chrome" {
		t.Fatalf("processes=%d snapshot=%d err=%v", len(procs), snapshot, err)
	}
	if procs, _, _ := st.QueryProcesses(ctx, "memory", "", 200); procs[0].Name != "chrome" {
		t.Fatalf("memory sort = %#v", procs)
	}

	counters, err := st.LoadCounters(ctx)
	if err != nil || counters["PhysicalDrive0"].ReadBytes != 1000 {
		t.Fatalf("counters=%#v err=%v", counters, err)
	}

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Join(filepath.Dir(st.DBPath()), "deskpulse-agent.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	procs, _, err = reopened.QueryProcesses(ctx, "cpu", "", 200)
	if err != nil || len(procs) != 2 {
		t.Fatalf("history after restart: %d %v", len(procs), err)
	}
}

func TestProcessLatestCapAndTopUnion(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	var procs []ingest.ProcessSample
	for i := 1; i <= 250; i++ {
		procs = append(procs, ingest.ProcessSample{
			TS: now, Name: "proc" + string(rune('A'+i%26)), PID: int32(i),
			CPU: fp(float64(i)), WorkingSet: fp(float64(i) * 1024),
		})
	}
	procs = append(procs, ingest.ProcessSample{TS: now, Name: "memory-hog", PID: 999, CPU: fp(0.1), WorkingSet: fp(64 << 30)})
	if err := st.InsertBatch(ctx, ingest.Batch{Processes: procs, ProcessTS: now}); err != nil {
		t.Fatal(err)
	}
	latest, _, err := st.QueryProcesses(ctx, "cpu", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 200 {
		t.Fatalf("latest cap = %d", len(latest))
	}
	aggs, err := st.TopProcesses(ctx, now-5*60*1000, now+1000, 12)
	if err != nil {
		t.Fatal(err)
	}
	foundMem := false
	for _, p := range aggs {
		if p.Name == "memory-hog" {
			foundMem = true
		}
	}
	if !foundMem {
		t.Fatalf("memory-heavy process missing from top union: %#v", aggs)
	}
}

func TestWriteFailureIsVisibleInStatus(t *testing.T) {
	st := newTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	err := st.InsertBatch(context.Background(), ingest.Batch{Samples: []ingest.Sample{
		{TS: time.Now().UnixMilli(), Kind: "cpu", Series: "total", A: fp(1)},
	}})
	if err == nil {
		t.Fatal("write after close should fail")
	}
	status := st.Status()
	if status.Ready || status.LastError == "" {
		t.Fatalf("storage status must expose failure: %#v", status)
	}
}

func TestRetentionAndDiagnosisLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-8 * 24 * time.Hour).UnixMilli()
	recent := now.UnixMilli()
	if err := st.InsertBatch(ctx, ingest.Batch{Samples: []ingest.Sample{
		{TS: old, Kind: "cpu", Series: "total", A: fp(1)},
		{TS: recent, Kind: "cpu", Series: "total", A: fp(2)},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Maintain(ctx, now); err != nil {
		t.Fatal(err)
	}
	counts, err := st.CountSamples(ctx, old, recent+1)
	if err != nil || counts["cpu"] != 1 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}

	d := Diagnosis{ID: "abc", CreatedAt: recent, UpdatedAt: recent, WindowMinutes: 15, Model: "deepseek-flash", Status: "running"}
	if err := st.CreateDiagnosis(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkInterrupted(ctx, now); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetDiagnosis(ctx, "abc")
	if err != nil || got.Status != "interrupted" {
		t.Fatalf("diagnosis=%#v err=%v", got, err)
	}
	if err := st.UpdateDiagnosis(ctx, "abc", "completed", "", "", `{"summary":"ok"}`, now); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListDiagnoses(ctx, 10)
	if err != nil || len(list) != 1 || list[0].Status != "completed" {
		t.Fatalf("list=%#v err=%v", list, err)
	}
}
