//go:build linux

package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// detect probes for a sandbox that protects both file contents and metadata.
// Landlock is intentionally not selected: ABI 1/2 cannot mediate truncate and
// no current ABI mediates chmod, so it cannot make a mutable worktree read-only.
func buildCapable(mechanism Mechanism) bool {
	return mechanism == Bwrap || mechanism == MountNS
}

func detect() Mechanism {
	if bwrapAvailable() {
		return Bwrap
	}
	if mountNSAvailable() {
		return MountNS
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
	return mechanismAvailableProbe(Bwrap, "ycc-bwrap-probe-")
}

func mountNSAvailable() bool {
	if _, err := exec.LookPath("unshare"); err != nil {
		return false
	}
	return mechanismAvailableProbe(MountNS, "ycc-mountns-probe-")
}

// NewStartupPipe creates a private named pipe used only for the one-byte
// execution receipt. A FIFO is required because upstream bubblewrap deliberately
// closes arbitrary inherited descriptors. The read side is nonblocking so setup
// failure is observable after the child exits without hanging the caller.
func NewStartupPipe(scratch string) (*os.File, string, error) {
	path := filepath.Join(scratch, ".startup-pipe")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		return nil, "", err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", err
	}
	return os.NewFile(uintptr(fd), path), path, nil
}

func mechanismAvailableProbe(mechanism Mechanism, prefix string) bool {
	base, err := os.MkdirTemp("", prefix)
	if err != nil {
		return false
	}
	defer os.RemoveAll(base)
	root := filepath.Join(base, "workspace")
	scratch := filepath.Join(base, "scratch")
	if os.Mkdir(root, 0o700) != nil || os.Mkdir(scratch, 0o700) != nil {
		return false
	}
	reader, startupPipe, err := NewStartupPipe(scratch)
	if err != nil {
		return false
	}
	defer reader.Close()
	// Exercise the exact production bootstrap and receipt path, including the
	// bounded tmpfs, private procfs, mount verification, capability drop, and shell.
	cmd, _ := commandForMechanismEnv(context.Background(), mechanism, root, scratch, root,
		startupPipe, "printf x >&3", os.Environ(), productionQuota)
	if cmd.Run() != nil {
		return false
	}
	var signal [1]byte
	n, err := reader.Read(signal[:])
	return n == 1 && err == nil && signal[0] == 'x'
}

// helperMain applies the requested policy then execs the wrapped command. It
// never returns: setup errors exit 126, so a sandbox request cannot degrade to
// an unconfined execution.
func helperMain(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "ycc sandbox: malformed helper invocation")
		os.Exit(126)
	}
	runtime.LockOSThread()
	var err error
	switch args[0] {
	case "landlock":
		err = landlockHelper(args[1:])
	case "bwrap":
		err = bwrapHelper(args[1:])
	case "mountns":
		err = mountNSHelper(args[1:])
	default:
		err = fmt.Errorf("unknown helper mechanism %q", args[0])
	}
	fmt.Fprintln(os.Stderr, "ycc sandbox: setup failed:", err)
	os.Exit(126)
}

func landlockHelper(args []string) error {
	if len(args) < 5 {
		return fmt.Errorf("malformed landlock invocation")
	}
	root, scratch, workingDir, envFile, argv := args[0], args[1], args[2], args[3], args[4:]
	if err := applyLandlock(root, scratch); err != nil {
		return fmt.Errorf("apply landlock policy: %w", err)
	}
	if err := os.Chdir(workingDir); err != nil {
		return fmt.Errorf("chdir: %w", err)
	}
	env, err := readCommandEnvironment(envFile)
	if err != nil {
		return err
	}
	return execArgv(argv, env)
}

// bwrapHelper runs after bubblewrap has established its read-only root and
// private PID/proc view. It creates the bounded writable tmpfs while it still has
// CAP_SYS_ADMIN, copies an optional exact-tree staging directory into that tmpfs,
// then drops every capability before applying the reviewed command environment.
func bwrapHelper(args []string) error {
	root, scratch, quota, stagedSource, workingDir, envFile, startupPipe, limits, argv, err := parseExecHelperArgs(args)
	if err != nil {
		return err
	}
	resolvedRoot, resolvedScratch, resolvedDir, err := validatePaths(root, scratch, workingDir)
	if err != nil {
		return err
	}
	// --ro-bind / / does not make inherited child mounts read-only. Protect or
	// cover every one before adding the sole writable quota mount; otherwise a
	// host tmpfs/overlay/volume could bypass the aggregate scratch limit.
	mounts, err := mountPointsBelow("/")
	if err != nil {
		return err
	}
	empty := filepath.Join(resolvedScratch, "empty-cover")
	var covers []string
	protected := []string{resolvedRoot, resolvedScratch, "/dev", "/proc"}
	for _, mount := range mounts {
		if mount == "/" || beneathAny(mount, covers) {
			continue
		}
		if _, err := os.Lstat(mount); os.IsNotExist(err) {
			// A later bubblewrap overlay (notably --dev) can hide inherited
			// mountpoints that remain listed in mountinfo but are unreachable.
			continue
		}
		var stat unix.Statfs_t
		if err := unix.Statfs(mount, &stat); err == nil && stat.Flags&unix.ST_RDONLY != 0 {
			continue
		}
		if err := remountBindReadOnly(mount); err != nil {
			cover, coverErr := coverLockedMount("/", mount, empty, protected)
			if coverErr != nil {
				return fmt.Errorf("remount or cover child mount %q: %v / %w", mount, err, coverErr)
			}
			covers = append(covers, cover)
		}
	}
	if err := remountBindReadOnly("/"); err != nil {
		return fmt.Errorf("make root read-only: %w", err)
	}
	if err := mountQuota(quota, limits); err != nil {
		return err
	}
	if err := verifyMounts("/", quota, covers); err != nil {
		return err
	}
	if err := prepareStagedSource(stagedSource, filepath.Join(quota, "source")); err != nil {
		return err
	}
	if err := attachStartupPipe(startupPipe); err != nil {
		return err
	}
	return enterReviewedCommand(resolvedDir, envFile, argv)
}

// mountNSHelper runs as PID 1 after unshare created private user, mount, and PID
// namespaces. It recursively clones the host tree only long enough to detach or
// protect every child mount, replaces inherited /proc, remounts the remaining
// view read-only, and mounts only the quota directory writable as a bounded
// tmpfs. Every mount/remount/verification error is fatal.
func mountNSHelper(args []string) error {
	if len(args) < 11 {
		return fmt.Errorf("malformed mount namespace invocation")
	}
	root, scratch, rootfs := args[0], args[1], args[2]
	quota, stagedSource, workingDir, envFile, startupPipe := args[3], args[4], args[5], args[6], args[7]
	limits, argv, err := parseLimitsAndArgv(args[8:])
	if err != nil {
		return err
	}
	resolvedRoot, resolvedScratch, resolvedDir, err := validatePaths(root, scratch, workingDir)
	if err != nil {
		return err
	}
	resolvedQuota := resolvePath(quota)
	if resolvedQuota != filepath.Join(resolvedScratch, "quota") {
		return fmt.Errorf("quota mountpoint escaped scratch")
	}
	if stagedSource != "-" && resolvePath(stagedSource) != filepath.Join(resolvedScratch, "staged-source") {
		return fmt.Errorf("staged source escaped scratch")
	}
	if !withinPath(resolvePath(envFile), resolvedScratch) {
		return fmt.Errorf("command environment escaped scratch")
	}
	if startupPipe != "-" && !withinPath(resolvePath(startupPipe), resolvedScratch) {
		return fmt.Errorf("startup pipe escaped scratch")
	}
	if resolvePath(rootfs) != filepath.Join(resolvedScratch, "rootfs") {
		return fmt.Errorf("rootfs escaped scratch")
	}
	if err := os.MkdirAll(rootfs, 0o700); err != nil {
		return fmt.Errorf("create rootfs: %w", err)
	}
	if err := unix.Mount("/", rootfs, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("clone root: %w", err)
	}
	if err := unix.Mount("", rootfs, "", unix.MS_SLAVE|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("make root private: %w", err)
	}
	mounts, err := mountPointsBelow(rootfs)
	if err != nil {
		return err
	}
	dev := filepath.Join(rootfs, "dev")
	proc := filepath.Join(rootfs, "proc")
	empty := filepath.Join(resolvedScratch, "empty-cover")
	if err := os.Mkdir(empty, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create empty cover: %w", err)
	}
	var covers []string
	protected := []string{filepath.Join(rootfs, resolvedRoot), filepath.Join(rootfs, resolvedScratch), dev, proc}
	for _, mount := range mounts {
		if mount == rootfs || mount == dev || mount == proc || pathInside(proc, mount) || beneathAny(mount, covers) {
			continue
		}
		if err := remountBindReadOnly(mount); err != nil {
			cover, coverErr := coverLockedMount(rootfs, mount, empty, protected)
			if coverErr != nil {
				return fmt.Errorf("remount or cover child mount %q: %v / %w", mount, err, coverErr)
			}
			covers = append(covers, cover)
		}
	}
	for _, mount := range []string{dev, rootfs} {
		if err := remountBindReadOnly(mount); err != nil {
			return fmt.Errorf("make %q read-only: %w", mount, err)
		}
	}
	if err := unix.Mount("proc", proc, "proc", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mount private procfs: %w", err)
	}
	if info, err := os.Stat(filepath.Join(rootfs, resolvedRoot)); err != nil || !info.IsDir() {
		return fmt.Errorf("workspace unavailable in confined root")
	}
	quotaTarget := filepath.Join(rootfs, resolvedQuota)
	if err := mountQuota(quotaTarget, limits); err != nil {
		return err
	}
	if err := verifyMounts(rootfs, quotaTarget, covers); err != nil {
		return err
	}
	if err := unix.Chroot(rootfs); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if err := prepareStagedSource(stagedSource, filepath.Join(resolvedQuota, "source")); err != nil {
		return err
	}
	if err := attachStartupPipe(startupPipe); err != nil {
		return err
	}
	return enterReviewedCommand(resolvedDir, envFile, argv)
}

func parseExecHelperArgs(args []string) (root, scratch, quota, stagedSource, workingDir, envFile, startupPipe string, limits quotaLimits, argv []string, err error) {
	if len(args) < 10 {
		err = fmt.Errorf("malformed sandbox helper invocation")
		return
	}
	root, scratch, quota, stagedSource, workingDir, envFile, startupPipe = args[0], args[1], args[2], args[3], args[4], args[5], args[6]
	limits, argv, err = parseLimitsAndArgv(args[7:])
	if err != nil {
		return
	}
	resolvedScratch := resolvePath(scratch)
	if resolvePath(quota) != filepath.Join(resolvedScratch, "quota") {
		err = fmt.Errorf("quota mountpoint escaped scratch")
	} else if stagedSource != "-" && resolvePath(stagedSource) != filepath.Join(resolvedScratch, "staged-source") {
		err = fmt.Errorf("staged source escaped scratch")
	} else if !withinPath(resolvePath(envFile), resolvedScratch) {
		err = fmt.Errorf("command environment escaped scratch")
	} else if startupPipe != "-" && !withinPath(resolvePath(startupPipe), resolvedScratch) {
		err = fmt.Errorf("startup pipe escaped scratch")
	}
	return
}

func parseLimitsAndArgv(args []string) (quotaLimits, []string, error) {
	if len(args) < 3 {
		return quotaLimits{}, nil, fmt.Errorf("malformed scratch limits")
	}
	bytes, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || bytes < 1 {
		return quotaLimits{}, nil, fmt.Errorf("invalid scratch byte limit")
	}
	inodes, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || inodes < 1 {
		return quotaLimits{}, nil, fmt.Errorf("invalid scratch inode limit")
	}
	return quotaLimits{bytes: bytes, inodes: inodes}, args[2:], nil
}

func mountQuota(path string, limits quotaLimits) error {
	options := fmt.Sprintf("size=%d,nr_inodes=%d,mode=0700", limits.bytes, limits.inodes)
	if err := unix.Mount("tmpfs", path, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, options); err != nil {
		return fmt.Errorf("mount bounded scratch tmpfs: %w", err)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return fmt.Errorf("verify bounded scratch tmpfs: %w", err)
	}
	if uint64(stat.Blocks)*uint64(stat.Bsize) > uint64(limits.bytes) || int64(stat.Files) > limits.inodes {
		return fmt.Errorf("scratch tmpfs exceeded requested capacity")
	}
	for _, name := range []string{"tmp", "cargo-home", "cargo-target", "cache", "go-build", "go-mod", "go"} {
		if err := os.Mkdir(filepath.Join(path, name), 0o700); err != nil {
			return fmt.Errorf("prepare bounded scratch directory: %w", err)
		}
	}
	return nil
}

func prepareStagedSource(source, destination string) error {
	if source == "-" {
		return nil
	}
	return copyTree(source, destination)
}

func copyTree(source, destination string) error {
	if err := os.Mkdir(destination, 0o755); err != nil {
		return fmt.Errorf("create bounded source root: %w", err)
	}
	var dirs []string
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == ".." || hasDotDotPrefix(rel) {
			return fmt.Errorf("staged source path escaped")
		}
		target := filepath.Join(destination, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case entry.IsDir():
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			dirs = append(dirs, target)
			return nil
		case entry.Type().IsRegular():
			return copyRegularFile(path, target, info.Mode().Perm())
		default:
			return fmt.Errorf("unsupported staged source mode %s", entry.Type())
		}
	})
	if err != nil {
		return fmt.Errorf("copy exact source into bounded scratch: %w", err)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o755); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func attachStartupPipe(path string) error {
	if path == "-" {
		return nil
	}
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open startup pipe: %w", err)
	}
	if fd != 3 {
		defer unix.Close(fd)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat startup pipe: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return fmt.Errorf("startup pipe is not a FIFO")
	}
	if fd == 3 {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
			return fmt.Errorf("inherit startup pipe: %w", err)
		}
	} else if err := unix.Dup3(fd, 3, 0); err != nil {
		return fmt.Errorf("inherit startup pipe: %w", err)
	}
	return nil
}

func readCommandEnvironment(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read reviewed command environment: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] != 0 {
		return nil, fmt.Errorf("malformed reviewed command environment")
	}
	parts := strings.Split(string(data[:len(data)-1]), "\x00")
	if len(parts) == 1 && parts[0] == "" {
		return nil, nil
	}
	return parts, nil
}

func dropAllCapabilities() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil && err != unix.EINVAL {
		return fmt.Errorf("clear ambient capabilities: %w", err)
	}
	lastCapabilityData, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil {
		return fmt.Errorf("read kernel capability range: %w", err)
	}
	lastCapability, err := strconv.Atoi(strings.TrimSpace(string(lastCapabilityData)))
	if err != nil || lastCapability < 0 || lastCapability > 63 {
		return fmt.Errorf("invalid kernel capability range")
	}
	for capability := 0; capability <= lastCapability; capability++ {
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0); err != nil {
			return fmt.Errorf("drop capability %d from bounding set: %w", capability, err)
		}
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if err := unix.Capset(&header, &data[0]); err != nil {
		return fmt.Errorf("clear process capabilities: %w", err)
	}
	if err := unix.Capget(&header, &data[0]); err != nil {
		return fmt.Errorf("verify process capabilities: %w", err)
	}
	for _, word := range data {
		if word.Effective != 0 || word.Permitted != 0 || word.Inheritable != 0 {
			return fmt.Errorf("capabilities remained after drop")
		}
	}
	for capability := 0; capability <= lastCapability; capability++ {
		value, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_CAPBSET_READ, uintptr(capability), 0, 0, 0, 0)
		if errno != 0 || value != 0 {
			return fmt.Errorf("capability %d remained in bounding set", capability)
		}
		value, _, errno = unix.Syscall6(unix.SYS_PRCTL, unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_IS_SET, uintptr(capability), 0, 0, 0)
		if errno != 0 || value != 0 {
			return fmt.Errorf("capability %d remained ambient", capability)
		}
	}
	value, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0, 0)
	if errno != 0 || value != 1 {
		return fmt.Errorf("no_new_privs was not retained")
	}
	return nil
}

func enterReviewedCommand(workingDir, envFile string, argv []string) error {
	env, err := readCommandEnvironment(envFile)
	if err != nil {
		return err
	}
	if err := os.Chdir(workingDir); err != nil {
		return fmt.Errorf("chdir: %w", err)
	}
	if err := dropAllCapabilities(); err != nil {
		return err
	}
	if err := blockNestedMountNamespaces(); err != nil {
		return err
	}
	return execArgv(argv, env)
}

// blockNestedMountNamespaces closes the one remaining route around the aggregate
// scratch quota. A process without capabilities in the current user namespace
// can otherwise create a child user and mount namespace, regain CAP_SYS_ADMIN
// there, and mount another unbounded tmpfs below scratch. Ordinary clone/fork
// calls remain available; clone is rejected only when it asks for a user or
// mount namespace. clone3 is reported as unavailable because classic seccomp
// cannot inspect its pointer argument safely, allowing libc to fall back to
// clone while preventing namespace flags from being hidden in that structure.
func blockNestedMountNamespaces() error {
	arch, flagsOffset, err := seccompAuditArch()
	if err != nil {
		return err
	}
	const (
		seccompDataNR   = 0
		seccompDataArch = 4
	)
	stmt := func(code uint16, k uint32) unix.SockFilter { return unix.SockFilter{Code: code, K: k} }
	jump := func(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
		return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
	}
	loadWord := uint16(unix.BPF_LD | unix.BPF_W | unix.BPF_ABS)
	returnCode := uint16(unix.BPF_RET | unix.BPF_K)
	jumpEqual := uint16(unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K)
	jumpSet := uint16(unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K)
	deny := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EPERM)
	unavailable := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.ENOSYS)

	filters := []unix.SockFilter{
		stmt(loadWord, seccompDataArch),
		jump(jumpEqual, arch, 1, 0),
		stmt(returnCode, unix.SECCOMP_RET_KILL_PROCESS),
		stmt(loadWord, seccompDataNR),
	}
	// x32 shares AUDIT_ARCH_X86_64 but uses a disjoint syscall-number bit. Do
	// not let that alternate ABI bypass the native syscall checks below.
	if runtime.GOARCH == "amd64" {
		filters = append(filters,
			jump(jumpSet, 0x40000000, 0, 1),
			stmt(returnCode, unavailable),
		)
	}
	for _, syscall := range []uintptr{
		unix.SYS_MOUNT,
		unix.SYS_UMOUNT2,
		unix.SYS_PIVOT_ROOT,
		unix.SYS_SETNS,
		unix.SYS_OPEN_TREE,
		unix.SYS_MOVE_MOUNT,
		unix.SYS_FSOPEN,
		unix.SYS_FSCONFIG,
		unix.SYS_FSMOUNT,
		unix.SYS_MOUNT_SETATTR,
		unix.SYS_OPEN_TREE_ATTR,
	} {
		filters = append(filters,
			jump(jumpEqual, uint32(syscall), 0, 1),
			stmt(returnCode, deny),
		)
	}
	// Returning ENOSYS for clone3 preserves normal process creation through
	// libc's clone fallback without trusting a userspace pointer in the filter.
	filters = append(filters,
		jump(jumpEqual, uint32(unix.SYS_CLONE3), 0, 1),
		stmt(returnCode, unavailable),
	)
	for _, syscall := range []uintptr{unix.SYS_UNSHARE, unix.SYS_CLONE} {
		filters = append(filters,
			jump(jumpEqual, uint32(syscall), 0, 3),
			stmt(loadWord, flagsOffset),
			jump(jumpSet, uint32(unix.CLONE_NEWUSER|unix.CLONE_NEWNS), 0, 1),
			stmt(returnCode, deny),
		)
	}
	filters = append(filters, stmt(returnCode, unix.SECCOMP_RET_ALLOW))
	if len(filters) > int(^uint16(0)) {
		return fmt.Errorf("seccomp policy is too large")
	}
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
		return fmt.Errorf("block nested mount namespaces: %w", err)
	}
	return nil
}

// seccompAuditArch returns the audit architecture and the offset of the low
// 32-bit clone flags word in seccomp_data. Architectures whose clone ABI has not
// been audited are deliberately unsupported: sandbox setup fails before the
// reviewed command runs rather than installing a filter with a bypass.
func seccompAuditArch() (uint32, uint32, error) {
	const firstArgument = 16
	switch runtime.GOARCH {
	case "386":
		return unix.AUDIT_ARCH_I386, firstArgument, nil
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, firstArgument, nil
	case "arm":
		return unix.AUDIT_ARCH_ARM, firstArgument, nil
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, firstArgument, nil
	case "loong64":
		return unix.AUDIT_ARCH_LOONGARCH64, firstArgument, nil
	case "mipsle":
		return unix.AUDIT_ARCH_MIPSEL, firstArgument, nil
	case "mips64le":
		return unix.AUDIT_ARCH_MIPSEL64, firstArgument, nil
	case "ppc64le":
		return unix.AUDIT_ARCH_PPC64LE, firstArgument, nil
	case "riscv64":
		return unix.AUDIT_ARCH_RISCV64, firstArgument, nil
	default:
		return 0, 0, fmt.Errorf("nested mount namespace policy is unsupported on linux/%s", runtime.GOARCH)
	}
}

func execArgv(argv, env []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("missing command")
	}
	path := argv[0]
	if !strings.ContainsRune(path, filepath.Separator) {
		var searchPath string
		for _, item := range env {
			if strings.HasPrefix(item, "PATH=") {
				searchPath = strings.TrimPrefix(item, "PATH=")
				break
			}
		}
		resolved, err := lookPathIn(path, searchPath)
		if err != nil {
			return err
		}
		path = resolved
	}
	return unix.Exec(path, argv, env)
}

func lookPathIn(name, searchPath string) (string, error) {
	for _, dir := range filepath.SplitList(searchPath) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable %q not found in reviewed PATH", name)
}

func remountBindReadOnly(path string) error {
	flags, err := preservedMountFlags(path)
	if err != nil {
		return err
	}
	return unix.Mount("", path, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY|flags, "")
}

func remountBindWritable(path string) error {
	flags, err := preservedMountFlags(path)
	if err != nil {
		return err
	}
	flags &^= unix.MS_RDONLY
	flags |= unix.MS_NOSUID | unix.MS_NODEV
	return unix.Mount("", path, "", unix.MS_REMOUNT|unix.MS_BIND|flags, "")
}

func preservedMountFlags(path string) (uintptr, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	const preserved = unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC |
		unix.MS_SYNCHRONOUS | unix.MS_MANDLOCK | unix.MS_DIRSYNC | unix.MS_NOATIME |
		unix.MS_NODIRATIME | unix.MS_RELATIME
	return uintptr(stat.Flags) & uintptr(preserved), nil
}

func mountPointsBelow(root string) ([]string, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("read mount table: %w", err)
	}
	defer file.Close()
	var mounts []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			return nil, fmt.Errorf("malformed mountinfo")
		}
		mount, err := unescapeMountInfo(fields[4])
		if err != nil {
			return nil, err
		}
		if mount == root || pathInside(root, mount) {
			mounts = append(mounts, mount)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(mounts, func(i, j int) bool { return len(mounts[i]) > len(mounts[j]) })
	return mounts, nil
}

func unescapeMountInfo(value string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '\\' {
			out.WriteByte(value[i])
			i++
			continue
		}
		if i+3 >= len(value) {
			return "", fmt.Errorf("bad mountinfo escape")
		}
		n, err := strconv.ParseUint(value[i+1:i+4], 8, 8)
		if err != nil {
			return "", fmt.Errorf("bad mountinfo escape: %w", err)
		}
		out.WriteByte(byte(n))
		i += 4
	}
	return out.String(), nil
}

func pathInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !hasDotDotPrefix(rel)
}

func beneathAny(path string, roots []string) bool {
	for _, root := range roots {
		if path == root || pathInside(root, path) {
			return true
		}
	}
	return false
}

// coverLockedMount hides a child mount the inherited user namespace marks
// locked. It walks toward rootfs until it can place a read-only bind of an empty
// directory over an ancestor, but never hides the workspace, scratch, /dev, or
// /proc. A failed cover is fatal; host permissions are never treated as a
// substitute for confinement.
func coverLockedMount(rootfs, mount, empty string, protected []string) (string, error) {
	for candidate := filepath.Dir(mount); candidate != rootfs && pathInside(rootfs, candidate); candidate = filepath.Dir(candidate) {
		conflict := false
		for _, path := range protected {
			if path == candidate || pathInside(candidate, path) {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		if err := unix.Mount(empty, candidate, "", unix.MS_BIND, ""); err != nil {
			continue
		}
		if err := remountBindReadOnly(candidate); err != nil {
			_ = unix.Unmount(candidate, unix.MNT_DETACH)
			return "", err
		}
		return candidate, nil
	}
	return "", fmt.Errorf("no safe read-only cover")
}

func verifyMounts(rootfs, scratch string, covers []string) error {
	mounts, err := mountPointsBelow(rootfs)
	if err != nil {
		return err
	}
	for _, mount := range mounts {
		if beneathAny(mount, covers) && !beneathAny(mount, []string{scratch}) {
			continue
		}
		var stat unix.Statfs_t
		if err := unix.Statfs(mount, &stat); os.IsNotExist(err) {
			// An overlay may hide an inherited mount while mountinfo retains it.
			continue
		} else if err != nil {
			return err
		}
		if mount == scratch {
			if stat.Flags&unix.ST_RDONLY != 0 {
				return fmt.Errorf("scratch remained read-only")
			}
			continue
		}
		if stat.Flags&unix.ST_RDONLY == 0 {
			return fmt.Errorf("writable mount escaped confinement: %s", mount)
		}
	}
	return nil
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
//   - grants full (write) access beneath the per-command scratch directory and
//     device access, so writes anywhere else (including shared caches and the
//     workspace) are denied by default.
//
// Because Landlock evaluates the real inode, symlinks cannot be used to escape
// the confinement in either direction.
func applyLandlock(root, scratch string) error {
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

	// The caller created scratch before entering the sandbox. Refuse overlap
	// rather than silently weakening workspace protection.
	if overlapsRoot(scratch, root) {
		return fmt.Errorf("scratch overlaps workspace")
	}
	if err := addPathRule(rulesetFD, scratch, handled); err != nil {
		return fmt.Errorf("scratch write rule: %w", err)
	}
	// Grant only existing device files needed by ordinary commands, never the
	// /dev directory tree (which may contain writable shared-memory mounts).
	for _, device := range []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/tty", "/dev/ptmx"} {
		if err := addFileRule(rulesetFD, device, unix.LANDLOCK_ACCESS_FS_WRITE_FILE); err != nil && device == "/dev/null" {
			return fmt.Errorf("device write rule: %w", err)
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
	return addOpenedPathRule(rulesetFD, dir, allowed, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC)
}

func addFileRule(rulesetFD int, path string, allowed uint64) error {
	return addOpenedPathRule(rulesetFD, path, allowed, unix.O_PATH|unix.O_CLOEXEC)
}

func addOpenedPathRule(rulesetFD int, path string, allowed uint64, flags int) error {
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	attr := unix.LandlockPathBeneathAttr{
		Allowed_access: allowed,
		Parent_fd:      int32(fd),
	}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
