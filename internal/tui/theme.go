// Centralized TUI color palette (task 0060). Every color used by the renderer
// lives here as a named semantic role, selected per the user's explicit dark/light
// theme preference.
//
// We deliberately use explicit per-theme palettes selected by the pref rather than
// lipgloss.AdaptiveColor: AdaptiveColor keys off the *actual* terminal background
// (which it detects by querying the terminal), so it would NOT honor the user's
// explicit dark/light toggle, and it reintroduces the terminal-background querying
// that makeRenderer() deliberately avoids (it can block Bubble Tea's event loop).
package tui

import "github.com/charmbracelet/lipgloss"

// theme holds every color role the TUI renders with. Colors are plain
// lipgloss.Color (ANSI-256) values; attributes (bold/italic/padding) are applied
// in applyTheme so the per-theme palette stays purely about color.
type theme struct {
	titleFg  lipgloss.Color
	titleBg  lipgloss.Color
	headerFg lipgloss.Color
	headerBg lipgloss.Color

	sel    lipgloss.Color // selection accent
	reco   lipgloss.Color // recommendation highlight
	selBar lipgloss.Color // selection bar
	dim    lipgloss.Color // dimmed/secondary text
	think  lipgloss.Color // thinking (italic) text
	typ    lipgloss.Color // type/label text
	askFg  lipgloss.Color
	askBg  lipgloss.Color
	err    lipgloss.Color

	// Tool-call "card" roles (task: LSP-style cards). border is the resting card
	// outline; borderSel is the outline of the currently-selected card; success is
	// the green glyph/accent for a completed tool call; path tints file paths in
	// structured tool output.
	border    lipgloss.Color
	borderSel lipgloss.Color
	success   lipgloss.Color
	path      lipgloss.Color

	diffAdd  lipgloss.Color
	diffDel  lipgloss.Color
	diffHunk lipgloss.Color
	diffMeta lipgloss.Color

	actorCoord    lipgloss.Color
	actorImpl     lipgloss.Color
	actorReviewer lipgloss.Color
	actorUser     lipgloss.Color

	// chromaStyleName names the chroma syntax-highlight style for this theme.
	chromaStyleName string
	// glamourStyle names the glamour standard markdown style for this theme.
	glamourStyle string
}

// darkTheme reproduces the historical hardcoded dark palette exactly, so the dark
// appearance is unchanged from before centralization.
var darkTheme = theme{
	titleFg:  "15",
	titleBg:  "63",
	headerFg: "15",
	headerBg: "238",

	sel:    "213",
	reco:   "220",
	selBar: "213",
	dim:    "245",
	think:  "245",
	typ:    "250",
	askFg:  "0",
	askBg:  "11",
	err:    "203",

	border:    "238",
	borderSel: "75",
	success:   "78",
	path:      "75",

	diffAdd:  "42",
	diffDel:  "203",
	diffHunk: "44",
	diffMeta: "250",

	actorCoord:    "44",
	actorImpl:     "42",
	actorReviewer: "170",
	actorUser:     "39",

	chromaStyleName: "monokai",
	glamourStyle:    "dark",
}

// lightTheme is a legible palette for light terminal backgrounds: darker text and
// accents that read on white, deeper diff greens/reds, and the github/light syntax
// + markdown styles.
var lightTheme = theme{
	titleFg:  "15", // white text on a saturated blue title bar
	titleBg:  "25",
	headerFg: "15",
	headerBg: "238",

	sel:    "92",  // purple, readable on white
	reco:   "166", // amber/orange
	selBar: "92",
	dim:    "240", // dark gray
	think:  "240",
	typ:    "238",
	askFg:  "0",
	askBg:  "11",
	err:    "124", // deep red

	border:    "250",
	borderSel: "92",
	success:   "28",
	path:      "26",

	diffAdd:  "28",  // deep green
	diffDel:  "124", // deep red
	diffHunk: "30",  // teal
	diffMeta: "238",

	actorCoord:    "30", // teal
	actorImpl:     "28", // green
	actorReviewer: "92", // purple
	actorUser:     "26", // blue

	chromaStyleName: "github",
	glamourStyle:    "light",
}

// themeByName resolves a persisted pref string to a theme value, defaulting to dark.
func themeByName(name string) theme {
	if name == "light" {
		return lightTheme
	}
	return darkTheme
}

// activeTheme is the palette currently driving the package-level styles; populated
// by applyTheme (called from init and on a live theme switch).
var activeTheme theme

// applyTheme rebuilds every package-level style and the chroma highlight style from
// the given theme palette and records it as the active theme.
func applyTheme(t theme) {
	activeTheme = t

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(t.titleFg).Background(t.titleBg).Padding(0, 1)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(t.headerFg).Background(t.headerBg).Padding(0, 1)
	selStyle = lipgloss.NewStyle().Bold(true).Foreground(t.sel)
	recoStyle = lipgloss.NewStyle().Bold(true).Foreground(t.reco)
	selBarStyle = lipgloss.NewStyle().Foreground(t.selBar)
	dimStyle = lipgloss.NewStyle().Foreground(t.dim)
	thinkStyle = lipgloss.NewStyle().Italic(true).Foreground(t.think)
	typeStyle = lipgloss.NewStyle().Foreground(t.typ)
	askStyle = lipgloss.NewStyle().Bold(true).Foreground(t.askFg).Background(t.askBg)
	errStyle = lipgloss.NewStyle().Foreground(t.err)
	diffAddStyle = lipgloss.NewStyle().Foreground(t.diffAdd)
	diffDelStyle = lipgloss.NewStyle().Foreground(t.diffDel)
	diffHunkStyle = lipgloss.NewStyle().Foreground(t.diffHunk)
	diffMetaStyle = lipgloss.NewStyle().Bold(true).Foreground(t.diffMeta)

	borderStyle = lipgloss.NewStyle().Foreground(t.border)
	borderSelStyle = lipgloss.NewStyle().Foreground(t.borderSel)
	successStyle = lipgloss.NewStyle().Foreground(t.success)
	pathStyle = lipgloss.NewStyle().Foreground(t.path)
	cardTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(t.titleFg)

	chromaStyle = pickStyle(t.chromaStyleName)
}

func init() { applyTheme(darkTheme) }
