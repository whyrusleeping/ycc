package server

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"connectrpc.com/connect"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// ListDir lists the subdirectories of a daemon-host path so a remote client
// can browse to a workspace and register it with AddProject (task 0193). It
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

// suggestProjects returns likely-project paths: git repositories that are
// siblings of already-registered projects (projects tend to live together,
// e.g. ~/code/*). Registered paths themselves are excluded. Results are
// sorted and capped.
func (s *Server) suggestProjects(registered map[string]bool) []string {
	const maxSuggestions = 50
	parents := make(map[string]bool)
	for p := range registered {
		parents[filepath.Dir(p)] = true
	}
	seen := make(map[string]bool)
	var out []string
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
			if seen[full] || registered[full] {
				continue
			}
			if isDir(de, full) && isGitRepo(full) {
				seen[full] = true
				out = append(out, full)
			}
		}
	}
	sort.Strings(out)
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	return out
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
