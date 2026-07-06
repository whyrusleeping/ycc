package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// sampleCommitDiff is a two-file `git show` fixture used by the parser + render
// tests: a preamble (commit header + --stat), then two "diff --git" sections.
const sampleCommitDiff = `commit deadbeefcafef00d
Author: ycc <ycc@localhost>
Date:   Mon Jan 1 00:00:00 2024 +0000

    add two files

 a.go | 3 +++
 b.go | 2 +-
 2 files changed, 4 insertions(+), 1 deletion(-)

diff --git a/a.go b/a.go
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/a.go
@@ -0,0 +1,3 @@
+package a
+
+func A() {}
diff --git a/b.go b/b.go
index 2222222..3333333 100644
--- a/b.go
+++ b/b.go
@@ -1,2 +1,2 @@
 package b
-func B() {}
+func B() int { return 1 }
`

// TestParseCommitDiff checks the parser splits files correctly, extracts paths,
// and counts added/removed content lines (excluding the +++/--- markers).
func TestParseCommitDiff(t *testing.T) {
	pre, files := parseCommitDiff(sampleCommitDiff)
	if !strings.Contains(pre, "add two files") || strings.Contains(pre, "diff --git") {
		t.Fatalf("preamble wrong:\n%s", pre)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	if files[0].path != "a.go" || files[1].path != "b.go" {
		t.Fatalf("paths = %q, %q, want a.go, b.go", files[0].path, files[1].path)
	}
	// a.go: 3 added lines (package a, blank, func), 0 removed.
	if files[0].adds != 3 || files[0].dels != 0 {
		t.Fatalf("a.go counts = +%d −%d, want +3 −0", files[0].adds, files[0].dels)
	}
	// b.go: 1 added, 1 removed.
	if files[1].adds != 1 || files[1].dels != 1 {
		t.Fatalf("b.go counts = +%d −%d, want +1 −1", files[1].adds, files[1].dels)
	}
	if !strings.HasPrefix(files[0].body, "diff --git a/a.go") {
		t.Fatalf("file body should include the diff --git header:\n%s", files[0].body)
	}
}

// TestCommitDiffFoldRendering verifies a folded file hides its body while an
// unfolded one shows it, and the truncation trailer appears when set.
func TestCommitDiffFoldRendering(t *testing.T) {
	pre, files := parseCommitDiff(sampleCommitDiff)
	m := &model{
		cdiffOpen:     true,
		cdiffPreamble: pre,
		cdiffFiles:    files,
		cdiffFold:     []bool{true, false}, // a.go folded, b.go open
		cdiffCursor:   0,
	}
	out := m.cdiffContent()
	// Both headers always present.
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "b.go") {
		t.Fatalf("both file headers should render:\n%s", out)
	}
	// a.go is folded → its body ("func A") is hidden.
	if strings.Contains(out, "func A()") {
		t.Fatalf("folded file body should be hidden:\n%s", out)
	}
	// b.go is unfolded → its body is shown.
	if !strings.Contains(out, "func B() int") {
		t.Fatalf("unfolded file body should be shown:\n%s", out)
	}
	// Fold markers.
	if !strings.Contains(out, "▸ a.go") || !strings.Contains(out, "▾ b.go") {
		t.Fatalf("fold markers wrong:\n%s", out)
	}

	// Truncation trailer only when the flag is set.
	if strings.Contains(out, "truncated") {
		t.Fatalf("no truncation trailer expected when not truncated:\n%s", out)
	}
	m.cdiffTruncated = true
	if !strings.Contains(m.cdiffContent(), "truncated") {
		t.Fatal("truncation trailer expected when truncated")
	}
}

// TestCommitDiffAutoFold checks the large-commit safety: past the file/line
// thresholds the overlay opens with every file folded.
func TestCommitDiffAutoFold(t *testing.T) {
	// Build a diff with > cdiffFoldAllFiles files.
	var b strings.Builder
	b.WriteString("commit abc\n\n")
	for i := 0; i < cdiffFoldAllFiles+5; i++ {
		b.WriteString("diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -0,0 +1 @@\n+x\n")
	}
	f := newFakeClient()
	f.commitDiff = b.String()
	m := model{client: f, ctx: context.Background(), w: 80, h: 24, ready: true}
	m.cdiffOpen = true
	m.cdiffSha = "abc123"
	updated, _ := m.Update(commitDiffMsg{sha: "abc123", diff: f.commitDiff})
	m = updated.(model)
	if len(m.cdiffFiles) <= cdiffFoldAllFiles {
		t.Fatalf("expected many files, got %d", len(m.cdiffFiles))
	}
	for i, folded := range m.cdiffFold {
		if !folded {
			t.Fatalf("file %d should be auto-folded on a large commit", i)
		}
	}
}

// TestCommitDiffOpenFromCommitRow drives Enter on a selected commit_made row in
// the live session: it opens the overlay, issues the fetch, and esc closes it.
func TestCommitDiffOpenFromCommitRow(t *testing.T) {
	f := newFakeClient()
	f.commitDiff = sampleCommitDiff
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "idle", sessionID: "s_live",
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{Seq: 1, Type: "commit_made", Actor: "coordinator", DataJson: `{"sha":"deadbeef","message":"add two files"}`})
	m.rebuild()
	m.selected = 0

	// Enter with an empty input opens the commit-diff overlay and fetches the diff.
	m = drive(t, m, "enter")
	if !m.cdiffOpen {
		t.Fatal("enter on a commit_made row should open the commit-diff overlay")
	}
	if f.lastCommitSha != "deadbeef" {
		t.Fatalf("GetCommitDiff sha = %q, want deadbeef", f.lastCommitSha)
	}
	if len(m.cdiffFiles) != 2 {
		t.Fatalf("overlay should have parsed 2 files, got %d", len(m.cdiffFiles))
	}

	// tab moves the file cursor; enter folds the cursor file.
	m = drive(t, m, "tab")
	if m.cdiffCursor != 1 {
		t.Fatalf("tab should advance the file cursor, got %d", m.cdiffCursor)
	}
	m = drive(t, m, "enter")
	if !m.cdiffFold[1] {
		t.Fatal("enter should fold the file under the cursor")
	}

	// esc closes the overlay and restores the session behind it.
	m = drive(t, m, "esc")
	if m.cdiffOpen {
		t.Fatal("esc should close the commit-diff overlay")
	}
	if m.state != stateSession {
		t.Fatalf("closing the overlay should return to the session, state=%v", m.state)
	}
}

// TestCommitDiffStaleReplyIgnored ensures a diff reply that arrives after the
// overlay was closed (or moved to another sha) is dropped.
func TestCommitDiffStaleReplyIgnored(t *testing.T) {
	m := model{w: 80, h: 24, ready: true}
	// Overlay closed → ignore.
	updated, _ := m.Update(commitDiffMsg{sha: "x", diff: sampleCommitDiff})
	m = updated.(model)
	if len(m.cdiffFiles) != 0 {
		t.Fatal("reply with the overlay closed must be ignored")
	}
	// Overlay open on a different sha → ignore.
	m.cdiffOpen = true
	m.cdiffSha = "y"
	updated, _ = m.Update(commitDiffMsg{sha: "x", diff: sampleCommitDiff})
	m = updated.(model)
	if len(m.cdiffFiles) != 0 {
		t.Fatal("reply for a different sha must be ignored")
	}
}
