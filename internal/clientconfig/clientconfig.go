// Package clientconfig persists client-only UI preferences (spec §18.2): the
// theme and the follow/auto-scroll toggle. These never touch the daemon — they
// live in a small local file under the user config dir.
package clientconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Prefs are the client-only UI preferences.
type Prefs struct {
	Theme          string `json:"theme"`          // "dark" | "light"
	Follow         bool   `json:"follow"`         // auto-scroll + auto-select latest
	AutoExpandLogs bool   `json:"autoExpandLogs"` // expand all agent log events by default
	NotifyBell     bool   `json:"notifyBell"`     // terminal bell (BEL) on question/idle/error/interrupt
	NotifyDesktop  bool   `json:"notifyDesktop"`  // OSC 9 desktop notification on those events
}

// Default returns the built-in defaults.
func Default() Prefs { return Prefs{Theme: "dark", Follow: true, NotifyBell: true} }

// path returns the prefs file location (best-effort).
func path() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "ycc", "client.json")
}

// Load reads the persisted prefs, falling back to defaults on any error.
func Load() Prefs {
	p := Default()
	fp := path()
	if fp == "" {
		return p
	}
	data, err := os.ReadFile(fp)
	if err != nil {
		return p
	}
	_ = json.Unmarshal(data, &p)
	if p.Theme == "" {
		p.Theme = "dark"
	}
	return p
}

// Save writes the prefs to the local client config file (best-effort).
func Save(p Prefs) error {
	fp := path()
	if fp == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fp, data, 0o644)
}
