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

import "charm.land/lipgloss/v2"

// theme holds every color role the TUI renders with. Colors are plain ANSI-256
// strings (resolved to lipgloss colors in applyTheme); attributes
// (bold/italic/padding) are applied in applyTheme so the per-theme palette stays
// purely about color.
type theme struct {
	titleFg  string
	titleBg  string
	headerFg string
	headerBg string

	sel    string // selection accent
	reco   string // recommendation highlight
	selBar string // selection bar
	dim    string // dimmed/secondary text
	think  string // thinking (italic) text
	typ    string // type/label text
	askFg  string
	askBg  string
	err    string
	warn   string // warning/intermediate verdict (e.g. revise)

	// Tool-call "card" roles (task: LSP-style cards). border is the resting card
	// outline; borderSel is the outline of the currently-selected card; success is
	// the green glyph/accent for a completed tool call; path tints file paths in
	// structured tool output.
	border    string
	borderSel string
	success   string
	path      string

	diffAdd  string
	diffDel  string
	diffHunk string
	diffMeta string

	actorCoord    string
	actorImpl     string
	actorReviewer string
	actorUser     string

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
	warn:   "214", // amber

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
	warn:   "166", // amber/orange, readable on white

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

	c := lipgloss.Color
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(c(t.titleFg)).Background(c(t.titleBg)).Padding(0, 1)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(c(t.headerFg)).Background(c(t.headerBg)).Padding(0, 1)
	selStyle = lipgloss.NewStyle().Bold(true).Foreground(c(t.sel))
	recoStyle = lipgloss.NewStyle().Bold(true).Foreground(c(t.reco))
	selBarStyle = lipgloss.NewStyle().Foreground(c(t.selBar))
	dimStyle = lipgloss.NewStyle().Foreground(c(t.dim))
	thinkStyle = lipgloss.NewStyle().Foreground(c(t.think))
	typeStyle = lipgloss.NewStyle().Foreground(c(t.typ))
	askStyle = lipgloss.NewStyle().Bold(true).Foreground(c(t.askFg)).Background(c(t.askBg))
	errStyle = lipgloss.NewStyle().Foreground(c(t.err))
	warnStyle = lipgloss.NewStyle().Foreground(c(t.warn))
	diffAddStyle = lipgloss.NewStyle().Foreground(c(t.diffAdd))
	diffDelStyle = lipgloss.NewStyle().Foreground(c(t.diffDel))
	diffHunkStyle = lipgloss.NewStyle().Foreground(c(t.diffHunk))
	diffMetaStyle = lipgloss.NewStyle().Bold(true).Foreground(c(t.diffMeta))

	borderStyle = lipgloss.NewStyle().Foreground(c(t.border))
	borderSelStyle = lipgloss.NewStyle().Foreground(c(t.borderSel))
	successStyle = lipgloss.NewStyle().Foreground(c(t.success))
	pathStyle = lipgloss.NewStyle().Foreground(c(t.path))
	cardTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(c(t.titleFg))

	inputFrameStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c(t.border)).
		Padding(0, 1)

	chromaStyle = pickStyle(t.chromaStyleName)
}

func init() { applyTheme(darkTheme) }
