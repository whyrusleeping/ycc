package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
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

func TestConcurrentSetPreservesAllKeys(t *testing.T) {
	setupDir(t)

	const count = 32
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("KEY_%02d", i)
			errs <- Set(key, fmt.Sprintf("token-%02d", i))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Set: %v", err)
		}
	}

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Tokens) != count {
		t.Fatalf("stored token count = %d, want %d", len(s.Tokens), count)
	}
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("KEY_%02d", i)
		want := fmt.Sprintf("token-%02d", i)
		if got := s.Tokens[key]; got != want {
			t.Errorf("token %q = %q, want %q", key, got, want)
		}
	}
}

func TestSetRepairsPermissions(t *testing.T) {
	setupDir(t)

	fp := Path()
	dir := filepath.Dir(fp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod dir: %v", err)
	}
	if err := os.WriteFile(fp, []byte(`{"tokens":{"OLD":"old-token"}}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(fp, 0o644); err != nil {
		t.Fatalf("Chmod file: %v", err)
	}

	if err := Set("NEW", "new-token"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	fi, err := os.Stat(fp)
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 600", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 700", perm)
	}
	if got, ok := Lookup("OLD"); !ok || got != "old-token" {
		t.Errorf("old token after permission repair = %q,%v", got, ok)
	}
}

func TestWriteFailurePreservesPreviousStore(t *testing.T) {
	setupDir(t)

	if err := Set("OLD", "old-token"); err != nil {
		t.Fatalf("initial Set: %v", err)
	}
	writeErr := errors.New("injected create-temp failure")
	oldCreateTempFile := createTempFile
	createTempFile = func(string, string) (*os.File, error) {
		return nil, writeErr
	}
	t.Cleanup(func() { createTempFile = oldCreateTempFile })

	if err := Set("NEW", "new-token"); !errors.Is(err, writeErr) {
		t.Fatalf("Set error = %v, want %v", err, writeErr)
	}

	s, err := Load()
	if err != nil {
		t.Fatalf("Load previous store: %v", err)
	}
	if want := map[string]string{"OLD": "old-token"}; !reflect.DeepEqual(s.Tokens, want) {
		t.Fatalf("tokens after write failure = %v, want %v", s.Tokens, want)
	}
}
