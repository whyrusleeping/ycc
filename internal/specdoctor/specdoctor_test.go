package specdoctor

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree creates a workspace with the given relative-path -> content files.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func hasRef(refs []Ref, text string) bool {
	for _, r := range refs {
		if r.Text == text {
			return true
		}
	}
	return false
}

func TestCheckPathsAndDirs(t *testing.T) {
	root := writeTree(t, map[string]string{
		"internal/docs/spec.go": "package docs\n",
		"cmd/ycc/main.go":       "package main\n",
		"go.mod":                "module x\n",
	})
	spec := DocFile{
		Path: "spec.md",
		Content: "The `internal/docs` package holds `internal/docs/spec.go`, and the module file is `go.mod`.\n" +
			"But `internal/gone` was removed and `cmd/ycc/old.go` too.\n",
	}
	rep := Check(root, []DocFile{spec})

	if !hasRef(rep.Found, "internal/docs") || !hasRef(rep.Found, "internal/docs/spec.go") || !hasRef(rep.Found, "go.mod") {
		t.Fatalf("expected existing paths/dirs to resolve; found=%v", rep.Found)
	}
	// internal/gone: parent internal/ exists → confidently a removed package.
	// cmd/ycc/old.go: parent cmd/ycc/ exists → confidently a removed file.
	if !hasRef(rep.Missing, "internal/gone") || !hasRef(rep.Missing, "cmd/ycc/old.go") {
		t.Fatalf("expected removed paths (with existing parents) to be flagged; missing=%v", rep.Missing)
	}
}

// Paths that do not resolve but are not confidently repo paths — external/runtime
// paths, illustrative example filenames, and paths whose parent dir is absent —
// must NOT be flagged (zero false positives).
func TestUnresolvablePathsNotFalselyFlagged(t *testing.T) {
	root := writeTree(t, map[string]string{"go.mod": "module x\n", "internal/x.go": "package x\n"})
	spec := DocFile{
		Path: "spec.md",
		Content: "Runtime state lives at `~/.local/state/ycc/state.json` and endpoints like `/v1/models`. " +
			"An example config `.ycc/config.toml`, a bare `ARCHITECTURE.md`, a file-type `.proto`, " +
			"an example task `backlog/0007-foo.md`, and a nonexistent tree `nowhere/deep/x.go`.\n",
	}
	rep := Check(root, []DocFile{spec})
	for _, bad := range []string{"~/.local/state/ycc/state.json", "/v1/models", ".ycc/config.toml", "ARCHITECTURE.md", ".proto", "backlog/0007-foo.md", "nowhere/deep/x.go"} {
		if hasRef(rep.Missing, bad) {
			t.Fatalf("unresolvable non-repo path %q should not be flagged", bad)
		}
	}
}

func TestCheckSymbols(t *testing.T) {
	root := writeTree(t, map[string]string{
		"internal/orchestrator/modes.go": "package orchestrator\nfunc BuildMode() {}\nvar doc_updated = 1\n",
		"internal/docs/spec.go":          "package docs\nfunc (s *Store) SpecPath() string { return \"\" }\n",
	})
	spec := DocFile{
		Path: "spec.md",
		Content: "The mode is built by `BuildMode`, which reads `Store.SpecPath` and fires `doc_updated`.\n" +
			"The old `RenderEverything` helper and `legacy_field` are gone.\n",
	}
	rep := Check(root, []DocFile{spec})

	if !hasRef(rep.Found, "BuildMode") || !hasRef(rep.Found, "Store.SpecPath") || !hasRef(rep.Found, "doc_updated") {
		t.Fatalf("expected existing symbols to resolve; found=%v", rep.Found)
	}
	if !hasRef(rep.Missing, "RenderEverything") || !hasRef(rep.Missing, "legacy_field") {
		t.Fatalf("expected stale symbols to be flagged; missing=%v", rep.Missing)
	}
}

func TestFencedBlocksSkipped(t *testing.T) {
	root := writeTree(t, map[string]string{"real.go": "package x\n"})
	spec := DocFile{
		Path: "spec.md",
		Content: "Prose mentions `MissingThing` inline.\n" +
			"```go\n// this block references NonExistentSymbol and internal/nope/x.go\nvar NonExistentSymbol int\n```\n" +
			"Back to prose.\n",
	}
	rep := Check(root, []DocFile{spec})

	// The inline reference is checked...
	if !hasRef(rep.Missing, "MissingThing") {
		t.Fatalf("inline reference should be checked; missing=%v", rep.Missing)
	}
	// ...but nothing from inside the fenced block is.
	for _, r := range append(append([]Ref{}, rep.Found...), rep.Missing...) {
		if r.Text == "NonExistentSymbol" || r.Text == "internal/nope/x.go" {
			t.Fatalf("fenced-block content should be skipped, saw %q", r.Text)
		}
	}
}

func TestAmbiguousAndGlobsSkipped(t *testing.T) {
	root := writeTree(t, map[string]string{"x.go": "package x\n"})
	spec := DocFile{
		Path: "spec.md",
		Content: "Run `ycc daemon` to start. Patterns like `docs/**` and `*.go` are globs. " +
			"Plain words like `task` and `spec` and acronyms like `RPC` are prose. " +
			"The symbol list `ListPlans/ReadPlan/SavePlan` is not a path.\n",
	}
	rep := Check(root, []DocFile{spec})

	all := append(append([]Ref{}, rep.Found...), rep.Missing...)
	for _, r := range all {
		for _, bad := range []string{"ycc daemon", "docs/**", "*.go", "task", "spec", "RPC", "ListPlans/ReadPlan/SavePlan"} {
			if r.Text == bad {
				t.Fatalf("ambiguous/glob span %q should have been skipped", bad)
			}
		}
	}
}

func TestSelfMentionNotResolved(t *testing.T) {
	// A symbol mentioned ONLY in the docs must not resolve against its own mention.
	root := writeTree(t, map[string]string{
		"real.go": "package x\n",
	})
	spec := DocFile{
		Path:    "spec.md",
		Content: "This spec introduces the concept `OnlyInSpecSymbol` which appears nowhere in code.\n",
	}
	rep := Check(root, []DocFile{spec})
	if !hasRef(rep.Missing, "OnlyInSpecSymbol") {
		t.Fatalf("symbol only in the docs should be flagged stale; missing=%v", rep.Missing)
	}
}

func TestBacklogExcludedFromSymbolSearch(t *testing.T) {
	root := writeTree(t, map[string]string{
		"real.go":             "package x\n",
		"backlog/0001-foo.md": "This task mentions GhostSymbol repeatedly.\n",
	})
	spec := DocFile{Path: "spec.md", Content: "The `GhostSymbol` type does the work.\n"}
	rep := Check(root, []DocFile{spec})
	if !hasRef(rep.Missing, "GhostSymbol") {
		t.Fatalf("backlog mention should not resolve a spec symbol; missing=%v", rep.Missing)
	}
}

func TestMarkdownCleanAndDirty(t *testing.T) {
	root := writeTree(t, map[string]string{"go.mod": "module x\n", "internal/x.go": "package x\n"})

	clean := Check(root, []DocFile{{Path: "spec.md", Content: "See `go.mod`.\n"}})
	if out := clean.Markdown(); !contains(out, "resolve") {
		t.Fatalf("clean report should say references resolve:\n%s", out)
	}

	dirty := Check(root, []DocFile{{Path: "spec.md", Content: "See `internal/gone.md`.\n"}})
	out := dirty.Markdown()
	if !contains(out, "Stale references") || !contains(out, "internal/gone.md") || !contains(out, "line 1") {
		t.Fatalf("dirty report should list the stale ref with its line:\n%s", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
