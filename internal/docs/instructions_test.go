package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadProjectInstructionsPrecedence(t *testing.T) {
	ws := t.TempDir()
	if _, ok := LoadProjectInstructions(ws); ok {
		t.Fatal("no instruction file should load nothing")
	}
	writeFile(t, filepath.Join(ws, "CLAUDE.md"), "claude rules\n")
	if pi, ok := LoadProjectInstructions(ws); !ok || pi.Name != "CLAUDE.md" || pi.Content != "claude rules\n" {
		t.Fatalf("CLAUDE.md fallback: %+v %v", pi, ok)
	}
	// An empty AGENTS.md yields nothing, so the fallback still applies.
	writeFile(t, filepath.Join(ws, "AGENTS.md"), " \n")
	if pi, ok := LoadProjectInstructions(ws); !ok || pi.Name != "CLAUDE.md" {
		t.Fatalf("empty AGENTS.md should fall back: %+v %v", pi, ok)
	}
	writeFile(t, filepath.Join(ws, "AGENTS.md"), "agents rules\n")
	if pi, ok := LoadProjectInstructions(ws); !ok || pi.Name != "AGENTS.md" || pi.Content != "agents rules\n" || pi.Truncated {
		t.Fatalf("AGENTS.md should win: %+v %v", pi, ok)
	}
}

func TestLoadProjectInstructionsIgnoresEscapesAndNonRegular(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	writeFile(t, secret, "outside the workspace\n")
	if err := os.Symlink(secret, filepath.Join(ws, "AGENTS.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(ws, "CLAUDE.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if pi, ok := LoadProjectInstructions(ws); ok {
		t.Fatalf("escaping symlink and directory must be ignored: %+v", pi)
	}

	// A symlink that stays inside the workspace is followed.
	ws2 := t.TempDir()
	writeFile(t, filepath.Join(ws2, "CLAUDE.md"), "shared rules\n")
	if err := os.Symlink("CLAUDE.md", filepath.Join(ws2, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if pi, ok := LoadProjectInstructions(ws2); !ok || pi.Name != "AGENTS.md" || pi.Content != "shared rules\n" {
		t.Fatalf("in-workspace symlink: %+v %v", pi, ok)
	}
}

func TestLoadProjectInstructionsTruncatesOnRuneBoundary(t *testing.T) {
	ws := t.TempDir()
	// A 3-byte rune straddles the cap.
	body := strings.Repeat("a", MaxProjectInstructions-1) + "€" + strings.Repeat("b", 100)
	writeFile(t, filepath.Join(ws, "AGENTS.md"), body)
	pi, ok := LoadProjectInstructions(ws)
	if !ok || !pi.Truncated || pi.Size != int64(len(body)) {
		t.Fatalf("expected truncation: %+v %v", pi.Truncated, ok)
	}
	if !utf8.ValidString(pi.Content) || !strings.HasPrefix(pi.Content, strings.Repeat("a", MaxProjectInstructions-1)+"\n…[truncated: AGENTS.md is") {
		t.Fatalf("truncated content must be valid UTF-8, end before the straddling rune, and carry a marker: %q", pi.Content[len(pi.Content)-80:])
	}
}
