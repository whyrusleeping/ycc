package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendMemoryCreatesFileWithHeader(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)

	if got, err := s.ReadMemory(); err != nil || got != "" {
		t.Fatalf("ReadMemory on absent file = %q, %v; want \"\", nil", got, err)
	}

	if err := s.AppendMemory("go test ./internal/tui is slow", "environment"); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}
	body, err := s.ReadMemory()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "# Project memory") {
		t.Fatalf("memory missing title header:\n%s", body)
	}
	if !strings.Contains(body, "Advisory, not normative") {
		t.Fatalf("memory missing advisory header:\n%s", body)
	}
	if !strings.Contains(body, "## Environment & tooling") {
		t.Fatalf("memory missing category section:\n%s", body)
	}
	today := time.Now().Format("2006-01-02")
	if !strings.Contains(body, "- "+today+": go test ./internal/tui is slow") {
		t.Fatalf("memory missing dated bullet:\n%s", body)
	}
}

func TestAppendMemoryDefaultCategoryAndUnknown(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)

	// Empty category defaults to lesson.
	if err := s.AppendMemory("tried X, failed", ""); err != nil {
		t.Fatalf("AppendMemory default: %v", err)
	}
	body, _ := s.ReadMemory()
	if !strings.Contains(body, "## Lessons learned") {
		t.Fatalf("default category should be Lessons learned:\n%s", body)
	}

	// Unknown category is an error.
	if err := s.AppendMemory("something", "bogus"); err == nil {
		t.Fatalf("expected error for unknown category")
	}

	// Empty note is rejected.
	if err := s.AppendMemory("   ", "lesson"); err == nil {
		t.Fatalf("expected error for empty note")
	}
}

func TestAppendMemoryAppendsToExistingSection(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)

	if err := s.AppendMemory("first gotcha", "gotcha"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMemory("second gotcha", "gotcha"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMemory("a preference", "preference"); err != nil {
		t.Fatal(err)
	}
	body, _ := s.ReadMemory()
	if strings.Count(body, "## Codebase gotchas") != 1 {
		t.Fatalf("gotcha section should exist exactly once:\n%s", body)
	}
	if !strings.Contains(body, "first gotcha") || !strings.Contains(body, "second gotcha") {
		t.Fatalf("both gotchas should be present:\n%s", body)
	}
	// second gotcha comes after first (appended below within the section).
	if strings.Index(body, "first gotcha") > strings.Index(body, "second gotcha") {
		t.Fatalf("entries out of order:\n%s", body)
	}
	if !strings.Contains(body, "## User preferences") {
		t.Fatalf("new section not created:\n%s", body)
	}
}

func TestAppendMemoryBudgetRefusal(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)
	// Pre-fill the file over budget.
	big := "# Project memory\n\n## Lessons learned\n" + strings.Repeat("- 2020-01-01: filler line\n", 300)
	if len(big) < memoryBudget {
		t.Fatalf("test fixture too small: %d", len(big))
	}
	if err := os.WriteFile(s.MemoryPath(), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	err := s.AppendMemory("one more", "lesson")
	if err == nil {
		t.Fatalf("expected budget refusal")
	}
	if !strings.Contains(err.Error(), "consolidate") {
		t.Fatalf("budget error should mention consolidating: %v", err)
	}
}

func TestIsMemoryAndMemoryPath(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)
	want := filepath.Join(ws, "memory.md")
	if s.MemoryPath() != want {
		t.Fatalf("MemoryPath = %q, want %q", s.MemoryPath(), want)
	}
	if !s.IsMemory(want) {
		t.Fatalf("IsMemory should be true for %q", want)
	}
	if s.IsMemory(filepath.Join(ws, "spec.md")) {
		t.Fatalf("IsMemory should be false for spec.md")
	}
}

// DocFiles must exclude memory.md even when a doc_glob would otherwise match it,
// so the spec doctor / spec-check never scans agent memory as spec.
func TestDocFilesExcludesMemory(t *testing.T) {
	ws := t.TempDir()
	// Configure a broad doc_glob that would match memory.md.
	if err := os.MkdirAll(filepath.Join(ws, ".ycc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".ycc", "config.toml"), []byte("doc_globs = [\"*.md\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "spec.md"), []byte("# Spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "memory.md"), []byte("# Project memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(ws)
	files, err := s.DocFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if s.IsMemory(f) {
			t.Fatalf("DocFiles must not include memory.md; got %v", files)
		}
	}
	// Sanity: spec.md is still included.
	var sawSpec bool
	for _, f := range files {
		if f == filepath.Join(ws, "spec.md") {
			sawSpec = true
		}
	}
	if !sawSpec {
		t.Fatalf("DocFiles should include spec.md; got %v", files)
	}
}
