package workspacelease

import (
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func linesRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	for name, content := range files {
		writeFile(t, filepath.Join(root, name), content)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "seed")
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitFile(t *testing.T, root, rel, content string) {
	t.Helper()
	dir := t.TempDir()
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(dir, "index"))
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "read-tree", "HEAD")
	blob := run(content, "hash-object", "-w", "--stdin")
	run("", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+rel)
	tree := run("", "write-tree")
	commit := run("partial", "commit-tree", tree, "-p", "HEAD")
	run("", "update-ref", "HEAD", commit)
}

// edit records a write of rel by tok, as the docs store or a file tool does.
func edit(t *testing.T, s *Service, root, rel string, tok *Token, change func(string) string) {
	t.Helper()
	path := filepath.Join(root, rel)
	before, _ := os.ReadFile(path)
	after := change(string(before))
	writeFile(t, path, after)
	s.RecordLines(path, root, tok, before, []byte(after))
}

func view(t *testing.T, s *Service, root, rel, scope string) (string, bool, bool) {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	content, _, mine, ok, err := s.LineView(root, strings.TrimSpace(string(out)), rel, scope, true)
	if err != nil {
		t.Fatal(err)
	}
	return string(content), mine, ok
}

func replace(old, new string) func(string) string {
	return func(s string) string { return strings.Replace(s, old, new, 1) }
}

func appendLine(line string) func(string) string {
	return func(s string) string { return s + line + "\n" }
}

const taskDoc = "---\nid: \"0001\"\nstatus: todo\n---\n\n## Work log\n"

// Two sessions edit one task file at once. Each version holds only its own
// lines; after one commits its version, the other's pending change stays
// attributed to it on top of the new HEAD.
func TestLineViewsSeparateConcurrentEditsOfOneTaskFile(t *testing.T) {
	root := linesRepo(t, map[string]string{"backlog/0001.md": taskDoc})
	s := NewService()
	a, b := s.NewScopedToken("s_a", "a"), s.NewScopedToken("s_b", "b")
	rel := "backlog/0001.md"

	edit(t, s, root, rel, a, replace("status: todo", "status: in_progress"))
	edit(t, s, root, rel, b, appendLine("- B progress"))
	edit(t, s, root, rel, a, appendLine("- A progress"))
	// An unrecorded hand edit rides along with either version.
	edit(t, s, root, rel, s.NewScopedToken("", "hand"), replace("## Work log", "## Work log\n<!-- hand -->"))

	wantA := "---\nid: \"0001\"\nstatus: in_progress\n---\n\n## Work log\n<!-- hand -->\n- A progress\n"
	if got, mine, ok := view(t, s, root, rel, "s_a"); !ok || !mine || got != wantA {
		t.Fatalf("A view ok=%v mine=%v:\n%s", ok, mine, got)
	}
	wantB := "---\nid: \"0001\"\nstatus: todo\n---\n\n## Work log\n<!-- hand -->\n- B progress\n"
	if got, mine, ok := view(t, s, root, rel, "s_b"); !ok || !mine || got != wantB {
		t.Fatalf("B view ok=%v mine=%v:\n%s", ok, mine, got)
	}
	if _, mine, ok := view(t, s, root, rel, "s_c"); !ok || mine {
		t.Fatalf("an uninvolved session's view: ok=%v mine=%v; want excluded", ok, mine)
	}

	commitFile(t, root, rel, wantA)
	if _, mine, ok := view(t, s, root, rel, "s_a"); !ok || mine {
		t.Fatalf("A after its commit: ok=%v mine=%v; want nothing of A's left", ok, mine)
	}
	wantB = "---\nid: \"0001\"\nstatus: in_progress\n---\n\n## Work log\n<!-- hand -->\n- B progress\n- A progress\n"
	if got, mine, ok := view(t, s, root, rel, "s_b"); !ok || !mine || got != wantB {
		t.Fatalf("B view after A's commit ok=%v mine=%v:\n%s", ok, mine, got)
	}
	data, _ := os.ReadFile(filepath.Join(root, rel))
	if string(data) != wantB {
		t.Fatalf("worktree changed:\n%s", data)
	}
}

// Changing a line another session wrote takes that session's change along, so
// a replaced line never appears twice; so does editing a file it created.
func TestLineViewAdoptsTheChangesItBuildsOn(t *testing.T) {
	root := linesRepo(t, map[string]string{"backlog/0001.md": taskDoc})
	s := NewService()
	a, b := s.NewScopedToken("s_a", "a"), s.NewScopedToken("s_b", "b")
	rel := "backlog/0001.md"
	edit(t, s, root, rel, b, replace("status: todo", "status: blocked"))
	edit(t, s, root, rel, b, appendLine("- B note"))
	edit(t, s, root, rel, a, replace("status: blocked", "status: in_progress"))
	if got, _, _ := view(t, s, root, rel, "s_a"); got != strings.Replace(taskDoc, "todo", "in_progress", 1) {
		t.Fatalf("A view:\n%s", got)
	}
	if got, _, _ := view(t, s, root, rel, "s_b"); got != strings.Replace(taskDoc, "todo", "blocked", 1)+"- B note\n" {
		t.Fatalf("B view:\n%s", got)
	}

	created := "backlog/0002.md"
	edit(t, s, root, created, b, func(string) string { return taskDoc })
	edit(t, s, root, created, a, appendLine("- A note"))
	if got, _, _ := view(t, s, root, created, "s_a"); got != taskDoc+"- A note\n" {
		t.Fatalf("A view of B's new task:\n%s", got)
	}
	if got, _, _ := view(t, s, root, created, "s_b"); got != taskDoc {
		t.Fatalf("B view of its new task:\n%s", got)
	}
}

// A finished session's leftover lines no longer hold anything back, and line
// records survive a daemon restart.
func TestLineViewsTreatEndedSessionsAsUnattributedAndPersist(t *testing.T) {
	root := linesRepo(t, map[string]string{"memory.md": "# Memory\n"})
	s := NewService()
	s.PersistClaims()
	a, b := s.NewScopedToken("s_a", "a"), s.NewScopedToken("s_b", "b")
	edit(t, s, root, "memory.md", b, appendLine("- B note"))
	edit(t, s, root, "memory.md", a, appendLine("- A note"))

	restarted := NewService()
	restarted.PersistClaims()
	if got, _, _ := view(t, restarted, root, "memory.md", "s_a"); got != "# Memory\n- A note\n" {
		t.Fatalf("A view after restart:\n%s", got)
	}
	restarted.SetLiveScopes(func(scope string) bool { return scope != "s_b" })
	if got, _, _ := view(t, restarted, root, "memory.md", "s_a"); got != "# Memory\n- B note\n- A note\n" {
		t.Fatalf("A view once B ended:\n%s", got)
	}
}

// Folding a commit of one session's note keeps the other sessions' pending
// replacements of the same line intact.
func TestLineFoldKeepsPendingReplacementChain(t *testing.T) {
	const rel = "backlog/0001.md"
	root := linesRepo(t, map[string]string{rel: taskDoc})
	s := NewService()
	a, b, c := s.NewScopedToken("s_a", "a"), s.NewScopedToken("s_b", "b"), s.NewScopedToken("s_c", "c")
	edit(t, s, root, rel, a, replace("status: todo", "status: in_progress"))
	edit(t, s, root, rel, b, replace("status: in_progress", "status: blocked"))
	edit(t, s, root, rel, c, appendLine("- C note"))
	cText, _, _ := view(t, s, root, rel, "s_c")
	commitFile(t, root, rel, cText)
	for _, tt := range []struct{ scope, status string }{{"s_a", "in_progress"}, {"s_b", "blocked"}} {
		want := strings.Replace(cText, "status: todo", "status: "+tt.status, 1)
		if got, mine, ok := view(t, s, root, rel, tt.scope); !mine || !ok || got != want {
			t.Errorf("%s view = %q, want %q", tt.scope, got, want)
		}
	}
}

// One write changing a few lines of a large file attributes only those lines.
func TestLineViewOfLargeFileKeepsUnchangedLinesAttribution(t *testing.T) {
	const rel = "memory.md"
	original := "top\n" + strings.Repeat("unchanged\n", 2100) + "bottom\n"
	root := linesRepo(t, map[string]string{rel: original})
	s := NewService()
	a, b := s.NewScopedToken("s_a", "a"), s.NewScopedToken("s_b", "b")
	edit(t, s, root, rel, a, func(text string) string { return text[:len(text)/2] + "A note\n" + text[len(text)/2:] })
	edit(t, s, root, rel, b, func(text string) string {
		return strings.Replace(strings.Replace(text, "top\n", "B top\n", 1), "bottom\n", "B bottom\n", 1)
	})
	got, _, _ := view(t, s, root, rel, "s_b")
	if strings.Contains(got, "A note") || !strings.HasPrefix(got, "B top\n") || !strings.HasSuffix(got, "B bottom\n") {
		t.Fatalf("B view took A's note or lost its own lines")
	}
}

// Rewriting far more lines than the in-process diff budget around another
// session's untouched note still leaves that note its own.
func TestLineViewOfLargeRewriteKeepsUntouchedLines(t *testing.T) {
	const rel = "memory.md"
	original := strings.Repeat("old prefix\n", 1200) + "middle\n" + strings.Repeat("old suffix\n", 1200)
	root := linesRepo(t, map[string]string{rel: original})
	s := NewService()
	a, b := s.NewScopedToken("s_a", "a"), s.NewScopedToken("s_b", "b")
	edit(t, s, root, rel, a, replace("middle\n", "middle\nA note\n"))
	edit(t, s, root, rel, b, func(text string) string { return strings.ReplaceAll(text, "old ", "new ") })
	if got, _, _ := view(t, s, root, rel, "s_b"); strings.Contains(got, "A note") || strings.Contains(got, "old ") {
		t.Fatal("B view took A's note or lost its rewrite")
	}
}

// A user's diff configuration cannot widen the fallback diff's hunks.
func TestLargeRewriteIgnoresDiffConfiguration(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "diff.interHunkContext")
	t.Setenv("GIT_CONFIG_VALUE_0", "5")
	t.Setenv("GIT_CONFIG_KEY_1", "diff.algorithm")
	t.Setenv("GIT_CONFIG_VALUE_1", "histogram")
	TestLineViewOfLargeRewriteKeepsUntouchedLines(t)
}

// A line inserted under a heading another session added keeps that heading,
// but not the other session's own lines under it.
func TestLineViewKeepsTheSectionALineSitsUnder(t *testing.T) {
	const rel = "memory.md"
	root := linesRepo(t, map[string]string{rel: "# Memory\n\n## Lessons\n- old\n"})
	s := NewService()
	a, b := s.NewScopedToken("s_a", "a"), s.NewScopedToken("s_b", "b")
	edit(t, s, root, rel, a, appendLine("\n## Gotchas\n- A gotcha"))
	edit(t, s, root, rel, b, appendLine("- B gotcha"))
	if got, _, _ := view(t, s, root, rel, "s_b"); got != "# Memory\n\n## Lessons\n- old\n## Gotchas\n- B gotcha\n" {
		t.Fatalf("B view:\n%s", got)
	}
}

// A view of a HEAD that has since moved is refused rather than re-anchoring
// the record backwards.
func TestLineViewRefusesAStaleHead(t *testing.T) {
	const rel = "backlog/0001.md"
	root := linesRepo(t, map[string]string{rel: taskDoc})
	out, _ := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	stale := strings.TrimSpace(string(out))
	s := NewService()
	a, b := s.NewScopedToken("s_a", "a"), s.NewScopedToken("s_b", "b")
	edit(t, s, root, rel, a, replace("status: todo", "status: in_progress"))
	edit(t, s, root, rel, b, appendLine("- B note"))
	aText, _, _ := view(t, s, root, rel, "s_a")
	commitFile(t, root, rel, aText)
	if _, mine, ok := view(t, s, root, rel, "s_b"); !mine || !ok {
		t.Fatal("B lost its note after A's commit")
	}
	if _, _, _, _, err := s.LineView(root, stale, rel, "s_c", false); err == nil {
		t.Fatal("a stale view was not refused")
	}
	if _, mine, ok := view(t, s, root, rel, "s_b"); !mine || !ok {
		t.Fatal("a stale view erased B's attribution")
	}
}

// diffLines yields a shortest edit script: its hunks turn a into b and touch
// exactly the lines outside a longest common subsequence.
func TestDiffLinesIsAShortestEditScript(t *testing.T) {
	t.Run("myers", func(t *testing.T) { checkShortestEditScripts(t, 2000) })
	t.Run("git fallback", func(t *testing.T) {
		saved := maxDiffEdits
		maxDiffEdits = 0
		defer func() { maxDiffEdits = saved }()
		checkShortestEditScripts(t, 200)
	})
}

func checkShortestEditScripts(t *testing.T, iterations int) {
	rng := rand.New(rand.NewSource(1))
	gen := func() []string {
		out := make([]string, rng.Intn(12))
		for i := range out {
			out[i] = string(rune('a'+rng.Intn(4))) + "\n"
		}
		return out
	}
	for iter := 0; iter < iterations; iter++ {
		a, b := gen(), gen()
		var got []string
		edits, last := 0, 0
		hunks, ok := diffLines(a, b)
		if !ok {
			t.Fatalf("no diff for %q -> %q", a, b)
		}
		for _, h := range hunks {
			got = append(append(got, a[last:h.a0]...), b[h.b0:h.b1]...)
			edits += h.a1 - h.a0 + h.b1 - h.b0
			last = h.a1
		}
		got = append(got, a[last:]...)
		if strings.Join(got, "") != strings.Join(b, "") {
			t.Fatalf("hunks of %q -> %q produce %q", a, b, got)
		}
		lcs := make([][]int, len(a)+1)
		for i := range lcs {
			lcs[i] = make([]int, len(b)+1)
		}
		for i := len(a) - 1; i >= 0; i-- {
			for j := len(b) - 1; j >= 0; j-- {
				if a[i] == b[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else {
					lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
				}
			}
		}
		if want := len(a) + len(b) - 2*lcs[0][0]; edits != want {
			t.Fatalf("%q -> %q: %d edited lines, want %d", a, b, edits, want)
		}
	}
}

func TestDiffLinesFindsMinimalHunks(t *testing.T) {
	a := []string{"a\n", "b\n", "c\n", "d\n"}
	b := []string{"a\n", "x\n", "c\n", "d\n", "e\n"}
	got, _ := diffLines(a, b)
	want := []lineHunk{{1, 2, 1, 2}, {4, 4, 4, 5}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("hunks = %v, want %v", got, want)
	}
}
