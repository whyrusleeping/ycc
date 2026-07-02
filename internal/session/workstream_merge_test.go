package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// commitInto opens the git tree at dir, writes name=content, and commits it,
// returning the new short sha. It is the test idiom for making a branch/base
// diverge (mirrors internal/git/worktree_test.go).
func commitInto(t *testing.T, dir, name, content, msg string) string {
	t.Helper()
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatalf("git.Open(%s): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	sha, err := repo.Commit(msg)
	if err != nil {
		t.Fatalf("commit %s: %v", name, err)
	}
	return sha
}

// snapshotHasEvent reports whether the session's in-memory log snapshot contains
// an event of the given type (survives worktree deletion after a merge).
func snapshotHasEvent(s *Session, t event.Type) (event.Event, bool) {
	for _, ev := range s.Log().Snapshot() {
		if ev.Type == t {
			return ev, true
		}
	}
	return event.Event{}, false
}

// TestMergeWorkstreamCleanAutonomous: a clean trial-merge under the autonomous
// level integrates silently, cleans up the worktree + branch, and records a
// workstream_merged event.
func TestMergeWorkstreamCleanAutonomous(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", InteractionLevel: "autonomous"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	defer m.Stop(s.ID)

	commitInto(t, ws.WorktreePath, "feature.txt", "hello\n", "add feature")

	out, err := m.MergeWorkstream(ws.ID, false)
	if err != nil {
		t.Fatalf("MergeWorkstream: %v", err)
	}
	if !out.Merged || out.Commit == "" {
		t.Fatalf("outcome = %+v, want Merged with commit", out)
	}
	// The change landed in the primary tree.
	if _, err := os.Stat(filepath.Join(proj, "feature.txt")); err != nil {
		t.Fatalf("feature.txt not in primary tree: %v", err)
	}
	// Worktree dir removed.
	if _, err := os.Stat(ws.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree dir still present: %v", err)
	}
	// Branch deleted.
	repo, _ := git.Open(proj)
	if _, err := repo.RevParse(ws.Branch); err == nil {
		t.Fatalf("branch %q still exists after merge", ws.Branch)
	}
	// Registry marks it merged.
	if got, _ := m.workstreams.Get(ws.ID); got.Status != workstream.StatusMerged {
		t.Fatalf("status = %v, want merged", got.Status)
	}
	// The merge is auditable on the session stream.
	if _, ok := snapshotHasEvent(s, event.WorkstreamMerged); !ok {
		t.Fatalf("no workstream_merged event in session log")
	}
}

// TestMergeWorkstreamAcceptGate: under a non-autonomous level a clean trial-merge
// is gated behind explicit acceptance; the first call returns the integrated diff
// and mutates nothing, the second (accept=true) integrates.
func TestMergeWorkstreamAcceptGate(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"}) // default judgement
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	defer m.Stop(s.ID)

	commitInto(t, ws.WorktreePath, "feature.txt", "hello\n", "add feature")

	repo, _ := git.Open(proj)
	baseBefore, _ := repo.RevParse("HEAD")

	out, err := m.MergeWorkstream(ws.ID, false)
	if err != nil {
		t.Fatalf("MergeWorkstream(accept=false): %v", err)
	}
	if !out.NeedsAccept || out.Diff == "" {
		t.Fatalf("outcome = %+v, want NeedsAccept with diff", out)
	}
	if out.Merged {
		t.Fatal("gate should not have merged")
	}
	// Nothing mutated.
	if baseAfter, _ := repo.RevParse("HEAD"); baseAfter != baseBefore {
		t.Fatalf("base HEAD changed %s -> %s under gate", baseBefore, baseAfter)
	}
	if got, _ := m.workstreams.Get(ws.ID); got.Status != workstream.StatusActive {
		t.Fatalf("status = %v, want still active", got.Status)
	}
	if _, err := os.Stat(ws.WorktreePath); err != nil {
		t.Fatalf("worktree removed under gate: %v", err)
	}
	if _, ok := snapshotHasEvent(s, event.WorkstreamMerged); ok {
		t.Fatal("workstream_merged emitted under gate")
	}

	// Explicit acceptance integrates.
	out, err = m.MergeWorkstream(ws.ID, true)
	if err != nil {
		t.Fatalf("MergeWorkstream(accept=true): %v", err)
	}
	if !out.Merged {
		t.Fatalf("outcome = %+v, want Merged after accept", out)
	}
	if got, _ := m.workstreams.Get(ws.ID); got.Status != workstream.StatusMerged {
		t.Fatalf("status = %v, want merged", got.Status)
	}
}

// TestMergeWorkstreamConflict: a conflicting trial-merge surfaces a
// workstream_conflict event with the conflicted paths and leaves the base branch,
// worktree, and active status untouched.
func TestMergeWorkstreamConflict(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	// Establish a shared base file so both sides diverge on the same path.
	commitInto(t, proj, "shared.txt", "base\n", "add shared")

	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", InteractionLevel: "autonomous"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	defer m.Stop(s.ID)

	// Divergent edits to the same file on base and on the branch.
	commitInto(t, proj, "shared.txt", "base-edit\n", "edit on base")
	commitInto(t, ws.WorktreePath, "shared.txt", "branch-edit\n", "edit on branch")

	repo, _ := git.Open(proj)
	baseBefore, _ := repo.RevParse("HEAD")

	out, err := m.MergeWorkstream(ws.ID, true)
	if err != nil {
		t.Fatalf("MergeWorkstream: %v", err)
	}
	if out.Merged || len(out.Conflicts) == 0 {
		t.Fatalf("outcome = %+v, want conflicts", out)
	}
	found := false
	for _, p := range out.Conflicts {
		if p == "shared.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("conflicts %v missing shared.txt", out.Conflicts)
	}
	// Base branch untouched.
	if baseAfter, _ := repo.RevParse("HEAD"); baseAfter != baseBefore {
		t.Fatalf("base HEAD changed on conflict: %s -> %s", baseBefore, baseAfter)
	}
	// Worktree, branch, and active status intact.
	if _, err := os.Stat(ws.WorktreePath); err != nil {
		t.Fatalf("worktree removed on conflict: %v", err)
	}
	if _, err := repo.RevParse(ws.Branch); err != nil {
		t.Fatalf("branch removed on conflict: %v", err)
	}
	if got, _ := m.workstreams.Get(ws.ID); got.Status != workstream.StatusActive {
		t.Fatalf("status = %v, want still active", got.Status)
	}
	// The conflict is auditable with the paths.
	ev, ok := snapshotHasEvent(s, event.WorkstreamConflict)
	if !ok {
		t.Fatal("no workstream_conflict event")
	}
	if got := event.Reduce(s.Log().Snapshot()).WorkstreamConflicts; len(got) == 0 || got[0] != "shared.txt" {
		t.Fatalf("projected conflicts = %v (event data %v)", got, ev.Data)
	}
}

// TestMergeWorkstreamSequential: merges are sequential across workstreams, so the
// second reconciles against the first's already-integrated changes.
func TestMergeWorkstreamSequential(t *testing.T) {
	m, _ := newWorkstreamManager(t)

	ws1, s1, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", InteractionLevel: "autonomous"})
	if err != nil {
		t.Fatalf("spawn ws1: %v", err)
	}
	defer m.Stop(s1.ID)
	ws2, s2, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", InteractionLevel: "autonomous"})
	if err != nil {
		t.Fatalf("spawn ws2: %v", err)
	}
	defer m.Stop(s2.ID)

	// Both add the SAME new file with different content (add/add conflict).
	commitInto(t, ws1.WorktreePath, "collide.txt", "from-ws1\n", "ws1 collide")
	commitInto(t, ws2.WorktreePath, "collide.txt", "from-ws2\n", "ws2 collide")

	// ws1 merges cleanly first.
	if out, err := m.MergeWorkstream(ws1.ID, false); err != nil || !out.Merged {
		t.Fatalf("merge ws1: out=%+v err=%v", out, err)
	}
	// ws2 now conflicts because it is reconciled against the post-ws1 base.
	out, err := m.MergeWorkstream(ws2.ID, false)
	if err != nil {
		t.Fatalf("merge ws2: %v", err)
	}
	if out.Merged || len(out.Conflicts) == 0 {
		t.Fatalf("ws2 outcome = %+v, want conflict against post-ws1 base", out)
	}
}

// TestMergeWorkstreamSequentialHappy: after ws1 merges, a second workstream that
// touches a different file merges cleanly and the final tree has both changes.
func TestMergeWorkstreamSequentialHappy(t *testing.T) {
	m, proj := newWorkstreamManager(t)

	ws1, s1, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", InteractionLevel: "autonomous"})
	if err != nil {
		t.Fatalf("spawn ws1: %v", err)
	}
	defer m.Stop(s1.ID)
	ws2, s2, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", InteractionLevel: "autonomous"})
	if err != nil {
		t.Fatalf("spawn ws2: %v", err)
	}
	defer m.Stop(s2.ID)

	commitInto(t, ws1.WorktreePath, "a.txt", "A\n", "ws1 a")
	commitInto(t, ws2.WorktreePath, "b.txt", "B\n", "ws2 b")

	if out, err := m.MergeWorkstream(ws1.ID, false); err != nil || !out.Merged {
		t.Fatalf("merge ws1: out=%+v err=%v", out, err)
	}
	if out, err := m.MergeWorkstream(ws2.ID, false); err != nil || !out.Merged {
		t.Fatalf("merge ws2: out=%+v err=%v", out, err)
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(proj, f)); err != nil {
			t.Fatalf("%s missing from final tree: %v", f, err)
		}
	}
}

// TestDiscardWorkstream: discarding abandons the workstream, cleaning up its
// worktree + branch and recording a workstream_discarded event.
func TestDiscardWorkstream(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	defer m.Stop(s.ID)

	if _, ok := snapshotHasEvent(s, event.WorkstreamCreated); !ok {
		t.Fatal("no workstream_created event at spawn")
	}

	if err := m.DiscardWorkstream(ws.ID); err != nil {
		t.Fatalf("DiscardWorkstream: %v", err)
	}
	if got, _ := m.workstreams.Get(ws.ID); got.Status != workstream.StatusDiscarded {
		t.Fatalf("status = %v, want discarded", got.Status)
	}
	if _, err := os.Stat(ws.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still present after discard: %v", err)
	}
	repo, _ := git.Open(proj)
	if _, err := repo.RevParse(ws.Branch); err == nil {
		t.Fatalf("branch %q still exists after discard", ws.Branch)
	}
	if _, ok := snapshotHasEvent(s, event.WorkstreamDiscarded); !ok {
		t.Fatal("no workstream_discarded event")
	}
}

// transcriptHasEvent reports whether a slice of events contains the given type.
func transcriptHasEvent(events []event.Event, t event.Type) bool {
	for _, ev := range events {
		if ev.Type == t {
			return true
		}
	}
	return false
}

// TestMergeWorkstreamPreservesTranscript: after a merge removes the worktree, the
// session's event log is preserved into the primary workspace so its transcript
// (including the workstream_merged event) is still viewable via SessionTranscript.
func TestMergeWorkstreamPreservesTranscript(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", InteractionLevel: "autonomous"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	sessionID := s.ID

	commitInto(t, ws.WorktreePath, "feature.txt", "hello\n", "add feature")

	if out, err := m.MergeWorkstream(ws.ID, false); err != nil || !out.Merged {
		t.Fatalf("MergeWorkstream: out=%+v err=%v", out, err)
	}

	// The worktree log is gone with the worktree...
	if _, err := os.Stat(ws.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	// ...but a copy landed in the primary workspace.
	preserved := filepath.Join(proj, ".ycc", "sessions", sessionID, "events.jsonl")
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("preserved log missing at %s: %v", preserved, err)
	}
	// And the transcript is viewable (session no longer live), including the merge.
	events, err := m.SessionTranscript("demo", sessionID)
	if err != nil {
		t.Fatalf("SessionTranscript after merge: %v", err)
	}
	if !transcriptHasEvent(events, event.WorkstreamMerged) {
		t.Fatal("preserved transcript missing workstream_merged event")
	}
}

// TestDiscardWorkstreamPreservesTranscript: discarding likewise preserves the
// session log so the transcript (including workstream_discarded) stays viewable.
func TestDiscardWorkstreamPreservesTranscript(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	sessionID := s.ID

	if err := m.DiscardWorkstream(ws.ID); err != nil {
		t.Fatalf("DiscardWorkstream: %v", err)
	}
	if _, err := os.Stat(ws.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still present after discard: %v", err)
	}
	preserved := filepath.Join(proj, ".ycc", "sessions", sessionID, "events.jsonl")
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("preserved log missing at %s: %v", preserved, err)
	}
	events, err := m.SessionTranscript("demo", sessionID)
	if err != nil {
		t.Fatalf("SessionTranscript after discard: %v", err)
	}
	if !transcriptHasEvent(events, event.WorkstreamDiscarded) {
		t.Fatal("preserved transcript missing workstream_discarded event")
	}
}
