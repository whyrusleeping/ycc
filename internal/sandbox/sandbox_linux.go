//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// detect probes for a usable sandbox mechanism on Linux, preferring Landlock
// (no external dependency, symlink-proof) over bubblewrap.
func detect() Mechanism {
	if landlockABI() >= 1 {
		return Landlock
	}
	if bwrapAvailable() {
		return Bwrap
	}
	return None
}

// landlockABI returns the Landlock ABI version supported by the kernel, or a
// negative value if Landlock is unavailable/disabled. It probes with
// landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION).
func landlockABI() int {
	r, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return -1
	}
	return int(r)
}

// bwrapAvailable reports whether bubblewrap is installed AND actually runnable
// here (user namespaces may be disabled), via a one-shot probe.
func bwrapAvailable() bool {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return false
	}
	cmd := exec.Command("bwrap", "--die-with-parent", "--dev-bind", "/", "/", "/bin/true")
	return cmd.Run() == nil
}

// helperMain (Landlock) applies the write-confinement policy for the workspace
// root, then execs the wrapped command. args is [root, argv0, argv1, ...] (here
// [root, "sh", "-c", script]). It never returns: on any failure it exits
// non-zero so the command is NEVER run unsandboxed (fail closed).
func helperMain(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "ycc sandbox: malformed helper invocation")
		os.Exit(126)
	}
	root := args[0]
	argv := args[1:]

	// landlock_restrict_self and prctl(NO_NEW_PRIVS) are per-thread, and execve
	// proceeds from the calling thread — lock the goroutine to its OS thread so
	// the policy we install is the one the exec'd program inherits.
	runtime.LockOSThread()

	if err := applyLandlock(root); err != nil {
		fmt.Fprintln(os.Stderr, "ycc sandbox: failed to apply landlock policy:", err)
		os.Exit(126)
	}

	path, err := exec.LookPath(argv[0])
	if err != nil {
		path = argv[0]
	}
	if err := unix.Exec(path, argv, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "ycc sandbox: exec failed:", err)
		os.Exit(126)
	}
}

// landlockRulesetAttr is the ABI-v1 landlock_ruleset_attr: just handled_access_fs.
// We deliberately pass this 8-byte form (with size 8) rather than x/sys's larger
// struct, which gets E2BIG on ABI-v1 kernels; the kernel zero-fills any fields it
// knows about beyond what we supply, so newer kernels accept it too.
type landlockRulesetAttr struct {
	handledAccessFS uint64
}

// applyLandlock installs a Landlock ruleset that:
//   - handles the full filesystem access set the kernel supports;
//   - grants read + execute (READ_FILE|READ_DIR|EXECUTE) beneath "/" — reads work
//     everywhere;
//   - grants full (write) access beneath a small allowlist of scratch/cache dirs
//     that excludes the workspace, so writes anywhere else (incl. the workspace)
//     are denied by default.
//
// Because Landlock evaluates the real inode, symlinks cannot be used to escape
// the confinement in either direction.
func applyLandlock(root string) error {
	abi := landlockABI()
	if abi < 1 {
		return fmt.Errorf("landlock unavailable")
	}
	handled := handledAccessFS(abi)

	attr := landlockRulesetAttr{handledAccessFS: handled}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("create_ruleset: %w", errno)
	}
	rulesetFD := int(fd)
	defer unix.Close(rulesetFD)

	// Read + execute allowed everywhere.
	const readAccess = unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_EXECUTE
	if err := addPathRule(rulesetFD, "/", readAccess); err != nil {
		return fmt.Errorf("root read rule: %w", err)
	}

	// Full access under scratch/cache dirs that are NOT inside/around the
	// workspace. Missing dirs and overlapping dirs are skipped.
	for _, dir := range writeAllowlist() {
		if overlapsRoot(dir, root) {
			continue
		}
		// Skip missing dirs silently; addPathRule opens with O_PATH|O_DIRECTORY.
		if err := addPathRule(rulesetFD, dir, handled); err != nil {
			// Non-fatal: a dir we cannot open (missing) just isn't granted.
			continue
		}
	}

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFD), 0, 0); errno != 0 {
		return fmt.Errorf("restrict_self: %w", errno)
	}
	return nil
}

// handledAccessFS returns the set of filesystem accesses the ruleset handles,
// widening for newer ABIs (REFER at v2, TRUNCATE at v3). Handling an access the
// kernel doesn't know rejects the ruleset, so we gate on the probed ABI.
func handledAccessFS(abi int) uint64 {
	access := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		access |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return access
}

// addPathRule adds a LANDLOCK_RULE_PATH_BENEATH rule granting allowed access
// beneath dir. Opening the dir with O_PATH|O_DIRECTORY fails for missing dirs,
// which the caller treats as "skip".
func addPathRule(rulesetFD int, dir string, allowed uint64) error {
	dfd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	attr := unix.LandlockPathBeneathAttr{
		Allowed_access: allowed,
		Parent_fd:      int32(dfd),
	}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// writeAllowlist is the set of directories writes are re-permitted under: scratch
// space and build/module caches an ordinary reviewer command (go build, git diff
// into a temp) legitimately needs. Entries that don't exist or that overlap the
// workspace are filtered out by the caller.
func writeAllowlist() []string {
	dirs := []string{
		os.TempDir(),
		"/var/tmp",
		"/dev",
		"/run",
	}
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		dirs = append(dirs, cache) // go-build cache lives here
	}
	if mc := goModCache(); mc != "" {
		dirs = append(dirs, mc)
	}
	return dirs
}

// goModCache resolves the Go module cache the way the toolchain does, mirroring
// tools.goModCache: $GOMODCACHE, else the first $GOPATH entry's pkg/mod, else
// $HOME/go/pkg/mod. Returns "" when nothing resolves.
func goModCache() string {
	if v := os.Getenv("GOMODCACHE"); v != "" {
		return filepath.Clean(v)
	}
	if gp := os.Getenv("GOPATH"); gp != "" {
		if parts := filepath.SplitList(gp); len(parts) > 0 && parts[0] != "" {
			return filepath.Clean(filepath.Join(parts[0], "pkg", "mod"))
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Clean(filepath.Join(home, "go", "pkg", "mod"))
	}
	return ""
}

// overlapsRoot reports whether dir equals, is inside, or contains root, with
// symlinks resolved on both sides. Such a dir must NOT be granted write access,
// or writes into the workspace would leak through it.
func overlapsRoot(dir, root string) bool {
	d := resolvePath(dir)
	r := resolvePath(root)
	if d == r {
		return true
	}
	// dir inside root?
	if rel, err := filepath.Rel(r, d); err == nil {
		if rel != ".." && !hasDotDotPrefix(rel) {
			return true
		}
	}
	// root inside dir (dir contains root)?
	if rel, err := filepath.Rel(d, r); err == nil {
		if rel != ".." && !hasDotDotPrefix(rel) {
			return true
		}
	}
	return false
}

// resolvePath resolves symlinks in the longest existing prefix of p and
// re-appends the non-existent trailing suffix, mirroring tools.evalExisting so
// containment checks are robust for paths that don't fully exist yet.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	cur := p
	var suffix string
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if suffix == "" {
				return resolved
			}
			return filepath.Join(resolved, suffix)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		suffix = filepath.Join(filepath.Base(cur), suffix)
		cur = parent
	}
}

func hasDotDotPrefix(rel string) bool {
	return len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && rel[2] == filepath.Separator
}
