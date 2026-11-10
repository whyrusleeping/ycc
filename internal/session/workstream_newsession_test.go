package session

import (
	"encoding/base64"
	"errors"
	"os"
	"testing"

	"github.com/whyrusleeping/ycc/internal/engine"
)

// New Session's "Separate workstream" keeps every other choice: the
// workstream's session gets the requested mode, coordinator override, preset,
// and opening pictures, exactly as StartSession would apply them.
func TestSpawnWorkstreamCarriesNewSessionChoices(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	picture := engine.Image{Base64: base64.StdEncoding.EncodeToString([]byte("png")), MediaType: "image/png", Filename: "shot.png"}
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{
		Project:          "demo",
		Prompt:           "what is this?",
		Mode:             "chat",
		CoordinatorModel: "b",
		Preset:           "spec-doctor",
		Images:           []engine.Image{picture},
	})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	defer m.Stop(s.ID)
	if s.Mode != "chat" || s.coordinator != "b" || !s.coordinatorExplicit || s.preset != "spec-doctor" {
		t.Fatalf("session mode=%q coordinator=%q explicit=%v preset=%q", s.Mode, s.coordinator, s.coordinatorExplicit, s.preset)
	}
	if len(s.promptImages) != 1 || s.promptImages[0].AttachmentID == "" {
		t.Fatalf("opening pictures = %+v, want one retained picture", s.promptImages)
	}
	if s.Workspace != ws.WorktreePath {
		t.Fatalf("session workspace %q, want the worktree %q", s.Workspace, ws.WorktreePath)
	}

	// Without a mode the workstream session stays a work session.
	_, plain, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Prompt: "go"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	defer m.Stop(plain.ID)
	if plain.Mode != "work" {
		t.Fatalf("default mode = %q, want work", plain.Mode)
	}
}

// A bad coordinator choice fails before any worktree, branch, or registry
// entry exists.
func TestSpawnWorkstreamRejectsUnknownModelBeforeWorktree(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	_, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Prompt: "hi", CoordinatorModel: "nope"})
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("err = %v, want ErrUnknownModel", err)
	}
	if got := m.Workstreams("demo"); len(got) != 0 {
		t.Fatalf("workstreams after rejected spawn = %+v", got)
	}
	if entries, _ := os.ReadDir(m.worktreesRoot); len(entries) != 0 {
		t.Fatalf("worktrees root not empty after rejected spawn: %v", entries)
	}
}

// The project's session history lists an in-flight workstream's session (its
// log lives in the worktree) and marks it with the workstream; the mark stays
// after discard preserves the log into the project's own tree.
func TestSessionHistoryListsWorkstreamSessions(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	own, err := m.Start(Config{Workspace: proj, Mode: "chat", Prompt: "plain"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(own.ID)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Prompt: "parallel"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}

	find := func(id string) (SessionSummary, bool) {
		t.Helper()
		rows, err := m.ListSessionHistory("demo")
		if err != nil {
			t.Fatalf("ListSessionHistory: %v", err)
		}
		for _, r := range rows {
			if r.ID == id {
				return r, true
			}
		}
		return SessionSummary{}, false
	}
	row, ok := find(s.ID)
	if !ok || row.WorkstreamID != ws.ID || !row.Live || row.Workspace != ws.WorktreePath {
		t.Fatalf("live workstream row = %+v (found %v), want workstream %s", row, ok, ws.ID)
	}
	if row, ok := find(own.ID); !ok || row.WorkstreamID != "" {
		t.Fatalf("project session row = %+v (found %v), want no workstream", row, ok)
	}

	// Stopped, its log is read from the worktree.
	if err := m.Stop(s.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if row, ok := find(s.ID); !ok || row.WorkstreamID != ws.ID || row.Live {
		t.Fatalf("stopped workstream row = %+v (found %v)", row, ok)
	}

	// Discarded, the preserved copy in the project tree keeps the mark.
	if err := m.DiscardWorkstream(ws.ID); err != nil {
		t.Fatalf("DiscardWorkstream: %v", err)
	}
	if row, ok := find(s.ID); !ok || row.WorkstreamID != ws.ID {
		t.Fatalf("preserved workstream row = %+v (found %v)", row, ok)
	}
}
