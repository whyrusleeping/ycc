// Package sandbox hard-enforces that reviewer bash (and any command run through
// it) cannot mutate the workspace, while keeping read-only inspection (git diff,
// cat, grep, ls, builds) working. It is a best-effort, platform-dependent guard
// that degrades gracefully: on Linux it uses Landlock (preferred, no external
// dependency) or bubblewrap; everywhere else it is a no-op and callers fall back
// to prompt-only enforcement.
//
// Mechanism selection (see Available):
//
//   - "landlock": a Landlock LSM ruleset that denies all filesystem writes by
//     default and re-allows writes only under a small allowlist (temp dirs, the
//     Go build/module caches, /dev, /run) that excludes the workspace. Reads and
//     execs are allowed everywhere. This is symlink-proof: Landlock resolves the
//     real inode, so a symlink inside the workspace pointing outward cannot be
//     used to write into a denied path, and vice versa.
//   - "bwrap": bubblewrap mounts the whole filesystem as-is but re-binds the
//     workspace read-only.
//   - "none": no sandbox available (non-Linux, or no kernel/tool support). The
//     command runs normally; reviewer non-mutation is prompt-enforced only and
//     the orchestrator emits a warning.
//
// The Landlock path re-executes the ycc binary as a hidden helper (HelperArg)
// which applies the policy and then execs the real command on the same locked OS
// thread. It fails CLOSED: if the policy cannot be applied the helper exits
// non-zero rather than running the command unsandboxed. cmd/ycc must call
// MaybeHelper at the very top of main so the helper dispatch runs before CLI
// parsing.
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Mechanism identifies which sandbox is in effect.
type Mechanism string

const (
	// None means no sandbox is available; the command runs unconfined.
	None Mechanism = "none"
	// Landlock uses the Landlock LSM (Linux >= 5.13).
	Landlock Mechanism = "landlock"
	// Bwrap uses the bubblewrap (bwrap) helper.
	Bwrap Mechanism = "bwrap"
)

// HelperArg is the hidden first argument that marks a re-exec of the ycc binary
// as a sandbox helper. MaybeHelper dispatches on it. It is deliberately unusual
// so it cannot collide with a real subcommand.
const HelperArg = "__ycc-sandbox-exec"

var (
	availableOnce sync.Once
	availableMech Mechanism
)

// Available reports which sandbox mechanism is usable on this host, probing once
// and caching the result. It returns None when nothing is available.
func Available() Mechanism {
	availableOnce.Do(func() { availableMech = detect() })
	return availableMech
}

// Command builds an *exec.Cmd that runs `sh -c script` with the workspace root
// confined read-only, using whichever mechanism Available reports. The caller is
// responsible for setting Dir, SysProcAttr, output capture, etc. — Command only
// decides how the command is wrapped. It also returns the mechanism actually
// used so callers can log/report it.
//
// For None the returned command is a plain unconfined `sh -c script`. For the
// Landlock path, if the helper re-exec cannot be set up (os.Executable fails),
// Command fails CLOSED: it returns a command that does NOT run the script but
// instead prints an error and exits non-zero, so the script is never run
// unsandboxed. The mechanism stays Landlock in that case.
func Command(ctx context.Context, root, script string) (*exec.Cmd, Mechanism) {
	switch Available() {
	case Landlock:
		exe, err := os.Executable()
		if err != nil {
			// Fail closed: we intended to sandbox but cannot locate the binary to
			// re-exec as the helper. Never run the script unconfined — emit a
			// visible error and exit non-zero. The error text is our own literal
			// (no script interpolation).
			msg := fmt.Sprintf("ycc sandbox: cannot locate ycc binary for sandbox helper: %v", err)
			return exec.CommandContext(ctx, "sh", "-c",
				fmt.Sprintf("echo %s >&2; exit 126", shellSingleQuote(msg))), Landlock
		}
		// Re-exec self as the sandbox helper: it applies the Landlock policy for
		// root, then execs `sh -c script`.
		return exec.CommandContext(ctx, exe, HelperArg, root, "sh", "-c", script), Landlock
	case Bwrap:
		return exec.CommandContext(ctx, "bwrap",
			"--die-with-parent",
			"--dev-bind", "/", "/",
			"--ro-bind", root, root,
			"--chdir", root,
			"sh", "-c", script,
		), Bwrap
	default:
		return plainCommand(ctx, script), None
	}
}

// shellSingleQuote wraps s in single quotes for safe embedding in a `sh -c`
// string, escaping any embedded single quotes. Used only for our own literal
// error text, never for the caller's script.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func plainCommand(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", script)
}

// MaybeHelper checks whether this process was re-executed as the sandbox helper
// (os.Args[1] == HelperArg) and, if so, applies the sandbox policy and execs the
// wrapped command. It NEVER returns in that case (it either execs or exits
// non-zero). Otherwise it returns immediately and normal startup proceeds.
//
// cmd/ycc must call this at the very top of main(), before any CLI parsing, and
// test binaries that exercise the Landlock path must call it from TestMain.
func MaybeHelper() {
	if len(os.Args) >= 2 && os.Args[1] == HelperArg {
		// helperMain applies the policy for os.Args[2] (root) and execs the
		// remaining args (os.Args[3:] == "sh" "-c" script). It never returns.
		helperMain(os.Args[2:])
	}
}
