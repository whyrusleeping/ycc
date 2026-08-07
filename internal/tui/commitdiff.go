// This file owns commit-diff parsing, navigation, and rendering.
package tui

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// fetchCommitDiff loads a commit's `git show` diff for the commit-diff overlay
// (task 0140). The result carries the sha so the handler can drop a reply that
// arrives after the overlay closed or moved to a different commit.
func (m model) fetchCommitDiff(sha string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.GetCommitDiff(m.ctx, connect.NewRequest(&v1.GetCommitDiffRequest{Project: m.project, Sha: sha}))
		if err != nil {
			return commitDiffMsg{sha: sha, err: err}
		}
		return commitDiffMsg{sha: sha, diff: resp.Msg.GetDiff(), truncated: resp.Msg.GetTruncated()}
	}
}

// cdiffFile is one file section of a parsed `git show` diff.
type cdiffFile struct {
	path string // file path (from +++ b/… or the diff --git header)
	body string // the raw "diff --git …" section, including its header lines
	adds int    // added content lines (excludes the +++ marker)
	dels int    // removed content lines (excludes the --- marker)
}

// Large-commit thresholds: past either, the overlay opens with every file folded
// so it renders instantly (§18.9 safety) and the user unfolds what they want.
const (
	cdiffFoldAllLines = 1500
	cdiffFoldAllFiles = 25
)

// parseCommitDiff splits raw `git show` output into a preamble (commit header +
// --stat block) and per-file sections (split on "diff --git " lines), counting
// added/removed content lines per file (excluding the +++/--- markers). It is a
// standalone function so the parser is unit-testable.
func parseCommitDiff(raw string) (preamble string, files []cdiffFile) {
	var pre, body strings.Builder
	var cur cdiffFile
	inFile := false
	flush := func() {
		if inFile {
			cur.body = strings.TrimRight(body.String(), "\n")
			files = append(files, cur)
		}
		body.Reset()
	}
	for _, ln := range strings.Split(raw, "\n") {
		if strings.HasPrefix(ln, "diff --git ") {
			flush()
			cur = cdiffFile{path: gitDiffPath(ln)}
			inFile = true
			body.WriteString(ln + "\n")
			continue
		}
		if !inFile {
			pre.WriteString(ln + "\n")
			continue
		}
		body.WriteString(ln + "\n")
		switch {
		case strings.HasPrefix(ln, "+++ b/"):
			cur.path = strings.TrimPrefix(ln, "+++ b/")
		case strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"):
			// file markers, not content lines
		case strings.HasPrefix(ln, "+"):
			cur.adds++
		case strings.HasPrefix(ln, "-"):
			cur.dels++
		}
	}
	flush()
	return strings.TrimRight(pre.String(), "\n"), files
}

// gitDiffPath extracts the file path from a "diff --git a/path b/path" header.
// The +++ b/… line refines it later for the common case; this is the fallback.
func gitDiffPath(diffLine string) string {
	h := strings.TrimPrefix(diffLine, "diff --git ")
	if i := strings.LastIndex(h, " b/"); i >= 0 {
		return h[i+len(" b/"):]
	}
	return strings.TrimSpace(h)
}

// openCommitDiff opens the commit-diff overlay for sha (with msg as its title
// subtitle) and returns the fetch command. A blank sha is a no-op (returns nil).
func (m *model) openCommitDiff(sha, msg string) tea.Cmd {
	if strings.TrimSpace(sha) == "" {
		return nil
	}
	m.closeCommitDiff()
	m.cdiffOpen = true
	m.cdiffSha = sha
	m.cdiffMsgTxt = msg
	m.cdiffLoading = true
	return m.fetchCommitDiff(sha)
}

// closeCommitDiff clears all overlay state, returning to whatever surface was
// underneath (render() simply falls through once cdiffOpen is false).
func (m *model) closeCommitDiff() {
	m.cdiffOpen = false
	m.cdiffSha = ""
	m.cdiffMsgTxt = ""
	m.cdiffLoading = false
	m.cdiffErr = ""
	m.cdiffTruncated = false
	m.cdiffFiles = nil
	m.cdiffPreamble = ""
	m.cdiffFold = nil
	m.cdiffHeaderLines = nil
	m.cdiffCursor = 0
}

// cdiffContent builds the scrollable overlay body and records each file header's
// content-line offset (m.cdiffHeaderLines) for scroll-into-view. Folded files
// show only their header; unfolded files render the section via colorizeDiff.
func (m *model) cdiffContent() string {
	var b strings.Builder
	line := 0
	write := func(s string) {
		b.WriteString(s)
		line += strings.Count(s, "\n")
	}
	if m.cdiffPreamble != "" {
		write(colorizeDiff(m.cdiffPreamble) + "\n\n")
	}
	m.cdiffHeaderLines = make([]int, len(m.cdiffFiles))
	for i, f := range m.cdiffFiles {
		folded := i < len(m.cdiffFold) && m.cdiffFold[i]
		marker := "▾"
		if folded {
			marker = "▸"
		}
		header := fmt.Sprintf("%s %s  (+%d −%d)", marker, f.path, f.adds, f.dels)
		if i == m.cdiffCursor {
			header = selStyle.Render(header)
		}
		m.cdiffHeaderLines[i] = line
		write(header + "\n")
		if !folded {
			write(colorizeDiff(f.body) + "\n")
		}
	}
	if m.cdiffTruncated {
		write("\n" + dimStyle.Render("… diff truncated (showing first ~1 MiB — use a shell for the full diff)"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// refreshCdiffVP (re)sizes the overlay viewport and reloads its content. It
// reserves rows for the title, the commit-message subtitle, and the footer.
func (m *model) refreshCdiffVP() {
	if !m.cdiffOpen || !m.ready {
		return
	}
	h := m.h - 3
	if h < 3 {
		h = 3
	}
	if m.cdiffVP.Height() == 0 && m.cdiffVP.Width() == 0 {
		m.cdiffVP = viewport.New(viewport.WithWidth(m.w), viewport.WithHeight(h))
	} else {
		m.cdiffVP.SetWidth(m.w)
		m.cdiffVP.SetHeight(h)
	}
	m.cdiffVP.SetContent(m.cdiffContent())
}

// cdiffScrollToCursor scrolls the viewport just enough to keep the cursor file's
// header visible after a tab/shift+tab move.
func (m *model) cdiffScrollToCursor() {
	if m.cdiffCursor < 0 || m.cdiffCursor >= len(m.cdiffHeaderLines) {
		return
	}
	target := m.cdiffHeaderLines[m.cdiffCursor]
	off := m.cdiffVP.YOffset()
	h := m.cdiffVP.Height()
	switch {
	case target < off:
		m.cdiffVP.SetYOffset(target)
	case target >= off+h:
		m.cdiffVP.SetYOffset(target - h + 1)
	}
}

// updateCommitDiff handles keys for the commit-diff overlay: tab/shift+tab move
// the file cursor, enter/space fold the cursor file, `a` folds/unfolds all, and
// everything else (↑↓/pgup/pgdn/wheel) scrolls the viewport.
func (m model) updateCommitDiff(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.cdiffVP, cmd = m.cdiffVP.Update(msg)
		return m, cmd
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q", "backspace", "left":
		m.closeCommitDiff()
		return m, nil
	case "tab":
		if n := len(m.cdiffFiles); n > 0 {
			m.cdiffCursor = (m.cdiffCursor + 1) % n
			m.cdiffVP.SetContent(m.cdiffContent())
			m.cdiffScrollToCursor()
		}
		return m, nil
	case "shift+tab":
		if n := len(m.cdiffFiles); n > 0 {
			m.cdiffCursor = (m.cdiffCursor - 1 + n) % n
			m.cdiffVP.SetContent(m.cdiffContent())
			m.cdiffScrollToCursor()
		}
		return m, nil
	case "enter", " ", "space":
		if m.cdiffCursor >= 0 && m.cdiffCursor < len(m.cdiffFold) {
			m.cdiffFold[m.cdiffCursor] = !m.cdiffFold[m.cdiffCursor]
			m.cdiffVP.SetContent(m.cdiffContent())
			m.cdiffScrollToCursor()
		}
		return m, nil
	case "a":
		// Toggle all: if any file is unfolded, fold everything; else unfold all.
		anyOpen := false
		for _, f := range m.cdiffFold {
			if !f {
				anyOpen = true
				break
			}
		}
		for i := range m.cdiffFold {
			m.cdiffFold[i] = anyOpen
		}
		m.cdiffVP.SetContent(m.cdiffContent())
		m.cdiffScrollToCursor()
		return m, nil
	}
	var cmd tea.Cmd
	m.cdiffVP, cmd = m.cdiffVP.Update(msg)
	return m, cmd
}

// commitDiffView renders the full-screen commit-diff overlay.
func (m model) commitDiffView() string {
	top := m.titleBar(" commit " + shortSHA(m.cdiffSha) + " ")
	sub := "  "
	if m.cdiffMsgTxt != "" {
		sub = "  " + dimStyle.Render(oneLine(m.cdiffMsgTxt, m.w-4))
	}
	body := ""
	switch {
	case m.cdiffLoading:
		body = "\n  " + dimStyle.Render("loading diff…")
	case m.cdiffErr != "":
		body = "\n  " + errStyle.Render("✗ "+m.cdiffErr)
	case m.ready:
		body = m.cdiffVP.View()
	}
	hint := " tab/shift+tab file · enter/space fold · a fold all · ↑↓ scroll · esc close "
	return top + "\n" + sub + "\n" + body + "\n" + m.footerBar(hint)
}
