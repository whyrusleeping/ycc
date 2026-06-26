package docs

import (
	"strings"
	"testing"
)

func TestUpdateSpecSectionAppendThenReplace(t *testing.T) {
	s := NewStore(t.TempDir())

	// Append into a fresh spec.
	if err := s.UpdateSpecSection("Architecture", "Daemon + clients."); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSpecSection("Goals", "Be useful."); err != nil {
		t.Fatal(err)
	}
	body, _ := s.ReadSpec()
	if !strings.Contains(body, "## Architecture") || !strings.Contains(body, "Daemon + clients.") {
		t.Fatalf("missing Architecture:\n%s", body)
	}
	if !strings.Contains(body, "## Goals") {
		t.Fatalf("missing Goals:\n%s", body)
	}

	// Replace an existing section without disturbing the other.
	if err := s.UpdateSpecSection("Architecture", "Now with a TUI."); err != nil {
		t.Fatal(err)
	}
	body, _ = s.ReadSpec()
	if strings.Contains(body, "Daemon + clients.") {
		t.Fatalf("old Architecture body not replaced:\n%s", body)
	}
	if !strings.Contains(body, "Now with a TUI.") || !strings.Contains(body, "Be useful.") {
		t.Fatalf("replace clobbered content:\n%s", body)
	}

	secs, _ := s.SpecSections()
	if len(secs) != 2 || secs[0] != "Architecture" || secs[1] != "Goals" {
		t.Fatalf("sections = %v", secs)
	}
}
