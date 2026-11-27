package daemon

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/session"
)

// Exercise the same post-login adoption as the browser RPCs, without performing
// OAuth, starting a listener, or running provider health checks.
func TestStartupSubscriptionAdoption(t *testing.T) {
	for _, provider := range []struct {
		name  string
		adopt func(*session.Manager) (bool, error)
	}{
		{"anthropic", (*session.Manager).AdoptAnthropicLogin},
		{"openai", (*session.Manager).AdoptOpenAILogin},
	} {
		for _, tc := range []struct {
			name string
			opts Options
		}{
			{"implicit defaults", Options{
				Model: "claude-opus-5-5", BaseURL: config.DefaultAnthropicBaseURL,
				KeyEnv: "ANTHROPIC_API_KEY", MaxTokens: config.DefaultMaxTokens, BuiltinFallback: true,
			}},
			{"explicit options", Options{
				Model: "test-model", BaseURL: "https://example.invalid",
				KeyEnv: "YCC_STARTUP_TEST_API_KEY", MaxTokens: 1024,
			}},
		} {
			t.Run(provider.name+"/"+tc.name, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				t.Setenv("APPDATA", t.TempDir())
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				o := tc.opts
				o.Workspace = t.TempDir()
				t.Setenv(o.KeyEnv, "")
				parts, err := buildHandler(o, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(parts.mgr.ReclaimAll)
				before, ok := parts.reg.GetModel(config.FallbackModelName)
				if !ok || before.Model != o.Model || before.BaseURL != o.BaseURL || before.KeyEnv != o.KeyEnv || parts.reg.MaxTokens() != o.MaxTokens {
					t.Fatal("startup did not use the supplied model options")
				}
				guidance := strings.Join(startupGuidance(o, parts.mgr, false), "\n")
				path, err := UserConfigPath()
				if err != nil {
					t.Fatal(err)
				}
				want := o.BuiltinFallback
				if adopted, err := provider.adopt(parts.mgr); err != nil || adopted != want {
					t.Fatalf("post-login adoption = %v, %v; want %v", adopted, err, want)
				}
				if got := strings.Contains(guidance, "no model is set up yet:"); got != want {
					t.Errorf("first-run guidance = %v, want %v: %s", got, want, guidance)
				}
				if !want {
					after, ok := parts.reg.GetModel(config.FallbackModelName)
					if !ok || !reflect.DeepEqual(before, after) || parts.reg.MaxTokens() != o.MaxTokens {
						t.Error("login changed the explicit model configuration")
					}
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Errorf("expected no config file after login, stat error = %v", err)
					}
					return
				}
				saved, err := config.Load(path)
				if err != nil {
					t.Fatalf("read adopted config: %v", err)
				}
				if model := saved.Models[saved.Roles.Coordinator]; model.Auth != "oauth" || model.Backend != provider.name {
					t.Errorf("persisted coordinator = %+v, want %s OAuth", model, provider.name)
				}
			})
		}
	}
}
