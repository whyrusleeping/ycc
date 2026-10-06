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
