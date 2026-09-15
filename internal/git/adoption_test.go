package git

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAdoptedFileCommitPreservesOtherBaselineWork(t *testing.T) {
	for _, state := range []string{"untracked", "unstaged", "staged"} {
		t.Run(state, func(t *testing.T) {
			r, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			const task = "project/backlog/0001-task.md"
			seedTracked(t, r, map[string]string{"source.go": "original\n"})
			if state != "untracked" {
				seedTracked(t, r, map[string]string{task: "original task\n"})
			}
			writeFile(t, filepath.Join(r.Dir, task), "selected task\n")
			if state == "staged" {
				gitAt(t, r.Dir, "add", task)
			}
			writeFile(t, filepath.Join(r.Dir, "source.go"), "user source\n")
			gitAt(t, r.Dir, "add", "source.go")
			writeFile(t, filepath.Join(r.Dir, "project/backlog/0002-other.md"), "other task\n")
			// Open inside the workspace subdirectory; scope paths must still name
			// the correct repository-relative file, including during revalidation.
			r, err = Open(filepath.Join(r.Dir, "project"))
			if err != nil {
				t.Fatal(err)
			}
			baseline := r.OpenBaseline()
			if err := r.PersistBaseline("adopt", baseline); err != nil {
				t.Fatal(err)
			}
			if _, err := r.ChangesIncluding(baseline, "backlog/0001-task.md"); err != nil {
				t.Fatalf("preexisting %s document cannot be adopted: %v", state, err)
			}
			writeFile(t, filepath.Join(r.Dir, "backlog/0001-task.md"), "selected task\nimplementation evidence\n")
			writeFile(t, filepath.Join(r.Dir, "new.go"), "task source\n")
			if _, err := r.Changes(baseline); err == nil {
				t.Fatal("strict Changes adopted dirty task")
			}
			changes, err := r.ChangesIncluding(baseline, "backlog/0001-task.md")
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{task, "project/new.go"}; !reflect.DeepEqual(changes.Paths, want) {
				t.Fatalf("scope = %v, want %v", changes.Paths, want)
			}
			if !strings.Contains(changes.Diff, "+selected task") || !strings.Contains(changes.Diff, "+implementation evidence") {
				t.Fatalf("incomplete adopted document: %s", changes.Diff)
			}
			// Reload exactly the persisted baseline, not a recapture of edited work.
			r, err = OpenExisting(r.Dir)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := r.LoadBaseline("adopt")
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := r.ChangesIncluding(restored, filepath.Join(r.Dir, "backlog/0001-task.md"))
			if err != nil {
				t.Fatal(err)
			}
			if resumed.ID != changes.ID || restored.ID != baseline.ID {
				t.Fatal("reopen changed ownership identity")
			}
			if _, err := r.Commit(resumed, "adopt selected task"); err != nil {
				t.Fatal(err)
			}
			if got := gitAt(t, r.Dir, "show", "HEAD:"+task); got != "selected task\nimplementation evidence" {
				t.Fatalf("committed task: %q", got)
			}
			if got := gitAt(t, r.Dir, "show", ":source.go"); got != "user source" {
				t.Fatalf("lost staged source: %q", got)
			}
			if got := gitAt(t, r.Dir, "show", "HEAD:source.go"); got != "original" {
				t.Fatalf("committed unrelated source: %q", got)
			}
			if got := gitAt(t, r.Dir, "ls-tree", "HEAD", "--", "backlog/0002-other.md"); got != "" {
				t.Fatalf("committed unrelated doc: %q", got)
			}
		})
	}
}

func TestAdoptionRefusesOverlapsAndDistinctStagedVariants(t *testing.T) {
	for _, conflict := range []string{"source", "other task", "baseline index", "current index"} {
		t.Run(conflict, func(t *testing.T) {
			r, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			seedTracked(t, r, map[string]string{"task.md": "head\n", "source.go": "head\n"})
			writeFile(t, filepath.Join(r.Dir, "task.md"), "selected\n")
			writeFile(t, filepath.Join(r.Dir, "source.go"), "user source\n")
			writeFile(t, filepath.Join(r.Dir, "other.md"), "other task\n")
			if conflict == "baseline index" {
				gitAt(t, r.Dir, "add", "task.md")
				writeFile(t, filepath.Join(r.Dir, "task.md"), "different worktree\n")
			}
			baseline, err := r.CaptureBaseline()
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(r.Dir, "task.md"), "task update\n")
			var reviewed *Changeset
			if conflict != "baseline index" {
				reviewed, err = r.ChangesIncluding(baseline, "task.md")
				if err != nil {
					t.Fatal(err)
				}
			}
			switch conflict {
			case "source":
				writeFile(t, filepath.Join(r.Dir, "source.go"), "overlap\n")
			case "other task":
				writeFile(t, filepath.Join(r.Dir, "other.md"), "overlap\n")
			case "current index":
				writeFile(t, filepath.Join(r.Dir, "task.md"), "staged variant\n")
				gitAt(t, r.Dir, "add", "task.md")
				writeFile(t, filepath.Join(r.Dir, "task.md"), "task update\n")
			}
			index := readIndex(t, r)
			head := gitAt(t, r.Dir, "rev-parse", "HEAD")
			if _, err := r.ChangesIncluding(baseline, "task.md"); err == nil {
				t.Fatal("unsafe adoption accepted")
			}
			if reviewed != nil {
				if _, err := r.Commit(reviewed, "unsafe"); err == nil {
					t.Fatal("commit skipped scope revalidation")
				}
			}
			if !bytes.Equal(index, readIndex(t, r)) || head != gitAt(t, r.Dir, "rev-parse", "HEAD") {
				t.Fatal("refusal changed index or HEAD")
			}
		})
	}
}

func TestAdoptionIsExactFileScopeAndPartOfIdentity(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"backlog/task.md": "task\n"})
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	strict, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := r.ChangesIncluding(baseline, "backlog/task.md")
	if err != nil {
		t.Fatal(err)
	}
	if strict.ID == adopted.ID {
		t.Fatal("scope missing from identity")
	}
	for _, path := range []string{"backlog", ".", "../outside"} {
		if _, err := r.ChangesIncluding(baseline, path); err == nil {
			t.Fatalf("accepted non-file scope %q", path)
		}
	}
}
