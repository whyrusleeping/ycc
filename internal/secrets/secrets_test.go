package secrets

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func setupDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// On macOS UserConfigDir uses HOME/Library; override both to be safe.
	t.Setenv("HOME", dir)
}

func TestSetLookupRoundTrip(t *testing.T) {
	setupDir(t)

	if err := Set("ANTHROPIC_API_KEY", "sk-ant-123"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := Set("OPENAI_API_KEY", "sk-oai-456"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	tok, ok := Lookup("ANTHROPIC_API_KEY")
	if !ok || tok != "sk-ant-123" {
		t.Fatalf("Lookup = %q,%v", tok, ok)
	}

	keys := Keys()
	if want := []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("Keys = %v, want %v", keys, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	setupDir(t)

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Tokens == nil {
		t.Fatal("Tokens map is nil")
	}
	if len(s.Tokens) != 0 {
		t.Fatalf("expected empty store, got %v", s.Tokens)
	}
	if _, ok := Lookup("ANTHROPIC_API_KEY"); ok {
		t.Fatal("Lookup on missing file returned ok=true")
	}
}

func TestSavePermissions(t *testing.T) {
	setupDir(t)

	if err := Set("ANTHROPIC_API_KEY", "sk-ant-123"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	fi, err := os.Stat(Path())
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file perm = %o, want 600", perm)
	}

	di, err := os.Stat(filepath.Dir(Path()))
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir perm = %o, want 700", perm)
	}
}

func TestLookupEmptyTokenAbsent(t *testing.T) {
	setupDir(t)

	if err := Set("EMPTY_KEY", ""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, ok := Lookup("EMPTY_KEY"); ok {
		t.Fatal("Lookup returned ok=true for empty token")
	}
}

func TestRemove(t *testing.T) {
	setupDir(t)

	if err := Set("K", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := Remove("K"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := Lookup("K"); ok {
		t.Fatal("token still present after Remove")
	}
}
