package orchestrator

import (
	"os"
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
	if got := buildReviewDiffHistory(repo); len(got.History) != 0 {
		t.Fatalf("empty diff produced history: %+v", got.History)
	}
	if got := buildReviewDiffHistory(nil); len(got.History) != 0 {
		t.Fatalf("nil repo produced history: %+v", got.History)
	}
}

func TestBuildReviewDiffHistoryExchange(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "changed.go"), []byte("package changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := buildReviewDiffHistory(repo)
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
	if call.ID != reviewDiffCallID || call.Type != "function" || call.Function.Name != "Bash" || call.Function.Arguments != `{"command":"git diff HEAD"}` {
		t.Fatalf("Bash call = %+v", call)
	}
	result := got.History[2]
	if result.Role != "tool" || result.ToolCallID != reviewDiffCallID || !strings.Contains(result.Content, "+package changed") {
		t.Fatalf("diff result = %+v", result)
	}
	if got.Result != result.Content {
		t.Fatal("event result and history result differ")
	}
}

func TestBuildReviewDiffHistoryTruncatesWithinCap(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	// Include multibyte text near the cut point to exercise valid UTF-8 truncation.
	big := strings.Repeat("é0123456789\n", maxReviewDiffBytes/4)
	if err := os.WriteFile(filepath.Join(ws, "large.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got := buildReviewDiffHistory(repo)
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
