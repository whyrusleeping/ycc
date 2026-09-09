package orchestrator

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/whyrusleeping/ycc/internal/git"
)

func TestBuildReviewDiffHistoryEmpty(t *testing.T) {
	repo, err := git.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := repo.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if got := buildReviewDiffHistory(repo, baseline); len(got.History) != 0 {
		t.Fatalf("empty diff produced history: %+v", got.History)
	}
	if got := buildReviewDiffHistory(nil, baseline); got.Err == nil || len(got.History) != 0 {
		t.Fatalf("nil repo result = %+v, want explicit error and no history", got)
	}
}

func TestBuildReviewDiffHistoryExchange(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := repo.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "changed.go"), []byte("package changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(ws, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	got := buildReviewDiffHistory(repo, baseline)
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(indexAfter, indexBefore) {
		t.Fatal("review preload mutated the user's index")
	}
	if len(got.History) != 3 {
		t.Fatalf("history len = %d, want 3: %+v", len(got.History), got.History)
	}
	if got.History[0].Role != "user" || got.History[0].Content != reviewDiffNudge {
		t.Fatalf("nudge = %+v", got.History[0])
	}
	assistant := got.History[1]
	if assistant.Role != "assistant" || assistant.Content != "" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant exchange = %+v", assistant)
	}
	call := assistant.ToolCalls[0]
	if call.ID != reviewDiffCallID || call.Type != "function" || call.Function.Name != "Bash" ||
		!strings.Contains(call.Function.Arguments, ` -- ':(top,literal)changed.go'`) ||
		!strings.Contains(call.Function.Arguments, `{"command":"git diff --binary --no-color --no-ext-diff `) {
		t.Fatalf("Bash call = %+v", call)
	}
	result := got.History[2]
	if result.Role != "tool" || result.ToolCallID != reviewDiffCallID || !strings.Contains(result.Content, "+package changed") {
		t.Fatalf("diff result = %+v", result)
	}
	if got.SnapshotID == "" || !strings.Contains(result.Content, "CHANGESET SNAPSHOT "+got.SnapshotID) || !strings.Contains(result.Content, "SCOPE: changed.go") {
		t.Fatalf("snapshot identity/scope missing from preload: %+v", got)
	}
	if got.Result != result.Content {
		t.Fatal("event result and history result differ")
	}
}

func TestReviewRetrievalWorksFromSubdirectoryWorkspace(t *testing.T) {
	root := t.TempDir()
	if _, err := git.Open(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "task.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "seed"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "outside.txt"), []byte("pre-existing outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := git.OpenExisting(filepath.Join(root, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := repo.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "task.txt"), []byte("reviewed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	built := buildReviewDiffHistory(repo, baseline)
	if built.Err != nil {
		t.Fatal(built.Err)
	}
	cmd := exec.Command("sh", "-c", built.Command)
	cmd.Dir = repo.Dir
	retrieved, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("exact retrieval failed from subdirectory: %v: %s\ncommand: %s", err, retrieved, built.Command)
	}
	if !strings.Contains(string(retrieved), "+reviewed") || strings.Contains(string(retrieved), "pre-existing outside") {
		t.Fatalf("retrieved wrong snapshot:\n%s\ncommand: %s", retrieved, built.Command)
	}
	changes, err := repo.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(changes, "subdirectory review"); err != nil {
		t.Fatal(err)
	}
	outside, err := os.ReadFile(filepath.Join(root, "outside.txt"))
	if err != nil || string(outside) != "pre-existing outside\n" {
		t.Fatalf("outside dirty work changed: %q, err=%v", outside, err)
	}
}

func TestBuildReviewDiffHistoryTruncatesWithinCap(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := repo.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	// Include multibyte text near the cut point to exercise valid UTF-8 truncation.
	big := strings.Repeat("é0123456789\n", maxReviewDiffBytes/4)
	if err := os.WriteFile(filepath.Join(ws, "large.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got := buildReviewDiffHistory(repo, baseline)
	if len(got.History) != 3 {
		t.Fatalf("history len = %d, want 3", len(got.History))
	}
	if len(got.Result) > maxReviewDiffBytes {
		t.Fatalf("diff bytes = %d, cap %d", len(got.Result), maxReviewDiffBytes)
	}
	if !utf8.ValidString(got.Result) {
		t.Fatal("truncated diff is not valid UTF-8")
	}
	if !strings.HasSuffix(got.Result, reviewDiffTruncated) {
		t.Fatalf("truncation notice absent; tail = %q", got.Result[len(got.Result)-200:])
	}
}
