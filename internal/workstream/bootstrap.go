package workstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
)

const (
	defaultSetupTimeout = 300 * time.Second
	maxSetupOutputBytes = 8 * 1024
)

// Bootstrap seeds a newly-created linked worktree from its primary tree, then
// runs its one-time setup commands. Steps are deliberately ordered copy, link,
// setup so setup can rely on every configured local file and shared dependency.
func Bootstrap(primary, dir string, cfg config.Worktree) error {
	if cfg.SetupTimeoutSeconds < 0 {
		return fmt.Errorf("worktree setup_timeout_seconds must be non-negative")
	}
	for _, path := range cfg.Copy {
		if err := validateBootstrapPath("copy", path); err != nil {
			return err
		}
		if err := copyBootstrapPath(primary, dir, path); err != nil {
			return fmt.Errorf("copy %q: %w", path, err)
		}
	}
	for _, path := range cfg.Link {
		if err := validateBootstrapPath("link", path); err != nil {
			return err
		}
		if err := linkBootstrapPath(primary, dir, path); err != nil {
			return fmt.Errorf("link %q: %w", path, err)
		}
	}
	for _, command := range cfg.Setup {
		if err := runSetupCommand(dir, command, cfg); err != nil {
			return err
		}
	}
	return nil
}

func validateBootstrapPath(kind, path string) error {
	if !filepath.IsLocal(path) {
		return fmt.Errorf("worktree %s path %q must be a non-empty workspace-relative path that does not escape the tree", kind, path)
	}
	return nil
}

func copyBootstrapPath(primary, dir, path string) error {
	src := filepath.Join(primary, path)
	dst := filepath.Join(dir, path)
	info, err := os.Lstat(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return nil // never overwrite tracked or otherwise existing worktree content
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	switch {
	case info.Mode().IsRegular():
		return copyRegularFile(src, dst, info.Mode())
	case info.IsDir():
		return copyDirectory(src, dst, info.Mode())
	default:
		return fmt.Errorf("source is not a regular file or directory")
	}
}

func copyRegularFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(dst, mode.Perm())
}

func copyDirectory(src, dst string, mode os.FileMode) error {
	// Create directories writable while populating them, then restore their source
	// modes in reverse order. Applying a read-only source mode before its children
	// are copied would make an otherwise valid tree impossible to seed.
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	type directoryMode struct {
		path string
		mode os.FileMode
	}
	directories := []directoryMode{{path: dst, mode: mode}}
	if err := filepath.Walk(src, func(source string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if source == src {
			return nil
		}
		rel, err := filepath.Rel(src, source)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			directories = append(directories, directoryMode{path: target, mode: info.Mode()})
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil // directory copies intentionally contain regular files only
		}
		return copyRegularFile(source, target, info.Mode())
	}); err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := os.Chmod(directories[i].path, directories[i].mode.Perm()); err != nil {
			return err
		}
	}
	return nil
}

func linkBootstrapPath(primary, dir, path string) error {
	src := filepath.Join(primary, path)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	dst := filepath.Join(dir, path)
	if _, err := os.Lstat(dst); err == nil {
		return nil // never replace tracked or otherwise existing worktree content
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	target, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	return os.Symlink(target, dst)
}

func runSetupCommand(dir, command string, cfg config.Worktree) error {
	timeout := defaultSetupTimeout
	if cfg.SetupTimeoutSeconds > 0 {
		timeout = time.Duration(cfg.SetupTimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), sortedEnv(cfg.Env)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second
	output, err := cmd.CombinedOutput()
	output = tailSetupOutput(output)
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(string(output))
	if text == "" {
		text = "(no output)"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("setup command %q timed out after %s:\n%s", command, timeout, text)
	}
	return fmt.Errorf("setup command %q failed: %w\n%s", command, err, text)
}

func sortedEnv(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func tailSetupOutput(output []byte) []byte {
	if len(output) <= maxSetupOutputBytes {
		return output
	}
	prefix := []byte("…[output truncated; showing tail]\n")
	return append(prefix, output[len(output)-maxSetupOutputBytes:]...)
}
