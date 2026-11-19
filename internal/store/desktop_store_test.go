package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/ingest"
)

func TestDesktopSnapshotProcessTreeAndMultiRTT(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "deskpulse-agent.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	ts := now.UnixMilli()
	samples := []ingest.Sample{
		{TS: ts, Kind: "cpu", Series: "total", A: fp(12)},
		{TS: ts, Kind: "mem", Series: "memory", A: fp(512), B: fp(2048), C: fp(25)},
		{TS: ts, Kind: "diskio", Series: "total", A: fp(2048), B: fp(1024), C: fp(4), D: fp(2)},
		{TS: ts, Kind: "rtt", Series: "www.baidu.com", A: fp(24), B: fp(31), C: fp(0)},
		{TS: ts, Kind: "rtt", Series: "10.0.0.1", A: fp(3), B: fp(4), C: fp(0)},
		{TS: ts, Kind: "rtt", Series: "old-host", A: fp(180), B: fp(200), C: fp(0)},
	}
	procs := []ingest.ProcessSample{
		{TS: ts, Name: "root.exe", PID: 100, CPU: fp(10), WorkingSet: fp(100)},
		{TS: ts, Name: "child.exe", PID: 200, CPU: fp(20), WorkingSet: fp(200)},
	}
	tree := []ProcessTreeRow{
		{PID: 100, PPID: 0, Name: "root.exe", StartMS: 1000, CPU: fp(10), WorkingSet: fp(100), TS: ts},
		{PID: 200, PPID: 100, Name: "child.exe", StartMS: 1100, CPU: fp(20), WorkingSet: fp(200), TS: ts},
	}
	if err := st.InsertDesktopSnapshot(context.Background(), ts, samples, procs, tree); err != nil {
		t.Fatal(err)
	}
	rows, err := st.LoadProcessTree(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("tree rows=%d err=%v", len(rows), err)
	}
	if rows[0].PPID != 0 || rows[1].PPID != 100 || rows[1].StartMS != 1100 {
		t.Fatalf("tree = %#v", rows)
	}
	rtt, err := st.QueryRTTTargets(context.Background(), now.Add(-time.Minute).UnixMilli(), now.Add(time.Minute).UnixMilli(), []string{"www.baidu.com", "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rtt["www.baidu.com"]) != 1 || len(rtt["10.0.0.1"]) != 1 {
		t.Fatalf("rtt = %#v", rtt)
	}
	win, err := st.QueryWindow(context.Background(), now.Add(-time.Minute).UnixMilli(), now.Add(time.Minute).UnixMilli(), "www.baidu.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(win.Disks) != 0 {
		t.Fatalf("desktop snapshot unexpectedly wrote logical disks: %#v", win.Disks)
	}
	if len(win.RTT) != 1 || win.RTT[0].Avg == nil || *win.RTT[0].Avg != 24 {
		t.Fatalf("window rtt = %#v", win.RTT)
	}

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(filepath.Join(dir, "deskpulse-agent.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	rows, err = st2.LoadProcessTree(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("tree after restart = %d %v", len(rows), err)
	}
}
