// This file owns shared input styling, terminal layout, and modal framing.
package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// newSessionInput builds the multi-line session input textarea (task 0011).
func newSessionInput() textarea.Model {
	return newChatInput("type to prod / answer · enter sends · shift+enter newline · ↑↓ select · click to expand")
}

// newChatInput builds a multi-line chat-input textarea shared by every input
// surface (menu prompt, session input, quick-add capture). It grows from one row
// up to maxInputRows as the text wraps, sends on Enter, and inserts a newline on
// shift+enter / ctrl+j. It is framed by a rounded border (see styleChatInput)
// rather than a dark-background block.
func newChatInput(placeholder string) textarea.Model {
	input := textarea.New()
	input.Placeholder = placeholder
	input.CharLimit = 8000
	input.ShowLineNumbers = false
	input.Prompt = ""
	input.MaxHeight = maxInputRows
	// DynamicHeight grows the box from total *visual* (soft-wrapped) lines up to
	// MaxHeight on every Update/SetValue/SetWidth, so a single very long line
	// that wraps grows the box too — not just explicit shift+enter newlines.
	input.MinHeight = 1
	input.DynamicHeight = true
	input.SetHeight(1)
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"))
	// bubbles v2 textarea only binds word motions to alt+arrows/alt+b/alt+f by
	// default; add ctrl+left/ctrl+right so word-wise cursor movement matches
	// common terminal/editor behavior (and the single-line textinput, which
	// already binds them). Keep the alt defaults so nothing regresses.
	input.KeyMap.WordBackward = key.NewBinding(key.WithKeys("alt+left", "alt+b", "ctrl+left"), key.WithHelp("alt+left", "word backward"))
	input.KeyMap.WordForward = key.NewBinding(key.WithKeys("alt+right", "alt+f", "ctrl+right"), key.WithHelp("alt+right", "word forward"))
	styleChatInput(&input)
	return input
}

// styleChatInput strips the default dark-background cursor-line block from a
// chat-input textarea so it can sit inside the rounded frame (inputFrameStyle)
// without a highlighted block behind the text. The frame itself is drawn around
// the textarea's View() (see framedInput); the textarea keeps a neutral Base so
// the library does not double-render the frame in its empty/placeholder state.
// Reapplied on a live theme switch so the blurred dim color tracks the palette
// (see restyleInputs).
func styleChatInput(ta *textarea.Model) {
	s := ta.Styles()
	s.Focused.Base = lipgloss.NewStyle()
	s.Blurred.Base = lipgloss.NewStyle()
	// Clear the dark-background highlight block: the focused line keeps no
	// background, the blurred line stays dimmed text (no block).
	s.Focused.CursorLine = lipgloss.NewStyle()
	s.Blurred.CursorLine = lipgloss.NewStyle().Foreground(lipgloss.Color(activeTheme.dim))
	ta.SetStyles(s)
}

// restyleInputs reapplies styleChatInput to every chat-input surface so a live
// theme switch repaints their blurred dim color in the new palette. (The rounded
// frame color is a package style rebuilt by applyTheme, so it needs no per-input
// fixup.)
func (m *model) restyleInputs() {
	styleChatInput(&m.prompt)
	styleChatInput(&m.input)
	styleChatInput(&m.captureInput)
}

// framedInput renders a chat-input textarea inside the rounded, expanding frame
// (inputFrameStyle, per lsp.webp), indented by n columns so every line of the
// multi-row frame aligns (a bare "  " prefix would only shift the first line).
func framedInput(ta textarea.Model, n int) string {
	return indentBlock(inputFrameStyle.Render(ta.View()), n)
}

// inputRow renders the framed session input with the activity spinner in the
// left gutter (task 0076): the spinner sits next to the place the user types.
// The spinner animates only while running (same gating as the old status-bar
// glyph and spinnerCmd); otherwise the gutter is a blank column, preserving the
// single-column indent framedInput(m.input, 1) used so the box does not shift.
func (m model) inputRow() string {
	frame := inputFrameStyle.Render(m.input.View())
	rows := strings.Split(frame, "\n")
	glyph := " "
	if m.status == "running" && len(m.spin.Spinner.Frames) > 0 {
		glyph = m.spin.View()
	}
	// The gutter must be the SAME display width on every row or the box's left
	// border goes crooked (task 0094). Some spinner frames are wider than one
	// column — e.g. the Dot spinner's frames are a braille glyph + a trailing
	// space (width 2) — so we can't assume the running glyph is one column. Pin
	// the gutter to the widest frame the spinner can show (falling back to 1),
	// so the width is stable across the running/idle transition AND across
	// animation frames, then pad the glyph and every blank row to that width.
	gw := 1
	for _, f := range m.spin.Spinner.Frames {
		if w := lipgloss.Width(f); w > gw {
			gw = w
		}
	}
	pad := func(s string) string {
		if d := gw - lipgloss.Width(s); d > 0 {
			return s + strings.Repeat(" ", d)
		}
		return s
	}
	glyph = pad(glyph)
	blank := strings.Repeat(" ", gw)
	// Place the glyph on the first content row (row index 1, just below the top
	// border); clamp for safety. Every other gutter row is blank of equal width.
	spinRow := 1
	if spinRow >= len(rows) {
		spinRow = 0
	}
	for i := range rows {
		if i == spinRow {
			rows[i] = glyph + rows[i]
		} else {
			rows[i] = blank + rows[i]
		}
	}
	return strings.Join(rows, "\n")
}

// indentBlock left-pads every line of s by n spaces.
func indentBlock(s string, n int) string {
	if n <= 0 {
		return s
	}
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

// inputViewHeight is the rendered height of the session input including its
// rounded frame (Height() reports only content rows; inputFrameStyle adds the
// vertical border).
func (m model) inputViewHeight() int {
	return m.input.Height() + inputFrameStyle.GetVerticalFrameSize()
}

// relayout recomputes the viewport height so the (possibly multi-row) footer
// stack — input box, question picker, wizard overview — and the help line never
// crowd out the event stream / status bar.
func (m *model) relayout() {
	if !m.ready {
		return
	}
	vpHeight := m.h - headerHeight - 1 - m.footerStackHeight()
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.vp.SetHeight(vpHeight)
}

// footerStackHeight is the number of rows sessionView stacks between the
// viewport body and the one-row help footer. Normally that is just the framed
// input box, but while a question with options is pending the option picker
// (question line + option rows) replaces it, and a multi-question ask_user
// additionally shows the wizard overview above it. Measuring the same rendered
// strings sessionView emits keeps this in lockstep with the actual layout, so
// the picker and help line can never be pushed off the bottom of the screen.
func (m model) footerStackHeight() int {
	// While the transcript search bar is active it replaces the whole footer
	// stack AND the help line with a single row (see sessionView), so nothing is
	// stacked above the one search-bar row (task 0116).
	if m.searching {
		return 0
	}
	h := 0
	if m.wizActive {
		h += lipgloss.Height(m.wizardView())
	}
	if m.picking {
		h += lipgloss.Height(m.pickerView())
	} else {
		h += m.inputViewHeight()
	}
	return h
}

// titleBar renders the standardized top title/breadcrumb pill used across every
// screen (menu / picker / history / backlog / overlays).
func (m model) titleBar(text string) string {
	return titleStyle.Render(text)
}

// footerBar renders a single-row, width-clamped key-hint line shared by every
// screen. The clamp guarantees a long hint can never wrap to a second physical
// row (which would corrupt Bubble Tea's line accounting / overflow the frame). A
// zero width (before the first WindowSizeMsg) is a no-op.
func (m model) footerBar(text string) string {
	// When the quit guard is armed, lead with the warning so it survives the
	// width clamp and is visible wherever the user is looking (task 0109).
	if m.quitArmed {
		warn := errStyle.Render("⚠ " + quitGuardHint)
		if strings.TrimSpace(text) == "" {
			text = warn
		} else {
			text = warn + dimStyle.Render(" · ") + text
		}
		if m.w > 0 {
			text = ansi.Truncate(strings.ReplaceAll(text, "\n", " "), m.w-1, "…")
		}
		return text
	}
	if m.w > 0 {
		// trunc may append a 1-col ellipsis, so clamp to m.w-1 to stay within m.w.
		text = trunc(strings.ReplaceAll(text, "\n", " "), m.w-1)
	}
	return dimStyle.Render(text)
}

// clampCardLines truncates each line of a multi-line block to width w (ANSI-aware)
// so a card's content can never make the bordered card wider than the screen.
func clampCardLines(s string, w int) string {
	if w < 1 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if lipgloss.Width(ln) > w {
			lines[i] = ansi.Truncate(ln, w, "…")
		}
	}
	return strings.Join(lines, "\n")
}

// modalCard renders content as a bordered, centered card floating over a cleared
// full-screen backdrop so an overlay reads as a modal rather than a full-screen
// text replacement. title becomes the card's title bar, content its body, and
// hint a clamped key-hint footer — all inside a rounded border with padding.
//
// Before the first WindowSizeMsg (m.w/m.h == 0, e.g. test-constructed models) it
// returns the plain title+content+hint without a border or Place so early renders
// and zero-size tests don't break.
func (m model) modalCard(title, content, hint string) string {
	var b strings.Builder
	b.WriteString(m.titleBar(title))
	b.WriteString("\n\n")
	b.WriteString(content)
	if hint != "" {
		b.WriteString("\n\n")
		b.WriteString(m.footerBar(hint))
	}
	body := b.String()

	if m.w == 0 || m.h == 0 {
		return body
	}

	// Vertical backstop: a card taller than the terminal renders as garbage, so
	// clamp the body to fit within m.h (minus the 2 border rows). Views with a
	// cursor window their own rows around it (browserCard, costView, …); this
	// clip only protects fixed-content cards on very short terminals. Keep the
	// footer hint visible by clipping content above it.
	if maxLines := m.h - 2; maxLines >= 3 {
		lines := strings.Split(body, "\n")
		if len(lines) > maxLines {
			keepTail := 0
			if hint != "" {
				keepTail = 2 // the blank spacer + footer bar
			}
			head := maxLines - keepTail - 1 // -1 for the clip marker
			if head < 1 {
				head = 1
			}
			clipped := append([]string{}, lines[:head]...)
			clipped = append(clipped, dimStyle.Render("…"))
			if keepTail > 0 {
				clipped = append(clipped, lines[len(lines)-keepTail:]...)
			}
			body = strings.Join(clipped, "\n")
		}
	}

	// Inner width budget: subtract the rounded border (2 cols) and padding (2 cols)
	// so the card — at most as wide as its widest content line — fits within m.w.
	inner := m.w - 4
	if inner < 1 {
		inner = 1
	}
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(activeTheme.border)).
		Padding(0, 1).
		Render(clampCardLines(body, inner))
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, card)
}
