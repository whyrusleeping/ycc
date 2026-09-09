package git

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func seedTracked(t *testing.T, r *Repo, files map[string]string) {
	t.Helper()
	for name, content := range files {
		writeFile(t, filepath.Join(r.Dir, name), content)
	}
	commitAllForTest(t, r, "seed")
}

func readIndex(t *testing.T, r *Repo) []byte {
	t.Helper()
	path, err := r.indexPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestChangesIsNonMutatingAndIncludesTrackedAndUntracked(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"tracked.txt": "old\n"})
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(r.Dir, "tracked.txt"), "new\n")
	gitAt(t, r.Dir, "add", "tracked.txt")
	writeFile(t, filepath.Join(r.Dir, "new file.txt"), "addition\n")
	statusBefore := gitAt(t, r.Dir, "status", "--porcelain=v1", "-uall")
	indexBefore := readIndex(t, r)

	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"new file.txt", "tracked.txt"}; !reflect.DeepEqual(changes.Paths, want) {
		t.Fatalf("paths = %q, want %q", changes.Paths, want)
	}
	if changes.ID == "" || changes.BaselineID != baseline.ID {
		t.Fatalf("snapshot identity missing: %+v baseline=%s", changes, baseline.ID)
	}
	if !strings.Contains(changes.Diff, "+addition") || !strings.Contains(changes.Diff, "+new") {
		t.Fatalf("diff omits tracked or untracked addition:\n%s", changes.Diff)
	}
	if got := gitAt(t, r.Dir, "status", "--porcelain=v1", "-uall"); got != statusBefore {
		t.Fatalf("inspection changed status:\nbefore %q\nafter  %q", statusBefore, got)
	}
	if got := readIndex(t, r); !bytes.Equal(got, indexBefore) {
		t.Fatal("inspection changed index bytes")
	}
}

func TestOpenExistingUnbornRepositoryRetainsBaselineFailure(t *testing.T) {
	dir := t.TempDir()
	gitAt(t, dir, "init")
	writeFile(t, filepath.Join(dir, "user.txt"), "uncommitted user work\n")

	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open unborn repository: %v", err)
	}
	if r.OpenBaseline() != nil || r.OpenBaselineError() == nil || !strings.Contains(r.OpenBaselineError().Error(), "HEAD") {
		t.Fatalf("unborn baseline state = baseline=%v err=%v", r.OpenBaseline(), r.OpenBaselineError())
	}
	if _, err := r.RevParse("HEAD"); err == nil {
		t.Fatal("Open created an initial commit in an existing unborn repository")
	}
	if got, err := os.ReadFile(filepath.Join(dir, "user.txt")); err != nil || string(got) != "uncommitted user work\n" {
		t.Fatalf("Open changed user work: %q, err=%v", got, err)
	}
}

func TestChangesCleanTree(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if changes.Diff != "" || len(changes.Paths) != 0 {
		t.Fatalf("clean changeset = %+v", changes)
	}
}

func TestScopedCommitPreservesDirtyBaseline(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{
		"task.txt":     "task old\n",
		"staged.txt":   "staged old\n",
		"mixed.txt":    "mixed old\n",
		"unstaged.txt": "unstaged old\n",
	})
	writeFile(t, filepath.Join(r.Dir, "staged.txt"), "staged user\n")
	gitAt(t, r.Dir, "add", "staged.txt")
	writeFile(t, filepath.Join(r.Dir, "mixed.txt"), "mixed staged\n")
	gitAt(t, r.Dir, "add", "mixed.txt")
	writeFile(t, filepath.Join(r.Dir, "mixed.txt"), "mixed worktree\n")
	writeFile(t, filepath.Join(r.Dir, "unstaged.txt"), "unstaged user\n")
	writeFile(t, filepath.Join(r.Dir, "untracked.txt"), "untracked user\n")

	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "task accepted\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"task.txt"}; !reflect.DeepEqual(changes.Paths, want) {
		t.Fatalf("scope = %q, want %q", changes.Paths, want)
	}
	statusBefore := gitAt(t, r.Dir, "status", "--porcelain=v1", "-uall")
	sha, err := r.Commit(changes, "scoped task")
	if err != nil {
		t.Fatal(err)
	}
	if sha == "" {
		t.Fatal("empty commit sha")
	}
	show := gitAt(t, r.Dir, "show", "--format=", "--name-only", "HEAD")
	if show != "task.txt" {
		t.Fatalf("commit paths = %q, want task.txt", show)
	}
	statusAfter := gitAt(t, r.Dir, "status", "--porcelain=v1", "-uall")
	for _, line := range strings.Split(statusBefore, "\n") {
		if strings.Contains(line, "task.txt") {
			continue
		}
		if !strings.Contains(statusAfter, line) {
			t.Fatalf("unrelated status %q not preserved:\n%s", line, statusAfter)
		}
	}
	if got := gitAt(t, r.Dir, "show", ":mixed.txt"); got != "mixed staged" {
		t.Fatalf("mixed staged content = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(r.Dir, "mixed.txt")); string(got) != "mixed worktree\n" {
		t.Fatalf("mixed worktree content = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(r.Dir, "untracked.txt")); string(got) != "untracked user\n" {
		t.Fatalf("untracked content = %q", got)
	}
}

func TestChangesRefusesDirtyBaselineOverlap(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"mixed.txt": "old\n"})
	writeFile(t, filepath.Join(r.Dir, "mixed.txt"), "staged\n")
	gitAt(t, r.Dir, "add", "mixed.txt")
	writeFile(t, filepath.Join(r.Dir, "mixed.txt"), "unstaged\n")
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(r.Dir, "mixed.txt"), "task touched ambiguous path\n")
	_, err = r.Changes(baseline)
	if err == nil || !strings.Contains(err.Error(), "already dirty") || !strings.Contains(err.Error(), "mixed.txt") || !strings.Contains(err.Error(), "clean worktree") {
		t.Fatalf("overlap error is not actionable: %v", err)
	}
}

func TestCommitRefusesStaleSnapshot(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"task.txt": "old\n"})
	baseline, _ := r.CaptureBaseline()
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "reviewed\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "changed after review\n")
	headBefore := gitAt(t, r.Dir, "rev-parse", "HEAD")
	if _, err := r.Commit(changes, "must refuse"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale commit error = %v", err)
	}
	if got := gitAt(t, r.Dir, "rev-parse", "HEAD"); got != headBefore {
		t.Fatal("stale refusal moved HEAD")
	}
}

func TestCommitRefusesContentStagedByHook(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"task.txt": "old\n", "hook-target.txt": "safe\n"})
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "reviewed\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	headBefore := gitAt(t, r.Dir, "rev-parse", "HEAD")
	indexBefore := readIndex(t, r)
	marker := filepath.Join(r.Dir, ".git", "hook-ran")
	hook := filepath.Join(r.Dir, ".git", "hooks", "pre-commit")
	script := "#!/bin/sh\necho evil > hook-target.txt\ngit add hook-target.txt\ntouch " + marker + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(changes, "exact tree"); err == nil || !strings.Contains(err.Error(), "changed the reviewed tree") {
		t.Fatalf("hook-staged content error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("pre-commit hook did not run: %v", err)
	}
	if got := gitAt(t, r.Dir, "rev-parse", "HEAD"); got != headBefore {
		t.Fatal("hook mutation refusal moved HEAD")
	}
	if got := readIndex(t, r); !bytes.Equal(got, indexBefore) {
		t.Fatal("hook mutation changed user's index")
	}
}

func TestCommitHookRejectionLeavesHeadAndIndexUnchanged(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"task.txt": "old\n", "user.txt": "old\n"})
	writeFile(t, filepath.Join(r.Dir, "user.txt"), "user staged\n")
	gitAt(t, r.Dir, "add", "user.txt")
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "task\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(r.Dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho policy says no >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	headBefore := gitAt(t, r.Dir, "rev-parse", "HEAD")
	indexBefore := readIndex(t, r)
	if _, err := r.Commit(changes, "rejected"); err == nil || !strings.Contains(err.Error(), "pre-commit hook rejected commit") || !strings.Contains(err.Error(), "policy says no") {
		t.Fatalf("hook rejection = %v", err)
	}
	if got := gitAt(t, r.Dir, "rev-parse", "HEAD"); got != headBefore {
		t.Fatal("rejecting hook moved HEAD")
	}
	if got := readIndex(t, r); !bytes.Equal(got, indexBefore) {
		t.Fatal("rejecting hook changed user's index")
	}
}

func TestCommitMessageHooksValidatePreparedMessage(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"task.txt": "old\n"})
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "task\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(r.Dir, ".git", "hooks")
	prepare := "#!/bin/sh\nprintf 'validated message\\n' > \"$1\"\n"
	if err := os.WriteFile(filepath.Join(hooks, "prepare-commit-msg"), []byte(prepare), 0o755); err != nil {
		t.Fatal(err)
	}
	validate := "#!/bin/sh\ngrep -qx 'validated message' \"$1\" || { echo invalid message >&2; exit 1; }\n"
	if err := os.WriteFile(filepath.Join(hooks, "commit-msg"), []byte(validate), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(changes, "unprepared message"); err != nil {
		t.Fatal(err)
	}
	if got := gitAt(t, r.Dir, "log", "-1", "--format=%B"); got != "validated message" {
		t.Fatalf("commit message = %q", got)
	}
}

func TestCommitHonorsSigningConfiguration(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"task.txt": "old\n"})
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "task\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(r.Dir, ".git", "signing-attempted")
	signer := filepath.Join(r.Dir, ".git", "reject-signing")
	script := "#!/bin/sh\ntouch " + marker + "\nexit 1\n"
	if err := os.WriteFile(signer, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, r.Dir, "config", "commit.gpgSign", "true")
	gitAt(t, r.Dir, "config", "gpg.program", signer)
	headBefore := gitAt(t, r.Dir, "rev-parse", "HEAD")
	indexBefore := readIndex(t, r)
	if _, err := r.Commit(changes, "must sign"); err == nil || !strings.Contains(err.Error(), "gpg failed to sign") {
		t.Fatalf("signing policy error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("configured signer was not invoked: %v", err)
	}
	if got := gitAt(t, r.Dir, "rev-parse", "HEAD"); got != headBefore {
		t.Fatal("failed signing moved HEAD")
	}
	if got := readIndex(t, r); !bytes.Equal(got, indexBefore) {
		t.Fatal("failed signing changed user's index")
	}
}

func TestScopedPathsAreAlwaysLiteral(t *testing.T) {
	for _, special := range []string{"*.txt", ":(glob)*.txt"} {
		t.Run(special, func(t *testing.T) {
			r, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			seedTracked(t, r, map[string]string{"victim.txt": "clean\n"})
			writeFile(t, filepath.Join(r.Dir, "victim.txt"), "pre-existing dirty\n")
			baseline, err := r.CaptureBaseline()
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(r.Dir, special), "task\n")
			changes, err := r.Changes(baseline)
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{special}; !reflect.DeepEqual(changes.Paths, want) {
				t.Fatalf("scope = %q, want %q", changes.Paths, want)
			}
			if strings.Contains(changes.Diff, "pre-existing dirty") {
				t.Fatalf("special filename widened scoped tree:\n%s", changes.Diff)
			}
			if _, err := r.Commit(changes, "literal path"); err != nil {
				t.Fatal(err)
			}
			if got := gitAt(t, r.Dir, "show", "HEAD:victim.txt"); got != "clean" {
				t.Fatalf("victim committed as %q", got)
			}
			if got, _ := os.ReadFile(filepath.Join(r.Dir, "victim.txt")); string(got) != "pre-existing dirty\n" {
				t.Fatalf("victim worktree changed to %q", got)
			}
		})
	}
}

func TestSubdirectoryWorkspaceInspectsAndCommitsRootRelativeScope(t *testing.T) {
	rootRepo, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, rootRepo, map[string]string{"sub/task.txt": "old\n", "outside.txt": "old\n"})
	writeFile(t, filepath.Join(rootRepo.Dir, "outside.txt"), "pre-existing outside work\n")
	subRepo, err := OpenExisting(filepath.Join(rootRepo.Dir, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := subRepo.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(subRepo.Dir, "task.txt"), "task\n")
	writeFile(t, filepath.Join(subRepo.Dir, "new.txt"), "new\n")
	changes, err := subRepo.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sub/new.txt", "sub/task.txt"}; !reflect.DeepEqual(changes.Paths, want) {
		t.Fatalf("subdirectory scope = %q, want %q", changes.Paths, want)
	}
	if !strings.Contains(changes.Diff, "+task") || !strings.Contains(changes.Diff, "+new") {
		t.Fatalf("subdirectory diff omitted task changes:\n%s", changes.Diff)
	}
	if _, err := subRepo.Commit(changes, "subdirectory task"); err != nil {
		t.Fatal(err)
	}
	if got := gitAt(t, rootRepo.Dir, "show", "--format=", "--name-only", "HEAD"); got != "sub/new.txt\nsub/task.txt" {
		t.Fatalf("commit paths = %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(rootRepo.Dir, "outside.txt")); err != nil || string(got) != "pre-existing outside work\n" {
		t.Fatalf("outside dirty work = %q, err=%v", got, err)
	}
}

func TestPersistedBaselineSurvivesReloadAndGC(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"task.txt": "old\n", "user.txt": "old\n"})
	writeFile(t, filepath.Join(r.Dir, "user.txt"), "pre-existing\n")
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PersistBaseline("s_reload", baseline); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "task\n")
	gitAt(t, r.Dir, "gc", "--prune=now")
	reloaded, err := r.LoadBaseline("s_reload")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ID != baseline.ID {
		t.Fatalf("reloaded baseline = %s, want %s", reloaded.ID, baseline.ID)
	}
	changes, err := r.Changes(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"task.txt"}; !reflect.DeepEqual(changes.Paths, want) {
		t.Fatalf("reloaded scope = %q, want %q", changes.Paths, want)
	}
}

func TestFailedScopedCommitLeavesStateUnchanged(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"task.txt": "old\n", "user.txt": "old\n"})
	writeFile(t, filepath.Join(r.Dir, "user.txt"), "user staged\n")
	gitAt(t, r.Dir, "add", "user.txt")
	baseline, _ := r.CaptureBaseline()
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "task\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	headBefore := gitAt(t, r.Dir, "rev-parse", "HEAD")
	statusBefore := gitAt(t, r.Dir, "status", "--porcelain=v1", "-uall")
	indexBefore := readIndex(t, r)
	// An invalid exec argument makes commit-tree fail before it can create or
	// install a commit, exercising the mutation-free failure path.
	if _, err := r.Commit(changes, "rejected\x00message"); err == nil {
		t.Fatal("commit unexpectedly succeeded")
	}
	if got := gitAt(t, r.Dir, "rev-parse", "HEAD"); got != headBefore {
		t.Fatal("failed commit moved HEAD")
	}
	if got := gitAt(t, r.Dir, "status", "--porcelain=v1", "-uall"); got != statusBefore {
		t.Fatalf("failed commit changed status:\nbefore %q\nafter  %q", statusBefore, got)
	}
	if got := readIndex(t, r); !bytes.Equal(got, indexBefore) {
		t.Fatal("failed commit changed index")
	}
}
