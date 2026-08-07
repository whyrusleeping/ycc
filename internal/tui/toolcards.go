// This file owns tool-call cards and structured tool result rendering.
package tui

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// renderToolCall renders a tool_call (optionally with its folded tool_result) as
// either a compact one-line summary (collapsed) or a bordered card (expanded).
// res is nil while the call is still in flight.
func (m *model) renderToolCall(i int, call, res *v1.Event, first bool) string {
	exp := m.eventExpanded(int(call.Seq), call.Type)
	selected := i == m.selected || (res != nil && i+1 == m.selected)

	paramsBody := m.cardParams(call)
	var resultBody string
	if res != nil {
		resultBody = m.cardResult(res)
	}
	hasBody := strings.TrimSpace(paramsBody) != "" || strings.TrimSpace(resultBody) != "" || res == nil

	// Sub-agent tool rows nest under the coordinator with the same tree connector
	// used by prose rows (└─ on the last row of a sub-run, ├─ otherwise).
	subConn := ""
	if isSub(call.Actor) {
		subConn = subConnector(m.lastOfSubRun(i))
	}

	if !(exp && hasBody) {
		return m.toolCollapsed(call, res, selected, hasBody, first, subConn)
	}
	return m.toolCardExpanded(call, res, selected, paramsBody, resultBody, first)
}

// toolStatusGlyph returns the status marker for a tool call: a dim ring while in
// flight (res == nil), a red ✗ on error, else a green ✓.
func toolStatusGlyph(res *v1.Event) string {
	switch {
	case res == nil:
		return dimStyle.Render("○")
	case dataField(res, "error") == "true":
		return errStyle.Render("✗")
	default:
		return successStyle.Render("✓")
	}
}

// toolCollapsed renders the single-line summary of a tool call: status glyph,
// bold name, a dim argument summary, and (for sub-agents) a dim actor tag.
func (m *model) toolCollapsed(call, res *v1.Event, selected, hasBody, first bool, subConn string) string {
	bar := "  "
	if selected {
		bar = selBarStyle.Render("▌ ")
	}
	indent := ""
	if isSub(call.Actor) {
		indent = subConn
	}
	tri := "  "
	if hasBody {
		tri = dimStyle.Render("▸ ")
	}
	line := toolStatusGlyph(res) + " " + cardTitleStyle.Render(dataField(call, "name"))
	// Tag the owning sub-agent only when it first starts acting; later rows in
	// the same run rely on the indent + the spelled-out name above them.
	if isSub(call.Actor) && first {
		line += " " + dimStyle.Render("("+call.Actor+")")
	}
	if s := argSummary(call); s != "" {
		avail := m.w - lipgloss.Width(indent) - 8 - lipgloss.Width(line)
		if avail < 8 {
			avail = 8
		}
		line += "  " + dimStyle.Render(oneLine(s, avail))
	}
	if res != nil {
		line = appendDur(res, line)
	}
	return bar + indent + tri + line
}

// toolCardExpanded renders the bordered tool card: an inset title in the top
// border, dim parameter lines, and a nested Response box around the result.
// Selection is shown by tinting the card's border (per the chosen design).
func (m *model) toolCardExpanded(call, res *v1.Event, selected bool, paramsBody, resultBody string, first bool) string {
	bc := borderStyle
	if selected {
		bc = borderSelStyle
	}
	title := toolStatusGlyph(res) + " " + cardTitleStyle.Render(dataField(call, "name"))
	if d := durSuffix(res); d != "" {
		title += " " + d
	}

	indent := 2
	if isSub(call.Actor) {
		// Expanded cards stay indented by spaces rather than a tree connector: the
		// boxed card is already visually nested, and a per-line indentLines prefix
		// can't host a single connector glyph cleanly across the card's many rows.
		indent += 2
		if first {
			title += " " + dimStyle.Render("("+call.Actor+")")
		}
	}
	contentW := m.w - indent - 4 // outer border (2) + outer padding (2)
	if contentW < 16 {
		contentW = 16
	}

	var parts []string
	if strings.TrimSpace(paramsBody) != "" {
		parts = append(parts, paramsBody)
	}
	switch {
	case res == nil:
		parts = append(parts, dimStyle.Render("running…"))
	case strings.TrimSpace(resultBody) != "":
		parts = append(parts, titledBox(dimStyle.Render("Response"), resultBody, contentW-4, borderStyle))
	}

	card := titledBox(title, strings.Join(parts, "\n"), contentW, bc)
	if indent > 0 {
		card = indentLines(card, strings.Repeat(" ", indent))
	}
	return card
}

var catnRe = regexp.MustCompile(`^(\s*\d+\t)(.*)$`)

func highlightResult(s string) string {
	if looksDiff(s) {
		return colorizeDiff(s)
	}
	if looksCatN(s) {
		return dimLineNumbers(s)
	}
	return s
}

// callFor returns the tool_call event that produced the given tool_result, by
// matching the call id, falling back to the nearest preceding tool_call. This
// correlation lets the renderer infer a result's language from the call's args
// (e.g. Read's file_path, Bash's command).
func (m *model) callFor(res *v1.Event) *v1.Event {
	id := dataField(res, "id")
	var prev *v1.Event
	for _, e := range m.evs {
		if e.Type == "tool_call" {
			if id != "" && dataField(e, "id") == id {
				return e
			}
			prev = e
		}
		if e.Seq == res.Seq {
			break
		}
	}
	return prev
}

// argField unmarshals a tool_call's args JSON (itself a JSON string) and returns
// the named string field, or "".
func argField(call *v1.Event, key string) string {
	if call == nil {
		return ""
	}
	args := dataField(call, "args")
	if args == "" {
		return ""
	}
	var mp map[string]any
	if json.Unmarshal([]byte(args), &mp) != nil {
		return ""
	}
	if v, ok := mp[key].(string); ok {
		return v
	}
	return ""
}

// highlightToolResult renders successful tool result content with best-effort
// syntax highlighting inferred from the originating tool call (task 0017):
//   - diffs are colorized as before;
//   - Read's `cat -n` output is highlighted by the file_path extension, keeping
//     the dimmed line-number gutter;
//   - Bash grep/ripgrep output is highlighted when the language is unambiguous.
//
// Anything not confidently inferable falls back to the existing plain rendering.
func (m *model) highlightToolResult(r string, res *v1.Event) string {
	if looksDiff(r) {
		return colorizeDiff(r)
	}
	call := m.callFor(res)
	name := ""
	if call != nil {
		name = dataField(call, "name")
	}
	if looksCatN(r) {
		lexer := ""
		if name == "Read" {
			lexer = lexerNameForPath(argField(call, "file_path"))
		}
		return highlightCatN(r, lexer)
	}
	if name == "Bash" {
		if lexer := grepLexer(argField(call, "command"), r); lexer != "" {
			return highlightGrep(r, lexer)
		}
		return r
	}
	return highlightResult(r)
}

func prettyArgs(s string) string {
	if s == "" {
		return ""
	}
	var buf bytes.Buffer
	if json.Indent(&buf, []byte(s), "", "  ") == nil {
		return buf.String()
	}
	return s
}

// titledBox draws a rounded border around body with the given (already-styled)
// title inset into the top border — the LSP-card look. width is the inner
// content width (excluding the 1-col padding and the border). The border is
// drawn in bc's foreground color. Tabs in body are expanded first so lipgloss's
// width accounting (and therefore the right border) stays aligned.
func titledBox(title, body string, width int, bc lipgloss.Style) string {
	if width < 4 {
		width = 4
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(bc.GetForeground()).
		Width(width).
		Padding(0, 1).
		Render(expandTabs(body))
	lines := strings.Split(box, "\n")
	if len(lines) == 0 {
		return box
	}
	// Rebuild the top border line as: ╭─ <title> ───…───╮ at the box's width.
	total := lipgloss.Width(lines[0])
	used := 3 + lipgloss.Width(title) + 1 // "╭─ " + title + " "
	dashes := total - used - 1            // trailing "╮"
	if dashes < 0 {
		dashes = 0
	}
	lines[0] = bc.Render("╭─ ") + title + bc.Render(" "+strings.Repeat("─", dashes)+"╮")
	return strings.Join(lines, "\n")
}

// expandTabs replaces tabs with spaces so box width math is correct (lipgloss
// counts a tab as a single cell, which misaligns bordered boxes).
func expandTabs(s string) string { return strings.ReplaceAll(s, "\t", "    ") }

// cardParams renders a tool call's arguments as dim "key: value" lines (scalars
// inline, complex values as compact JSON), falling back to pretty-printed JSON.
// This is the param block shown at the top of an expanded tool card.
func (m *model) cardParams(call *v1.Event) string {
	args := dataField(call, "args")
	if strings.TrimSpace(args) == "" {
		return ""
	}
	var mp map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &mp) != nil {
		return dimStyle.Render(prettyArgs(args))
	}
	// Edit calls render as a git-style unified diff of old_string vs new_string.
	if dataField(call, "name") == "Edit" {
		var oldStr, newStr, path string
		_, hasOld := mp["old_string"]
		_, hasNew := mp["new_string"]
		okOld := hasOld && json.Unmarshal(mp["old_string"], &oldStr) == nil
		okNew := hasNew && json.Unmarshal(mp["new_string"], &newStr) == nil
		if okOld && okNew {
			_ = json.Unmarshal(mp["file_path"], &path)
			var out string
			if path != "" {
				out = dimStyle.Render("file_path: ") + typeStyle.Render(path) + "\n\n"
			}
			out += colorizeDiff(unifiedDiff(oldStr, newStr, 3))
			return out
		}
	}
	keys := make([]string, 0, len(mp))
	for k := range mp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		raw := strings.TrimSpace(string(mp[k]))
		var sv string
		if json.Unmarshal(mp[k], &sv) != nil { // not a plain string → keep JSON
			sv = raw
		}
		lines = append(lines, dimStyle.Render(k+": ")+typeStyle.Render(oneLine(sv, 200)))
	}
	return strings.Join(lines, "\n")
}

// cardResult renders a tool_result's body for display inside a card's Response
// box (no left-rail prefix — the box border provides the framing). When the
// result carries a structured view (LSP-style tree), that is rendered instead of
// the raw text.
func (m *model) cardResult(res *v1.Event) string {
	if v := toolViewOf(res); v != nil {
		return renderToolView(v)
	}
	r := dataField(res, "result")
	if r == "" {
		return ""
	}
	if dataField(res, "error") == "true" {
		return highlightResult(r)
	}
	return m.highlightToolResult(r, res)
}

// toolView mirrors tools.ResultView for decoding from a tool_result event's
// "view" field. It is the structured rendering a display tool attached.
type toolView struct {
	Summary string     `json:"summary"`
	Status  string     `json:"status"`
	Nodes   []viewNode `json:"nodes"`
}

type viewNode struct {
	Label    string     `json:"label"`
	Detail   string     `json:"detail"`
	Kind     string     `json:"kind"`
	Children []viewNode `json:"children"`
}

// toolViewOf extracts the structured view attached to a tool_result event, or
// nil when absent/unparsable.
func toolViewOf(ev *v1.Event) *toolView {
	if ev == nil || ev.DataJson == "" {
		return nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal([]byte(ev.DataJson), &top) != nil {
		return nil
	}
	raw, ok := top["view"]
	if !ok {
		return nil
	}
	var v toolView
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	if v.Summary == "" && len(v.Nodes) == 0 {
		return nil
	}
	return &v
}

// renderToolView renders a structured view as a connector tree: a glyph+summary
// headline followed by ├─/└─ nested rows, colored by each node's Kind.
func renderToolView(v *toolView) string {
	var b strings.Builder
	if v.Summary != "" {
		b.WriteString(viewKindStyle(v.Status).Render(viewGlyph(v.Status)) + " " + typeStyle.Render(v.Summary))
		if len(v.Nodes) > 0 {
			b.WriteByte('\n')
		}
	}
	b.WriteString(renderViewNodes(v.Nodes, ""))
	return strings.TrimRight(b.String(), "\n")
}

func renderViewNodes(nodes []viewNode, prefix string) string {
	var b strings.Builder
	for i, n := range nodes {
		last := i == len(nodes)-1
		conn, cont := "├─ ", "│  "
		if last {
			conn, cont = "└─ ", "   "
		}
		b.WriteString(prefix + dimStyle.Render(conn) + viewKindStyle(n.Kind).Render(n.Label))
		if n.Detail != "" {
			b.WriteString(" " + dimStyle.Render(n.Detail))
		}
		b.WriteByte('\n')
		if len(n.Children) > 0 {
			b.WriteString(renderViewNodes(n.Children, prefix+dimStyle.Render(cont)))
		}
	}
	return b.String()
}

// viewKindStyle maps a view node/summary kind to a style.
func viewKindStyle(kind string) lipgloss.Style {
	switch kind {
	case "path":
		return pathStyle
	case "ok":
		return successStyle
	case "warn":
		return recoStyle
	case "error":
		return errStyle
	case "muted":
		return dimStyle
	default:
		return typeStyle
	}
}

// viewGlyph is the headline marker for a view's status.
func viewGlyph(status string) string {
	switch status {
	case "warn":
		return "!"
	case "error":
		return "✗"
	default:
		return "✓"
	}
}

// argSummary is the one-line argument hint shown on a collapsed tool card: the
// most salient argument value (path/pattern/command) when present, else a
// compact rendering of all args.
func argSummary(call *v1.Event) string {
	for _, k := range []string{"file_path", "path", "pattern", "command", "query", "url", "task_id"} {
		if v := argField(call, k); v != "" {
			return v
		}
	}
	return oneLine(dataField(call, "args"), 80)
}

// durSuffix renders an event's duration_ms as a dim suffix (e.g. "340ms"), or ""
// when absent.
func durSuffix(ev *v1.Event) string {
	if ev == nil {
		return ""
	}
	if ms := durationMSField(ev); ms > 0 {
		return dimStyle.Render(fmtDurMS(ms))
	}
	return ""
}
