// Package tui is the Bubble Tea home-menu + session client for ycc (spec §3).
// It lists modes, starts a session, and renders the live event stream with
// click-to-expand turns, auto-expanded final responses, and syntax highlighting
// (markdown via glamour, colorized diffs, dimmed cat -n line numbers).
package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

type state int

const (
	stateMenu state = iota
	stateSession
)

const headerHeight = 1 // the session status bar occupies the first row

type model struct {
	client    yccv1connect.SessionServiceClient
	ctx       context.Context
	workspace string

	state  state
	modes  []*v1.Mode
	cursor int
	prompt textinput.Model

	sessionID string
	mode      string
	events    chan *v1.Event

	evs        []*v1.Event
	expanded   map[int]bool   // seq -> manually expanded
	bodyCache  map[int]string // seq -> rendered multi-line body
	eventStart []int          // content line index where each event begins
	selected   int            // index into evs, or -1
	follow     bool           // auto-scroll + auto-select latest

	vp      viewport.Model
	input   textinput.Model
	glam    *glamour.TermRenderer
	pending string
	status  string

	err   error
	ready bool
	w, h  int
}

// Run starts the TUI against the daemon client.
func Run(ctx context.Context, client yccv1connect.SessionServiceClient, workspace string) error {
	p := tea.NewProgram(initialModel(ctx, client, workspace), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

func initialModel(ctx context.Context, client yccv1connect.SessionServiceClient, workspace string) model {
	prompt := textinput.New()
	prompt.Placeholder = "what should the agent do? (optional for 'work')"
	prompt.Focus()
	prompt.CharLimit = 8000
	prompt.Width = 60

	input := textinput.New()
	input.Placeholder = "type to prod / answer · Enter sends · ↑↓ select · click to expand"
	input.CharLimit = 8000
	input.Width = 60

	return model{
		client: client, ctx: ctx, workspace: workspace,
		state: stateMenu, prompt: prompt, input: input,
		events: make(chan *v1.Event, 256), status: "starting",
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1, follow: true,
	}
}

// --- messages ---

type modesMsg struct{ modes []*v1.Mode }
type startedMsg struct{ id, mode string }
type evMsg struct{ ev *v1.Event }
type streamClosedMsg struct{}
type errMsg struct{ err error }

func (m model) Init() tea.Cmd { return m.fetchModes }

func (m model) fetchModes() tea.Msg {
	resp, err := m.client.ListModes(m.ctx, connect.NewRequest(&v1.ListModesRequest{}))
	if err != nil {
		return errMsg{err}
	}
	return modesMsg{resp.Msg.Modes}
}

func (m model) startSession(mode, prompt string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.StartSession(m.ctx, connect.NewRequest(&v1.StartSessionRequest{
			Mode: mode, Prompt: prompt, Workspace: m.workspace,
		}))
		if err != nil {
			return errMsg{err}
		}
		return startedMsg{id: resp.Msg.SessionId, mode: mode}
	}
}

func (m model) subscribe() tea.Cmd {
	return func() tea.Msg {
		stream, err := m.client.Subscribe(m.ctx, connect.NewRequest(&v1.SubscribeRequest{SessionId: m.sessionID}))
		if err != nil {
			return errMsg{err}
		}
		go func() {
			for stream.Receive() {
				m.events <- stream.Msg()
			}
			close(m.events)
		}()
		return waitEvent(m.events)()
	}
}

func waitEvent(ch chan *v1.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamClosedMsg{}
		}
		return evMsg{ev}
	}
}

func (m model) sendInput(text string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.SendInput(m.ctx, connect.NewRequest(&v1.SendInputRequest{SessionId: m.sessionID, Text: text})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		vpHeight := msg.Height - headerHeight - 2
		if vpHeight < 3 {
			vpHeight = 3
		}
		if !m.ready {
			m.vp = viewport.New(msg.Width, vpHeight)
			m.ready = true
		} else {
			m.vp.Width, m.vp.Height = msg.Width, vpHeight
		}
		m.prompt.Width = msg.Width - 4
		m.input.Width = msg.Width - 4
		m.makeRenderer()
		m.bodyCache = map[int]string{} // re-render bodies at the new width
		m.rebuild()
		return m, nil

	case modesMsg:
		m.modes = msg.modes
		return m, nil
	case errMsg:
		m.err = msg.err
		return m, nil
	case startedMsg:
		m.sessionID, m.mode, m.state, m.status = msg.id, msg.mode, stateSession, "running"
		m.input.Focus()
		return m, m.subscribe()
	case streamClosedMsg:
		m.status = "stream closed"
		return m, nil
	case evMsg:
		m.appendEvent(msg.ev)
		return m, waitEvent(m.events)
	}

	if m.state == stateMenu {
		return m.updateMenu(msg)
	}
	return m.updateSession(msg)
}

func (m model) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down":
			if m.cursor < len(m.modes)-1 {
				m.cursor++
			}
			return m, nil
		case "enter":
			if len(m.modes) == 0 {
				return m, nil
			}
			return m, m.startSession(m.modes[m.cursor].Name, m.prompt.Value())
		}
	}
	var cmd tea.Cmd
	m.prompt, cmd = m.prompt.Update(msg)
	return m, cmd
}

func (m model) updateSession(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			m.follow = false
			return m, cmd
		}
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			row := msg.Y - headerHeight + m.vp.YOffset
			if i := m.eventAt(row); i >= 0 {
				m.selected = i
				m.toggle(i)
				m.follow = false
			}
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.state = stateMenu
			return m, nil
		case "up":
			m.moveSelection(-1)
			return m, nil
		case "down":
			m.moveSelection(1)
			return m, nil
		case "pgup":
			m.vp.HalfPageUp()
			m.follow = false
			return m, nil
		case "pgdown":
			m.vp.HalfPageDown()
			m.follow = false
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				// Empty input: Enter expands/collapses the selected turn.
				if m.selected >= 0 {
					m.toggle(m.selected)
				}
				return m, nil
			}
			m.input.SetValue("")
			m.pending = ""
			m.follow = true
			return m, m.sendInput(text)
		}
	}
	var icmd tea.Cmd
	m.input, icmd = m.input.Update(msg)
	return m, icmd
}

func (m *model) moveSelection(d int) {
	if len(m.evs) == 0 {
		return
	}
	if m.selected < 0 {
		m.selected = len(m.evs) - 1
	}
	m.selected += d
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(m.evs) {
		m.selected = len(m.evs) - 1
	}
	m.follow = m.selected == len(m.evs)-1
	m.rebuild()
	m.ensureVisible()
}

func (m *model) toggle(i int) {
	if i < 0 || i >= len(m.evs) {
		return
	}
	seq := int(m.evs[i].Seq)
	m.expanded[seq] = !m.expanded[seq]
	m.rebuild()
	m.ensureVisible()
}

func (m *model) appendEvent(ev *v1.Event) {
	m.evs = append(m.evs, ev)
	switch ev.Type {
	case "question_asked":
		m.pending = dataField(ev, "question")
		m.status = "waiting for your answer"
	case "question_answered":
		m.pending = ""
		m.status = "running"
	case "session_idle":
		m.status = "idle"
	case "session_error":
		m.status = "error"
	case "mode_changed":
		m.mode = dataField(ev, "to")
		m.status = "running"
	}
	if m.follow {
		m.selected = len(m.evs) - 1
	}
	m.rebuild()
}

// eventAt returns the index of the event whose rendered block contains content
// line `row`, or -1.
func (m *model) eventAt(row int) int {
	if row < 0 {
		return -1
	}
	for i := len(m.eventStart) - 1; i >= 0; i-- {
		if row >= m.eventStart[i] {
			return i
		}
	}
	return -1
}

func (m *model) ensureVisible() {
	if m.selected < 0 || m.selected >= len(m.eventStart) {
		return
	}
	start := m.eventStart[m.selected]
	if start < m.vp.YOffset {
		m.vp.SetYOffset(start)
	} else if start >= m.vp.YOffset+m.vp.Height {
		m.vp.SetYOffset(start - m.vp.Height + 1)
	}
}

func (m *model) makeRenderer() {
	w := m.w - 4
	if w < 20 {
		w = 20
	}
	// Use a fixed style, NOT WithAutoStyle: auto-style queries the terminal's
	// background by reading stdin, which Bubble Tea already owns — that blocks the
	// event loop and freezes the UI. "dark" is a safe default for terminals.
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(w))
	if err == nil {
		m.glam = r
	}
}

// rebuild re-renders the whole event stream into the viewport, tracking the line
// offset of each event for click mapping.
func (m *model) rebuild() {
	if !m.ready {
		return
	}
	var b strings.Builder
	m.eventStart = m.eventStart[:0]
	line := 0
	for i, ev := range m.evs {
		m.eventStart = append(m.eventStart, line)
		block := m.renderBlock(i, ev)
		b.WriteString(block)
		b.WriteByte('\n')
		line += strings.Count(block, "\n") + 1
	}
	m.vp.SetContent(b.String())
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("\n  error: %v\n\n  (ctrl+c to quit)\n", m.err)
	}
	if m.state == stateMenu {
		return m.menuView()
	}
	return m.sessionView()
}

func (m model) menuView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" ycc — home ") + "\n\n")
	if len(m.modes) == 0 {
		b.WriteString("  loading modes…\n")
	}
	for i, mode := range m.modes {
		cursor := "  "
		label := fmt.Sprintf("%-9s %s", mode.Name, dimStyle.Render(mode.Description))
		if i == m.cursor {
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render(fmt.Sprintf("%-9s ", mode.Name)) + dimStyle.Render(mode.Description)
		}
		b.WriteString("  " + cursor + label + "\n")
	}
	b.WriteString("\n  " + m.prompt.View() + "\n")
	b.WriteString("\n" + dimStyle.Render("  ↑/↓ choose mode · type a prompt · enter start · esc quit"))
	return b.String()
}

func (m model) sessionView() string {
	statusTxt := fmt.Sprintf(" %s · mode:%s · %s ", short(m.sessionID), m.mode, m.status)
	if m.pending != "" {
		statusTxt = askStyle.Render(" ? answer below ") + statusTxt
	}
	top := headerStyle.Render(statusTxt)
	body := ""
	if m.ready {
		body = m.vp.View()
	}
	help := dimStyle.Render(" enter send/expand · ↑↓ select · click expand · pgup/pgdn scroll · esc menu")
	return top + "\n" + body + "\n " + m.input.View() + "\n" + help
}

// --- per-event rendering ---

func autoExpand(t string) bool { return t == "session_idle" || t == "question_asked" }

func (m *model) renderBlock(i int, ev *v1.Event) string {
	body := m.bodyFor(ev)
	hasBody := strings.TrimSpace(body) != ""
	exp := m.expanded[int(ev.Seq)] || autoExpand(ev.Type)
	header := m.renderHeader(ev, i == m.selected, exp && hasBody, hasBody)
	if exp && hasBody {
		return header + "\n" + body
	}
	return header
}

func (m *model) renderHeader(ev *v1.Event, selected, expanded, hasBody bool) string {
	bar := "  "
	if selected {
		bar = selBarStyle.Render("▌ ")
	}
	indent := ""
	if isSub(ev.Actor) {
		indent = "  "
	}
	tri := "  "
	if hasBody {
		if expanded {
			tri = "▼ "
		} else {
			tri = "▸ "
		}
	}
	avail := m.w - len(indent) - 21
	if avail < 12 {
		avail = 12
	}
	return fmt.Sprintf("%s%s%s%s %s",
		bar, indent, dimStyle.Render(tri),
		actorStyle(ev.Actor).Render(fmt.Sprintf("%-13s", ev.Actor)),
		typeStyle.Render(ev.Type)+" "+trunc(detailLine(ev), avail))
}

func (m *model) bodyFor(ev *v1.Event) string {
	if c, ok := m.bodyCache[int(ev.Seq)]; ok {
		return c
	}
	c := m.renderBody(ev)
	m.bodyCache[int(ev.Seq)] = c
	return c
}

func (m *model) renderBody(ev *v1.Event) string {
	switch ev.Type {
	case "model_turn", "session_idle", "user_input", "question_asked", "question_answered":
		txt := firstField(ev, "text", "report", "question", "answer")
		if txt == "" {
			return ""
		}
		return indentLines(m.markdown(txt), "  ")
	case "tool_call":
		return indentLines(prettyArgs(dataField(ev, "args")), bodyBar)
	case "tool_result":
		r := dataField(ev, "result")
		if r == "" {
			return ""
		}
		return indentLines(highlightResult(r), bodyBar)
	case "review_submitted":
		txt := fmt.Sprintf("%s — %s\n%s", dataField(ev, "model"), dataField(ev, "verdict"), dataField(ev, "summary"))
		return indentLines(m.markdown(txt), "  ")
	case "session_error":
		return indentLines(errStyle.Render(dataField(ev, "msg")), bodyBar)
	default:
		return ""
	}
}

func (m *model) markdown(s string) string {
	if m.glam == nil {
		return s
	}
	out, err := m.glam.Render(s)
	if err != nil {
		return s
	}
	return strings.Trim(out, "\n")
}

// --- highlighting helpers ---

const bodyBar = "  │ "

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

func looksDiff(s string) bool {
	return strings.Contains(s, "diff --git ") || strings.Contains(s, "\n@@ ") || strings.HasPrefix(s, "@@ ")
}

func looksCatN(s string) bool {
	first := s
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		first = s[:i]
	}
	return catnRe.MatchString(first)
}

func colorizeDiff(s string) string {
	var b strings.Builder
	for _, ln := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(ln, "+++") || strings.HasPrefix(ln, "---"):
			b.WriteString(diffMetaStyle.Render(ln))
		case strings.HasPrefix(ln, "@@"):
			b.WriteString(diffHunkStyle.Render(ln))
		case strings.HasPrefix(ln, "+"):
			b.WriteString(diffAddStyle.Render(ln))
		case strings.HasPrefix(ln, "-"):
			b.WriteString(diffDelStyle.Render(ln))
		case strings.HasPrefix(ln, "diff ") || strings.HasPrefix(ln, "index "):
			b.WriteString(dimStyle.Render(ln))
		default:
			b.WriteString(ln)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func dimLineNumbers(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if mm := catnRe.FindStringSubmatch(ln); mm != nil {
			lines[i] = dimStyle.Render(mm[1]) + mm[2]
		}
	}
	return strings.Join(lines, "\n")
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

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// --- styles ---

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("63")).Padding(0, 1)
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("238")).Padding(0, 1)
	selStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))
	selBarStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("213"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	typeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	askStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("11"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	diffAddStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	diffDelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	diffHunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("44"))
	diffMetaStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("250"))
)

func actorStyle(actor string) lipgloss.Style {
	switch {
	case actor == "coordinator":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("44"))
	case actor == "implementer":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	case strings.HasPrefix(actor, "reviewer"):
		return lipgloss.NewStyle().Foreground(lipgloss.Color("170"))
	case actor == "user":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	default:
		return dimStyle
	}
}

func isSub(actor string) bool {
	return actor == "implementer" || strings.HasPrefix(actor, "reviewer")
}

func detailLine(ev *v1.Event) string {
	switch ev.Type {
	case "tool_call":
		return fmt.Sprintf("%s(%s)", dataField(ev, "name"), oneLine(dataField(ev, "args"), 70))
	case "tool_result":
		return oneLine(dataField(ev, "result"), 90)
	case "model_turn":
		return oneLine(dataField(ev, "text"), 120)
	case "user_input":
		return "› " + oneLine(dataField(ev, "text"), 120)
	case "question_asked":
		return "? " + oneLine(dataField(ev, "question"), 120)
	case "question_answered":
		return oneLine(dataField(ev, "answer"), 100)
	case "subagent_spawned", "subagent_finished":
		return strings.TrimSpace(dataField(ev, "role") + " " + dataField(ev, "model"))
	case "review_submitted":
		return fmt.Sprintf("%s: %s — %s", dataField(ev, "model"), dataField(ev, "verdict"), oneLine(dataField(ev, "summary"), 80))
	case "commit_made":
		return dataField(ev, "sha") + " " + oneLine(dataField(ev, "message"), 80)
	case "doc_updated":
		return strings.TrimSpace(dataField(ev, "task") + " " + dataField(ev, "section") + " " + dataField(ev, "status"))
	case "mode_changed":
		return dataField(ev, "from") + " → " + dataField(ev, "to")
	case "session_idle":
		return oneLine(dataField(ev, "report"), 120)
	case "session_error":
		return oneLine(dataField(ev, "msg"), 120)
	}
	return ""
}

func dataField(ev *v1.Event, key string) string {
	if ev.DataJson == "" {
		return ""
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return ""
	}
	switch v := mp[key].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	}
	return ""
}

func firstField(ev *v1.Event, keys ...string) string {
	for _, k := range keys {
		if v := dataField(ev, k); v != "" {
			return v
		}
	}
	return ""
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func oneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return trunc(s, n)
}

func trunc(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	if n < 1 {
		n = 1
	}
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r) + "…"
}
