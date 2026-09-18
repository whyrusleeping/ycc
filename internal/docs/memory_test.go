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

	if _, err := s.AppendMemory("go test ./internal/tui is slow", "environment"); err != nil {
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
	if !strings.Contains(body, "- "+today+" [model inference] go test ./internal/tui is slow") {
		t.Fatalf("memory missing typed dated bullet:\n%s", body)
	}
	if !strings.Contains(body, "scope=workspace") || !strings.Contains(body, "classified=model") {
		t.Fatalf("memory missing automatic scope/classifier metadata:\n%s", body)
	}
}

func TestAppendMemoryDefaultCategoryAndUnknown(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)

	// Empty category defaults to lesson.
	if _, err := s.AppendMemory("tried X, failed", ""); err != nil {
		t.Fatalf("AppendMemory default: %v", err)
	}
	body, _ := s.ReadMemory()
	if !strings.Contains(body, "## Lessons learned") {
		t.Fatalf("default category should be Lessons learned:\n%s", body)
	}

	// Unknown category is an error.
	if _, err := s.AppendMemory("something", "bogus"); err == nil {
		t.Fatalf("expected error for unknown category")
	}

	// Empty note is rejected.
	if _, err := s.AppendMemory("   ", "lesson"); err == nil {
		t.Fatalf("expected error for empty note")
	}
}

func TestAppendMemoryAppendsToExistingSection(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)

	if _, err := s.AppendMemory("first gotcha", "gotcha"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMemory("second gotcha", "gotcha"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMemory("a preference", "preference"); err != nil {
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

// Crossing the active-memory soft budget must not block a write: the note is
// still recorded and AppendMemory returns a grooming nudge. Ordinary growth is
// refused only once active prompt memory has reached the hard ceiling.
func TestAppendMemorySoftBudgetNudgeThenHardRefusal(t *testing.T) {
	s := NewStore(t.TempDir())

	overSoft := "# Project memory\n\n## Lessons learned\n" + strings.Repeat("- 2020-01-01: filler line\n", 75)
	softSize := len(RenderMemoryForPrompt(overSoft))
	if softSize < memorySoftBudget || softSize >= memoryHardBudget {
		t.Fatalf("active fixture size %d must be in [soft=%d, hard=%d)", softSize, memorySoftBudget, memoryHardBudget)
	}
	if err := os.WriteFile(s.MemoryPath(), []byte(overSoft), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := s.AppendMemory("one more", "lesson")
	if err != nil {
		t.Fatalf("over-soft-budget write must succeed, got: %v", err)
	}
	if !strings.Contains(res.Advice, "active prompt memory") || !strings.Contains(res.Advice, "soft budget") {
		t.Fatalf("expected an active-memory grooming nudge, got advice=%q", res.Advice)
	}
	body, _ := s.ReadMemory()
	if !strings.Contains(body, "one more") {
		t.Fatalf("note must be recorded even over budget:\n%s", body)
	}

	overHard := "# Project memory\n\n## Lessons learned\n" + strings.Repeat("- 2020-01-01: filler line\n", 250)
	if activeSize := len(RenderMemoryForPrompt(overHard)); activeSize < memoryHardBudget {
		t.Fatalf("active fixture too small for hard ceiling: %d", activeSize)
	}
	if err := os.WriteFile(s.MemoryPath(), []byte(overHard), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMemory("blocked", "lesson"); err == nil {
		t.Fatalf("expected active hard-ceiling refusal")
	} else if !strings.Contains(err.Error(), "active prompt memory") || !strings.Contains(err.Error(), "consolidate") {
		t.Fatalf("hard-ceiling error should explain active-memory consolidation: %v", err)
	}
}

func TestAppendMemoryAllowsRawAuditOverCeilingWhenActiveMemoryIsSmall(t *testing.T) {
	s := NewStore(t.TempDir())
	when := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	old, err := s.AppendMemoryEntry(MemoryEntry{
		Note: strings.Repeat("obsolete audit evidence ", 700), Kind: MemoryInference,
		Provenance: MemoryProvenance{EventTime: when},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMemoryEntry(MemoryEntry{
		Note: "Concise replacement", Kind: MemoryInference, Supersedes: []string{old.ID},
		Provenance: MemoryProvenance{EventTime: when.Add(time.Second)},
	}); err != nil {
		t.Fatalf("reduce oversized active memory: %v", err)
	}
	body, _ := s.ReadMemory()
	if len(body) < memoryHardBudget {
		t.Fatalf("raw audit fixture must exceed hard ceiling: %d", len(body))
	}
	if activeSize := len(RenderMemoryForPrompt(body)); activeSize >= memorySoftBudget {
		t.Fatalf("active memory should be small despite retained raw audit: %d", activeSize)
	}

	if _, err := s.AppendMemory("a further active note", "lesson"); err != nil {
		t.Fatalf("raw audit size must not block a write when active memory is small: %v", err)
	}
}

func TestOversizedActiveMemoryCanRecoverThroughReducingSupersession(t *testing.T) {
	s := NewStore(t.TempDir())
	when := time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC)
	old, err := s.AppendMemoryEntry(MemoryEntry{
		Note: strings.Repeat("oversized active note ", 700), Kind: MemoryInference,
		Provenance: MemoryProvenance{EventTime: when},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.ReadMemory()
	beforeActive := len(RenderMemoryForPrompt(before))
	if beforeActive < memoryHardBudget {
		t.Fatalf("active fixture too small for hard ceiling: %d", beforeActive)
	}
	if !strings.Contains(old.Advice, "hard ceiling") || !strings.Contains(old.Advice, "shorter replacements") {
		t.Fatalf("oversized write advice must explain the reducing recovery path: %q", old.Advice)
	}
	if _, err := s.AppendMemory("ordinary growth", "lesson"); err == nil {
		t.Fatal("ordinary growth should be refused while active memory is over the hard ceiling")
	}

	if _, err := s.AppendMemoryEntry(MemoryEntry{
		Note: "Short consolidated note", Kind: MemoryInference, Supersedes: []string{old.ID},
		Provenance: MemoryProvenance{EventTime: when.Add(time.Second)},
	}); err != nil {
		t.Fatalf("reducing supersession must remain permitted: %v", err)
	}
	after, _ := s.ReadMemory()
	if !strings.Contains(after, "oversized active note") {
		t.Fatal("reducing supersession deleted raw audit evidence")
	}
	if afterActive := len(RenderMemoryForPrompt(after)); afterActive >= beforeActive {
		t.Fatalf("supersession did not reduce active memory: before=%d after=%d", beforeActive, afterActive)
	}
}

// A long note is still recorded, but AppendMemory flags it as too verbose.
func TestAppendMemoryLongEntryNudge(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)
	long := strings.Repeat("x", memoryEntryHint+50)
	res, err := s.AppendMemory(long, "lesson")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Advice, "long") {
		t.Fatalf("expected a terseness nudge, got advice=%q", res.Advice)
	}
	body, _ := s.ReadMemory()
	if !strings.Contains(body, long) {
		t.Fatalf("long note should still be recorded verbatim")
	}
}

func TestMemoryRenderDistinguishesObservationFromUserThreshold(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)
	measuredAt := time.Date(2026, 8, 28, 14, 0, 0, 0, time.UTC)
	if _, err := s.AppendMemoryEntry(MemoryEntry{
		Note:     "one benchmark run varied by 13 percent",
		Category: "lesson",
		Kind:     MemoryObservation,
		Provenance: MemoryProvenance{
			SessionID: "s_benchmark", EventSeq: 120, EventTime: measuredAt, Actor: "coordinator",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMemoryEntry(MemoryEntry{
		Note:     "fail deployment when error rate exceeds 2 percent",
		Category: "preference",
		Kind:     MemoryUserGuidance,
		Provenance: MemoryProvenance{
			SessionID: "s_benchmark", EventSeq: 135, EventTime: measuredAt.Add(time.Hour), Actor: "user",
		},
	}); err != nil {
		t.Fatal(err)
	}

	body, _ := s.ReadMemory()
	rendered := RenderMemoryForPrompt(body)
	for _, want := range []string{
		"[measured observation; model-classified, not verified authority] one benchmark run varied by 13 percent",
		"candidate evidence: session s_benchmark event #120",
		"[user-stated guidance; model-classified, not verified authority] fail deployment when error rate exceeds 2 percent",
		"candidate evidence: session s_benchmark event #135 (user)",
		"scope workspace",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered memory missing %q:\n%s", want, rendered)
		}
	}
}

func TestMemoryCorrectionSupersedesWithoutDeletingAudit(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)
	when := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	invented, err := s.AppendMemoryEntry(MemoryEntry{
		Note: "Treat plus or minus 13 percent as the accepted benchmark variance rule", Kind: MemoryProposedPolicy,
		Provenance: MemoryProvenance{SessionID: "s_vals", EventSeq: 690, EventTime: when, Actor: "coordinator"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AppendMemoryEntry(MemoryEntry{
		Note:       "Benchmark thresholds must be explicitly user-stated; one noisy run is only an observation",
		Kind:       MemoryUserGuidance,
		Provenance: MemoryProvenance{SessionID: "s_vals", EventSeq: 694, EventTime: when.Add(time.Minute), Actor: "user"},
		Supersedes: []string{invented.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	body, _ := s.ReadMemory()
	if !strings.Contains(body, "accepted benchmark variance rule") || !strings.Contains(body, "supersedes="+invented.ID) {
		t.Fatalf("raw memory must retain the contradicted audit record and correction link:\n%s", body)
	}
	freshPromptMemory := RenderMemoryForPrompt(body)
	if strings.Contains(freshPromptMemory, "accepted benchmark variance rule") {
		t.Fatalf("fresh prompt resurrected superseded policy:\n%s", freshPromptMemory)
	}
	if !strings.Contains(freshPromptMemory, "Benchmark thresholds must be explicitly user-stated") {
		t.Fatalf("fresh prompt missing active correction:\n%s", freshPromptMemory)
	}
}

func TestCorrectionCanSupersedeLegacyUnverifiedNote(t *testing.T) {
	s := NewStore(t.TempDir())
	legacy := "# Project memory\n\n## User preferences\n- 2026-08-28: Allow 13 percent benchmark variance\n"
	if err := os.WriteFile(s.MemoryPath(), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed := parseMemory(legacy)
	if len(parsed.entries) != 1 || !parsed.entries[0].legacy {
		t.Fatalf("legacy fixture did not parse: %+v", parsed.entries)
	}
	_, err := s.AppendMemoryEntry(MemoryEntry{
		Note: "That percentage was one measurement, not a user threshold", Kind: MemoryUserGuidance,
		Provenance: MemoryProvenance{SessionID: "s_vals", EventSeq: 694, EventTime: time.Now(), Actor: "user"},
		Supersedes: []string{parsed.entries[0].id},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := s.ReadMemory()
	if !strings.Contains(body, "Allow 13 percent benchmark variance") {
		t.Fatalf("legacy audit evidence was deleted:\n%s", body)
	}
	if rendered := RenderMemoryForPrompt(body); strings.Contains(rendered, "Allow 13 percent benchmark variance") {
		t.Fatalf("superseded legacy rule entered fresh prompt:\n%s", rendered)
	}
}

func TestUserGuidanceWithoutUserEventIsDowngraded(t *testing.T) {
	s := NewStore(t.TempDir())
	res, err := s.AppendMemoryEntry(MemoryEntry{
		Note: "delete retained data whenever useful", Kind: MemoryUserGuidance,
		Provenance: MemoryProvenance{SessionID: "s_x", EventSeq: 9, Actor: "coordinator"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != MemoryInference || !strings.Contains(res.Advice, "recorded conservatively") {
		t.Fatalf("unsupported user authority was not downgraded: %+v", res)
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
