package session

import (
	"fmt"
	"sort"

	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// sharedHeadPeers is the ownership service's shared-HEAD hook: the live
// sessions other than scope working in canonical worktree, whose HEAD, index
// and stash a shell's history rewrite or stash puts at risk. Sessions of the
// writer's project are named with what they are doing; sessions of another
// project sharing the worktree are only counted, so nothing about them leaks.
// Unlike overlap notices this is not gated on session coordination.
func (m *Manager) sharedHeadPeers(worktree, scope string) []string {
	primary := ""
	if self, ok := m.Get(scope); ok {
		primary = canonicalPath(m.primaryTreeFor(self.Workspace))
	}
	var named []string
	foreign := 0
	for _, s := range m.List() {
		if s.ID == scope || !inWorktree(s.Workspace, worktree) {
			continue
		}
		if primary != "" && canonicalPath(m.primaryTreeFor(s.Workspace)) == primary {
			named = append(named, s.ID+m.briefSession(primary, s.ID).purpose())
		} else {
			foreign++
		}
	}
	sort.Strings(named)
	if foreign > 0 {
		named = append(named, fmt.Sprintf("%d session(s) of another project", foreign))
	}
	return named
}

// inWorktree reports whether a session working in dir works in canonical
// worktree: dir is that worktree's top or a directory inside it that is not a
// nested worktree of its own.
func inWorktree(dir, worktree string) bool {
	resolved := canonicalPath(dir)
	if resolved == worktree {
		return true
	}
	if !withinDir(resolved, worktree) {
		return false
	}
	top, err := workspacelease.Canonical(resolved)
	return err == nil && top == worktree
}
