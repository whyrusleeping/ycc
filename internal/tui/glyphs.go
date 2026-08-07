// This file owns actor, event-type, and verdict glyph styling.
package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// These package-level styles are (re)built from the active theme by applyTheme
// (see theme.go); init() populates them with the dark theme. No raw color
// literals live here — every color is a named role in theme.go.
var (
	titleStyle  lipgloss.Style
	headerStyle lipgloss.Style
	selStyle    lipgloss.Style
	recoStyle   lipgloss.Style
	selBarStyle lipgloss.Style
	dimStyle    lipgloss.Style
	// histHighlightStyle marks the current search-match / jump-target line in the
	// modal session-browser transcript (task 0119): a reverse-video bar.
	histHighlightStyle lipgloss.Style
	thinkStyle         lipgloss.Style
	typeStyle          lipgloss.Style
	askStyle           lipgloss.Style
	errStyle           lipgloss.Style
	warnStyle          lipgloss.Style
	diffAddStyle       lipgloss.Style
	diffDelStyle       lipgloss.Style
	diffHunkStyle      lipgloss.Style
	diffMetaStyle      lipgloss.Style

	borderStyle    lipgloss.Style
	borderSelStyle lipgloss.Style
	successStyle   lipgloss.Style
	pathStyle      lipgloss.Style
	cardTitleStyle lipgloss.Style

	// inputFrameStyle is the rounded, expanding frame drawn around every chat
	// input (per lsp.webp): a rounded border in the palette's border color with a
	// single column of horizontal padding. Rebuilt by applyTheme on a theme switch.
	inputFrameStyle lipgloss.Style
)

func actorStyle(actor string) lipgloss.Style {
	switch {
	case actor == "coordinator":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(activeTheme.actorCoord))
	case actor == "implementer":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(activeTheme.actorImpl))
	case strings.HasPrefix(actor, "reviewer"):
		return lipgloss.NewStyle().Foreground(lipgloss.Color(activeTheme.actorReviewer))
	case actor == "user":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(activeTheme.actorUser))
	default:
		return dimStyle
	}
}

// actorGlyph returns a compact per-role icon used on continuation rows (where the
// actor's name was already spelled out above). Color still distinguishes roles;
// the glyph gives a second, shape-based cue (diamond/circle/square).
func actorGlyph(actor string) string {
	switch {
	case actor == "coordinator":
		return "◆"
	case actor == "implementer":
		return "●"
	case strings.HasPrefix(actor, "reviewer"):
		return "■"
	case actor == "user":
		return "›"
	default:
		return "·"
	}
}

// actorColumn renders the fixed-width (13-cell) actor column: the spelled-out
// name when the actor first starts a run, else just its glyph. Both are colored
// by role so a glance still reads who is acting.
func (m *model) actorColumn(actor string, first bool) string {
	label := actor
	if !first {
		label = actorGlyph(actor)
	}
	return actorStyle(actor).Render(fmt.Sprintf("%-13s", label))
}

func isSub(actor string) bool {
	return actor == "implementer" || strings.HasPrefix(actor, "reviewer")
}

// typeGlyph returns a single-width leading icon for an event type, giving each
// row a fast, shape-based scanning cue. All glyphs are single-cell unicode from
// the families already used elsewhere in the renderer so column alignment and
// the line-offset accounting in rebuild() are unaffected. tool_call returns ""
// because tool rows lead with toolStatusGlyph (✓/✗/○) instead.
func typeGlyph(t string) string {
	switch t {
	case "tool_call":
		return ""
	case "thinking":
		return "✦"
	case "model_turn":
		return "»"
	case "user_input":
		return "›"
	case "review_submitted":
		return "§"
	case "commit_made":
		return "●"
	case "doc_updated":
		return "✎"
	case "mode_changed":
		return "↻"
	case "subagent_spawned", "subagent_finished":
		return "◇"
	case "job_started", "job_finished", "job_notified":
		return "◈"
	case "question_asked":
		return "?"
	case "question_answered":
		return "✓"
	case "session_idle":
		return "■"
	case "session_error":
		return "✗"
	case "budget_warning", "budget_exceeded":
		return "⚠"
	default:
		return "·"
	}
}

// typeGlyphStyle picks a palette role to tint a type's leading glyph: errors and
// commits get danger/success accents, everything else uses the dim type color so
// the glyph reads as quiet metadata.
func typeGlyphStyle(t string) lipgloss.Style {
	switch t {
	case "session_error":
		return errStyle
	case "budget_exceeded":
		return errStyle
	case "budget_warning":
		return recoStyle
	case "commit_made", "question_answered", "session_idle":
		return successStyle
	default:
		return typeStyle
	}
}

// verdictStyle color-codes a review verdict token: accept/approve = success,
// revise = warn (amber), reject = danger (red); anything else stays neutral.
func verdictStyle(verdict string) lipgloss.Style {
	switch strings.ToLower(strings.TrimSpace(verdict)) {
	case "accept", "approve", "approved":
		return successStyle
	case "revise":
		return warnStyle
	case "reject", "rejected":
		return errStyle
	default:
		return typeStyle
	}
}

// subConnector renders the tree guide that nests a sub-agent (implementer /
// reviewer) row under the coordinator: "└─ " on the last row of a contiguous
// sub-run, "├─ " otherwise. It is a single-line, 3-cell inline prefix, so the
// per-block line counts in rebuild() are unchanged.
func subConnector(last bool) string {
	if last {
		return dimStyle.Render("└─ ")
	}
	return dimStyle.Render("├─ ")
}
