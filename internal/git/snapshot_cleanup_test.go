package git

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPruneSnapshotsRetainsLiveSharedAndJournalEvidence(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDir := filepath.Join(r.Dir, ".ycc", "sessions", "live")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	live, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PersistBaseline("live", live); err != nil {
		t.Fatal(err)
	}
	// A second record can share the same pinned baseline. Expiring it must not
	// remove refs still needed by the live session.
	if err := r.PersistBaseline("expired-shared", live); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "live.txt"), "live\n")
	liveChange, err := r.Changes(live)
	if err != nil {
		t.Fatal(err)
	}
	commitAllForTest(t, r, "advance")

	expired, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PersistBaseline("expired", expired); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "reviewed.txt"), "reviewed\n")
	reviewed, err := r.Changes(expired)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "expired.txt"), "expired\n")
	unreferenced, err := r.Changes(expired)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "journal.txt"), "journal\n")
	journaled, err := r.Changes(expired)
	if err != nil {
		t.Fatal(err)
	}

	events := fmt.Sprintf("{\"type\":\"tool_result\",\"data\":{\"changeset_id\":%q}}\n", reviewed.ID)
	if err := os.WriteFile(filepath.Join(liveDir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	journalPath, err := r.FinalizationPath("0001")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(journalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	journal := fmt.Sprintf("{\"version\":1,\"baseline_id\":%q,\"finalization_id\":%q}\n", expired.ID, journaled.ID)
	if err := os.WriteFile(journalPath, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}

	dry, err := r.PruneSnapshots(true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.RemovedRecords == 0 || dry.RemovedRefs == 0 {
		t.Fatalf("dry run found no expired evidence: %+v", dry)
	}
	assertRef(t, r, "refs/ycc/changesets/"+unreferenced.ID, true)

	got, err := r.PruneSnapshots(false)
	if err != nil {
		t.Fatal(err)
	}
	if got.RemovedRecords != dry.RemovedRecords || got.RemovedRefs != dry.RemovedRefs {
		t.Fatalf("apply = %+v, dry run = %+v", got, dry)
	}
	assertRef(t, r, "refs/ycc/baselines/"+live.ID+"/head", true)
	assertRef(t, r, "refs/ycc/baselines/"+expired.ID+"/head", true) // finalization journal retains it
	assertRef(t, r, "refs/ycc/changesets/"+liveChange.ID, true)
	assertRef(t, r, "refs/ycc/changesets/"+reviewed.ID, true)
	assertRef(t, r, "refs/ycc/changesets/"+journaled.ID, true)
	assertRef(t, r, "refs/ycc/changesets/"+unreferenced.ID, false)
	if _, err := r.LoadBaseline("live"); err != nil {
		t.Fatalf("retained session is no longer reopenable: %v", err)
	}

	expiredSharedPath, err := r.sessionBaselinePath("expired-shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expiredSharedPath); !os.IsNotExist(err) {
		t.Fatalf("expired shared record still exists: %v", err)
	}
}

func TestPruneSnapshotsKeepsThenExpiresCommitRecoveryWithSessionEvidence(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(r.Dir, ".ycc", "sessions", "commit-session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PersistBaseline("commit-session", baseline); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "committed.txt"), "committed\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	const message = "retained commit"
	if _, err := r.Commit(changes, message); err != nil {
		t.Fatal(err)
	}
	events := fmt.Sprintf("{\"type\":\"tool_result\",\"data\":{\"changeset_id\":%q}}\n", changes.ID)
	if err := os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	identityPath, err := r.commitIdentityPath(changes.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.PruneSnapshots(false); err != nil {
		t.Fatal(err)
	}
	assertRef(t, r, commitRecoveryRef(changes.Recovery(), message), true)
	if _, err := os.Stat(identityPath); err != nil {
		t.Fatalf("live commit recovery record was removed: %v", err)
	}

	if err := os.RemoveAll(sessionDir); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PruneSnapshots(false); err != nil {
		t.Fatal(err)
	}
	assertRef(t, r, commitRecoveryRef(changes.Recovery(), message), false)
	assertRef(t, r, "refs/ycc/changesets/"+changes.ID, false)
	if _, err := os.Stat(identityPath); !os.IsNotExist(err) {
		t.Fatalf("expired commit recovery record still exists: %v", err)
	}
}

func TestPruneSnapshotsIncludesEvidenceFromLinkedWorktrees(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main")
	if err := os.Mkdir(mainPath, 0o755); err != nil {
		t.Fatal(err)
	}
	mainRepo, err := Open(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	linkedPath := filepath.Join(root, "linked")
	gitAt(t, mainPath, "worktree", "add", "--detach", linkedPath, "HEAD")
	linkedRepo, err := OpenExisting(linkedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(linkedPath, ".ycc", "sessions", "linked-live"), 0o755); err != nil {
		t.Fatal(err)
	}
	baseline, err := linkedRepo.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := linkedRepo.PersistBaseline("linked-live", baseline); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(linkedPath, "linked.txt"), "linked\n")
	changes, err := linkedRepo.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mainRepo.PruneSnapshots(false); err != nil {
		t.Fatal(err)
	}
	assertRef(t, mainRepo, "refs/ycc/baselines/"+baseline.ID+"/head", true)
	assertRef(t, mainRepo, "refs/ycc/changesets/"+changes.ID, true)
	if _, err := linkedRepo.LoadBaseline("linked-live"); err != nil {
		t.Fatalf("linked-worktree session is no longer reopenable: %v", err)
	}
}

func TestPruneSnapshotsFailsClosedOnMalformedSessionEvidence(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PersistBaseline("expired", baseline); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "task\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	badDir := filepath.Join(r.Dir, ".ycc", "sessions", "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "events.jsonl"), []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PruneSnapshots(false); err == nil {
		t.Fatal("cleanup succeeded despite malformed retained session evidence")
	}
	assertRef(t, r, "refs/ycc/changesets/"+changes.ID, true)
	path, err := r.sessionBaselinePath("expired")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cleanup mutated records before failing: %v", err)
	}
}

func TestPruneSnapshotsDiscoversNestedWorkspaceOwnershipFromRootAndNested(t *testing.T) {
	root := t.TempDir()
	rootRepo, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	nestedRepo, err := OpenExisting(nested)
	if err != nil {
		t.Fatal(err)
	}

	liveDir := filepath.Join(nested, ".ycc", "sessions", "nested-live")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	live, err := nestedRepo.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := nestedRepo.PersistBaseline("nested-live", live); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nested, "live.txt"), "live\n")
	liveChanges, err := nestedRepo.Changes(live)
	if err != nil {
		t.Fatal(err)
	}

	// This originating session directory is gone, but another retained session's
	// review evidence still names both its baseline and changeset.
	reviewBaseline, err := nestedRepo.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := nestedRepo.PersistBaseline("review-origin", reviewBaseline); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nested, "reviewed.txt"), "reviewed\n")
	reviewed, err := nestedRepo.Changes(reviewBaseline)
	if err != nil {
		t.Fatal(err)
	}
	keeperDir := filepath.Join(nested, ".ycc", "sessions", "review-keeper")
	if err := os.MkdirAll(keeperDir, 0o755); err != nil {
		t.Fatal(err)
	}
	events := fmt.Sprintf("{\"type\":\"review\",\"data\":{\"baseline_id\":%q,\"changeset_id\":%q}}\n", reviewBaseline.ID, reviewed.ID)
	if err := os.WriteFile(filepath.Join(keeperDir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}

	// Root cleanup must discover the nested owner from the persisted baseline;
	// nested cleanup must use the same complete repository-wide evidence set.
	for _, tc := range []struct {
		name string
		repo *Repo
	}{{"root", rootRepo}, {"nested", nestedRepo}} {
		t.Run(tc.name, func(t *testing.T) {
			repo := tc.repo
			if _, err := repo.PruneSnapshots(false); err != nil {
				t.Fatal(err)
			}
			assertRef(t, repo, "refs/ycc/baselines/"+live.ID+"/head", true)
			assertRef(t, repo, "refs/ycc/changesets/"+liveChanges.ID, true)
			assertRef(t, repo, "refs/ycc/baselines/"+reviewBaseline.ID+"/head", true)
			assertRef(t, repo, "refs/ycc/changesets/"+reviewed.ID, true)
			if _, err := nestedRepo.LoadBaseline("nested-live"); err != nil {
				t.Fatalf("nested live session is no longer reopenable: %v", err)
			}
			if _, err := nestedRepo.LoadBaseline("review-origin"); err != nil {
				t.Fatalf("retained review baseline is no longer loadable: %v", err)
			}
		})
	}
}

func TestPruneSnapshotsRetainsLegacyBaselineWithoutWorkspaceOwnership(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PersistBaseline("legacy", baseline); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "legacy.txt"), "legacy\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	baselinePath, err := r.sessionBaselinePath("legacy")
	if err != nil {
		t.Fatal(err)
	}
	var record baselineRecord
	if err := readJSONFile(baselinePath, &record); err != nil {
		t.Fatal(err)
	}
	record.Version = baselineRecordLegacyVersion
	record.Workspace = ""
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := r.PruneSnapshots(false); err != nil {
		t.Fatal(err)
	}
	assertRef(t, r, "refs/ycc/baselines/"+baseline.ID+"/head", true)
	assertRef(t, r, "refs/ycc/changesets/"+changes.ID, true)
	if _, err := os.Stat(baselinePath); err != nil {
		t.Fatalf("legacy ownership record was removed: %v", err)
	}
}

func TestPruneSnapshotsResumesAfterExpiredChangesetRefWasDeleted(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PersistBaseline("expired", baseline); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "expired.txt"), "expired\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.run("update-ref", "-d", "refs/ycc/changesets/"+changes.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"head", "index", "worktree"} {
		if _, err := r.run("update-ref", "-d", "refs/ycc/baselines/"+baseline.ID+"/"+name); err != nil {
			t.Fatal(err)
		}
	}
	// Prove resumption does not depend on objects which the interrupted cleanup
	// already made unreachable.
	gitAt(t, r.Dir, "reflog", "expire", "--expire=now", "--all")
	gitAt(t, r.Dir, "gc", "--prune=now")

	if _, err := r.PruneSnapshots(false); err != nil {
		t.Fatalf("resume cleanup after ref deletion: %v", err)
	}
	common, err := r.commonGitDir()
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(common, "ycc", "changeset-records", changes.ID+".json")
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("expired changeset record still exists: %v", err)
	}
}

func assertRef(t *testing.T, r *Repo, ref string, want bool) {
	t.Helper()
	_, err := r.run("show-ref", "--verify", "--quiet", ref)
	if (err == nil) != want {
		t.Fatalf("ref %s exists = %v, want %v", ref, err == nil, want)
	}
}
