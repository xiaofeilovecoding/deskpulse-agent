//go:build windows

package credential

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
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
