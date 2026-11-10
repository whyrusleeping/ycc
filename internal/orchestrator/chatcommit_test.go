package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
)

func callChatCommit(t *testing.T, d *Deps, policy string, params map[string]any) (string, bool) {
	t.Helper()
	result, err := chatCommitTool(d, policy).Call(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	return result.Content, result.IsError
}

// [chat] finished_work picks the policy; anything but the exact push value
// never pushes. The chat prompt carries the matching guidance.
func TestChatFinishedWorkPolicy(t *testing.T) {
	for _, tc := range []struct{ config, want, guidance string }{
		{"", docs.ChatCommit, "Finished work: commit it."},
		{"[chat]\nfinished_work = \"ask\"\n", docs.ChatAsk, "this project asks before committing"},
		{"[chat]\nfinished_work = \" Commit+Push \"\n", docs.ChatCommitPush, "Finished work: commit and push it."},
		{"[chat]\nfinished_work = \"push\"\n", docs.ChatCommit, "Finished work: commit it."},
	} {
		d := depsFor(t)
		if tc.config != "" {
			if err := os.MkdirAll(filepath.Join(d.Workspace, ".ycc"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d.Workspace, ".ycc", "config.toml"), []byte(tc.config), 0o644); err != nil {
				t.Fatal(err)
			}
			d.Docs = docs.NewStore(d.Workspace)
		}
		if got := d.Docs.ChatFinishedWork(); got != tc.want {
			t.Fatalf("config %q: policy = %q, want %q", tc.config, got, tc.want)
		}
		_, system := BuildMode("chat", d, false)
		if !strings.Contains(system, tc.guidance) {
			t.Fatalf("config %q: chat prompt lacks %q", tc.config, tc.guidance)
		}
	}
}

// Without a task, chat commit takes only the session's changes, leaves earlier
// staged user work alone, records commit_made, and refuses an empty commit.
func TestChatCommitSessionChanges(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	rec := &captureRec{}
	d := finalizationDeps(ws, repo, baseline, store, rec)

	got, failed := callChatCommit(t, d, docs.ChatCommit, map[string]any{"message": "chat change"})
	if failed || !strings.HasPrefix(got, "committed ") {
		t.Fatalf("commit = error %v, %q", failed, got)
	}
	if got := gitRun(t, ws, "show", "HEAD:owned.txt"); got != "after\n" {
		t.Fatalf("HEAD:owned.txt = %q", got)
	}
	if files := gitRun(t, ws, "show", "--name-only", "--format=", "HEAD"); strings.TrimSpace(files) != "owned.txt" {
		t.Fatalf("committed files = %q", files)
	}
	if got := gitRun(t, ws, "show", ":unrelated.txt"); got != "user work\n" {
		t.Fatalf("lost staged user work: %q", got)
	}
	if task, err := store.Get("0001"); err != nil || task.Status != docs.StatusInReview {
		t.Fatalf("task changed by a task-less commit: %+v, %v", task, err)
	}
	var commits int
	for _, ev := range rec.events {
		if ev.Type == event.CommitMade {
			commits++
			if ev.Data["message"] != "chat change" || ev.Data["sha"] == "" {
				t.Fatalf("commit_made data = %v", ev.Data)
			}
		}
	}
	if commits != 1 {
		t.Fatalf("commit_made events = %d, want 1", commits)
	}

	if got, failed := callChatCommit(t, d, docs.ChatCommit, map[string]any{"message": "again"}); !failed || !strings.Contains(got, "nothing to commit") {
		t.Fatalf("empty commit = error %v, %q", failed, got)
	}
	// A later change in the same session commits on top.
	if err := os.WriteFile(filepath.Join(ws, "owned.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, failed := callChatCommit(t, d, docs.ChatCommit, map[string]any{"message": "follow-up"}); failed {
		t.Fatalf("second commit = %q", got)
	}
	if got := gitRun(t, ws, "show", "HEAD:owned.txt"); got != "later\n" {
		t.Fatalf("HEAD:owned.txt after second commit = %q", got)
	}
}

// With task_id, chat commit finalizes the task in the same commit.
func TestChatCommitCompletesTask(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	d := finalizationDeps(ws, repo, baseline, store, &captureRec{})
	if got, failed := callChatCommit(t, d, docs.ChatCommit, map[string]any{"message": "finish task", "task_id": "0001"}); !failed || !strings.Contains(got, "outcome is required") {
		t.Fatalf("task commit without outcome = error %v, %q", failed, got)
	}
	if got, failed := callChatCommit(t, d, docs.ChatCommit, map[string]any{
		"message": "finish task", "task_id": "0001", "outcome": "Done in chat.",
	}); failed {
		t.Fatalf("task commit = %q", got)
	}
	committed := gitRun(t, ws, "show", "HEAD:backlog/0001-recover-commit.md")
	if !strings.Contains(committed, "status: done") || !strings.Contains(committed, "Done in chat.") {
		t.Fatalf("commit lacks the completed task:\n%s", committed)
	}
	if got := gitRun(t, ws, "show", "HEAD:owned.txt"); got != "after\n" {
		t.Fatalf("HEAD:owned.txt = %q", got)
	}
}

// commit+push pushes the current branch to its upstream; plain commit never
// pushes; a push failure is reported without failing the commit.
func TestChatCommitPushPolicy(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitRun(t, ws, "init", "-q", "--bare", remote)
	branch := strings.TrimSpace(gitRun(t, ws, "rev-parse", "--abbrev-ref", "HEAD"))
	d := finalizationDeps(ws, repo, baseline, store, &captureRec{})

	// No upstream yet: committed, push failure reported.
	got, failed := callChatCommit(t, d, docs.ChatCommitPush, map[string]any{"message": "first"})
	if failed || !strings.Contains(got, "committed ") || !strings.Contains(got, "not pushed:") {
		t.Fatalf("push without upstream = error %v, %q", failed, got)
	}

	gitRun(t, ws, "remote", "add", "origin", remote)
	gitRun(t, ws, "branch", "other")
	gitRun(t, ws, "push", "-q", "origin", "other")
	gitRun(t, ws, "push", "-q", "-u", "origin", branch)
	// Push settings that would widen a bare `git push` to every matching
	// branch, a new local commit on another branch, and a hook that records
	// whether daemon credentials reach push subprocesses.
	gitRun(t, ws, "config", "push.default", "matching")
	otherTip := strings.TrimSpace(gitRun(t, ws, "commit-tree", "HEAD^{tree}", "-p", "other", "-m", "unrelated branch work"))
	gitRun(t, ws, "update-ref", "refs/heads/other", otherTip)
	t.Setenv("YCC_TOKEN", "daemon-sentinel")
	hookOut := filepath.Join(t.TempDir(), "hook-env")
	hook := "#!/bin/sh\nprintf 'token=%s\\n' \"$YCC_TOKEN\" >> " + hookOut + "\n"
	if err := os.WriteFile(filepath.Join(ws, ".git", "hooks", "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	remoteOther := strings.TrimSpace(gitRun(t, ws, "--git-dir", remote, "rev-parse", "other"))
	if err := os.WriteFile(filepath.Join(ws, "owned.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, failed := callChatCommit(t, d, docs.ChatCommit, map[string]any{"message": "second"}); failed || strings.Contains(got, "pushed") {
		t.Fatalf("commit policy = error %v, %q", failed, got)
	}
	head := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD"))
	if pushed := strings.TrimSpace(gitRun(t, ws, "--git-dir", remote, "rev-parse", branch)); pushed == head {
		t.Fatal("commit policy pushed")
	}

	if err := os.WriteFile(filepath.Join(ws, "owned.txt"), []byte("third\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, failed := callChatCommit(t, d, docs.ChatCommitPush, map[string]any{"message": "third"}); failed || !strings.Contains(got, "\n- pushed: ") {
		t.Fatalf("commit+push = error %v, %q", failed, got)
	}
	head = strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD"))
	if pushed := strings.TrimSpace(gitRun(t, ws, "--git-dir", remote, "rev-parse", branch)); pushed != head {
		t.Fatalf("remote %s = %s, want pushed HEAD %s", branch, pushed, head)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "--git-dir", remote, "rev-parse", "other")); got != remoteOther {
		t.Fatalf("push widened to another branch: remote other = %s, want %s", got, remoteOther)
	}
	if env, err := os.ReadFile(hookOut); err != nil || strings.TrimSpace(string(env)) != "token=" {
		t.Fatalf("pre-push hook env = %q, %v; want the daemon token scrubbed", env, err)
	}
}
