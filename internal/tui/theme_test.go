package tui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

// TestApplyThemeLive verifies switching themes updates the active palette, keeps
// chromaStyle non-nil, and is reflected by actorStyle. Resets to dark at the end.
func TestApplyThemeLive(t *testing.T) {
	defer applyTheme(darkTheme)

	applyTheme(lightTheme)
	if chromaStyle == nil {
		t.Fatal("chromaStyle nil after applyTheme(lightTheme)")
	}
	if got := actorStyle("coordinator").GetForeground(); got != lipgloss.Color(lightTheme.actorCoord) {
		t.Errorf("coordinator color = %v, want %v", got, lightTheme.actorCoord)
	}

	applyTheme(darkTheme)
	if chromaStyle == nil {
		t.Fatal("chromaStyle nil after applyTheme(darkTheme)")
	}
	if got := actorStyle("coordinator").GetForeground(); got != lipgloss.Color(darkTheme.actorCoord) {
		t.Errorf("coordinator color = %v, want %v", got, darkTheme.actorCoord)
	}
}
