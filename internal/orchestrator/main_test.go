package orchestrator

import (
	"os"
	"testing"

	"github.com/whyrusleeping/ycc/internal/sandbox"
)

// TestMain dispatches the sandbox helper when the reviewer sandbox probe
// re-executes this test binary; without it the probe recursively reruns tests.
func TestMain(m *testing.M) {
	sandbox.MaybeHelper()
	os.Exit(m.Run())
}
