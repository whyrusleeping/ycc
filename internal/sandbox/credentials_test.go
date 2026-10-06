package sandbox

import (
	"context"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/credenv"
)

func TestSandboxBootstrapScrubsCredentials(t *testing.T) {
	t.Setenv("YCC_TOKEN", "daemon-secret")
	t.Setenv("SANDBOX_TEST_PROVIDER_KEY", "provider-secret")
	credenv.Register("SANDBOX_TEST_PROVIDER_KEY")
	root, scratch := workspace(t)
	cmd, _ := commandForMechanismEnv(context.Background(), Landlock, root, scratch, root, "-", "true", []string{"PATH=/bin"}, productionQuota)
	for _, item := range cmd.Env {
		if strings.HasPrefix(item, "YCC_TOKEN=") || strings.HasPrefix(item, "SANDBOX_TEST_PROVIDER_KEY=") {
			t.Fatalf("sandbox bootstrap inherited credential: %s", item)
		}
	}
}
