package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/whyrusleeping/ycc/internal/codex"
	"github.com/whyrusleeping/ycc/internal/secrets"
)

// A browser login on a daemon nobody has set up must leave it working, but must
// never rewrite a configuration the user chose (or a fallback that has a key).
func TestAdoptSubscriptionLogin(t *testing.T) {
	cases := []struct {
		name     string
		fallback bool
		key      bool
		external bool // a ycc.toml written by another process after startup
		edited   bool // settings edited through this daemon
		changed  bool // fallback no longer matches its startup snapshot
		want     bool
	}{
		{name: "keyless fallback adopts", fallback: true, want: true},
		{name: "fallback with an API key is kept", fallback: true, key: true},
		{name: "loaded config is kept"},
		{name: "config written elsewhere is kept", fallback: true, external: true},
		{name: "edited fallback is kept", fallback: true, edited: true},
		{name: "changed fallback is kept", fallback: true, changed: true},
	}
	for _, backend := range []string{"anthropic", "openai"} {
		t.Run(backend, func(t *testing.T) {
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					isolateSecrets(t)
					t.Setenv("ANTHROPIC_API_KEY", "")
					if c.key {
						if err := secrets.Set("ANTHROPIC_API_KEY", "sk-test"); err != nil {
							t.Fatal(err)
						}
					}
					path := filepath.Join(t.TempDir(), "ycc.toml")
					reg := NewRegistry(DefaultAnthropic(DefaultAnthropicBaseURL, "claude-opus-5-5", "ANTHROPIC_API_KEY", DefaultMaxTokens))
					reg.SetPath(path)
					if c.fallback {
						reg.MarkBuiltinFallback()
					}
					if c.external {
						if err := os.WriteFile(path, []byte("# written by ycc login\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					if c.edited || c.changed {
						m, _ := reg.GetModel(FallbackModelName)
						m.BaseURL, m.KeyEnv = "https://gateway.example", "GATEWAY_KEY"
						if c.edited {
							if err := reg.UpsertModel(FallbackModelName, m, true); err != nil {
								t.Fatal(err)
							}
						} else {
							reg.cfg.Models[FallbackModelName] = m
						}
					}
					before, err := json.Marshal(reg.cfg)
					if err != nil {
						t.Fatal(err)
					}
					adopt := reg.AdoptAnthropicLogin
					if backend == "openai" {
						adopt = reg.AdoptOpenAILogin
					}
					got, err := adopt()
					if err != nil || got != c.want {
						t.Fatalf("adopt = %v, %v; want %v", got, err, c.want)
					}
					if !c.want {
						after, _ := json.Marshal(reg.cfg)
						if string(before) != string(after) {
							t.Fatal("login changed chosen configuration")
						}
						return
					}
					saved, err := Load(path)
					if err != nil {
						t.Fatalf("adopted config not persisted: %v", err)
					}
					name, modelID := FallbackModelName, "claude-opus-5-5"
					if backend == "openai" {
						name, modelID = "chatgpt", codex.Models[0]
						if _, ok := saved.Models[FallbackModelName]; ok {
							t.Fatal("ChatGPT adoption retained the unusable fallback")
						}
					}
					m := saved.Models[name]
					if len(saved.Models) != 1 || m.Backend != backend || m.Auth != "oauth" || m.KeyEnv != "" || m.Model != modelID ||
						saved.Roles.Coordinator != name || saved.Roles.Implementer != name || !reflect.DeepEqual(saved.Roles.Reviewers, []string{name}) {
						t.Fatalf("persisted models %+v roles %+v", saved.Models, saved.Roles)
					}
					if again, _ := adopt(); again {
						t.Fatal("a second login rewrote the now-chosen config")
					}
				})
			}
		})
	}
}

func TestAdoptSubscriptionLoginRollsBackOnPersistFailure(t *testing.T) {
	for _, backend := range []string{"anthropic", "openai"} {
		t.Run(backend, func(t *testing.T) {
			isolateSecrets(t)
			t.Setenv("ANTHROPIC_API_KEY", "")
			path := filepath.Join(t.TempDir(), "ycc.toml")
			// Stat sees no config, but writing through this dangling symlink fails
			// even as root: its target directory does not exist.
			if err := os.Symlink(filepath.Join(t.TempDir(), "missing", "ycc.toml"), path); err != nil {
				t.Fatal(err)
			}
			reg := NewRegistry(DefaultAnthropic(DefaultAnthropicBaseURL, "claude-opus-5-5", "ANTHROPIC_API_KEY", DefaultMaxTokens))
			reg.SetPath(path)
			reg.MarkBuiltinFallback()
			before, _ := json.Marshal(reg.cfg)
			adopt := reg.AdoptAnthropicLogin
			if backend == "openai" {
				adopt = reg.AdoptOpenAILogin
			}
			if got, err := adopt(); err == nil || got {
				t.Fatalf("persist failure: adopted=%v err=%v", got, err)
			}
			after, _ := json.Marshal(reg.cfg)
			if string(before) != string(after) {
				t.Fatal("failed persistence changed models or roles")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if got, err := adopt(); !got || err != nil {
				t.Fatalf("failed persistence lost fallback status: adopted=%v err=%v", got, err)
			}
		})
	}
}
