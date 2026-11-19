package winmetrics

import "testing"

func f(v float64) *float64 { return &v }

func TestBuildTreeAggregatesOnceAndSeparatesOrphans(t *testing.T) {
	rows := []ProcessInfo{
		{PID: 100, PPID: 0, Name: "root.exe", StartMS: 1000, CPU: f(10), WorkingSet: f(100)},
		{PID: 200, PPID: 100, Name: "child.exe", StartMS: 1100, CPU: f(20), WorkingSet: f(200)},
		{PID: 300, PPID: 200, Name: "grand.exe", StartMS: 1200, CPU: f(30), WorkingSet: f(300)},
		{PID: 400, PPID: 999, Name: "orphan.exe", StartMS: 1300, CPU: f(5), WorkingSet: f(50)},
	}
	tree := BuildTree(rows)
	if tree.Total != 4 || len(tree.Roots) != 1 || len(tree.Orphans) != 1 {
		t.Fatalf("tree roots=%d orphans=%d total=%d", len(tree.Roots), len(tree.Orphans), tree.Total)
	}
	root := tree.Roots[0]
	if root.Proc.PID != 100 || root.Descendants != 2 {
		t.Fatalf("root = %#v", root)
	}
	if root.TotalCPU == nil || *root.TotalCPU != 60 {
		t.Fatalf("root total cpu = %v", root.TotalCPU)
	}
	if root.TotalWS == nil || *root.TotalWS != 600 {
		t.Fatalf("root total ws = %v", root.TotalWS)
	}
	if len(root.Children) != 1 || root.Children[0].Descendants != 1 {
		t.Fatalf("child = %#v", root.Children)
	}
	if tree.Orphans[0].Proc.Name != "orphan.exe" || !tree.Orphans[0].Orphan {
		t.Fatalf("orphan = %#v", tree.Orphans[0])
	}
}

func TestBuildTreeSameNameDifferentTrees(t *testing.T) {
	tree := BuildTree([]ProcessInfo{
		{PID: 1, PPID: 0, Name: "svc.exe", StartMS: 10},
		{PID: 2, PPID: 1, Name: "svc.exe", StartMS: 20},
		{PID: 3, PPID: 0, Name: "svc.exe", StartMS: 30},
	})
	if len(tree.Roots) != 2 {
		t.Fatalf("roots = %d", len(tree.Roots))
	}
	if tree.Roots[0].Proc.PID == tree.Roots[1].Proc.PID {
		t.Fatal("same PID reused")
	}
}

func TestBuildTreePIDReuseAndCycles(t *testing.T) {
	tree := BuildTree([]ProcessInfo{
		{PID: 500, PPID: 0, Name: "new-parent.exe", StartMS: 5000},
		{PID: 501, PPID: 500, Name: "old-child.exe", StartMS: 4000},
	})
	if len(tree.Roots) != 1 || len(tree.Orphans) != 1 {
		t.Fatalf("reuse tree roots=%d orphans=%d", len(tree.Roots), len(tree.Orphans))
	}
	if !tree.Orphans[0].Orphan || tree.Orphans[0].Proc.PID != 501 {
		t.Fatalf("reuse orphan = %#v", tree.Orphans)
	}

	cycle := BuildTree([]ProcessInfo{
		{PID: 1, PPID: 2, Name: "a.exe", StartMS: 10},
		{PID: 2, PPID: 1, Name: "b.exe", StartMS: 10},
	})
	if cycle.Total != 2 || len(cycle.Roots) != 0 || len(cycle.Orphans) != 2 {
		t.Fatalf("cycle roots=%d orphans=%d total=%d", len(cycle.Roots), len(cycle.Orphans), cycle.Total)
	}
}

func TestCalculations(t *testing.T) {
	prev := cpuCounters{idle: 100, kernel: 200, user: 100, valid: true}
	cur := cpuCounters{idle: 150, kernel: 350, user: 150, valid: true}
	got := cpuPercentDelta(cur, prev)
	if got == nil || *got < 74.9 || *got > 75.1 {
		t.Fatalf("cpu = %v", got)
	}
	if cpuPercentDelta(cur, cpuCounters{}) != nil {
		t.Fatal("first cpu sample must be nil")
	}
	if rateFromCounter(50, 100, 5) != nil {
		t.Fatal("counter reset must be nil")
	}
	if v := rateFromCounter(100, 50, 301); v != nil {
		t.Fatalf("long interval = %v", v)
	}
	if v := rateFromCounter(250, 100, 5); v == nil || *v != 30 {
		t.Fatalf("rate = %v", v)
	}
	if v := processCPUPercent(1e7, 1, 4); v == nil || *v != 25 {
		t.Fatalf("process cpu = %v", v)
	}
	if v := lossPercent(3, 1); v == nil || *v < 66.6 || *v > 66.7 {
		t.Fatalf("loss = %v", v)
	}
}
