package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
)

func writeSessionLog(t *testing.T, ws, id string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(ws, ".ycc", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func projectNames(m *Manager) []string {
	var out []string
	for _, p := range m.ProjectsByRecency() {
		out = append(out, p.Name)
	}
	return out
}

func TestProjectsByRecency(t *testing.T) {
	m := NewManager(config.NewRegistry(&config.Config{}), "")
	defer m.ReclaimAll()
	old, recent, never, never2 := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for name, ws := range map[string]string{"a-old": old, "b-recent": recent, "c-never": never, "d-never": never2} {
		if _, err := m.AddProject(ws, name); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	writeSessionLog(t, old, "s1", now.Add(-48*time.Hour))
	writeSessionLog(t, recent, "s1", now.Add(-72*time.Hour))
	writeSessionLog(t, recent, "s2", now.Add(-time.Hour)) // newest log wins

	want := []string{"b-recent", "a-old", "c-never", "d-never"}
	if got := projectNames(m); !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}

	// A touch (user-started session / input) applies immediately, despite the
	// cached scan.
	m.TouchProjectWorkspace(never2)
	want = []string{"d-never", "b-recent", "a-old", "c-never"}
	if got := projectNames(m); !equalStrings(got, want) {
		t.Fatalf("order after touch = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
