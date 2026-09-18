//go:build linux

package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRawTruncateHelper(t *testing.T) {
	target := os.Getenv("YCC_RAW_TRUNCATE_TARGET")
	if target == "" {
		t.Skip("helper mode only")
	}
	err := os.Truncate(target, 0)
	if os.Getenv("YCC_RAW_TRUNCATE_DENIED") == "1" {
		if err == nil {
			t.Fatal("raw truncate unexpectedly succeeded")
		}
		return
	}
	if err != nil {
		t.Fatalf("raw truncate failed: %v", err)
	}
}

func TestLandlockConfinementDirect(t *testing.T) {
	if landlockABI() < 1 {
		t.Skip("Landlock is unavailable on this host")
	}
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	scratch := filepath.Join(base, "scratch")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceTarget := filepath.Join(root, "denied")
	existingTarget := filepath.Join(root, "existing")
	if err := os.WriteFile(existingTarget, []byte("preserve"), 0o644); err != nil {
		t.Fatal(err)
	}
	scratchTarget := filepath.Join(scratch, "allowed")
	link := filepath.Join(scratch, "workspace-link")
	if err := os.Symlink(workspaceTarget, link); err != nil {
		t.Fatal(err)
	}
	script := "touch " + workspaceTarget + " 2>/dev/null || true; touch " + link + " 2>/dev/null || true; " +
		"chmod 0777 " + existingTarget + "; "
	if landlockABI() < 3 {
		script += "YCC_RAW_TRUNCATE_TARGET=" + shellSingleQuote(existingTarget) + " " + shellSingleQuote(os.Args[0]) + " -test.run '^TestRawTruncateHelper$'; "
	}
	script += "touch " + scratchTarget
	cmd, _ := commandForMechanism(context.Background(), Landlock, root, scratch, root, script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Landlock command failed: %v (%s)", err, out)
	}
	if _, err := os.Stat(workspaceTarget); !os.IsNotExist(err) {
		t.Fatalf("Landlock wrote workspace: %v", err)
	}
	if _, err := os.Stat(scratchTarget); err != nil {
		t.Fatalf("Landlock did not write private scratch: %v", err)
	}
	info, err := os.Stat(existingTarget)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("Landlock unexpectedly mediated chmod: mode=%o", info.Mode().Perm())
	}
	contents, err := os.ReadFile(existingTarget)
	if err != nil {
		t.Fatal(err)
	}
	if landlockABI() < 3 && len(contents) != 0 {
		t.Fatalf("Landlock ABI %d unexpectedly mediated truncate: %q", landlockABI(), contents)
	}
	if landlockABI() >= 3 && string(contents) != "preserve" {
		t.Fatalf("Landlock ABI %d failed to mediate truncate: %q", landlockABI(), contents)
	}
}

func TestBuildCapableMechanismsPreserveBytesAndModes(t *testing.T) {
	if buildCapable(Landlock) {
		t.Fatal("Landlock must not be advertised as build-capable: chmod is not mediated")
	}
	mechanisms := []Mechanism{}
	if bwrapAvailable() {
		mechanisms = append(mechanisms, Bwrap)
	}
	if mountNSAvailable() {
		mechanisms = append(mechanisms, MountNS)
	}
	if len(mechanisms) == 0 {
		t.Skip("no byte-and-mode confinement mechanism is runnable on this host")
	}
	for _, mechanism := range mechanisms {
		t.Run(string(mechanism), func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "workspace")
			scratch := filepath.Join(base, "scratch")
			for _, dir := range []string{root, filepath.Join(root, ".git"), scratch, filepath.Join(scratch, "rootfs")} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			paths := []string{filepath.Join(root, "source.rs"), filepath.Join(root, "Cargo.lock"), filepath.Join(root, ".git", "index")}
			for _, path := range paths {
				if err := os.WriteFile(path, []byte("preserve"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			allowed := filepath.Join(scratch, "quota", "allowed")
			var script strings.Builder
			for _, path := range paths {
				fmt.Fprintf(&script, "YCC_RAW_TRUNCATE_TARGET=%s YCC_RAW_TRUNCATE_DENIED=1 %s -test.run '^TestRawTruncateHelper$'; chmod 0777 %s 2>/dev/null || true; ", shellSingleQuote(path), shellSingleQuote(os.Args[0]), shellSingleQuote(path))
			}
			fmt.Fprintf(&script, "touch %s && test -f %s", shellSingleQuote(allowed), shellSingleQuote(allowed))
			cmd, _ := commandForMechanism(context.Background(), mechanism, root, scratch, root, script.String())
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("secure sandbox command failed: %v (%s)", err, out)
			}
			for _, path := range paths {
				contents, err := os.ReadFile(path)
				if err != nil || string(contents) != "preserve" {
					t.Fatalf("%s bytes changed: %q, %v", path, contents, err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("stat %s: %v", path, err)
				}
				if info.Mode().Perm() != 0o644 {
					t.Fatalf("%s mode changed: got %o", path, info.Mode().Perm())
				}
			}
		})
	}
}

func TestMountNSFallbackConfinement(t *testing.T) {
	if !mountNSAvailable() {
		t.Skip("mount namespace fallback is not runnable on this host")
	}
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	scratch := filepath.Join(base, "scratch")
	for _, dir := range []string{root, scratch, filepath.Join(scratch, "rootfs")} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	workspaceTarget := filepath.Join(root, "denied")
	scratchTarget := filepath.Join(scratch, "quota", "allowed")
	outsideTarget := filepath.Join(base, "outside")
	link := filepath.Join(scratch, "source-link")
	if err := os.Symlink(filepath.Join(root, "through-link"), link); err != nil {
		t.Fatal(err)
	}
	cmd, _ := commandForMechanism(context.Background(), MountNS, root, scratch, root,
		"touch "+workspaceTarget+" 2>/dev/null || true; "+
			"touch "+outsideTarget+" 2>/dev/null || true; "+
			"touch "+link+" 2>/dev/null || true; "+
			"unshare -Ur -m sh -c 'mount -o remount,rw / 2>/dev/null && touch "+workspaceTarget+"' 2>/dev/null || true; "+
			"touch "+scratchTarget)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mount namespace fallback failed: %v (%s)", err, out)
	}
	if _, err := os.Stat(workspaceTarget); !os.IsNotExist(err) {
		t.Fatalf("mount namespace fallback wrote workspace: %v", err)
	}
	if _, err := os.Stat(outsideTarget); !os.IsNotExist(err) {
		t.Fatalf("mount namespace fallback wrote outside private scratch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "through-link")); !os.IsNotExist(err) {
		t.Fatalf("mount namespace fallback wrote workspace through scratch symlink: %v", err)
	}
}

func TestSandboxPathValidationFailsClosed(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	scratch := filepath.Join(base, "scratch")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, scratch, outside} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(scratch, "ran")
	cmd, _ := commandForMechanism(context.Background(), Landlock, root, scratch, escape, "touch "+marker)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("escaped working directory unexpectedly ran: %s", out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("failed path validation executed command: %v", err)
	}
}

func TestMountNSNestedCapabilityIsTruthful(t *testing.T) {
	if os.Getenv("YCC_NESTED_MOUNTNS_PROBE") == "1" {
		if mountNSAvailable() {
			t.Fatal("nested sandbox advertised mount namespace setup as available")
		}
		return
	}
	if !mountNSAvailable() {
		t.Skip("mount namespace fallback is not runnable on this host")
	}
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	scratch := filepath.Join(base, "scratch")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "YCC_NESTED_TEST_BINARY="+os.Args[0])
	cmd, _ := commandForMechanismEnv(context.Background(), MountNS, root, scratch, root, "-",
		`YCC_NESTED_MOUNTNS_PROBE=1 "$YCC_NESTED_TEST_BINARY" -test.run '^TestMountNSNestedCapabilityIsTruthful$'`, env, productionQuota)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nested capability probe failed: %v (%s)", err, out)
	}
}

func TestMountNSSetupFailureDoesNotRunCommand(t *testing.T) {
	if !mountNSAvailable() {
		t.Skip("mount namespace fallback is not runnable on this host")
	}
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	scratch := filepath.Join(base, "scratch")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	// The helper requires rootfs to be a directory. A regular file forces setup
	// to fail before the reviewed script can run.
	if err := os.WriteFile(filepath.Join(scratch, "rootfs"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(scratch, "ran")
	cmd, _ := commandForMechanism(context.Background(), MountNS, root, scratch, root, "touch "+marker)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("broken setup unexpectedly ran: %s", out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("failed mount setup executed command: %v", err)
	}
}

func TestBwrapProductionStartupPipe(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not installed")
	}
	// Gate only on the host's basic ability to launch upstream bubblewrap, not on
	// bwrapAvailable: this test must catch incompatibilities in our full probe.
	probe := exec.Command("bwrap", "--die-with-parent", "--unshare-user", "--ro-bind", "/", "/", "/bin/true")
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("host cannot launch bubblewrap: %v (%s)", err, out)
	}
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	scratch := filepath.Join(base, "scratch")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	reader, startupPipe, err := NewStartupPipe(scratch)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	cmd, mechanism := commandForMechanismEnv(context.Background(), Bwrap, root, scratch, root,
		startupPipe, "printf x >&3", os.Environ(), productionQuota)
	if mechanism != Bwrap {
		t.Fatalf("mechanism = %s, want bwrap", mechanism)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("production bubblewrap invocation failed: %v (%s)", err, out)
	}
	var signal [1]byte
	if n, err := reader.Read(signal[:]); n != 1 || err != nil || signal[0] != 'x' {
		t.Fatalf("startup receipt = %q, n=%d, err=%v", signal, n, err)
	}
}

// TestBwrapFallbackConfinement exercises the fallback directly when this host
// can run bubblewrap, even when another mechanism would otherwise be selected.
func TestBwrapFallbackConfinement(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bubblewrap fallback is not runnable on this host")
	}
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	scratch := filepath.Join(base, "scratch")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceTarget := filepath.Join(root, "denied")
	scratchTarget := filepath.Join(scratch, "quota", "allowed")
	outsideTarget := filepath.Join(base, "outside")
	cmd, mechanism := commandForMechanism(context.Background(), Bwrap, root, scratch, root,
		"touch "+workspaceTarget+" 2>/dev/null || true; "+
			"touch "+outsideTarget+" 2>/dev/null || true; "+
			"mkdir /dev/output 2>/dev/null || true; "+
			"touch /dev/shm/output 2>/dev/null || true; "+
			"i=0; while [ $i -lt 100 ] && touch /dev/inode-$i 2>/dev/null; do i=$((i+1)); done; [ $i -eq 0 ]; "+
			"touch "+scratchTarget)
	if mechanism != Bwrap {
		t.Fatalf("mechanism = %s, want bwrap", mechanism)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bwrap fallback failed: %v (%s)", err, out)
	}
	if _, err := os.Stat(workspaceTarget); !os.IsNotExist(err) {
		t.Fatalf("bwrap fallback wrote workspace: %v", err)
	}
	if _, err := os.Stat(outsideTarget); !os.IsNotExist(err) {
		t.Fatalf("bwrap fallback wrote outside bounded scratch: %v", err)
	}
}

func TestBuildCapableMechanismsBlockNestedQuotaMounts(t *testing.T) {
	mechanisms := runnableBuildMechanisms()
	if len(mechanisms) == 0 {
		t.Skip("no byte-and-mode confinement mechanism is runnable on this host")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare is unavailable for the nested namespace regression")
	}
	for _, mechanism := range mechanisms {
		t.Run(string(mechanism), func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "workspace")
			scratch := filepath.Join(base, "scratch")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			mountpoint := filepath.Join(scratch, "quota", "nested")
			marker := filepath.Join(mountpoint, "mounted")
			script := "command -v unshare >/dev/null; " +
				"test \"$(awk '/^Seccomp:/{print $2}' /proc/self/status)\" = 2; " +
				"mkdir " + shellSingleQuote(mountpoint) + "; " +
				"before=$(stat -c %d " + shellSingleQuote(mountpoint) + "); " +
				"if unshare -Ur -m sh -c " + shellSingleQuote("mount -t tmpfs tmpfs "+shellSingleQuote(mountpoint)+" && touch "+shellSingleQuote(marker)) + " 2>/dev/null; then exit 72; fi; " +
				"after=$(stat -c %d " + shellSingleQuote(mountpoint) + "); " +
				"test \"$before\" = \"$after\" && test ! -e " + shellSingleQuote(marker) + "; " +
				// The filter must remain narrow enough for ordinary subprocesses.
				"sh -c 'exit 0'"
			cmd, _ := commandForMechanism(context.Background(), mechanism, root, scratch, root, script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("nested namespace/mount was not denied safely: %v (%s)", err, out)
			}
		})
	}
}

func TestBuildCapableMechanismsEnforceByteAndInodeCapacity(t *testing.T) {
	mechanisms := runnableBuildMechanisms()
	if len(mechanisms) == 0 {
		t.Skip("no byte-and-mode confinement mechanism is runnable on this host")
	}
	for _, mechanism := range mechanisms {
		t.Run(string(mechanism), func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				limits quotaLimits
				script func(string) string
			}{
				{
					name: "open-unlinked-bytes", limits: quotaLimits{bytes: 1 << 20, inodes: 256},
					script: func(quota string) string {
						file := shellSingleQuote(filepath.Join(quota, "held-open"))
						return "exec 9>" + file + "; rm " + file + "; if dd if=/dev/zero bs=65536 count=64 >&9 2>/dev/null; then exit 70; fi"
					},
				},
				{
					name: "metadata-inodes", limits: quotaLimits{bytes: 8 << 20, inodes: 32},
					script: func(quota string) string {
						prefix := shellSingleQuote(filepath.Join(quota, "inode-"))
						return "i=0; while touch " + prefix + "$i 2>/dev/null; do i=$((i+1)); [ $i -lt 200 ] || exit 71; done; [ $i -lt 100 ]"
					},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					base := t.TempDir()
					root := filepath.Join(base, "workspace")
					scratch := filepath.Join(base, "scratch")
					if err := os.Mkdir(root, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(scratch, 0o700); err != nil {
						t.Fatal(err)
					}
					quota := filepath.Join(scratch, "quota")
					cmd, _ := commandForMechanismEnv(context.Background(), mechanism, root, scratch, root, "-",
						tc.script(quota), os.Environ(), tc.limits)
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("bounded scratch regression failed: %v (%s)", err, out)
					}
				})
			}
		})
	}
}

func TestBuildCapableMechanismsIgnoreReviewedPathAndLoaderEnvironment(t *testing.T) {
	mechanisms := runnableBuildMechanisms()
	if len(mechanisms) == 0 {
		t.Skip("no byte-and-mode confinement mechanism is runnable on this host")
	}
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("C compiler unavailable for loader-injection regression")
	}
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{root, bin} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(base, "bootstrap-escaped")
	fakeSetpriv := filepath.Join(bin, "setpriv")
	if err := os.WriteFile(fakeSetpriv, []byte("#!/bin/sh\nprintf escaped > \"$YCC_OUTSIDE\"\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(bin, "review-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf tool-ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	loaderSource := filepath.Join(root, "inject.c")
	loader := filepath.Join(root, "inject.so")
	code := `#include <fcntl.h>
#include <stdlib.h>
#include <unistd.h>
__attribute__((constructor)) static void inject(void) {
  const char *p = getenv("YCC_OUTSIDE");
  if (!p) return;
  int fd = open(p, O_WRONLY|O_CREAT|O_TRUNC, 0600);
  if (fd >= 0) { (void)write(fd, "escaped", 7); close(fd); }
}
`
	if err := os.WriteFile(loaderSource, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	compile := exec.Command("cc", "-shared", "-fPIC", "-o", loader, loaderSource)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile loader regression fixture: %v (%s)", err, out)
	}
	env := append(os.Environ(),
		"PATH="+bin+":/usr/bin:/bin",
		"LD_PRELOAD="+loader,
		"YCC_OUTSIDE="+outside,
	)
	for _, mechanism := range mechanisms {
		t.Run(string(mechanism), func(t *testing.T) {
			scratch := filepath.Join(base, "scratch-"+string(mechanism))
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			cmd, _ := commandForMechanismEnv(context.Background(), mechanism, root, scratch, root, "-",
				`test "$(awk '/CapEff/{print $2}' /proc/self/status)" = 0000000000000000 && review-tool`, env, productionQuota)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("reviewed PATH was not usable after isolation: %v (%s)", err, out)
			}
			if !strings.Contains(string(out), "tool-ok") {
				t.Fatalf("review tool did not run from reviewed PATH: %s", out)
			}
			if _, err := os.Stat(outside); !os.IsNotExist(err) {
				t.Fatalf("reviewed setpriv/PATH or loader environment escaped before confinement: %v", err)
			}
		})
	}
}

func runnableBuildMechanisms() []Mechanism {
	var mechanisms []Mechanism
	if bwrapAvailable() {
		mechanisms = append(mechanisms, Bwrap)
	}
	if mountNSAvailable() {
		mechanisms = append(mechanisms, MountNS)
	}
	return mechanisms
}

// TestSameUIDHostProcessHelper deliberately opts out of Yama's sibling ptrace
// restriction and holds a writable descriptor. It makes the procfs boundary test
// independent of the host's current ptrace_scope setting.
func TestSameUIDHostProcessHelper(t *testing.T) {
	target := os.Getenv("YCC_PROC_HOST_TARGET")
	if target == "" {
		t.Skip("helper mode only")
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0); err != nil {
		t.Fatalf("make same-UID proc inspection eligible: %v", err)
	}
	if err := unix.Prctl(unix.PR_SET_PTRACER, unix.PR_SET_PTRACER_ANY, 0, 0, 0); err != nil {
		t.Fatalf("allow same-UID proc inspection: %v", err)
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fmt.Printf("%d %d\n", os.Getpid(), file.Fd())
	for {
		time.Sleep(time.Hour)
	}
}

func TestBuildCapableMechanismsHideHostProcMagicLinks(t *testing.T) {
	mechanisms := runnableBuildMechanisms()
	if len(mechanisms) == 0 {
		t.Skip("no byte-and-mode confinement mechanism is runnable on this host")
	}
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	rootTarget := filepath.Join(base, "proc-root-target")
	fdTarget := filepath.Join(base, "proc-fd-target")
	if err := os.WriteFile(rootTarget, []byte("preserve-root"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fdTarget, []byte("preserve-fd"), 0o600); err != nil {
		t.Fatal(err)
	}

	host := exec.Command(os.Args[0], "-test.run", "^TestSameUIDHostProcessHelper$")
	host.Env = append(os.Environ(), "YCC_PROC_HOST_TARGET="+fdTarget)
	hostOut, err := host.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	host.Stderr = os.Stderr
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = host.Process.Kill()
		_ = host.Wait()
	}()
	line, err := bufio.NewReader(hostOut).ReadString('\n')
	if err != nil {
		t.Fatalf("read host helper identity: %v", err)
	}
	fields := strings.Fields(line)
	if len(fields) != 2 {
		t.Fatalf("malformed host helper identity %q", line)
	}
	hostPID, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatal(err)
	}
	hostFD, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	rootMagic := filepath.Join("/proc", strconv.Itoa(hostPID), "root", rootTarget)
	fdMagic := filepath.Join("/proc", strconv.Itoa(hostPID), "fd", strconv.Itoa(hostFD))
	if _, err := os.Stat(rootMagic); err != nil {
		t.Fatalf("host proc root magic link is not accessible for regression precondition: %v", err)
	}
	probe, err := os.OpenFile(fdMagic, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("host proc fd magic link is not accessible despite PR_SET_PTRACER_ANY: %v", err)
	}
	if _, err := probe.WriteString("-preflight"); err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fdTarget, []byte("preserve-fd"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, mechanism := range mechanisms {
		t.Run(string(mechanism), func(t *testing.T) {
			scratch := filepath.Join(base, "scratch-"+string(mechanism))
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			script := fmt.Sprintf("test -r /proc/1/status || exit 70; test ! -e /proc/%d || exit 71; printf attacked > %s 2>/dev/null || true; printf attacked > %s 2>/dev/null || true",
				hostPID, shellSingleQuote(rootMagic), shellSingleQuote(fdMagic))
			cmd, _ := commandForMechanism(context.Background(), mechanism, root, scratch, root, script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("private proc check failed: %v (%s)", err, out)
			}
			for path, want := range map[string]string{rootTarget: "preserve-root", fdTarget: "preserve-fd"} {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("host target %s changed through proc magic link: %q, %v", path, got, err)
				}
			}
		})
	}
}

func TestBuildCapableMechanismsContainDetachedDescendants(t *testing.T) {
	mechanisms := runnableBuildMechanisms()
	if len(mechanisms) == 0 {
		t.Skip("no byte-and-mode confinement mechanism is runnable on this host")
	}
	for _, mechanism := range mechanisms {
		t.Run(string(mechanism), func(t *testing.T) {
			for _, timeout := range []bool{false, true} {
				name := "leader-exit"
				if timeout {
					name = "timeout"
				}
				t.Run(name, func(t *testing.T) {
					base := t.TempDir()
					root := filepath.Join(base, "workspace")
					scratch := filepath.Join(base, "scratch")
					if err := os.Mkdir(root, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(scratch, 0o700); err != nil {
						t.Fatal(err)
					}
					target := filepath.Join(scratch, "quota", "detached-wrote")
					script := "setsid sh -c " + shellSingleQuote("sleep 0.4; printf escaped > "+shellSingleQuote(target)) + " </dev/null >/dev/null 2>&1 &"
					ctx := context.Background()
					cancel := func() {}
					if timeout {
						var cancelContext context.CancelFunc
						ctx, cancelContext = context.WithTimeout(ctx, 150*time.Millisecond)
						cancel = cancelContext
						script += " sleep 30"
					}
					defer cancel()
					cmd, _ := commandForMechanism(ctx, mechanism, root, scratch, root, script)
					out, runErr := cmd.CombinedOutput()
					if timeout {
						if runErr == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
							t.Fatalf("timed command did not reach deadline: err=%v ctx=%v out=%s", runErr, ctx.Err(), out)
						}
					} else if runErr != nil {
						t.Fatalf("leader-exit command failed: %v (%s)", runErr, out)
					}
					time.Sleep(600 * time.Millisecond)
					if _, err := os.Stat(target); !os.IsNotExist(err) {
						t.Fatalf("detached descendant outlived reviewer command: %v", err)
					}
				})
			}
		})
	}
}
