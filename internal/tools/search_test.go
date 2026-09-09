package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
)

func callSearch(t *testing.T, ctx context.Context, root string, params map[string]any) *gollama.ToolResult {
	t.Helper()
	res, err := search(&Workspace{Root: root}).Call(ctx, params)
	if err != nil {
		t.Fatalf("Search call: %v", err)
	}
	return res
}

func requireRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
}

func writeSearchFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSearchLiteralQuotingUnicodeAndScope(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	pattern := `price is $5; say 'hello' "世界".*`
	writeSearchFixture(t, root, "src/hit.go", "before\n"+pattern+"\nafter\n")
	writeSearchFixture(t, root, "other/hit.txt", pattern+"\n")

	res := callSearch(t, context.Background(), root, map[string]any{
		"pattern": pattern,
		"path":    "src",
		"glob":    []any{"*.go"},
		"context": float64(1),
	})
	if res.IsError {
		t.Fatalf("Search failed: %s", res.Content)
	}
	for _, want := range []string{"status=ok", "src/hit.go-1-before", "src/hit.go:2:" + pattern, "src/hit.go-3-after"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("result missing %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "other/hit.txt") {
		t.Fatalf("search escaped path/glob scope:\n%s", res.Content)
	}
}

func TestSearchRegexFilesAndCount(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	writeSearchFixture(t, root, "a.go", "item-1\nitem-2\n")
	writeSearchFixture(t, root, "b.go", "item-3\n")
	writeSearchFixture(t, root, "c.txt", "item-4\n")

	files := callSearch(t, context.Background(), root, map[string]any{
		"pattern": `item-[0-9]`, "mode": "regex", "output": "files", "glob": []any{"*.go"},
	})
	if files.IsError || !strings.Contains(files.Content, "a.go\n") || !strings.Contains(files.Content, "b.go\n") || strings.Contains(files.Content, "c.txt") {
		t.Fatalf("unexpected files output:\n%s", files.Content)
	}
	counts := callSearch(t, context.Background(), root, map[string]any{
		"pattern": `item-[0-9]`, "mode": "regex", "output": "count", "glob": []any{"*.go"},
	})
	if counts.IsError || !strings.Contains(counts.Content, "a.go:2") || !strings.Contains(counts.Content, "b.go:1") {
		t.Fatalf("unexpected count output:\n%s", counts.Content)
	}
}

func TestSearchIgnoresAndGeneratedOverrides(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	writeSearchFixture(t, root, ".gitignore", "ignored.txt\n")
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchFixture(t, root, "visible.txt", "needle\n")
	writeSearchFixture(t, root, "ignored.txt", "needle\n")
	writeSearchFixture(t, root, ".ycc/sessions/s1/events.jsonl", "needle\n")

	defaults := callSearch(t, context.Background(), root, map[string]any{"pattern": "needle", "include_hidden": true})
	if defaults.IsError || !strings.Contains(defaults.Content, "visible.txt") || strings.Contains(defaults.Content, "ignored.txt") || strings.Contains(defaults.Content, ".ycc/") {
		t.Fatalf("default exclusions not honored:\n%s", defaults.Content)
	}
	overrides := callSearch(t, context.Background(), root, map[string]any{
		"pattern": "needle", "include_ignored": true, "include_hidden": true, "include_generated": true,
	})
	for _, want := range []string{"visible.txt", "ignored.txt", ".ycc/sessions/s1/events.jsonl"} {
		if overrides.IsError || !strings.Contains(overrides.Content, want) {
			t.Fatalf("explicit overrides missing %q:\n%s", want, overrides.Content)
		}
	}
}

func TestSearchNoMatchAndInvalidRegex(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	writeSearchFixture(t, root, "a.txt", "haystack\n")

	none := callSearch(t, context.Background(), root, map[string]any{"pattern": "needle"})
	if none.IsError || !strings.Contains(none.Content, "status=no_match") || !strings.Contains(none.Content, "truncated=false") {
		t.Fatalf("no-match result is not explicit:\n%s", none.Content)
	}
	invalid := callSearch(t, context.Background(), root, map[string]any{"pattern": "[", "mode": "regex"})
	if !invalid.IsError || !strings.Contains(invalid.Content, "ripgrep failed") || !strings.Contains(strings.ToLower(invalid.Content), "regex") {
		t.Fatalf("invalid regex is not actionable: error=%t content=%q", invalid.IsError, invalid.Content)
	}
}

func TestSearchPaginationAndByteBudget(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	var content strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&content, "needle result %02d %s\n", i, strings.Repeat("x", 80))
	}
	writeSearchFixture(t, root, "many.txt", content.String())

	first := callSearch(t, context.Background(), root, map[string]any{"pattern": "needle", "limit": float64(7)})
	if first.IsError || !strings.Contains(first.Content, "returned=7 truncated=true next_offset=7") {
		t.Fatalf("first page metadata incorrect:\n%s", first.Content)
	}
	second := callSearch(t, context.Background(), root, map[string]any{"pattern": "needle", "limit": float64(7), "offset": float64(7)})
	if second.IsError || !strings.Contains(second.Content, "offset=7 returned=7") || !strings.Contains(second.Content, "many.txt:8:") || strings.Contains(second.Content, "many.txt:1:") {
		t.Fatalf("second page is not a deterministic continuation:\n%s", second.Content)
	}

	bounded := callSearch(t, context.Background(), root, map[string]any{"pattern": "needle", "limit": float64(500), "max_bytes": float64(1024)})
	if bounded.IsError || !strings.Contains(bounded.Content, "truncated=true next_offset=") {
		t.Fatalf("byte-bounded result lacks continuation metadata:\n%s", bounded.Content)
	}
	if len(bounded.Content) > 1400 { // metadata is deliberately outside max_bytes.
		t.Fatalf("byte budget was not respected: got %d bytes", len(bounded.Content))
	}
}

func TestSearchContinuationBeyondFormerOffsetBoundary(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	const matchCount = 100102
	var content strings.Builder
	content.Grow(matchCount * 16)
	for i := 1; i <= matchCount; i++ {
		fmt.Fprintf(&content, "needle %06d\n", i)
	}
	writeSearchFixture(t, root, "many.txt", content.String())

	first := callSearch(t, context.Background(), root, map[string]any{
		"pattern": "needle", "offset": float64(100000), "limit": float64(100),
	})
	if first.IsError || !strings.Contains(first.Content, "returned=100 truncated=true next_offset=100100") || !strings.Contains(first.Content, "many.txt:100001:") {
		t.Fatalf("boundary page metadata incorrect:\n%s", first.Content)
	}
	following := callSearch(t, context.Background(), root, map[string]any{
		"pattern": "needle", "offset": float64(100100), "limit": float64(100),
	})
	if following.IsError || !strings.Contains(following.Content, "offset=100100 returned=2 truncated=false") || !strings.Contains(following.Content, "many.txt:100101:") || !strings.Contains(following.Content, "many.txt:100102:") {
		t.Fatalf("emitted continuation was not usable:\n%s", following.Content)
	}
}

func TestSearchCancellationAndUnavailableBackend(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	writeSearchFixture(t, root, "a.txt", "needle\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := callSearch(t, ctx, root, map[string]any{"pattern": "needle"})
	if !canceled.IsError || !strings.Contains(canceled.Content, "canceled") {
		t.Fatalf("cancellation is not explicit: error=%t content=%q", canceled.IsError, canceled.Content)
	}

	bin := filepath.Join(root, "bin")
	writeSearchFixture(t, root, "bin/rg", "#!/bin/sh\nexec sleep 10\n")
	if err := os.Chmod(filepath.Join(bin, "rg"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runningCtx, stop := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer stop()
	running := callSearch(t, runningCtx, root, map[string]any{"pattern": "needle"})
	if !running.IsError || !strings.Contains(running.Content, "canceled") {
		t.Fatalf("running cancellation is not explicit: error=%t content=%q", running.IsError, running.Content)
	}

	t.Setenv("PATH", "")
	missing := callSearch(t, context.Background(), root, map[string]any{"pattern": "needle"})
	if !missing.IsError || !strings.Contains(missing.Content, "ripgrep") || !strings.Contains(missing.Content, "install") {
		t.Fatalf("missing backend is not actionable: error=%t content=%q", missing.IsError, missing.Content)
	}
}

// This fixture comparison keeps the documented Search-versus-Bash+rg claims
// reproducible: both operations must find the same references, while the log
// records one-call round trips and actual response bytes for periodic review.
func TestSearchRepresentativeComparison(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		writeSearchFixture(t, root, filepath.Join("src", "f"+strconv.Itoa(i)+".go"), fmt.Sprintf("package p\n// marker-%d\n", i))
	}
	literal := `$value 'quoted' [x].* 世界`
	writeSearchFixture(t, root, "src/special.go", literal+"\n")

	t.Run("quoting-sensitive literal", func(t *testing.T) {
		searchResult := callSearch(t, context.Background(), root, map[string]any{
			"pattern": literal, "path": "src", "glob": []any{"*.go"},
		})
		bashResult := callComparisonBash(t, root, "rg --line-number -F --sort path --glob '*.go' "+shellQuote(literal)+" src")
		if !strings.Contains(searchResult.Content, "src/special.go:1:") || !strings.Contains(bashResult.Content, "src/special.go:1:") {
			t.Fatalf("literal results disagree\nSearch:\n%s\nBash+rg:\n%s", searchResult.Content, bashResult.Content)
		}
		t.Logf("Search calls=1 bytes=%d; Bash+rg calls=1 bytes=%d", len(searchResult.Content), len(bashResult.Content))
	})

	t.Run("scoped regex matches", func(t *testing.T) {
		searchResult := callSearch(t, context.Background(), root, map[string]any{
			"pattern": `marker-[12][0-9]`, "mode": "regex", "path": "src", "glob": []any{"*.go"},
		})
		bashResult := callComparisonBash(t, root, "rg --line-number --sort path --glob '*.go' 'marker-[12][0-9]' src")
		for i := 10; i < 30; i++ {
			ref := fmt.Sprintf("src/f%d.go:2:", i)
			if !strings.Contains(searchResult.Content, ref) || !strings.Contains(bashResult.Content, ref) {
				t.Fatalf("results disagree for %s\nSearch:\n%s\nBash+rg:\n%s", ref, searchResult.Content, bashResult.Content)
			}
		}
		t.Logf("Search calls=1 bytes=%d; Bash+rg calls=1 bytes=%d", len(searchResult.Content), len(bashResult.Content))
	})

	t.Run("per-file counts", func(t *testing.T) {
		searchResult := callSearch(t, context.Background(), root, map[string]any{
			"pattern": "marker-", "path": "src", "glob": []any{"*.go"}, "output": "count",
		})
		bashResult := callComparisonBash(t, root, "rg -F --count-matches --sort path --glob '*.go' 'marker-' src")
		for i := 0; i < 30; i++ {
			ref := fmt.Sprintf("src/f%d.go:1", i)
			if !strings.Contains(searchResult.Content, ref) || !strings.Contains(bashResult.Content, ref) {
				t.Fatalf("count results disagree for %s\nSearch:\n%s\nBash+rg:\n%s", ref, searchResult.Content, bashResult.Content)
			}
		}
		t.Logf("Search calls=1 bytes=%d; Bash+rg calls=1 bytes=%d", len(searchResult.Content), len(bashResult.Content))
	})
}

func callComparisonBash(t *testing.T, root, command string) *gollama.ToolResult {
	t.Helper()
	res, err := bash(&Workspace{Root: root}).Call(context.Background(), map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal(res.Content)
	}
	return res
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
