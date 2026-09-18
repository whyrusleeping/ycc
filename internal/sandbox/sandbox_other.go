//go:build !linux

package sandbox

import (
	"fmt"
	"os"
)

// detect always reports None on non-Linux platforms: no sandbox mechanism is
// implemented, so reviewer shell execution fails closed.
func detect() Mechanism { return None }

func buildCapable(Mechanism) bool { return false }

func NewStartupPipe(string) (*os.File, string, error) {
	return nil, "", fmt.Errorf("reviewer sandbox startup pipe is unsupported")
}

// helperMain should never be reached off Linux (Command never produces a
// HelperArg re-exec there). Fail closed if it somehow is.
func helperMain(args []string) {
	fmt.Fprintln(os.Stderr, "ycc sandbox: helper invoked on unsupported platform")
	os.Exit(126)
}
