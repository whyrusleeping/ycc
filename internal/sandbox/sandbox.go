// Package sandbox hard-enforces that reviewer bash (and any command run through
// it) cannot mutate the workspace, while keeping read-only inspection (git diff,
// cat, grep, ls) working and enabling builds only when the selected mechanism
// supports them safely. On Linux production selection uses bubblewrap or a
// verified unprivileged mount namespace; when neither is available callers must
// fail closed rather than execute reviewer shell commands without confinement.
//
// Mechanism selection (see Available):
//
//   - "landlock": available only for direct capability tests, not production
//     selection. Landlock cannot mediate chmod (and ABI 1/2 cannot mediate
//     truncate), so it cannot uphold the required read-only source contract.
//   - "bwrap": bubblewrap mounts the filesystem read-only, adds only a bounded
//     scratch tmpfs writable, and supervises a private PID/proc view.
//   - "mountns": unprivileged user/mount/PID namespaces provide the same layout
//     and kill all namespace descendants when the reviewed command exits.
//   - "none": no sandbox with the required byte-and-mode protection is available.
//     Reviewer callers fail closed and retain non-shell inspection tools.
//
// Landlock and mountns re-execute the ycc binary as a hidden helper (HelperArg)
// which applies the policy and then execs the real command on the same locked OS
// thread. They fail CLOSED: if the policy cannot be applied the helper exits
// non-zero rather than running the command unsandboxed. cmd/ycc must call
// MaybeHelper at the very top of main so the helper dispatch runs before CLI
// parsing.
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// MountNS uses an unprivileged user/mount namespace and chroot.
	MountNS Mechanism = "mountns"
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

// BuildCapable reports whether the selected mechanism supports compiler artifact
// publication (including cross-directory rename) while preserving confinement.
func BuildCapable() bool { return buildCapable(Available()) }

// ReviewerScratchBytes and ReviewerScratchInodes are hard tmpfs capacity limits
// for one reviewer command. The exact-tree source copy and every build/cache/temp
// output share these limits.
const (
	ReviewerScratchBytes  int64 = 4 << 30
	ReviewerScratchInodes int64 = 1_000_000
)

type quotaLimits struct {
	bytes  int64
	inodes int64
}

var productionQuota = quotaLimits{bytes: ReviewerScratchBytes, inodes: ReviewerScratchInodes}

// Command builds an *exec.Cmd that runs `sh -c script` with the filesystem
// read-only except for a size-and-inode-bounded tmpfs at scratch/quota. root,
// scratch, and workingDir must already exist; workingDir must be within root or
// scratch, and scratch must not overlap root. startupPipe is either "-" or a
// private FIFO beneath scratch that the helper opens as FD 3. If
// scratch/staged-source exists it is copied into the tmpfs as
// scratch/quota/source before the reviewed command starts. commandEnv is
// deliberately applied only after confinement and capability dropping;
// bootstrap helpers receive a sanitized environment.
func Command(ctx context.Context, root, scratch, workingDir, startupPipe, script string, commandEnv []string) (*exec.Cmd, Mechanism) {
	return commandForMechanismEnv(ctx, Available(), root, scratch, workingDir, startupPipe, script, commandEnv, productionQuota)
}

// commandForMechanism is retained for focused package tests that do not need a
// custom command environment or startup receipt pipe.
func commandForMechanism(ctx context.Context, mechanism Mechanism, root, scratch, workingDir, script string) (*exec.Cmd, Mechanism) {
	return commandForMechanismEnv(ctx, mechanism, root, scratch, workingDir, "-", script, os.Environ(), productionQuota)
}

func commandForMechanismEnv(ctx context.Context, mechanism Mechanism, root, scratch, workingDir, startupPipe, script string, commandEnv []string, limits quotaLimits) (*exec.Cmd, Mechanism) {
	if mechanism == None {
		cmd := plainCommand(ctx, script)
		cmd.Dir = workingDir
		cmd.Env = append([]string(nil), commandEnv...)
		return cmd, None
	}
	resolvedRoot, resolvedScratch, resolvedDir, err := validatePaths(root, scratch, workingDir)
	if err != nil {
		return failedCommand(ctx, fmt.Sprintf("ycc sandbox: unsafe paths: %v", err)), mechanism
	}
	root, scratch, workingDir = resolvedRoot, resolvedScratch, resolvedDir
	if limits.bytes < 1 || limits.inodes < 1 {
		return failedCommand(ctx, "ycc sandbox: invalid scratch capacity"), mechanism
	}
	quota := filepath.Join(scratch, "quota")
	if err := os.MkdirAll(quota, 0o700); err != nil {
		return failedCommand(ctx, "ycc sandbox: cannot prepare scratch mountpoint"), mechanism
	}
	if err := os.MkdirAll(filepath.Join(scratch, "empty-cover"), 0o700); err != nil {
		return failedCommand(ctx, "ycc sandbox: cannot prepare read-only mount cover"), mechanism
	}
	if !withinPath(resolvePath(quota), scratch) {
		return failedCommand(ctx, "ycc sandbox: scratch mountpoint escaped private root"), mechanism
	}
	stagedSource := filepath.Join(scratch, "staged-source")
	if info, statErr := os.Stat(stagedSource); statErr != nil || !info.IsDir() {
		stagedSource = "-"
	}
	if startupPipe != "-" && !withinPath(resolvePath(startupPipe), scratch) {
		return failedCommand(ctx, "ycc sandbox: startup pipe escaped private root"), mechanism
	}
	envFile, err := writeCommandEnvironment(scratch, commandEnv)
	if err != nil {
		return failedCommand(ctx, "ycc sandbox: cannot prepare command environment"), mechanism
	}
	exe, err := os.Executable()
	if err != nil {
		return failedCommand(ctx, "ycc sandbox: cannot resolve executable"), mechanism
	}
	limitArgs := []string{strconv.FormatInt(limits.bytes, 10), strconv.FormatInt(limits.inodes, 10)}
	var cmd *exec.Cmd
	switch mechanism {
	case Landlock:
		cmd = exec.CommandContext(ctx, exe, HelperArg, "landlock", root, scratch, workingDir, envFile, "/bin/sh", "-c", script)
	case Bwrap:
		bwrap, resolveErr := trustedExecutable("bwrap", root, scratch)
		if resolveErr != nil {
			return failedCommand(ctx, "ycc sandbox: trusted bubblewrap unavailable"), Bwrap
		}
		// Bubblewrap intentionally closes arbitrary inherited descriptors. The
		// receipt pipe is therefore a named FIFO in private scratch, opened by the
		// confined helper, rather than an unsupported --preserve-fds option.
		args := []string{
			"--die-with-parent", "--unshare-user", "--unshare-pid",
			"--uid", "0", "--gid", "0", "--cap-add", "CAP_SYS_ADMIN", "--cap-add", "CAP_SETPCAP",
			"--ro-bind", "/", "/", "--dev", "/dev", "--remount-ro", "/dev",
			"--proc", "/proc", "--remount-ro", "/proc", "--chdir", "/",
			exe, HelperArg, "bwrap", root, scratch, quota, stagedSource, workingDir, envFile, startupPipe,
		}
		args = append(args, limitArgs...)
		args = append(args, "/bin/sh", "-c", script)
		cmd = exec.CommandContext(ctx, bwrap, args...)
	case MountNS:
		unshare, resolveErr := trustedExecutable("unshare", root, scratch)
		if resolveErr != nil {
			return failedCommand(ctx, "ycc sandbox: trusted unshare unavailable"), MountNS
		}
		rootfs := filepath.Join(scratch, "rootfs")
		args := []string{"-Urmpf", "--kill-child=KILL", exe, HelperArg, "mountns", root, scratch, rootfs, quota, stagedSource, workingDir, envFile, startupPipe}
		args = append(args, limitArgs...)
		args = append(args, "/bin/sh", "-c", script)
		cmd = exec.CommandContext(ctx, unshare, args...)
	default:
		return failedCommand(ctx, "ycc sandbox: unknown confinement mechanism"), mechanism
	}
	cmd.Env = bootstrapEnvironment()
	return cmd, mechanism
}

func writeCommandEnvironment(scratch string, env []string) (string, error) {
	env = normalizeEnvironment(env)
	for _, item := range env {
		if strings.IndexByte(item, 0) >= 0 || !strings.Contains(item, "=") {
			return "", fmt.Errorf("invalid environment entry")
		}
	}
	file, err := os.CreateTemp(scratch, ".command-env-")
	if err != nil {
		return "", err
	}
	name := file.Name()
	defer func() {
		_ = file.Close()
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := file.Write([]byte(strings.Join(env, "\x00") + "\x00")); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return name, nil
}

func normalizeEnvironment(env []string) []string {
	seen := make(map[string]bool, len(env))
	out := make([]string, 0, len(env))
	for i := len(env) - 1; i >= 0; i-- {
		name, _, ok := strings.Cut(env[i], "=")
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, env[i])
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out
}

func bootstrapEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if upper == "PATH" || strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "DYLD_") ||
			upper == "GCONV_PATH" || upper == "GLIBC_TUNABLES" || upper == "LOCPATH" || upper == "NLSPATH" ||
			upper == "BASH_ENV" || upper == "ENV" {
			continue
		}
		env = append(env, item)
	}
	return append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
}

func trustedExecutable(name, root, scratch string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path = resolvePath(path)
	if withinPath(path, root) || withinPath(path, scratch) {
		return "", fmt.Errorf("bootstrap executable is reviewer-controlled")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("bootstrap executable is not executable")
	}
	return path, nil
}

func failedCommand(ctx context.Context, message string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "printf '%s\\n' \"$1\" >&2; exit 126", "ycc-sandbox", message)
	cmd.Env = bootstrapEnvironment()
	return cmd
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func withinPath(path, root string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validatePaths(root, scratch, workingDir string) (string, string, string, error) {
	resolvedRoot := resolvePath(root)
	resolvedScratch := resolvePath(scratch)
	resolvedDir := resolvePath(workingDir)
	for label, path := range map[string]string{"root": resolvedRoot, "scratch": resolvedScratch, "working directory": resolvedDir} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", "", "", fmt.Errorf("%s is not an existing directory", label)
		}
	}
	if overlapsRoot(resolvedScratch, resolvedRoot) {
		return "", "", "", fmt.Errorf("scratch overlaps workspace")
	}
	if !withinPath(resolvedDir, resolvedScratch) && !withinPath(resolvedDir, resolvedRoot) {
		return "", "", "", fmt.Errorf("working directory is outside workspace and scratch")
	}
	return resolvedRoot, resolvedScratch, resolvedDir, nil
}

// overlapsRoot reports whether dir equals, is inside, or contains root, with
// symlinks resolved on both sides.
func overlapsRoot(dir, root string) bool {
	d := resolvePath(dir)
	r := resolvePath(root)
	if d == r {
		return true
	}
	if rel, err := filepath.Rel(r, d); err == nil && rel != ".." && !hasDotDotPrefix(rel) {
		return true
	}
	if rel, err := filepath.Rel(d, r); err == nil && rel != ".." && !hasDotDotPrefix(rel) {
		return true
	}
	return false
}

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

func plainCommand(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", script)
}

// MaybeHelper checks whether this process was re-executed as the sandbox helper
// (os.Args[1] == HelperArg) and, if so, applies the sandbox policy and execs the
// wrapped command. It NEVER returns in that case (it either execs or exits
// non-zero). Otherwise it returns immediately and normal startup proceeds.
//
// cmd/ycc must call this at the very top of main(), before any CLI parsing, and
// test binaries that exercise re-exec sandbox paths must call it from TestMain.
func MaybeHelper() {
	if len(os.Args) >= 2 && os.Args[1] == HelperArg {
		// helperMain applies the policy for os.Args[2:4] (root, scratch) and
		// execs the remaining command arguments. It never returns.
		helperMain(os.Args[2:])
	}
}
