package workstream

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	safeProjectDirPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	generatedProjectDirPattern = regexp.MustCompile(`-[0-9a-f]{16}$`)
)

// SafeProjectDir maps a project display name to one safe, stable path component.
// Conservative ASCII names are preserved verbatim so existing workstreams keep
// their historical locations. Names ending in "-<16 lowercase hex>" are reserved
// for generated directories and are hashed even when otherwise safe; this keeps
// literal names from aliasing transformed names. Rare existing safe names with
// that suffix therefore relocate. All renamed/pre-upgrade out-of-root workstreams
// are surfaced by ReconcileWorkstreams as needs-attention with instructions to
// merge if wanted and remove the old worktree manually.
func SafeProjectDir(name string) string {
	if safeProjectDirPattern.MatchString(name) && !generatedProjectDirPattern.MatchString(name) {
		return name
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	base := strings.Trim(b.String(), "._-")
	if base == "" {
		base = "project"
	}
	// Leave ample room below common filesystem component limits, even after the
	// collision-resistant suffix is appended.
	if len(base) > 48 {
		base = strings.TrimRight(base[:48], "._-")
		if base == "" {
			base = "project"
		}
	}
	sum := sha256.Sum256([]byte(name))
	return base + "-" + hex.EncodeToString(sum[:8])
}

// ContainedPath joins parts beneath root and returns a clean absolute path. The
// final joined result must be strictly below root: root itself and every lexical
// traversal outside it are rejected.
func ContainedPath(root string, parts ...string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("worktrees root is empty")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve worktrees root: %w", err)
	}
	joined := append([]string{filepath.Clean(absRoot)}, parts...)
	path, err := filepath.Abs(filepath.Join(joined...))
	if err != nil {
		return "", fmt.Errorf("resolve worktree path: %w", err)
	}
	path = filepath.Clean(path)
	if err := VerifyUnderRoot(absRoot, path); err != nil {
		return "", err
	}
	return path, nil
}

// VerifyUnderRoot validates an already-recorded worktree path. Registry paths
// are treated as untrusted and must be absolute and strictly beneath root.
func VerifyUnderRoot(root, path string) error {
	if root == "" {
		return fmt.Errorf("worktrees root is empty")
	}
	if path == "" {
		return fmt.Errorf("worktree path is empty")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("worktree path %q is not absolute", path)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve worktrees root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)
	absPath := filepath.Clean(path)
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return fmt.Errorf("compare worktree path to root: %w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("worktree path %q is not beneath root %q", absPath, absRoot)
	}
	return nil
}
