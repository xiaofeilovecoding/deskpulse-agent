package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadDefaultsAndV1Migration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	if len(got.RTTTargets) != 0 {
		t.Fatalf("default targets = %#v", got.RTTTargets)
	}
	if got.AIWindowSeconds != 30 || got.DeepSeekModel != DefaultModel {
		t.Fatalf("defaults = %#v", got)
	}

	v1 := `{"schema_version":1,"rtt_target":"example.com","deepseek_model":"deepseek-flash"}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got = s.Get()
	if !reflect.DeepEqual(got.RTTTargets, []string{"example.com"}) {
		t.Fatalf("migrated targets = %#v", got.RTTTargets)
	}
	if got.AIWindowSeconds != 30 {
		t.Fatalf("migrated AI window = %d", got.AIWindowSeconds)
	}
}

func TestExplicitEmptyV2TargetsStayEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":2,"rtt_targets":[],"ai_window_seconds":45}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get(); len(got.RTTTargets) != 0 || got.AIWindowSeconds != 45 {
		t.Fatalf("settings = %#v", got)
	}
	if err := s.Save(s.Get()); err != nil {
		t.Fatal(err)
	}
	s2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Get().RTTTargets) != 0 {
		t.Fatalf("empty list did not persist: %#v", s2.Get().RTTTargets)
	}
}

func TestNormalizeTargets(t *testing.T) {
	got, err := NormalizeTargets([]string{" www.baidu.com ", "WWW.BAIDU.COM", "10.0.0.1", "2001:db8::1"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"www.baidu.com", "10.0.0.1", "2001:db8::1"}) {
		t.Fatalf("targets = %#v", got)
	}
	for _, bad := range []string{"https://example.com", "example.com:443", "example.com/path", "bad host", "http://x"} {
		if err := ValidateTarget(bad); err == nil {
			t.Fatalf("ValidateTarget(%q) accepted", bad)
		}
	}
	if _, err := NormalizeTargets([]string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}); err == nil {
		t.Fatal("nine targets accepted")
	}
	if err := ValidateAIWindow(14); err == nil {
		t.Fatal("14 seconds accepted")
	}
	if err := ValidateAIWindow(3601); err == nil {
		t.Fatal("3601 seconds accepted")
	}
}

func TestMoveTarget(t *testing.T) {
	in := []string{"a", "b", "c"}
	got := MoveTarget(in, 2, 0)
	if !reflect.DeepEqual(got, []string{"c", "a", "b"}) {
		t.Fatalf("move = %#v", got)
	}
	if !reflect.DeepEqual(in, []string{"a", "b", "c"}) {
		t.Fatalf("input mutated = %#v", in)
	}
	if got := MoveTarget(in, 9, 0); !reflect.DeepEqual(got, in) {
		t.Fatalf("invalid move = %#v", got)
	}
}

func TestBallPositionPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := New(path)
	v := s.Get()
	v.BallPosition = WindowPosition{X: 123, Y: 456, Set: true}
	if err := s.Save(v); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Get().BallPosition; got != v.BallPosition {
		t.Fatalf("position = %#v want %#v", got, v.BallPosition)
	}
}
