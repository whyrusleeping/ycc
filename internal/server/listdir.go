package server

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// ListDir lists the subdirectories of a daemon-host path so a remote client
// can browse to a workspace and register it with AddProject. It
// returns DIRECTORIES ONLY — never files or file contents. An empty path
// resolves to the daemon user's home directory. Hidden directories are
// omitted. Note the bearer token already permits StartSession in an arbitrary
// workspace path, so listing directory names does not expand the trust
// surface.
func (s *Server) ListDir(_ context.Context, req *connect.Request[v1.ListDirRequest]) (*connect.Response[v1.ListDirResponse], error) {
	dir := req.Msg.Path
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		dir = home
	}
	if !filepath.IsAbs(dir) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("path must be absolute"))
	}
	dir = filepath.Clean(dir)

	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		if errors.Is(err, fs.ErrPermission) {
			return nil, connect.NewError(connect.CodePermissionDenied, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !info.IsDir() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("not a directory: "+dir))
	}

	registered := s.registeredPaths()

	dirents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, connect.NewError(connect.CodePermissionDenied, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var entries []*v1.DirEntry
	for _, de := range dirents {
		name := de.Name()
		if name == "" || name[0] == '.' {
			continue
		}
		full := filepath.Join(dir, name)
		if !isDir(de, full) {
			continue
		}
		entries = append(entries, &v1.DirEntry{
			Name:         name,
			IsGitRepo:    isGitRepo(full),
			IsRegistered: registered[full],
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	parent := filepath.Dir(dir)
	if parent == dir { // filesystem root
		parent = ""
	}
	resp := &v1.ListDirResponse{Path: dir, Parent: parent, Entries: entries}
	if req.Msg.Suggest {
		resp.Suggestions = s.suggestProjects(registered)
		home, _ := os.UserHomeDir()
		resp.DefaultParent = defaultProjectParent(registered, home)
	}
	return connect.NewResponse(resp), nil
}

// registeredPaths returns the cleaned absolute paths of all registered
// projects for is_registered / suggestion-dedupe checks.
func (s *Server) registeredPaths() map[string]bool {
	m := make(map[string]bool)
	for _, p := range s.mgr.Projects() {
		m[filepath.Clean(p.Path)] = true
	}
	return m
}

// maxSuggestions bounds ListDir suggestions: a short "recent" list you can
// take in at a glance; everything else stays reachable through browsing.
const maxSuggestions = 8

// suggestProjects returns likely-project paths: git repositories that are
// siblings of already-registered projects (projects tend to live together,
// e.g. ~/code/*). Registered paths themselves are excluded. The most recently
// active repositories come first.
func (s *Server) suggestProjects(registered map[string]bool) []string {
	parents := make(map[string]bool)
	for p := range registered {
		parents[filepath.Dir(p)] = true
	}
	type candidate struct {
		path   string
		active time.Time
	}
	var found []candidate
	for parent := range parents {
		dirents, err := os.ReadDir(parent)
		if err != nil {
			continue // unreadable parent: skip, not fatal
		}
		for _, de := range dirents {
			name := de.Name()
			if name == "" || name[0] == '.' {
				continue
			}
			full := filepath.Join(parent, name)
			if registered[full] || !isDir(de, full) {
				continue
			}
			if active, ok := gitActivity(full); ok {
				found = append(found, candidate{full, active})
			}
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].active.Equal(found[j].active) {
			return found[i].active.After(found[j].active)
		}
		return found[i].path < found[j].path
	})
	if len(found) > maxSuggestions {
		found = found[:maxSuggestions]
	}
	out := make([]string, len(found))
	for i, c := range found {
		out[i] = c.path
	}
	return out
}

// gitActivity reports whether dir is a git repository and approximately when
// it was last used: the newest of the files git rewrites on status, commit,
// checkout, and fetch. A worktree/submodule `.git` file only has its own time.
func gitActivity(dir string) (time.Time, bool) {
	gitDir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return time.Time{}, false
	}
	latest := info.ModTime()
	if info.IsDir() {
		for _, f := range []string{"index", "HEAD", "FETCH_HEAD", filepath.Join("logs", "HEAD")} {
			if fi, err := os.Stat(filepath.Join(gitDir, f)); err == nil && fi.ModTime().After(latest) {
				latest = fi.ModTime()
			}
		}
	}
	return latest, true
}

// defaultProjectParent is where a new project most likely belongs: the
// directory holding the most registered projects (ties broken by path), else
// home.
func defaultProjectParent(registered map[string]bool, home string) string {
	counts := make(map[string]int)
	for p := range registered {
		counts[filepath.Dir(p)]++
	}
	best, n := home, 0
	for dir, c := range counts {
		if c > n || (c == n && dir < best) {
			best, n = dir, c
		}
	}
	return best
}

// isDir reports whether a directory entry is a directory, following symlinks
// (a symlinked project dir should still be browsable).
func isDir(de fs.DirEntry, full string) bool {
	if de.IsDir() {
		return true
	}
	if de.Type()&fs.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(full)
	return err == nil && info.IsDir()
}

// isGitRepo reports whether dir contains a .git entry (a directory for normal
// clones, a file for worktrees/submodules).
func isGitRepo(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}
