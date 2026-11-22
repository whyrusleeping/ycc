package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The setting is off by default and toggling it edits only its own line, so
// the rest of a hand-written config (comments included) survives.
func TestSetSessionCoordinationKeepsTheRestOfTheConfig(t *testing.T) {
	root := t.TempDir()
	if SessionCoordination(root) {
		t.Fatal("setting is on without a config file")
	}
	path := filepath.Join(root, ".ycc", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "# my project\nspec_path = \"docs/spec.md\"\n\n[backlog]\nauto_groom = false # keep quiet\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{true, false, true} {
		if err := SetSessionCoordination(root, on); err != nil {
			t.Fatal(err)
		}
		if got := SessionCoordination(root); got != on {
			t.Fatalf("after set %v read %v", on, got)
		}
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.HasPrefix(text, original) || strings.Count(text, "session_coordination") != 1 {
		t.Fatalf("config after toggling:\n%s", text)
	}
	if s := NewStore(root); !strings.HasSuffix(s.SpecPath(), "docs/spec.md") || s.BacklogAutoGroom() {
		t.Fatalf("other settings changed: spec=%q autoGroom=%v", s.SpecPath(), s.BacklogAutoGroom())
	}

	// An existing [experimental] table gets the key under its header.
	other := t.TempDir()
	otherPath := filepath.Join(other, ".ycc", "config.toml")
	_ = os.MkdirAll(filepath.Dir(otherPath), 0o755)
	if err := os.WriteFile(otherPath, []byte("[experimental]\nother = 1\n\n[chat]\nfinished_work = \"ask\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetSessionCoordination(other, true); err != nil || !SessionCoordination(other) {
		t.Fatalf("set in existing table: err=%v on=%v", err, SessionCoordination(other))
	}

	// Quoted table names and dotted keys are the same setting; an inline
	// comment on the line is kept.
	for _, doc := range []string{
		"[\"experimental\"]\nsession_coordination = true # local opt-in\n",
		"experimental.session_coordination = true\n[chat]\nfinished_work = \"ask\"\n",
		"[experimental] # controls [experimental features]\nsession_coordination = true\n",
		"notes = '''\n[experimental]\nsession_coordination = false\n'''\n\n[experimental]\nsession_coordination = true\n",
		"# multiline strings use \"\"\" or '''\n[experimental]\nsession_coordination = true\n",
		"experimental = { tags = [\n  \"local\", # why\n], session_coordination = true }\n",
		"[experimental]\nnote = \"\"\"\nsession_coordination = false \\\"\"\"\"\"\nsession_coordination = true\n",
		"experimental = { other = 1, session_coordination = true }\n",
		"metadata = { created = 1979-05-27 07:32:00Z }\n[experimental]\nsession_coordination = true\n",
		"experimental = { \"session_coordination\" = true } # note\n",
		"experimental = { note = \"session_coordination = false, {x}\", session_coordination = true } # session_coordination = false\n",
	} {
		if err := os.WriteFile(otherPath, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		if !SessionCoordination(other) {
			t.Fatalf("not read as on:\n%s", doc)
		}
		if err := SetSessionCoordination(other, false); err != nil || SessionCoordination(other) {
			t.Fatalf("not turned off (err=%v):\n%s", err, doc)
		}
		data, _ := os.ReadFile(otherPath)
		if _, comment, ok := strings.Cut(doc, "#"); ok && !strings.Contains(string(data), "#"+strings.SplitN(comment, "\n", 2)[0]) {
			t.Fatalf("inline comment lost:\n%s", data)
		}
	}

	if err := os.WriteFile(otherPath, []byte("experimental = { other = 1 } # session_coordination = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetSessionCoordination(other, true); err != nil || !SessionCoordination(other) {
		t.Fatalf("not added to an inline table: %v", err)
	}

	// Malformed TOML is refused, not rewritten.
	if err := os.WriteFile(otherPath, []byte("[broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetSessionCoordination(other, true); err == nil {
		t.Fatal("malformed config was rewritten")
	}
}
