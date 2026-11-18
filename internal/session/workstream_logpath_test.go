package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A finished workstream session (and its integrate-agent session) keeps its log
// in the worktree until merge/discard preserves it; read paths such as
// GetSessionView resolve it there through the owning project.
func TestSessionLogPathFindsWorkstreamWorktreeLogs(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Prompt: "do the thing"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	if err := m.Stop(s.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, live := m.Get(s.ID); live {
		t.Fatal("session still live after Stop")
	}

	wsDir, logPath, err := m.SessionLogPath("demo", s.ID)
	if err != nil {
		t.Fatalf("SessionLogPath(stopped workstream session): %v", err)
	}
	if wsDir != ws.WorktreePath || logPath != filepath.Join(ws.WorktreePath, ".ycc", "sessions", s.ID, "events.jsonl") {
		t.Fatalf("resolved %q %q, want the worktree log", wsDir, logPath)
	}

	// The integrate-agent session is found the same way.
	integrate := "s_integrate0001"
	dir := filepath.Join(ws.WorktreePath, ".ycc", "sessions", integrate)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.workstreams.SetIntegrateSessionID(ws.ID, integrate); err != nil {
		t.Fatal(err)
	}
	if wsDir, _, err := m.SessionLogPath("demo", integrate); err != nil || wsDir != ws.WorktreePath {
		t.Fatalf("integrate session resolved to %q, %v", wsDir, err)
	}

	// Unknown ids still fail as unknown sessions.
	if _, _, err := m.SessionLogPath("demo", "s_nope"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("unknown session: %v", err)
	}

	// Once the workstream is no longer in flight, its worktree is not consulted.
	if err := m.DiscardWorkstream(ws.ID); err != nil {
		t.Fatalf("DiscardWorkstream: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if wsDir, _, err := m.SessionLogPath("demo", integrate); err == nil && wsDir == ws.WorktreePath {
		t.Fatal("a discarded workstream's worktree must not be consulted")
	}
}

// Opening a stopped workstream session continues it in its worktree. An empty
// primary log (left by older daemons, whose Reopen created it) must not shadow
// the worktree log for reads, reopen, project lookup, or preservation.
func TestReopenWorkstreamSessionUsesWorktreeLog(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Prompt: "do the thing"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	if err := m.Stop(s.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	wtLog := filepath.Join(ws.WorktreePath, ".ycc", "sessions", s.ID, "events.jsonl")
	primaryDir := filepath.Join(proj, ".ycc", "sessions", s.ID)
	primaryLog := filepath.Join(primaryDir, "events.jsonl")

	// Without a stray, reopening must not create one in the primary.
	if p, ok := m.persistedSessionProject(s.ID); !ok || p != "demo" {
		t.Fatalf("persistedSessionProject = %q, %v", p, ok)
	}
	r, err := m.Reopen("demo", s.ID)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if r.Workspace != ws.WorktreePath {
		t.Fatalf("reopened in %q, want the worktree %q", r.Workspace, ws.WorktreePath)
	}
	if _, err := os.Stat(primaryDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Reopen touched the primary session dir: %v", err)
	}
	if err := m.Stop(s.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// With a stray empty primary log, every path still resolves the worktree log.
	if err := os.MkdirAll(primaryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(primaryLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, logPath, err := m.SessionLogPath("demo", s.ID); err != nil || logPath != wtLog {
		t.Fatalf("SessionLogPath = %q, %v; want %q", logPath, err, wtLog)
	}
	if evs, err := m.SessionTranscript("demo", s.ID); err != nil || len(evs) == 0 {
		t.Fatalf("SessionTranscript = %d events, %v", len(evs), err)
	}
	r, err = m.Reopen("demo", s.ID)
	if err != nil {
		t.Fatalf("Reopen with stray primary log: %v", err)
	}
	if r.Workspace != ws.WorktreePath {
		t.Fatalf("reopened in %q, want the worktree", r.Workspace)
	}
	if err := m.Stop(s.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Discard preserves the real log over the stray.
	if err := m.DiscardWorkstream(ws.ID); err != nil {
		t.Fatalf("DiscardWorkstream: %v", err)
	}
	if info, err := os.Stat(primaryLog); err != nil || info.Size() == 0 {
		t.Fatalf("preserved primary log: %v, %v", info, err)
	}
}
