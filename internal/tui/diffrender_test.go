package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestDiffDetectionAndColorize(t *testing.T) {
	diff := "diff --git a/x b/x\n@@ -1 +1 @@\n-old line\n+new line\n unchanged"
	if !looksDiff(diff) {
		t.Fatal("looksDiff should detect a git diff")
	}
	out := colorizeDiff(diff)
	for _, want := range []string{"old line", "new line", "unchanged", "@@ -1 +1 @@"} {
		if !strings.Contains(out, want) {
			t.Fatalf("colorizeDiff dropped %q:\n%s", want, out)
		}
	}
	if looksDiff("just some text\nno diff here") {
		t.Fatal("looksDiff false positive")
	}
}

func TestCatNDimming(t *testing.T) {
	src := "     1\tpackage main\n     2\tfunc main() {}"
	if !looksCatN(src) {
		t.Fatal("looksCatN should detect cat -n output")
	}
	out := dimLineNumbers(src)
	if !strings.Contains(out, "package main") || !strings.Contains(out, "func main() {}") {
		t.Fatalf("dimLineNumbers dropped code:\n%s", out)
	}
}

func TestUnifiedDiff(t *testing.T) {
	oldStr := "line one\n\tindented\nmiddle old\nfour\nfive"
	newStr := "line one\n\tindented\nmiddle new\nfour\nfive"
	out := unifiedDiff(oldStr, newStr, 3)
	if !strings.Contains(out, "@@") {
		t.Fatalf("expected hunk header, got:\n%s", out)
	}
	if !strings.Contains(out, "-middle old") {
		t.Errorf("expected removed line, got:\n%s", out)
	}
	if !strings.Contains(out, "+middle new") {
		t.Errorf("expected added line, got:\n%s", out)
	}
	if !strings.Contains(out, " line one") {
		t.Errorf("expected context line prefixed with space, got:\n%s", out)
	}
	// Indentation preserved after the prefix.
	if !strings.Contains(out, " \tindented") {
		t.Errorf("expected indentation preserved, got:\n%q", out)
	}
}

func TestUnifiedDiffTruncation(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&oldB, "old %d\n", i)
		fmt.Fprintf(&newB, "new %d\n", i)
	}
	out := unifiedDiff(oldB.String(), newB.String(), 3)
	if !strings.Contains(out, "diff truncated") {
		t.Errorf("expected truncation marker for very large input")
	}
	if n := strings.Count(out, "\n"); n > 420 {
		t.Errorf("expected output bounded, got %d lines", n)
	}
}
