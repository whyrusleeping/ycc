package health

import (
	"context"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
)

// A model with no credential on the host is reported without a provider
// request, as "Not set up": the web client keys its setup wording on that
// prefix, so it is a client contract.
func TestModelWithoutCredentialIsNotSetUp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NO_SUCH_TEST_KEY", "")
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{
			// An unreachable endpoint: a probe would fail as a network error, not "Not set up".
			"m": {Backend: "openai-compatible", BaseURL: "http://127.0.0.1:1/v1", Model: "x", KeyEnv: "NO_SUCH_TEST_KEY"},
		},
		Roles: config.Roles{Coordinator: "m", Implementer: "m", Reviewers: []string{"m"}},
	})
	c := modelTarget(reg, "m").Run(context.Background())
	if c.Status != StatusFail || !strings.HasPrefix(c.Detail, "Not set up") || !strings.Contains(c.Remedy, "ycc token set NO_SUCH_TEST_KEY") {
		t.Fatalf("got %+v", c)
	}
}
