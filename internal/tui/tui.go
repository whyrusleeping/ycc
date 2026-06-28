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
	"os"
	"path/filepath"
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

	"github.com/whyrusleeping/ycc/internal/clientconfig"
)

type state int

const (
	statePicker state = iota
	stateMenu
	stateSession
)

const headerHeight = 1 // the session status bar occupies the first row

type model struct {
	client    yccv1connect.SessionServiceClient
	ctx       context.Context
	workspace string

	// project scoping (spec §3.1). When attached to a persistent/remote daemon
	// the picker selects a project; one-shot leaves these empty (cwd is the
	// single implicit project) and skips the picker.
	showPicker bool
	project    string            // selected project name ("" => use workspace)
	projects   []*v1.ProjectInfo // registered projects for the picker
	projectCur int               // cursor in the project picker

	state   state
	entries []menuEntry // modes + presets, in menu order
	cursor  int
	prompt  textinput.Model

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
	paused  bool // session is paused-to-steer (spec §18.7)

	// picker state: when the pending question carries options, the footer shows
	// a navigable list instead of the textinput until the user picks "other…".
	pickerOpts   []string // suggested answers ("" sentinel handled separately)
	pickerCursor int      // index into pickerOpts; len(pickerOpts) == "other…"
	picking      bool     // true while the picker (not the textarea) has focus

	err   error
	ready bool
	w, h  int

	// settings overlay (spec §18.2): modal over both menu and session, opened by
	// Esc. It exposes interaction level, per-role model config, UI prefs, and Quit.
	overlay      bool
	ovCursor     int
	models       []*v1.ModelInfo   // populated from ListModels
	level        string            // current interaction level (session)
	thinkLevels  map[string]string // per-role thinking levels (coordinator|implementer|reviewers)
	roleCoord    string            // logical model driving the coordinator
	roleImpl     string            // logical model for the implementer
	roleReviewrs []string          // logical models for reviewers (multi-select)
	reviewerSub  int               // rotating sub-index for toggling reviewer membership
	prefs        clientconfig.Prefs

	// backlog browser (spec §18.5): modal over menu/session, opened with ctrl+b.
	// Read-only: lists tasks, drills into one task's full detail.
	backlog       bool
	backlogTasks  []*v1.BacklogTaskSummary
	backlogCursor int
	backlogDetail *v1.TaskDetail // nil => list view; set => detail view

	// quick-add backlog capture overlay (spec §18.2, task 0016): modal over
	// menu/session, opened with ctrl+n. It runs a lightweight, off-stream capture
	// agent server-side so the running session is undisturbed.
	capture         bool
	captureInput    textinput.Model
	captureStage    int    // 0 describe · 1 answer clarification · 2 created (dismiss)
	captureQuestion string // the agent's clarifying question (stage 1)
	captureDesc     string // the original description (carried into stage 1)
	captureMsg      string // status/result/error line
	captureBusy     bool   // a CaptureBacklogItem RPC is in flight
}

// Run starts the TUI against the daemon client. showPicker selects the initial
// project-picker screen (persistent/remote daemon); a one-shot daemon passes
// false so the cwd is the single implicit project (spec §3.1).
func Run(ctx context.Context, client yccv1connect.SessionServiceClient, workspace string, showPicker bool) error {
	p := tea.NewProgram(initialModel(ctx, client, workspace, showPicker), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

func initialModel(ctx context.Context, client yccv1connect.SessionServiceClient, workspace string, showPicker bool) model {
	prefs := clientconfig.Load()
	prompt := textinput.New()
	prompt.Placeholder = "what should the agent do? (optional for 'work')"
	prompt.Focus()
	prompt.CharLimit = 8000
	prompt.Width = 60

	input := textinput.New()
	input.Placeholder = "type to prod / answer · Enter sends · ↑↓ select · click to expand"
	input.CharLimit = 8000
	input.Width = 60

	captureInput := textinput.New()
	captureInput.Placeholder = "describe a new backlog item…"
	captureInput.CharLimit = 8000
	captureInput.Width = 60

	initState := stateMenu
	if showPicker {
		initState = statePicker
	}
	return model{
		client: client, ctx: ctx, workspace: workspace,
		showPicker: showPicker,
		state:      initState, prompt: prompt, input: input,
		captureInput: captureInput,
		events:       make(chan *v1.Event, 256), status: "starting",
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1, follow: prefs.Follow,
		prefs: prefs, level: "judgement",
		thinkLevels: map[string]string{"coordinator": "high", "implementer": "high", "reviewers": "high"},
	}
}

// --- messages ---

type modesMsg struct {
	modes   []*v1.Mode
	presets []*v1.Preset
}

// menuEntry is a single home-menu row: either a mode (openingPrompt empty) or a
// preset (openingPrompt set). Selecting it starts a session in `mode`; for a
// preset the openingPrompt seeds the session when the user typed nothing.
type menuEntry struct {
	label         string
	description   string
	mode          string
	openingPrompt string
	prominent     bool // surfaced at the top (e.g. onboarding on an un-onboarded workspace)
}
type modelsMsg struct{ models []*v1.ModelInfo }
type projectsMsg struct{ projects []*v1.ProjectInfo }
type startedMsg struct{ id, mode string }
type evMsg struct{ ev *v1.Event }
type streamClosedMsg struct{}
type errMsg struct{ err error }
type backlogMsg struct{ tasks []*v1.BacklogTaskSummary }
type taskDetailMsg struct{ task *v1.TaskDetail }

// captureResultMsg carries the outcome of a CaptureBacklogItem RPC (task 0016):
// a created task (taskID/title) or a single clarifying question, or an error.
type captureResultMsg struct {
	taskID, title, question string
	err                     error
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.fetchModes, m.fetchModels}
	if m.showPicker {
		cmds = append(cmds, m.fetchProjects)
	}
	return tea.Batch(cmds...)
}

func (m model) fetchProjects() tea.Msg {
	resp, err := m.client.ListProjects(m.ctx, connect.NewRequest(&v1.ListProjectsRequest{}))
	if err != nil {
		return errMsg{err}
	}
	return projectsMsg{resp.Msg.Projects}
}

func (m model) fetchModes() tea.Msg {
	resp, err := m.client.ListModes(m.ctx, connect.NewRequest(&v1.ListModesRequest{}))
	if err != nil {
		return errMsg{err}
	}
	return modesMsg{modes: resp.Msg.Modes, presets: resp.Msg.Presets}
}

func (m model) fetchModels() tea.Msg {
	resp, err := m.client.ListModels(m.ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		return nil // models are optional for the overlay; don't surface as a fatal error
	}
	return modelsMsg{resp.Msg.Models}
}

func (m model) startSession(mode, prompt string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.StartSession(m.ctx, connect.NewRequest(&v1.StartSessionRequest{
			Mode: mode, Prompt: prompt, Workspace: m.workspace, Project: m.project,
		}))
		if err != nil {
			return errMsg{err}
		}
		return startedMsg{id: resp.Msg.SessionId, mode: mode}
	}
}

// addProject registers the current workspace as a project and refreshes the
// picker list (spec §3.1).
func (m model) addProject(path string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.AddProject(m.ctx, connect.NewRequest(&v1.AddProjectRequest{Path: path})); err != nil {
			return errMsg{err}
		}
		return m.fetchProjects()
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

// interrupt gracefully pauses the running session at its next safe checkpoint
// (spec §18.7) so the user can steer or resume.
func (m model) interrupt() tea.Cmd {
	return func() tea.Msg {
		if m.sessionID == "" {
			return nil
		}
		if _, err := m.client.Interrupt(m.ctx, connect.NewRequest(&v1.InterruptRequest{SessionId: m.sessionID})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// resume continues a paused session unchanged (spec §18.7).
func (m model) resume() tea.Cmd {
	return func() tea.Msg {
		if m.sessionID == "" {
			return nil
		}
		if _, err := m.client.Resume(m.ctx, connect.NewRequest(&v1.ResumeRequest{SessionId: m.sessionID})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// answerQuestion sends a structured answer to a pending question: optIdx >= 0
// selects a suggested option (resolved to its text on the daemon), otherwise
// optIdx is -1 and text is taken as free text.
func (m model) answerQuestion(optIdx int, text string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.AnswerQuestion(m.ctx, connect.NewRequest(&v1.AnswerQuestionRequest{
			SessionId: m.sessionID, Text: text, OptionIndex: int32(optIdx),
		}))
		if err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// setLevel issues SetInteractionLevel for the current session (spec §18.2).
func (m model) setLevel(level string) tea.Cmd {
	return func() tea.Msg {
		if m.sessionID == "" {
			return nil
		}
		if _, err := m.client.SetInteractionLevel(m.ctx, connect.NewRequest(&v1.SetInteractionLevelRequest{
			SessionId: m.sessionID, Level: level,
		})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// setThinking issues SetThinking for the current session per role (spec §7.4,
// §18.2). An empty role updates all roles.
func (m model) setThinking(role, level string) tea.Cmd {
	return func() tea.Msg {
		if m.sessionID == "" {
			return nil
		}
		if _, err := m.client.SetThinking(m.ctx, connect.NewRequest(&v1.SetThinkingRequest{
			SessionId: m.sessionID, Level: level, Role: role,
		})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// setRoleConfig issues SetRoleConfig for the current session (spec §18.2).
func (m model) setRoleConfig(coord, impl string, reviewers []string) tea.Cmd {
	return func() tea.Msg {
		if m.sessionID == "" {
			return nil
		}
		if _, err := m.client.SetRoleConfig(m.ctx, connect.NewRequest(&v1.SetRoleConfigRequest{
			SessionId: m.sessionID, Coordinator: coord, Implementer: impl, Reviewers: reviewers,
		})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// fetchBacklog loads the backlog summary rows for the backlog browser (spec §18.5).
func (m model) fetchBacklog() tea.Msg {
	resp, err := m.client.ListBacklog(m.ctx, connect.NewRequest(&v1.ListBacklogRequest{Project: m.project}))
	if err != nil {
		return errMsg{err}
	}
	return backlogMsg{resp.Msg.Tasks}
}

// fetchTask loads one task's full detail for the backlog browser (spec §18.5).
func (m model) fetchTask(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.GetTask(m.ctx, connect.NewRequest(&v1.GetTaskRequest{Project: m.project, Id: id}))
		if err != nil {
			return errMsg{err}
		}
		return taskDetailMsg{resp.Msg.Task}
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
		m.captureInput.Width = msg.Width - 4
		m.makeRenderer()
		m.bodyCache = map[int]string{} // re-render bodies at the new width
		m.rebuild()
		return m, nil

	case modesMsg:
		m.entries = m.entries[:0]
		for _, md := range msg.modes {
			m.entries = append(m.entries, menuEntry{label: md.Name, description: md.Description, mode: md.Name})
		}
		for _, p := range msg.presets {
			m.entries = append(m.entries, menuEntry{label: p.Name, description: p.Description, mode: p.Mode, openingPrompt: p.OpeningPrompt})
		}
		// When the workspace looks un-onboarded, surface the onboarding entry
		// prominently at the top of the menu (spec §19.2). It stays a normal
		// preset otherwise ("onboard later" is valid).
		if needsOnboarding(m.workspace) {
			for i := range m.entries {
				if m.entries[i].label == "onboard" {
					e := m.entries[i]
					e.prominent = true
					m.entries = append(m.entries[:i], m.entries[i+1:]...)
					m.entries = append([]menuEntry{e}, m.entries...)
					break
				}
			}
		}
		return m, nil
	case modelsMsg:
		m.models = msg.models
		return m, nil
	case projectsMsg:
		m.projects = msg.projects
		if m.projectCur >= len(m.projects) {
			m.projectCur = 0
		}
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
	case backlogMsg:
		m.backlogTasks = msg.tasks
		if m.backlogCursor >= len(m.backlogTasks) {
			m.backlogCursor = 0
		}
		return m, nil
	case taskDetailMsg:
		m.backlogDetail = msg.task
		return m, nil
	case captureResultMsg:
		m.captureBusy = false
		if msg.err != nil {
			m.captureMsg = "error: " + msg.err.Error()
			return m, nil
		}
		if msg.taskID != "" {
			m.captureStage = 2
			m.captureMsg = "created " + msg.taskID + ": " + msg.title
			return m, nil
		}
		if msg.question != "" {
			m.captureStage = 1
			m.captureQuestion = msg.question
			m.captureInput.SetValue("")
			m.captureInput.Focus()
			return m, nil
		}
		m.captureMsg = "(no result)"
		return m, nil
	}

	// The project picker (spec §3.1) is shown first when attached to a
	// persistent/remote daemon; it owns input until a project is chosen.
	if m.state == statePicker {
		return m.updatePicker(msg)
	}

	// The quick-add backlog capture overlay (ctrl+n) is modal over both the menu
	// and a session (spec §18.2, task 0016). It runs entirely server-side so the
	// session keeps streaming behind it.
	if m.capture {
		return m.updateCapture(msg)
	}

	// The backlog browser (ctrl+b) is modal over both the menu and a session
	// (spec §18.5).
	if m.backlog {
		return m.updateBacklog(msg)
	}

	// The settings overlay (Esc) is modal over BOTH the menu and a session.
	if m.overlay {
		return m.updateOverlay(msg)
	}
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		// Esc opens the overlay rather than leaving the session (spec §18.2).
		m.openOverlay()
		return m, nil
	}

	if m.state == stateMenu {
		return m.updateMenu(msg)
	}
	return m.updateSession(msg)
}

// openOverlay enters the modal settings overlay, seeding role defaults from the
// configured models when this is a fresh session.
func (m *model) openOverlay() {
	m.overlay = true
	m.ovCursor = 0
	if m.roleCoord == "" && len(m.models) > 0 {
		m.roleCoord = m.models[0].Name
		m.roleImpl = m.models[0].Name
		m.roleReviewrs = []string{m.models[0].Name}
	}
}

// updatePicker handles the project-picker screen (spec §3.1): navigate the list
// of registered projects, Enter scopes the session UI to one, `a` registers the
// current workspace as a new project.
func (m model) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "up":
		if m.projectCur > 0 {
			m.projectCur--
		}
		return m, nil
	case "down":
		if m.projectCur < len(m.projects)-1 {
			m.projectCur++
		}
		return m, nil
	case "a":
		// Register the current workspace, then refresh the list.
		return m, m.addProject(m.workspace)
	case "enter":
		if len(m.projects) == 0 {
			// Nothing registered yet: register the cwd and continue once listed.
			return m, m.addProject(m.workspace)
		}
		p := m.projects[m.projectCur]
		m.project = p.Name
		m.workspace = p.Path
		m.state = stateMenu
		return m, nil
	}
	return m, nil
}

// openCapture enters the quick-add backlog capture overlay (task 0016), resetting
// it to the "describe" stage with a focused, empty input.
func (m *model) openCapture() {
	m.capture = true
	m.captureStage = 0
	m.captureQuestion = ""
	m.captureDesc = ""
	m.captureMsg = ""
	m.captureBusy = false
	m.captureInput.SetValue("")
	m.captureInput.Focus()
}

func (m model) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+n":
			// Quick-add a backlog item (spec §18.2, task 0016).
			m.openCapture()
			return m, nil
		case "ctrl+b":
			// Open the read-only backlog browser (spec §18.5).
			m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
			return m, m.fetchBacklog
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
			return m, nil
		case "enter":
			if len(m.entries) == 0 {
				return m, nil
			}
			e := m.entries[m.cursor]
			// A typed prompt always wins; otherwise a preset seeds its opening prompt.
			prompt := m.prompt.Value()
			if strings.TrimSpace(prompt) == "" {
				prompt = e.openingPrompt
			}
			return m, m.startSession(e.mode, prompt)
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
			m.follow = m.vp.AtBottom()
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
		// When a question with options is pending, the footer is a picker that
		// owns ↑/↓/enter until the user chooses "other…" to free-type.
		if m.picking {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "up":
				if m.pickerCursor > 0 {
					m.pickerCursor--
				}
				return m, nil
			case "down":
				if m.pickerCursor < len(m.pickerOpts) {
					m.pickerCursor++
				}
				return m, nil
			case "enter":
				if m.pickerCursor >= len(m.pickerOpts) {
					// "other…": drop into the free-text textarea.
					m.picking = false
					m.input.Focus()
					return m, nil
				}
				idx := m.pickerCursor
				m.picking = false
				m.pending = ""
				m.pickerOpts = nil
				m.follow = true
				return m, m.answerQuestion(idx, "")
			}
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+n":
			// Quick-add a backlog item without pausing the session (task 0016).
			m.openCapture()
			return m, nil
		case "ctrl+b":
			// Open the read-only backlog browser (spec §18.5).
			m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
			return m, m.fetchBacklog
		case "ctrl+i":
			// Gracefully interrupt the running agent to steer it (spec §18.7).
			if !m.paused {
				return m, m.interrupt()
			}
			return m, nil
		case "up":
			m.moveSelection(-1)
			return m, nil
		case "down":
			m.moveSelection(1)
			return m, nil
		case "pgup":
			m.vp.HalfPageUp()
			m.follow = m.vp.AtBottom()
			return m, nil
		case "pgdown":
			m.vp.HalfPageDown()
			m.follow = m.vp.AtBottom()
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				// While paused, an empty Enter resumes the agent unchanged (§18.7).
				if m.paused {
					return m, m.resume()
				}
				// Empty input: Enter expands/collapses the selected turn.
				if m.selected >= 0 {
					m.toggle(m.selected)
				}
				return m, nil
			}
			m.input.SetValue("")
			// While paused, a non-empty Enter steers: send the correction AND
			// resume so the agent continues with it (spec §18.7).
			if m.paused {
				m.follow = true
				return m, tea.Sequence(m.sendInput(text), m.resume())
			}
			// If a question is pending, answer it as free text (option_index -1);
			// otherwise it's a prod handled by SendInput.
			if m.pending != "" {
				m.pending = ""
				m.follow = true
				return m, m.answerQuestion(-1, text)
			}
			m.follow = true
			return m, m.sendInput(text)
		}
	}
	if m.picking {
		return m, nil
	}
	var icmd tea.Cmd
	m.input, icmd = m.input.Update(msg)
	return m, icmd
}

// --- quick-add backlog capture overlay (spec §18.2, task 0016) ---

// updateCapture handles the modal quick-add overlay: describe an item, optionally
// answer one clarifying question, then see the created task id. The capture runs
// server-side (a separate off-stream agent), so the main session keeps streaming
// behind the overlay — opening or using it never pauses the running session.
func (m model) updateCapture(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var c tea.Cmd
		m.captureInput, c = m.captureInput.Update(msg)
		return m, c
	}
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.capture = false
		return m, nil
	case "enter":
		if m.captureBusy {
			return m, nil
		}
		if m.captureStage == 2 {
			// Dismiss after a successful creation.
			m.capture = false
			return m, nil
		}
		val := strings.TrimSpace(m.captureInput.Value())
		if val == "" {
			return m, nil
		}
		if m.captureStage == 0 {
			m.captureDesc = val
			m.captureBusy = true
			m.captureMsg = ""
			m.captureInput.SetValue("")
			return m, m.captureSubmit(m.captureDesc, "", "")
		}
		// Stage 1: answering the agent's clarifying question.
		m.captureBusy = true
		m.captureMsg = ""
		ans := val
		m.captureInput.SetValue("")
		return m, m.captureSubmit(m.captureDesc, m.captureQuestion, ans)
	default:
		var c tea.Cmd
		m.captureInput, c = m.captureInput.Update(msg)
		return m, c
	}
}

// captureSubmit issues the CaptureBacklogItem RPC, scoped to the current project,
// returning a captureResultMsg. It does not touch the session stream.
func (m model) captureSubmit(desc, q, a string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.CaptureBacklogItem(m.ctx, connect.NewRequest(&v1.CaptureBacklogItemRequest{
			Project: m.project, Description: desc, PriorQuestion: q, PriorAnswer: a,
		}))
		if err != nil {
			return captureResultMsg{err: err}
		}
		return captureResultMsg{taskID: resp.Msg.TaskId, title: resp.Msg.Title, question: resp.Msg.Question}
	}
}

// captureView renders the quick-add backlog capture overlay.
func (m model) captureView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" capture backlog item ") + "\n\n")
	switch m.captureStage {
	case 0:
		b.WriteString("  Describe a new backlog item:\n\n")
		b.WriteString("  " + m.captureInput.View() + "\n")
	case 1:
		b.WriteString("  " + selStyle.Render("The capture agent asks:") + "\n")
		b.WriteString("  " + m.captureQuestion + "\n\n")
		b.WriteString("  Your answer:\n\n")
		b.WriteString("  " + m.captureInput.View() + "\n")
	case 2:
		b.WriteString("  " + selStyle.Render(m.captureMsg) + "\n")
	}
	if m.captureBusy {
		b.WriteString("\n" + dimStyle.Render("  capturing…"))
	} else if strings.HasPrefix(m.captureMsg, "error:") {
		b.WriteString("\n  " + selStyle.Render(m.captureMsg))
	}
	hint := "  enter submit · esc cancel"
	if m.captureStage == 2 {
		hint = "  enter/esc close"
	}
	b.WriteString("\n\n" + dimStyle.Render(hint))
	b.WriteString("\n" + dimStyle.Render("  (the running session keeps going — capture is off-stream)"))
	return b.String()
}

// --- backlog browser (spec §18.5) ---

// updateBacklog handles the modal backlog browser: a task list with drill-down
// into a single task's read-only detail.
func (m model) updateBacklog(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.backlogDetail != nil {
		// Detail view: back returns to the list.
		switch key.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc", "backspace", "left":
			m.backlogDetail = nil
		}
		return m, nil
	}
	// List view.
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.backlog = false
		return m, nil
	case "up":
		if m.backlogCursor > 0 {
			m.backlogCursor--
		}
		return m, nil
	case "down":
		if m.backlogCursor < len(m.backlogTasks)-1 {
			m.backlogCursor++
		}
		return m, nil
	case "enter":
		if len(m.backlogTasks) > 0 {
			return m, m.fetchTask(m.backlogTasks[m.backlogCursor].Id)
		}
		return m, nil
	}
	return m, nil
}

// backlogView renders the modal backlog browser (list or detail).
func (m model) backlogView() string {
	if m.backlogDetail != nil {
		return m.taskDetailView(m.backlogDetail)
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render(" ycc — backlog ") + "\n\n")
	if len(m.backlogTasks) == 0 {
		b.WriteString("  " + dimStyle.Render("(no backlog tasks)") + "\n")
	}
	for i, t := range m.backlogTasks {
		cursor := "  "
		row := fmt.Sprintf("%-5s %-12s p%d  %s", t.Id, t.Status, t.Priority, t.Title)
		var tag string
		if t.Status != "done" {
			if t.Ready {
				tag = " " + dimStyle.Render("[ready]")
			} else {
				tag = " " + dimStyle.Render("[blocked by "+strings.Join(t.BlockedBy, ", ")+"]")
			}
		}
		if i == m.backlogCursor {
			cursor = selStyle.Render("▸ ")
			row = selStyle.Render(row)
		}
		b.WriteString("  " + cursor + row + tag + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("  ↑/↓ select · enter inspect · esc close"))
	return b.String()
}

// taskDetailView renders a single task's full, read-only detail (spec §18.5).
func (m model) taskDetailView(t *v1.TaskDetail) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" "+t.Id+" — "+t.Title+" ") + "\n\n")
	readiness := "ready"
	if t.Status == "done" {
		readiness = "done"
	} else if !t.Ready {
		readiness = "blocked by " + strings.Join(t.BlockedBy, ", ")
	}
	meta := fmt.Sprintf("status:%s · p%d · %s", t.Status, t.Priority, readiness)
	if len(t.DependsOn) > 0 {
		meta += " · deps: " + strings.Join(t.DependsOn, ", ")
	}
	if len(t.SpecRefs) > 0 {
		meta += " · spec: " + strings.Join(t.SpecRefs, ", ")
	}
	b.WriteString("  " + dimStyle.Render(meta) + "\n\n")
	body := t.Body
	if m.glam != nil {
		if out, err := m.glam.Render(body); err == nil {
			body = strings.Trim(out, "\n")
		}
	}
	b.WriteString(indentLines(body, "  "))
	b.WriteString("\n\n" + dimStyle.Render("  esc/← back · ctrl+c quit"))
	return b.String()
}

// --- settings overlay (spec §18.2) ---

// overlay rows (indices into the navigable list).
const (
	ovLevel = iota
	ovCoord
	ovImpl
	ovReviewers
	ovTheme
	ovFollow
	ovApplyRoles
	ovBackHome
	ovQuit
	ovCount
)

var (
	levels      = []string{"interactive", "judgement", "autonomous"}
	thinkLevels = []string{"off", "low", "medium", "high", "xhigh", "max"}
	themes      = []string{"dark", "light"}
)

func (m model) updateOverlay(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "esc":
		// Esc closes the overlay without leaving the session (spec §18.2).
		m.overlay = false
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "up":
		if m.ovCursor > 0 {
			m.ovCursor--
		} else {
			m.ovCursor = ovCount - 1
		}
		return m, nil
	case "down":
		if m.ovCursor < ovCount-1 {
			m.ovCursor++
		} else {
			m.ovCursor = 0
		}
		return m, nil
	case "left":
		return m.overlayAdjust(-1)
	case "right":
		return m.overlayAdjust(1)
	case "+", "=":
		return m.overlayAdjustThinking(1)
	case "-", "_":
		return m.overlayAdjustThinking(-1)
	case " ", "space":
		if m.ovCursor == ovReviewers {
			m.toggleReviewer()
		}
		return m, nil
	case "enter":
		return m.overlayActivate()
	}
	return m, nil
}

// overlayAdjust cycles the value under the cursor (left/right).
func (m model) overlayAdjust(d int) (tea.Model, tea.Cmd) {
	switch m.ovCursor {
	case ovLevel:
		m.level = cycle(levels, m.level, d)
		return m, m.setLevel(m.level)
	case ovCoord:
		m.roleCoord = cycleModel(m.models, m.roleCoord, d)
		return m, nil
	case ovImpl:
		m.roleImpl = cycleModel(m.models, m.roleImpl, d)
		return m, nil
	case ovTheme:
		m.prefs.Theme = cycle(themes, m.prefs.Theme, d)
		clientconfig.Save(m.prefs)
		return m, nil
	case ovFollow:
		m.prefs.Follow = !m.prefs.Follow
		m.follow = m.prefs.Follow
		clientconfig.Save(m.prefs)
		return m, nil
	}
	return m, nil
}

// overlayAdjustThinking cycles the per-role thinking level under the cursor
// (+/-). The thinking level lives inline on each role's row (e.g. "claude opus
// (xhigh)") rather than as a separate menu entry.
func (m model) overlayAdjustThinking(d int) (tea.Model, tea.Cmd) {
	var role string
	switch m.ovCursor {
	case ovCoord:
		role = "coordinator"
	case ovImpl:
		role = "implementer"
	case ovReviewers:
		role = "reviewers"
	default:
		return m, nil
	}
	m.thinkLevels[role] = cycle(thinkLevels, m.thinkLevels[role], d)
	return m, m.setThinking(role, m.thinkLevels[role])
}

// overlayActivate runs the action under the cursor (enter).
func (m model) overlayActivate() (tea.Model, tea.Cmd) {
	switch m.ovCursor {
	case ovReviewers:
		m.toggleReviewer()
		return m, nil
	case ovApplyRoles:
		// Commit per-role assignment; reviewers must be non-empty to apply.
		revs := m.roleReviewrs
		if len(revs) == 0 && len(m.models) > 0 {
			revs = []string{m.models[0].Name}
			m.roleReviewrs = revs
		}
		m.overlay = false
		return m, m.setRoleConfig(m.roleCoord, m.roleImpl, revs)
	case ovBackHome:
		// Explicit, intentional exit from the session (spec §18.2).
		m.overlay = false
		m.state = stateMenu
		return m, nil
	case ovQuit:
		return m, tea.Quit
	}
	return m, nil
}

// toggleReviewer flips the reviewers row's multi-select membership. Each
// space/enter toggles inclusion of one model, advancing a rotating sub-index so
// repeated presses walk through every configured model in turn.
func (m *model) toggleReviewer() {
	if len(m.models) == 0 {
		return
	}
	// Rotate a hidden sub-index across the model list, toggling membership.
	name := m.models[m.reviewerSub].Name
	if m.contains(name) {
		m.roleReviewrs = remove(m.roleReviewrs, name)
	} else {
		m.roleReviewrs = append(m.roleReviewrs, name)
	}
	m.reviewerSub = (m.reviewerSub + 1) % len(m.models)
}

func (m model) contains(name string) bool {
	for _, r := range m.roleReviewrs {
		if r == name {
			return true
		}
	}
	return false
}

func remove(s []string, name string) []string {
	out := s[:0]
	for _, v := range s {
		if v != name {
			out = append(out, v)
		}
	}
	return append([]string(nil), out...)
}

func cycle(vals []string, cur string, d int) string {
	idx := 0
	for i, v := range vals {
		if v == cur {
			idx = i
			break
		}
	}
	idx = (idx + d + len(vals)) % len(vals)
	return vals[idx]
}

func cycleModel(models []*v1.ModelInfo, cur string, d int) string {
	if len(models) == 0 {
		return cur
	}
	idx := 0
	for i, mm := range models {
		if mm.Name == cur {
			idx = i
			break
		}
	}
	idx = (idx + d + len(models)) % len(models)
	return models[idx].Name
}

func (m model) overlayView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" settings ") + "\n\n")
	rows := []struct{ label, val string }{
		{"interaction level", m.level},
		{"coordinator model", m.roleCoord + " (" + m.thinkLevels["coordinator"] + ")"},
		{"implementer model", m.roleImpl + " (" + m.thinkLevels["implementer"] + ")"},
		{"reviewers", strings.Join(m.roleReviewrs, ", ")},
		{"theme", m.prefs.Theme},
		{"follow / auto-scroll", boolStr(m.prefs.Follow)},
		{"apply role config", ""},
		{"back to home menu", ""},
		{"quit", ""},
	}
	for i, r := range rows {
		cursor := "  "
		label := fmt.Sprintf("%-22s", r.label)
		if i == m.ovCursor {
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render(label)
		}
		val := r.val
		if i == ovReviewers && len(m.models) > 0 {
			val = "(" + m.thinkLevels["reviewers"] + ")  " + m.reviewerSummary()
		}
		b.WriteString("  " + cursor + label + dimStyle.Render(val) + "\n")
	}
	help := "  ↑/↓ move · ←/→ change · +/- thinking · space toggle reviewer · enter activate · esc close"
	b.WriteString("\n" + dimStyle.Render(help))
	if m.sessionID == "" {
		b.WriteString("\n" + dimStyle.Render("  (no active session: level/role changes apply only within a session)"))
	}
	return b.String()
}

func (m model) reviewerSummary() string {
	var parts []string
	for _, mm := range m.models {
		mark := "[ ]"
		if m.contains(mm.Name) {
			mark = "[x]"
		}
		parts = append(parts, mark+" "+mm.Name)
	}
	return strings.Join(parts, "  ")
}

func boolStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
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
		m.pickerOpts = dataList(ev, "options")
		if len(m.pickerOpts) > 0 {
			m.picking = true
			m.pickerCursor = 0
			m.input.Blur()
		} else {
			m.picking = false
		}
	case "question_answered":
		m.pending = ""
		m.status = "running"
		m.pickerOpts = nil
		m.picking = false
	case "session_idle":
		m.status = "idle"
	case "session_error":
		m.status = "error"
	case "interrupted":
		m.status = "paused"
		m.paused = true
	case "resumed":
		m.status = "running"
		m.paused = false
	case "mode_changed":
		m.mode = dataField(ev, "to")
		m.status = "running"
	case "session_started":
		if lvl := dataField(ev, "interaction_level"); lvl != "" {
			m.level = lvl
		}
	case "interaction_level_changed":
		if to := dataField(ev, "to"); to != "" {
			m.level = to
		}
	case "thinking_level_changed":
		if to := dataField(ev, "to"); to != "" {
			role := dataField(ev, "role")
			if role == "" || role == "all" {
				m.thinkLevels["coordinator"] = to
				m.thinkLevels["implementer"] = to
				m.thinkLevels["reviewers"] = to
			} else {
				m.thinkLevels[role] = to
			}
		}
	case "role_config_changed":
		if c := dataField(ev, "coordinator"); c != "" {
			m.roleCoord = c
		}
		if i := dataField(ev, "implementer"); i != "" {
			m.roleImpl = i
		}
		if rv := dataList(ev, "reviewers"); len(rv) > 0 {
			m.roleReviewrs = rv
		}
	}
	// Clear a latched error status once real activity resumes (task 0051):
	// the header must not stay stuck on "error" after recovery.
	if m.status == "error" {
		switch ev.Type {
		case "model_turn", "tool_call", "tool_result", "thinking", "user_input":
			m.status = "running"
		}
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
	if m.capture {
		return m.captureView()
	}
	if m.backlog {
		return m.backlogView()
	}
	if m.overlay {
		return m.overlayView()
	}
	if m.state == statePicker {
		return m.pickerScreenView()
	}
	if m.state == stateMenu {
		return m.menuView()
	}
	return m.sessionView()
}

// pickerScreenView renders the project picker (spec §3.1).
func (m model) pickerScreenView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" ycc — projects ") + "\n\n")
	if len(m.projects) == 0 {
		b.WriteString("  " + dimStyle.Render("no projects registered yet") + "\n")
	}
	for i, p := range m.projects {
		cursor := "  "
		label := fmt.Sprintf("%-20s %s", p.Name, dimStyle.Render(p.Path))
		if i == m.projectCur {
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render(fmt.Sprintf("%-20s ", p.Name)) + dimStyle.Render(p.Path)
		}
		b.WriteString("  " + cursor + label + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("  ↑/↓ choose · enter open · a add current dir · q quit"))
	b.WriteString("\n" + dimStyle.Render("  cwd: "+m.workspace))
	return b.String()
}

func (m model) menuView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" ycc — home ") + "\n\n")
	if len(m.entries) == 0 {
		b.WriteString("  loading modes…\n")
	}
	for i, e := range m.entries {
		cursor := "  "
		label := fmt.Sprintf("%-9s %s", e.label, dimStyle.Render(e.description))
		switch {
		case i == m.cursor && e.prominent:
			// Selected AND recommended: keep the selection treatment but still
			// surface the ★ marker and "(recommended)" hint so onboarding reads
			// as recommended even when it's the default-selected row.
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render("★ "+fmt.Sprintf("%-7s ", e.label)) + dimStyle.Render(e.description+"  (recommended)")
		case i == m.cursor:
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render(fmt.Sprintf("%-9s ", e.label)) + dimStyle.Render(e.description)
		case e.prominent:
			// Surface a recommended entry (e.g. onboarding on an un-onboarded
			// workspace) so it stands out without stealing the cursor highlight.
			label = recoStyle.Render("★ "+fmt.Sprintf("%-7s ", e.label)) + dimStyle.Render(e.description+"  (recommended)")
		}
		b.WriteString("  " + cursor + label + "\n")
	}
	b.WriteString("\n  " + m.prompt.View() + "\n")
	b.WriteString("\n" + dimStyle.Render("  ↑/↓ choose mode · type a prompt · enter start · esc settings · ctrl+b backlog · ctrl+n new task"))
	return b.String()
}

// needsOnboarding reports whether a workspace looks un-onboarded (spec §19.2): it
// has no real spec.md AND no backlog tasks. It is conservative — on any unexpected
// read error it returns false so onboarding is not surfaced spuriously.
func needsOnboarding(workspace string) bool {
	if strings.TrimSpace(workspace) == "" {
		return false
	}
	return specIsEmpty(workspace) && !hasBacklogTasks(workspace)
}

// specIsEmpty reports whether spec.md is missing or trivially empty (only blank
// lines and markdown headings, no real content).
func specIsEmpty(workspace string) bool {
	data, err := os.ReadFile(filepath.Join(workspace, "spec.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return true
		}
		return false // unexpected error: treat as not-empty (don't surface onboarding)
	}
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		return false // real content
	}
	return true
}

// hasBacklogTasks reports whether backlog/ exists and contains at least one task
// file matching the NNNN-*.md pattern (the generated backlog.md index doesn't
// count).
func hasBacklogTasks(workspace string) bool {
	entries, err := os.ReadDir(filepath.Join(workspace, "backlog"))
	if err != nil {
		return false // missing dir (or unreadable): no tasks
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		stem := strings.TrimSuffix(name, ".md")
		dash := strings.IndexByte(stem, '-')
		if dash <= 0 {
			continue
		}
		if isAllDigits(stem[:dash]) {
			return true
		}
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (m model) sessionView() string {
	statusTxt := fmt.Sprintf(" %s · mode:%s · %s ", short(m.sessionID), m.mode, m.status)
	// Clamp the (plain) status to the terminal width so the header occupies a
	// single physical row: a wrapped header pushes the whole frame down by a row,
	// which is what lets the input box overlap the agent's last output line.
	if m.w > 0 {
		// headerStyle adds 1 col of padding on each side; reserve room for the
		// "? answer below" badge when a question is pending so it never wraps.
		// trunc may append a 1-col ellipsis, so reserve that column too.
		max := m.w - 2 - 1
		if m.pending != "" {
			max -= lipgloss.Width(askStyle.Render(" ? answer below "))
		}
		if max < 1 {
			max = 1
		}
		statusTxt = trunc(statusTxt, max)
	}
	if m.pending != "" {
		statusTxt = askStyle.Render(" ? answer below ") + statusTxt
	}
	top := headerStyle.Render(statusTxt)
	body := ""
	if m.ready {
		body = m.vp.View()
	}
	if m.picking {
		help := m.footer(" ↑↓ choose · enter select · esc settings")
		return top + "\n" + body + "\n" + m.pickerView() + "\n" + help
	}
	if m.paused {
		help := m.footer(" ⏸ paused — type a correction + enter to steer · enter to resume · esc settings")
		return top + "\n" + body + "\n " + m.input.View() + "\n" + help
	}
	help := m.footer(" enter send/expand · ↑↓ select · click expand · pgup/pgdn scroll · ctrl+i interrupt · esc settings · ctrl+b backlog · ctrl+n new task")
	return top + "\n" + body + "\n " + m.input.View() + "\n" + help
}

// footer renders a single-row help/status line, clamped to the terminal width so
// it can never wrap to a second physical row. Without this clamp a long help line
// wraps, overflowing the H-row frame and corrupting Bubble Tea's line accounting —
// which visually shows up as the input box overlapping the agent's last output
// line. A zero width (before the first WindowSizeMsg) is a no-op.
func (m model) footer(text string) string {
	if m.w > 0 {
		// trunc may append a 1-col ellipsis, so clamp to m.w-1 to stay within m.w.
		text = trunc(strings.ReplaceAll(text, "\n", " "), m.w-1)
	}
	return dimStyle.Render(text)
}

// pickerView renders the navigable list of suggested answers plus an "other…"
// escape into the free-text textarea.
func (m model) pickerView() string {
	var b strings.Builder
	if m.pending != "" {
		b.WriteString(" " + askStyle.Render(" ? ") + " " + oneLine(m.pending, m.w-6) + "\n")
	}
	rows := append(append([]string(nil), m.pickerOpts...), "other…")
	for i, opt := range rows {
		cursor := "  "
		label := opt
		// Clamp option text so a long suggestion can't wrap to a second physical
		// row (reserve the "  " + cursor "▸ " = 4 leading columns; trunc may add a
		// 1-col ellipsis, so reserve that too).
		if m.w > 0 {
			label = trunc(label, m.w-4-1)
		}
		if i == m.pickerCursor {
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render(label)
		}
		b.WriteString("  " + cursor + label + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
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
	case "thinking":
		// Render the reasoning summary dimmed + italic so it reads as the
		// model's "inner voice", distinct from its actual response (spec §18).
		txt := dataField(ev, "text")
		if txt == "" {
			return ""
		}
		return indentLines(thinkStyle.Render(txt), bodyBar)
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
	recoStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	selBarStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("213"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	thinkStyle    = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("245"))
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
	case "thinking":
		return dimStyle.Render("(reasoning) " + oneLine(dataField(ev, "text"), 110))
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

// dataList pulls a list-of-strings field from an event's data JSON, dropping
// non-string and empty entries. Returns nil when absent.
func dataList(ev *v1.Event, key string) []string {
	if ev.DataJson == "" {
		return nil
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return nil
	}
	raw, ok := mp[key].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
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
