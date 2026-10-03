package session

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/whyrusleeping/ycc/internal/project"
)

// projectRecencyTTL bounds how stale a project's scanned activity time may be.
// Project pickers only need an approximately most-recently-used order, and a
// coarse refresh keeps two concurrently active projects from swapping places
// on every poll. Explicit touches (session start/reopen, user input) apply
// immediately regardless of the TTL.
const projectRecencyTTL = 5 * time.Minute

// projectRecency caches a per-workspace "last used" time for ordering project
// pickers. scanned is derived from the newest session event-log mtime under
// the workspace's .ycc/sessions (so history from before a daemon restart still
// counts); touched records in-memory user/session activity since then.
type projectRecency struct {
	mu      sync.Mutex
	scanned map[string]recencyScan
	touched map[string]time.Time
}

type recencyScan struct {
	at      time.Time // newest session log mtime (zero if none)
	scanned time.Time // when the scan ran
}

// TouchProjectWorkspace marks the project owning workspace (a primary tree or
// one of its workstream worktrees) as used now.
func (m *Manager) TouchProjectWorkspace(workspace string) {
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return
	}
	primary := m.primaryTreeFor(abs)
	now := time.Now()
	m.recency.mu.Lock()
	if m.recency.touched == nil {
		m.recency.touched = map[string]time.Time{}
	}
	if now.After(m.recency.touched[primary]) {
		m.recency.touched[primary] = now
	}
	m.recency.mu.Unlock()
}

// ProjectLastUsed returns the approximate last time the project at path saw
// session activity: the later of an explicit touch and the newest session log
// modification time (rescanned at most every projectRecencyTTL). Zero means
// no known activity.
func (m *Manager) ProjectLastUsed(path string) time.Time {
	key := filepath.Clean(path)
	now := time.Now()
	m.recency.mu.Lock()
	scan, ok := m.recency.scanned[key]
	touched := m.recency.touched[key]
	m.recency.mu.Unlock()

	if !ok || now.Sub(scan.scanned) >= projectRecencyTTL {
		scan = recencyScan{at: newestSessionLog(key), scanned: now}
		m.recency.mu.Lock()
		if m.recency.scanned == nil {
			m.recency.scanned = map[string]recencyScan{}
		}
		m.recency.scanned[key] = scan
		m.recency.mu.Unlock()
	}
	if touched.After(scan.at) {
		return touched
	}
	return scan.at
}

// newestSessionLog returns the newest events.jsonl mtime under workspace's
// session directory, or zero when there are none.
func newestSessionLog(workspace string) time.Time {
	paths, _ := filepath.Glob(filepath.Join(workspace, ".ycc", "sessions", "*", "events.jsonl"))
	var newest time.Time
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest
}

// ProjectsByRecency returns the registered projects most-recently-used first
// (see ProjectLastUsed), breaking ties — including never-used projects — by
// name. This is the order project pickers present.
func (m *Manager) ProjectsByRecency() []project.Project {
	projects := m.projects.List()
	lastUsed := make(map[string]time.Time, len(projects))
	for _, p := range projects {
		lastUsed[p.Name] = m.ProjectLastUsed(p.Path)
	}
	sort.SliceStable(projects, func(a, b int) bool {
		ta, tb := lastUsed[projects[a].Name], lastUsed[projects[b].Name]
		if !ta.Equal(tb) {
			return ta.After(tb)
		}
		return projects[a].Name < projects[b].Name
	})
	return projects
}
