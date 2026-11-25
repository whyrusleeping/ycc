package session

import (
	"reflect"
	"strings"
	"testing"
	"time"

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

// Ending a session drops its ambiguous claims, in memory and in the persisted
// claims.json, and keeps its definite ones and the other holder's ambiguous
// claim.
func TestStoppingASessionReleasesItsAmbiguousClaims(t *testing.T) {
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
	m.ownership.RecordAmbiguous(ws, []string{"gen.go"}, []string{a.ID, "s_other"})
	m.ownership.RecordWrites(ws, []string{"mine.go"}, m.ownership.Child(a.deps.CoordinatorToken, "test"))
	if err := m.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"a.go": {a.ID}, "gen.go": {"s_other"}, "mine.go": {a.ID}} // the stub model writes a.go
	if got := m.ownership.Claims(ws); !reflect.DeepEqual(got, want) {
		t.Fatalf("claims after stop = %v, want %v", got, want)
	}
	persisted := workspacelease.NewService()
	persisted.PersistClaims()
	if got := persisted.Claims(ws); !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted claims after stop = %v, want %v", got, want)
	}
}

// sessionClaimant is s's instance as a shell claimant.
func sessionClaimant(s *Session) workspacelease.ShellClaimant {
	return workspacelease.ShellClaimant{Scope: s.ID, Incarnation: s.deps.CoordinatorToken.Incarnation()}
}

// A session reopened while its stopped instance is still ending keeps its own
// ambiguous claims, and the old instance's are released, on disk too.
func TestReopenedSessionOutlivesItsOldInstancesEnd(t *testing.T) {
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
	old, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "write a.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, old, 1)
	m.ownership.RecordShellAmbiguous(ws, []string{"old.go"}, []workspacelease.ShellClaimant{sessionClaimant(old)})
	// A tracked job holds the old instance's Stop open after it has left the
	// live map, so the session can be reopened before the stop ends.
	job, ok := old.deps.Jobs.TryStartTracked("bash", "job", "coordinator")
	if !ok {
		t.Fatal("job not started")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- m.Stop(old.ID) }()
	select {
	case <-job.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not cancel the job")
	}
	reopened, err := m.Reopen("proj", old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened == old {
		t.Fatal("not reopened")
	}
	m.ownership.RecordShellAmbiguous(ws, []string{"new.go"}, []workspacelease.ShellClaimant{sessionClaimant(reopened)})
	job.ExecutionComplete()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	m.ownership.RecordShellAmbiguous(ws, []string{"later.go"}, []workspacelease.ShellClaimant{sessionClaimant(reopened), {Scope: "s_peer"}})
	persisted := workspacelease.NewService()
	persisted.PersistClaims()
	got := persisted.Claims(ws)
	if len(got["old.go"]) != 0 || !reflect.DeepEqual(got["new.go"], []string{old.ID}) || !reflect.DeepEqual(got["later.go"], []string{old.ID, "s_peer"}) {
		t.Fatalf("persisted claims = %v", got)
	}
}

// Whichever of several concurrent reopens wins, stopping it releases the
// ambiguous claims recorded for it.
func TestConcurrentReopenWinnerReleasesItsClaims(t *testing.T) {
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
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "write a.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, s, 1)
	if err := m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	const n = 8
	start, done := make(chan struct{}), make(chan error, n)
	for range n {
		go func() {
			<-start
			_, err := m.Reopen("proj", s.ID)
			done <- err
		}()
	}
	close(start)
	for range n {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	winner, ok := m.Get(s.ID)
	if !ok {
		t.Fatal("not reopened")
	}
	m.ownership.RecordShellAmbiguous(ws, []string{"dirty.go"}, []workspacelease.ShellClaimant{sessionClaimant(winner)})
	if err := m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	if got := m.ownership.Claims(ws)["dirty.go"]; len(got) != 0 {
		t.Fatalf("dirty.go after the winner stopped = %v", got)
	}
}
