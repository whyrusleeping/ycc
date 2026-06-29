package tui

import "testing"

// TestThemesDiffer ensures the light and dark palettes are actually distinct so
// the settings toggle has a visible effect.
func TestThemesDiffer(t *testing.T) {
	if lightTheme.chromaStyleName == darkTheme.chromaStyleName {
		t.Errorf("light/dark chroma style names should differ: %q", lightTheme.chromaStyleName)
	}
	if lightTheme.glamourStyle == darkTheme.glamourStyle {
		t.Errorf("light/dark glamour styles should differ: %q", lightTheme.glamourStyle)
	}
	if lightTheme.dim == darkTheme.dim && lightTheme.actorCoord == darkTheme.actorCoord {
		t.Errorf("light/dark palettes look identical")
	}
}

// TestApplyThemeLive verifies switching themes updates the active palette, keeps
// chromaStyle non-nil, and is reflected by actorStyle. Resets to dark at the end.
func TestApplyThemeLive(t *testing.T) {
	defer applyTheme(darkTheme)

	applyTheme(lightTheme)
	if chromaStyle == nil {
		t.Fatal("chromaStyle nil after applyTheme(lightTheme)")
	}
	if got := actorStyle("coordinator").GetForeground(); got != lightTheme.actorCoord {
		t.Errorf("coordinator color = %v, want %v", got, lightTheme.actorCoord)
	}

	applyTheme(darkTheme)
	if chromaStyle == nil {
		t.Fatal("chromaStyle nil after applyTheme(darkTheme)")
	}
	if got := actorStyle("coordinator").GetForeground(); got != darkTheme.actorCoord {
		t.Errorf("coordinator color = %v, want %v", got, darkTheme.actorCoord)
	}
}
