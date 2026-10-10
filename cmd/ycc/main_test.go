package main

import (
	"os"
	"testing"

	"github.com/whyrusleeping/ycc/internal/sandbox"
)

// TestMain dispatches the sandbox helper when this test binary is re-executed
// with sandbox.HelperArg (doctor's sandbox probe re-execs os.Executable, which
// is this test binary). Without it the probe child reruns the whole suite.
func TestMain(m *testing.M) {
	sandbox.MaybeHelper()
	os.Exit(m.Run())
}
