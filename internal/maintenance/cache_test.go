package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveAndRestorePreserveOtherFiles(t *testing.T) {
	dir := t.TempDir()
	cache, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	rel, err := cache.StageSnapshot([]byte("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, cacheDir, "user-document.txt")
	if err := os.WriteFile(other, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := cache.Preview(time.Now().Add(8 * 24 * time.Hour))
	if err != nil || len(plan) != 1 {
		t.Fatalf("preview: %v, %v", plan, err)
	}
	if n, err := cache.Archive(plan); n != 1 || err != nil {
		t.Fatalf("archive: %d, %v", n, err)
	}
	path := filepath.Join(dir, rel)
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := cache.Restore(); n != 0 || err == nil {
		t.Fatalf("restore over an existing file: %d, %v", n, err)
	}
	if got, err := os.ReadFile(path); string(got) != "new" || err != nil {
		t.Fatalf("existing item changed: %q, %v", got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if n, err := cache.Restore(); n != 1 || err != nil {
		t.Fatalf("restore: %d, %v", n, err)
	}
	if got, err := os.ReadFile(path); string(got) != "synthetic" || err != nil {
		t.Fatalf("restored bytes: %q, %v", got, err)
	}
	if got, err := os.ReadFile(other); string(got) != "keep" || err != nil {
		t.Fatalf("unrelated file: %q, %v", got, err)
	}
}

func TestChangedSnapshotIsSkipped(t *testing.T) {
	dir := t.TempDir()
	cache, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	rel, err := cache.StageSnapshot([]byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := cache.Preview(time.Now().Add(8 * 24 * time.Hour))
	if err != nil || len(plan) != 1 {
		t.Fatalf("preview: %v, %v", plan, err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := cache.Archive(plan); n != 0 || err == nil {
		t.Fatalf("archive after change: %d, %v", n, err)
	}
}
