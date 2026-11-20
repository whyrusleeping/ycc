package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/whyrusleeping/ycc/internal/credenv"
)

// lineVersion is a scope's line-attributed version of a bookkeeping path.
type lineVersion struct {
	mode, blob string // blob is empty when the version has no file
	partial    bool   // differs from the worktree file: other scopes' lines stay there
}

// lineDecisions records which bookkeeping paths Attribution.Lines decided:
// include holds this scope's versions that differ from HEAD; excluded holds
// paths only other live scopes changed; decided covers both plus paths whose
// version equals HEAD.
type lineDecisions struct {
	include  map[string]lineVersion
	excluded map[string]bool
	decided  map[string]bool
}

func (r *Repo) lineVersions(attr *Attribution, head string, record bool, candidates ...map[string]struct{}) (lineDecisions, error) {
	out := lineDecisions{include: map[string]lineVersion{}, excluded: map[string]bool{}, decided: map[string]bool{}}
	if attr == nil || attr.Lines == nil || attr.Bookkeeping == nil {
		return out, nil
	}
	whole, err := r.canonicalAdoptedPaths(attr.Whole)
	if err != nil {
		return out, err
	}
	skip := make(map[string]bool, len(whole))
	for _, path := range whole {
		skip[path] = true
	}
	seen := map[string]bool{}
	var paths []string
	for _, set := range candidates {
		for path := range set {
			if !seen[path] && !skip[path] && attr.Bookkeeping(path) {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	sort.Strings(paths)
	top := ""
	for _, path := range paths {
		content, exists, mine, ok, err := attr.Lines(head, path, record)
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		out.decided[path] = true
		if !mine {
			out.excluded[path] = true
			continue
		}
		entry, err := r.run("--literal-pathspecs", "ls-tree", "--full-tree", head, "--", path)
		if err != nil {
			return out, err
		}
		mode, headBlob := "100644", ""
		if fields := strings.Fields(entry); len(fields) >= 3 {
			mode, headBlob = fields[0], fields[2]
		}
		version := lineVersion{mode: mode}
		if exists {
			if version.blob, err = r.hashBlob(path, content); err != nil {
				return out, fmt.Errorf("record line-attributed %s: %w", path, err)
			}
		}
		if version.blob == headBlob {
			continue // nothing of this scope's left to commit
		}
		if top == "" {
			if top, err = r.TopLevel(); err != nil {
				return out, err
			}
		}
		current, err := os.ReadFile(filepath.Join(top, filepath.FromSlash(path)))
		version.partial = err != nil || !exists || !bytes.Equal(current, content)
		out.include[path] = version
	}
	return out, nil
}

// hashBlob stores worktree-form content as path's blob, applying its clean
// filters and line-ending conversion as `git add` would.
func (r *Repo) hashBlob(path string, content []byte) (string, error) {
	top, err := r.TopLevel()
	if err != nil {
		return "", err
	}
	cmd := exec.Command("git", "hash-object", "-w", "--stdin", "--path="+path)
	cmd.Dir = top
	cmd.Env = credenv.Scrub(os.Environ())
	cmd.Stdin = bytes.NewReader(content)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git hash-object: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// overlayLineVersions writes the line-attributed versions into tree.
func (r *Repo) overlayLineVersions(tree string, include map[string]lineVersion) (string, error) {
	if len(include) == 0 {
		return tree, nil
	}
	var info bytes.Buffer
	for path, v := range include {
		if v.blob == "" {
			fmt.Fprintf(&info, "0 %s\t%s\x00", strings.Repeat("0", len(tree)), path)
		} else {
			fmt.Fprintf(&info, "%s %s\t%s\x00", v.mode, v.blob, path)
		}
	}
	return r.overlayTree(tree, info.Bytes())
}
