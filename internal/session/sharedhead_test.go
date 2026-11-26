package session

import (
	"reflect"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// The shared-HEAD hook names the other live sessions in the writer's worktree,
// with or without session coordination, and never the writer itself.
func TestSharedHeadPeersNameOtherSessionsInTheWorktree(t *testing.T) {
	ws := t.TempDir()
	sessionGitAt(t, ws, "init", "-q")
	sessionGitAt(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
	stub, _ := writeStub(t, ws)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ReclaimAll)
	a, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "write a.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, a, 1)
	key, err := workspacelease.Canonical(ws)
	if err != nil {
		t.Fatal(err)
	}
	if peers := m.ownership.SharedHeadPeers(key, a.ID); len(peers) != 0 {
		t.Fatalf("lone session has peers: %v", peers)
	}
	b, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "write b.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, b, 1)
	peers := m.ownership.SharedHeadPeers(key, a.ID)
	if len(peers) != 1 || !strings.HasPrefix(peers[0], b.ID+" (") {
		t.Fatalf("peers of %s = %v, want %s with its purpose", a.ID, peers, b.ID)
	}
	if peers := m.ownership.SharedHeadPeers(t.TempDir(), a.ID); len(peers) != 0 {
		t.Fatalf("another worktree has peers: %v", peers)
	}
}

// Stopping a session keeps its definite claims and the unattributed paths its
// shells left, in memory and in the persisted claims.json: neither depends on
// which sessions are live.
func TestStoppingASessionKeepsClaimsAndUnattributedPaths(t *testing.T) {
	ws := t.TempDir()
	sessionGitAt(t, ws, "init", "-q")
	sessionGitAt(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
	stub, _ := writeStub(t, ws)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ReclaimAll)
	a, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "write a.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, a, 1)
	m.ownership.RecordUnattributed(ws, []string{"gen.go"})
	m.ownership.RecordWrites(ws, []string{"mine.go"}, m.ownership.Child(a.deps.CoordinatorToken, "test"))
	if err := m.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"a.go": {a.ID}, "mine.go": {a.ID}} // the stub model writes a.go
	wantUnattributed := map[string]bool{"gen.go": true}
	if got := m.ownership.Claims(ws); !reflect.DeepEqual(got, want) {
		t.Fatalf("claims after stop = %v, want %v", got, want)
	}
	if got := m.ownership.Unattributed(ws); !reflect.DeepEqual(got, wantUnattributed) {
		t.Fatalf("unattributed after stop = %v", got)
	}
	persisted := workspacelease.NewService()
	persisted.PersistClaims()
	if got := persisted.Claims(ws); !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted claims after stop = %v, want %v", got, want)
	}
	if got := persisted.Unattributed(ws); !reflect.DeepEqual(got, wantUnattributed) {
		t.Fatalf("persisted unattributed after stop = %v", got)
	}
}
