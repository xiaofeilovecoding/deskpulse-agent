//go:build windows

package credential

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDPAPIRoundTripAndNoPlaintext(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	var seed [24]byte
	rand.Read(seed[:])
	key := hex.EncodeToString(seed[:])
	if err := s.Set(key); err != nil {
		t.Fatalf("Set: %v", err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(key)) {
		t.Fatal("plaintext key found in DPAPI ciphertext file")
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != key {
		t.Fatalf("round trip = %q", got)
	}
	if st := s.Status(); !st.Present {
		t.Fatalf("status = %#v", st)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing after clear, got %v", err)
	}
	if err := s.Clear(); err != nil {
		t.Fatalf("idempotent clear: %v", err)
	}
}

func TestCiphertextCannotMoveBetweenEndpoints(t *testing.T) {
	dir := t.TempDir()
	a := ForEndpoint(dir, "responses\nhttps://example.invalid/v1")
	b := ForEndpoint(dir, "responses\nhttps://example.invalid/other")
	if err := a.Set("synthetic-key-no-vendor"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(a.Path())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(b.Path()) != filepath.Dir(a.Path()) || a.Path() == b.Path() {
		t.Fatal("incorrect scope for scoped credential paths")
	}
	if err := os.WriteFile(b.Path(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(); err == nil {
		t.Fatal("mismatched endpoint decrypted another endpoint's ciphertext")
	}
}
