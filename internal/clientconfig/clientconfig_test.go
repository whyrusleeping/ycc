package clientconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// On macOS UserConfigDir uses HOME/Library; override both to be safe.
	t.Setenv("HOME", dir)

	// Without a file, Load yields the defaults rather than a zero Prefs.
	if got := Load(); got != Default() {
		t.Fatalf("Load without file = %+v, want %+v", got, Default())
	}
	p := Prefs{Theme: "light", Follow: false, AutoExpandLogs: true}
	if err := Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := Load()
	if got.Theme != "light" || got.Follow != false || got.AutoExpandLogs != true {
		t.Fatalf("round trip = %+v", got)
	}
}

// TestLoadKeepsNotifyDefaultsWhenKeysAbsent verifies that a config file written
// before the notify prefs existed still loads NotifyBell=true (Load unmarshals
// over Default), while an explicit false in the file is honoured (task 0108).
func TestLoadKeepsNotifyDefaultsWhenKeysAbsent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	fp := path()
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		t.Fatal(err)
	}
	// Legacy file lacking the notify* keys.
	if err := os.WriteFile(fp, []byte(`{"theme":"light","follow":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.NotifyBell != true {
		t.Fatalf("absent notifyBell should default true, got %+v", got)
	}
	if got.NotifyDesktop != false {
		t.Fatalf("absent notifyDesktop should default false, got %+v", got)
	}

	// Explicit false in the file must be honoured (not reset to the default).
	if err := os.WriteFile(fp, []byte(`{"notifyBell":false,"notifyDesktop":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got = Load()
	if got.NotifyBell != false || got.NotifyDesktop != true {
		t.Fatalf("explicit notify values not honoured: %+v", got)
	}
}
