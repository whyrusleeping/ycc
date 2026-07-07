package tui

// Mouse drag-select-to-copy for the transcript viewport (live session view and
// the read-only history transcript, both backed by m.vp).
//
// The TUI runs with cell-motion mouse reporting enabled, which means the
// terminal's native drag-selection is unavailable inside it. This file
// re-implements the affordance in-app: pressing the left button over the
// viewport and dragging highlights a linear (stream-order) region of the
// rendered transcript; releasing copies its plain text to the system clipboard
// via OSC 52 (tea.SetClipboard) — the same escape-sequence mechanism the `y`
// row-yank uses, so it works over SSH too. A press+release without motion is
// still a plain click (expand/collapse a row in the session view).
//
// Selection coordinates are CONTENT-relative — row is a content line index,
// col a terminal cell column — so the highlight stays glued to the text when
// the viewport scrolls mid-drag (including the edge auto-scroll below).

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// selHLStyle marks selected cells. Reverse video swaps each cell's own
// foreground/background, so the highlight is visible on any theme without
// hard-coding a selection color.
var selHLStyle = lipgloss.NewStyle().Reverse(true)

// selContentPos converts a terminal cell coordinate (0-based, from a mouse
// event) into viewport-content coordinates, accounting for the status/title
// bar above the viewport and the current scroll offset.
func (m *model) selContentPos(x, y int) (row, col int) {
	row = y - headerHeight + m.vp.YOffset()
	if row < 0 {
		row = 0
	}
	col = x
	if col < 0 {
		col = 0
	}
	return row, col
}

// selMouseDown records a potential drag start. Whether this press turns out to
// be a plain click or a drag is decided by whether motion arrives before the
// release (selRegion).
func (m *model) selMouseDown(x, y int) {
	m.selDrag = true
	m.selRegion = false
	m.selAnchorRow, m.selAnchorCol = m.selContentPos(x, y)
	m.selHeadRow, m.selHeadCol = m.selAnchorRow, m.selAnchorCol
}

// selMouseMotion extends the selection to the pointer while the button is
// held. Dragging past the top or bottom edge auto-scrolls the viewport one
// line per motion report so a selection can grow beyond one screen.
func (m *model) selMouseMotion(x, y int) {
	if !m.selDrag {
		return
	}
	switch {
	case y < headerHeight:
		m.vp.SetYOffset(m.vp.YOffset() - 1)
		y = headerHeight
	case y >= headerHeight+m.vp.Height():
		m.vp.SetYOffset(m.vp.YOffset() + 1)
		y = headerHeight + m.vp.Height() - 1
	}
	m.selHeadRow, m.selHeadCol = m.selContentPos(x, y)
	if !m.selRegion && (m.selHeadRow != m.selAnchorRow || m.selHeadCol != m.selAnchorCol) {
		m.selRegion = true
		// Stop auto-following: a streaming append must not yank the view (and
		// the growing highlight) to the bottom mid-drag. Scrolling back to the
		// bottom re-arms follow, as with the mouse wheel.
		m.follow = false
	}
}

// selMouseUp ends a drag. dragged reports whether the press actually selected
// a region (otherwise it was a plain click and the caller should apply its
// click behaviour); text is the selection's plain-text content, "" when the
// region contains nothing copyable. The highlight clears with the release —
// the "copied ✓" flash is the confirmation.
func (m *model) selMouseUp() (text string, dragged bool) {
	if !m.selDrag {
		return "", false
	}
	dragged = m.selRegion
	m.selDrag, m.selRegion = false, false
	if !dragged {
		return "", false
	}
	return m.selectionText(), true
}

// selBounds normalizes anchor/head into reading order: (r0,c0) is the start
// cell, (r1,c1) the END cell INCLUSIVE (the cell under the pointer belongs to
// the selection, like native terminal selection).
func (m model) selBounds() (r0, c0, r1, c1 int) {
	r0, c0 = m.selAnchorRow, m.selAnchorCol
	r1, c1 = m.selHeadRow, m.selHeadCol
	if r1 < r0 || (r1 == r0 && c1 < c0) {
		r0, c0, r1, c1 = r1, c1, r0, c0
	}
	return r0, c0, r1, c1
}

// selLineSpan clamps the selection to one content row: given the row index and
// its rendered cell width it returns the selected cell range [lo, hi) on that
// row, hi<=w. ok is false when the selection misses the row entirely (also
// when it starts past the line's end).
func (m model) selLineSpan(row, w int) (lo, hi int, ok bool) {
	r0, c0, r1, c1 := m.selBounds()
	if row < r0 || row > r1 {
		return 0, 0, false
	}
	lo, hi = 0, w
	if row == r0 {
		lo = c0
	}
	if row == r1 && c1+1 < hi {
		hi = c1 + 1
	}
	if lo >= hi || lo >= w {
		return 0, 0, false
	}
	return lo, hi, true
}

// selectionText extracts the selected region's plain text from the viewport
// content (m.vpContent mirrors exactly what m.vp renders). Middle rows
// contribute their whole line; the first/last rows are cut at the anchor/head
// cells. Styling is stripped and per-line trailing padding trimmed, so what
// lands on the clipboard is clean text.
func (m model) selectionText() string {
	if m.vpContent == "" {
		return ""
	}
	lines := strings.Split(m.vpContent, "\n")
	r0, _, r1, _ := m.selBounds()
	var out []string
	for r := r0; r <= r1 && r < len(lines); r++ {
		plain := ansi.Strip(lines[r])
		w := ansi.StringWidth(plain)
		lo, hi, ok := m.selLineSpan(r, w)
		seg := ""
		if ok {
			if lo == 0 && hi == w {
				seg = plain
			} else {
				seg = ansi.Cut(plain, lo, hi)
			}
		}
		out = append(out, strings.TrimRight(seg, " \t"))
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// overlaySelection re-renders the visible viewport body with the in-progress
// drag selection shown in reverse video. body is m.vp.View()'s output (the
// visible window), so visible row i corresponds to content row YOffset+i.
// The selected span is stripped of its own styling and re-rendered reversed;
// ansi.Cut/TruncateLeft re-emit the escape sequences preceding a cut, so the
// unselected right-hand remainder keeps its original styling.
func (m model) overlaySelection(body string) string {
	if !m.selRegion {
		return body
	}
	off := m.vp.YOffset()
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		w := ansi.StringWidth(ln)
		lo, hi, ok := m.selLineSpan(off+i, w)
		if !ok {
			continue
		}
		left := ansi.Cut(ln, 0, lo)
		mid := ansi.Strip(ansi.Cut(ln, lo, hi))
		right := ansi.Cut(ln, hi, w)
		lines[i] = left + selHLStyle.Render(mid) + right
	}
	return strings.Join(lines, "\n")
}
