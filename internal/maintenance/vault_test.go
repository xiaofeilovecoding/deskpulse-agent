package maintenance

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveryCopyBeforeSourceRemoval(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	if candidate := os.Getenv("DESKPULSE_VAULT_SMOKE_DIR"); candidate != "" {
		dir, err := os.MkdirTemp(candidate, "vault-smoke-*")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		destination = dir
	}
	cache, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	want := []byte("synthetic snapshot")
	rel, err := cache.StageSnapshot(want)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := cache.Preview(time.Now().Add(8 * 24 * time.Hour))
	if err != nil || len(plans) != 1 {
		t.Fatalf("preview failed: %v", err)
	}
	if _, err := cache.ArchiveTo(plans, filepath.Join(destination, "missing")); err == nil {
		t.Fatal("archive attempted to use a missing parent")
	}
	if got, err := os.ReadFile(filepath.Join(source, rel)); err != nil || !bytes.Equal(got, want) {
		t.Fatal("snapshot changed after a failed backup")
	}
	result, err := cache.ArchiveTo(plans, destination)
	if err != nil || result.Files != 1 || result.SourceBytes != int64(len(want)) {
		t.Fatalf("archive: %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(source, rel)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected original state: %v", err)
	}
	if n, err := cache.RestoreFrom(destination); n != 1 || err != nil {
		t.Fatalf("restore: %d, %v", n, err)
	}
	if n, err := cache.RestoreFrom(destination); n != 0 || err != nil {
		t.Fatalf("idempotent restore: %d, %v", n, err)
	}
	if err := os.WriteFile(filepath.Join(source, rel), []byte("another file"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := cache.RestoreFrom(destination); n != 0 || err == nil {
		t.Fatalf("a conflict was not reported: %d, %v", n, err)
	}
	if got, err := os.ReadFile(filepath.Join(source, rel)); err != nil || string(got) != "another file" {
		t.Fatal("conflict overwrote a new file")
	}
}

func TestDamagedRecoveryCopyIsRefused(t *testing.T) {
	cache, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	destination := t.TempDir()
	vault, err := openVault(destination, true)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	if err := writeExclusive(vault, newID()+".dpbackup",
		[]byte(`{"version":1,"snapshot":"snapshot-0000000000000000.tmp","data":"dGVzdA=="}`)); err != nil {
		t.Fatal(err)
	}
	if n, err := cache.RestoreFrom(destination); n != 0 || err == nil {
		t.Fatalf("an invalid checksum was accepted: %d, %v", n, err)
	}
}
