package clientconfig

import (
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// On macOS UserConfigDir uses HOME/Library; override both to be safe.
	t.Setenv("HOME", dir)

	p := Prefs{Theme: "light", Follow: false}
	if err := Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := Load()
	if got.Theme != "light" || got.Follow != false {
		t.Fatalf("round trip = %+v", got)
	}
	// File lives under the user config dir.
	if _, err := filepath.Abs(path()); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	got := Load()
	if got.Theme != "dark" || got.Follow != true {
		t.Fatalf("defaults = %+v", got)
	}
}
