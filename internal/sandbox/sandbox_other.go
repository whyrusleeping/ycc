//go:build !linux

package sandbox

import (
	"fmt"
	"os"
)

// detect always reports None on non-Linux platforms: no sandbox mechanism is
// implemented, so reviewer non-mutation degrades to prompt-only enforcement.
func detect() Mechanism { return None }

// helperMain should never be reached off Linux (Command never produces a
// HelperArg re-exec there). Fail closed if it somehow is.
func helperMain(args []string) {
	fmt.Fprintln(os.Stderr, "ycc sandbox: helper invoked on unsupported platform")
	os.Exit(126)
}
