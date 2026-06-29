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
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"

	"github.com/whyrusleeping/ycc/internal/clientconfig"
)

type state int

const (
	statePicker state = iota
	stateMenu
	stateHistory
	stateSession
)

const headerHeight = 1 // the session status bar occupies the first row

const maxInputRows = 6 // session input grows up to this many rows, then scrolls

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

	// previous-sessions screen (spec §18.6): a navigable list of persisted
	// sessions reached from the menu (ctrl+r). Enter reopens the selected one via
	// ResumeSession ("resume = replay").
	history       []*v1.SessionSummary
	historyCursor int
	historyMsgTxt string // status/error line for the history screen

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
	input   textarea.Model
	glam    *glamour.TermRenderer
	pending string
	status  string
	paused  bool // session is paused-to-steer (spec §18.7)

	// picker state: when the pending question carries options, the footer shows
	// a navigable list instead of the textinput until the user picks "other…".
	pickerOpts   []string // suggested answers ("" sentinel handled separately)
	pickerCursor int      // index into pickerOpts; len(pickerOpts) == "other…"
	picking      bool     // true while the picker (not the textarea) has focus

	// questionnaire wizard state: when an ask_user call poses MULTIPLE questions,
	// the user answers them one at a time (picker or free-text per question) and
	// all answers are submitted together at the end. wizActive gates this mode.
	wizActive    bool
	wizQuestions []wizQuestion // parsed questions (prompt + per-question options)
	wizAnswers   []wizAnswer   // collected answers, parallel to wizQuestions
	wizIdx       int           // index of the question currently being answered

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
	backlog         bool
	backlogTasks    []*v1.BacklogTaskSummary
	backlogCursor   int
	backlogDetail   *v1.TaskDetail // nil => list view; set => detail view
	backlogShowDone bool           // when false (default), done tasks are hidden in the list view

	// quick-add backlog capture overlay (spec §18.2, task 0016): modal over
	// menu/session, opened with ctrl+n. It runs a lightweight, off-stream capture
	// agent server-side so the running session is undisturbed.
	capture         bool
	captureInput    textinput.Model
	captureStage    int            // 0 describe · 1 answer clarification · 2 created (dismiss)
	captureQuestion string         // the agent's clarifying question (stage 1)
	captureDesc     string         // the original description (carried into stage 1)
	captureMsg      string         // status/result/error line
	captureBusy     bool           // a CaptureBacklogItem RPC is in flight
	captureEvents   chan *v1.Event // live capture agent action-log stream
	captureLog      []*v1.Event    // accumulated capture agent events for display

	// model-backends management modal (spec §18.2, task 0044): list / add / edit /
	// duplicate / remove logical model backends, wired to the 0041 RPCs
	// (ListModels/GetModelConfig/UpsertModel/RemoveModel). Opened from the settings
	// overlay's "model backends" row; modal over both menu and session.
	mbOpen       bool
	mbView       int    // 0=list · 1=form · 2=confirm-remove
	mbCursor     int    // cursor into m.models in the list view
	mbErr        string // inline error/validation message
	mbPersist    bool   // persist=true writes the change to ycc.toml
	mbFormMode   int    // mbAdd | mbEdit | mbDuplicate
	mbOrigName   string // name of the model loaded for edit/duplicate
	mbInputs     [mbNumFields]textinput.Model
	mbBackends   []string // per-form backend cycle list (preserves an unknown loaded backend)
	mbBackendIdx int
	mbThinkIdx   int
	mbEffortIdx  int
	mbDisplayIdx int
	mbPresetIdx  int // cursor into the current backend's model-id presets (-1 = none yet)
	mbFocus      int
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
	// Apply the persisted theme to the package-level palette/chroma at launch so
	// the lipgloss palette and syntax style match the saved pref (glamour already
	// reads prefs.Theme in makeRenderer).
	applyTheme(themeByName(prefs.Theme))
	prompt := textinput.New()
	prompt.Placeholder = "what should the agent do? (optional for 'work')"
	prompt.Focus()
	prompt.CharLimit = 8000
	prompt.Width = 60

	input := newSessionInput()

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
type historyMsg struct {
	sessions []*v1.SessionSummary
	err      error
}
type evMsg struct{ ev *v1.Event }
type streamClosedMsg struct{}
type errMsg struct{ err error }
type backlogMsg struct{ tasks []*v1.BacklogTaskSummary }
type taskDetailMsg struct{ task *v1.TaskDetail }

// captureEvMsg carries one streamed capture-agent action-log event. A terminal
// event of type "capture_result" carries the outcome of a CaptureBacklogItem RPC
// (task 0016): a created task (task_id/title), a single clarifying question, or
// an error — in its data_json.
type captureEvMsg struct{ ev *v1.Event }

// captureStreamClosedMsg signals the capture stream ended (the goroutine closed
// the channel) without a terminal capture_result event.
type captureStreamClosedMsg struct{}

// captureErrMsg reports a transport/RPC error opening or reading the capture
// stream.
type captureErrMsg struct{ err error }

// mbPrefillMsg carries a model backend's full record loaded via GetModelConfig
// for the edit/duplicate form (task 0044). On error the form is not opened.
type mbPrefillMsg struct {
	cfg  *v1.ModelConfig
	mode int
	err  error
}

// mbWriteMsg is the result of an UpsertModel/RemoveModel RPC (task 0044). On
// success the modal returns to the list and refreshes ListModels; on error the
// message is surfaced inline via mbErr.
type mbWriteMsg struct{ err error }

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

// fetchHistory loads the persisted session history for the previous-sessions
// screen (spec §18.6), scoped to the current project.
func (m model) fetchHistory() tea.Msg {
	resp, err := m.client.ListSessionHistory(m.ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{Project: m.project}))
	if err != nil {
		return historyMsg{err: err}
	}
	return historyMsg{sessions: resp.Msg.Sessions}
}

// reopenSession re-opens a persisted session on its existing event log via
// ResumeSession ("resume = replay", spec §4.5/§18.6) and enters the session view.
func (m model) reopenSession(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.ResumeSession(m.ctx, connect.NewRequest(&v1.ResumeSessionRequest{
			Project: m.project, SessionId: id,
		}))
		if err != nil {
			return errMsg{err}
		}
		return startedMsg{id: resp.Msg.SessionId, mode: resp.Msg.Mode}
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

// stopSession hard-terminates the running session (spec §12) and returns to the
// menu. Distinct from interrupt() which only pauses to steer.
func (m model) stopSession() tea.Cmd {
	return func() tea.Msg {
		if m.sessionID == "" {
			return nil
		}
		if _, err := m.client.StopSession(m.ctx, connect.NewRequest(&v1.StopSessionRequest{SessionId: m.sessionID})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// answerQuestions submits a batch of answers for a multi-question ask_user call.
func (m model) answerQuestions(answers []*v1.QuestionAnswer) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.AnswerQuestions(m.ctx, connect.NewRequest(&v1.AnswerQuestionsRequest{
			SessionId: m.sessionID, Answers: answers,
		}))
		if err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// startWizard enters the questionnaire wizard for a multi-question ask_user call,
// resetting collected answers and presenting the first question.
func (m *model) startWizard(qs []wizQuestion) {
	m.wizActive = true
	m.wizQuestions = qs
	m.wizAnswers = make([]wizAnswer, len(qs))
	for i := range m.wizAnswers {
		m.wizAnswers[i] = wizAnswer{idx: -1}
	}
	m.wizIdx = 0
	m.status = "waiting for your answer"
	m.loadWizQuestion()
}

// loadWizQuestion configures the per-question input (picker or free-text) for the
// current wizard question. For a free-text question it focuses the textarea and
// returns its blink command; the caller in Update propagates it. For a picker
// question it blurs the textarea and returns nil.
func (m *model) loadWizQuestion() tea.Cmd {
	if m.wizIdx < 0 || m.wizIdx >= len(m.wizQuestions) {
		return nil
	}
	q := m.wizQuestions[m.wizIdx]
	m.pending = q.prompt
	m.pickerOpts = append([]string(nil), q.options...)
	m.input.SetValue("")
	if len(m.pickerOpts) > 0 {
		m.picking = true
		m.pickerCursor = 0
		m.input.Blur()
		return nil
	}
	// Free-text question: re-focus the textarea so the user can type, even when a
	// preceding picker question blurred it. Focus() sets the focused state
	// synchronously (what matters for typing) and returns the cosmetic blink cmd.
	m.picking = false
	return m.input.Focus()
}

// clearWizard exits the questionnaire wizard and resets its state.
func (m *model) clearWizard() {
	m.wizActive = false
	m.wizQuestions = nil
	m.wizAnswers = nil
	m.wizIdx = 0
}

// recordWizAnswer stores the answer for the current question and advances. When
// the last question is answered it returns the command that submits all answers;
// otherwise it loads the next question and returns nil.
func (m *model) recordWizAnswer(idx int, text string, viaPicker bool) tea.Cmd {
	if m.wizIdx >= 0 && m.wizIdx < len(m.wizAnswers) {
		m.wizAnswers[m.wizIdx] = wizAnswer{idx: idx, text: text, done: true, picking: viaPicker}
	}
	if m.wizIdx < len(m.wizQuestions)-1 {
		m.wizIdx++
		return m.loadWizQuestion()
	}
	// Last question answered: submit the whole batch.
	answers := make([]*v1.QuestionAnswer, len(m.wizAnswers))
	for i, a := range m.wizAnswers {
		answers[i] = &v1.QuestionAnswer{Text: a.text, OptionIndex: int32(a.idx)}
	}
	m.pending = ""
	m.picking = false
	m.pickerOpts = nil
	m.follow = true
	return m.answerQuestions(answers)
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

// newSessionInput builds the multi-line session input textarea (task 0011).
func newSessionInput() textarea.Model {
	input := textarea.New()
	input.Placeholder = "type to prod / answer · enter sends · shift+enter newline · ↑↓ select · click to expand"
	input.CharLimit = 8000
	input.ShowLineNumbers = false
	input.Prompt = ""
	input.MaxHeight = maxInputRows
	input.SetHeight(1)
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"))
	return input
}

// syncInputHeight grows the session textarea with its content up to
// maxInputRows, after which it scrolls internally.
func (m *model) syncInputHeight() {
	h := m.input.LineCount()
	if h < 1 {
		h = 1
	}
	if h > maxInputRows {
		h = maxInputRows
	}
	if h != m.input.Height() {
		m.input.SetHeight(h)
	}
}

// relayout recomputes the viewport height so the (possibly multi-row) input
// and the help line never crowd out the event stream / status bar.
func (m *model) relayout() {
	if !m.ready {
		return
	}
	vpHeight := m.h - headerHeight - 1 - m.input.Height()
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.vp.Height = vpHeight
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if m.input.MaxHeight == 0 { // zero-value textarea (e.g. a test-constructed model literal)
			m.input = newSessionInput()
		}
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
		m.input.SetWidth(msg.Width - 4)
		m.captureInput.Width = msg.Width - 4
		m.makeRenderer()
		m.bodyCache = map[int]string{} // re-render bodies at the new width
		m.rebuild()
		m.syncInputHeight()
		m.relayout()
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
		// Keep the model-backends cursor in range: a removal can shrink the list
		// out from under it (task 0044).
		if m.mbCursor >= len(m.models) {
			if len(m.models) == 0 {
				m.mbCursor = 0
			} else {
				m.mbCursor = len(m.models) - 1
			}
		}
		return m, nil
	case projectsMsg:
		m.projects = msg.projects
		if m.projectCur >= len(m.projects) {
			m.projectCur = 0
		}
		return m, nil
	case historyMsg:
		if msg.err != nil {
			m.history = nil
			m.historyMsgTxt = "error: " + msg.err.Error()
			return m, nil
		}
		m.history = msg.sessions
		if m.historyCursor >= len(m.history) {
			m.historyCursor = 0
		}
		if len(m.history) == 0 {
			m.historyMsgTxt = "no previous sessions"
		} else {
			m.historyMsgTxt = ""
		}
		return m, nil
	case errMsg:
		m.err = msg.err
		return m, nil
	case startedMsg:
		// Reset any stale event/view state from a prior session so a reopened
		// session renders cleanly from its replayed log (spec §18.6).
		m.evs = nil
		m.expanded = map[int]bool{}
		m.bodyCache = map[int]string{}
		m.eventStart = nil
		m.selected = -1
		m.follow = m.prefs.Follow
		m.pending, m.paused, m.picking = "", false, false
		m.pickerOpts, m.pickerCursor = nil, 0
		m.clearWizard()
		m.sessionID, m.mode, m.state, m.status = msg.id, msg.mode, stateSession, "running"
		m.input.SetValue("")
		fc := m.input.Focus()
		m.syncInputHeight()
		m.relayout()
		return m, tea.Batch(m.subscribe(), fc)
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
	case captureEvMsg:
		ev := msg.ev
		if ev.Type == "capture_result" {
			m.captureBusy = false
			if e := dataField(ev, "error"); e != "" {
				m.captureMsg = "error: " + e
				return m, nil
			}
			taskID, title, q := dataField(ev, "task_id"), dataField(ev, "title"), dataField(ev, "question")
			if taskID != "" {
				m.captureStage = 2
				m.captureMsg = "created " + taskID + ": " + title
				return m, nil
			}
			if q != "" {
				m.captureStage = 1
				m.captureQuestion = q
				m.captureInput.SetValue("")
				m.captureInput.Focus()
				return m, nil
			}
			m.captureMsg = "(no result)"
			return m, nil
		}
		m.captureLog = append(m.captureLog, ev)
		return m, waitCaptureEvent(m.captureEvents)
	case captureStreamClosedMsg:
		if m.captureBusy {
			m.captureBusy = false
			if m.captureMsg == "" {
				m.captureMsg = "error: capture ended without a result"
			}
		}
		return m, nil
	case captureErrMsg:
		m.captureBusy = false
		m.captureMsg = "error: " + msg.err.Error()
		return m, nil
	case mbPrefillMsg:
		if msg.err != nil {
			m.mbErr = "load failed: " + msg.err.Error()
			return m, nil
		}
		m.mbPrefill(msg.cfg, msg.mode)
		return m, nil
	case mbWriteMsg:
		if msg.err != nil {
			// Surface RPC/validation errors inline (e.g. removing a role-referenced
			// model) so the modal stays usable — never the global m.err.
			m.mbErr = msg.err.Error()
			return m, nil
		}
		m.mbErr = ""
		m.mbView = 0
		// Refresh ListModels so the role pickers reflect the change.
		return m, m.fetchModels
	}

	// The project picker (spec §3.1) is shown first when attached to a
	// persistent/remote daemon; it owns input until a project is chosen.
	if m.state == statePicker {
		return m.updatePicker(msg)
	}

	// The previous-sessions screen (ctrl+r from the menu) owns input until the
	// user reopens a session or returns to the menu (spec §18.6).
	if m.state == stateHistory {
		return m.updateHistory(msg)
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

	// The model-backends management modal (task 0044) owns input while open. It is
	// reached from the settings overlay and is modal over menu/session.
	if m.mbOpen {
		return m.updateModelBackends(msg)
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

// updateHistory handles the previous-sessions screen (spec §18.6): navigate the
// list of persisted sessions, Enter reopens the selected one via ResumeSession,
// `r` refreshes, Esc/q returns to the menu.
func (m model) updateHistory(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.state = stateMenu
		return m, nil
	case "r":
		m.historyMsgTxt = "loading…"
		return m, m.fetchHistory
	case "up":
		if m.historyCursor > 0 {
			m.historyCursor--
		}
		return m, nil
	case "down":
		if m.historyCursor < len(m.history)-1 {
			m.historyCursor++
		}
		return m, nil
	case "enter":
		if len(m.history) == 0 {
			return m, nil
		}
		sel := m.history[m.historyCursor]
		m.historyMsgTxt = "reopening " + short(sel.SessionId) + "…"
		return m, m.reopenSession(sel.SessionId)
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
			m.backlogShowDone = false
			return m, m.fetchBacklog
		case "ctrl+r":
			// Open the previous-sessions screen to reopen a persisted session
			// (spec §18.6).
			m.state = stateHistory
			m.historyCursor = 0
			m.history = nil
			m.historyMsgTxt = "loading…"
			return m, m.fetchHistory
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
					return m, m.input.Focus()
				}
				idx := m.pickerCursor
				m.picking = false
				if m.wizActive {
					cmd := m.recordWizAnswer(idx, m.pickerOpts[idx], true)
					return m, cmd
				}
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
			m.backlogShowDone = false
			return m, m.fetchBacklog
		case "ctrl+i":
			// Gracefully interrupt the running agent to steer it (spec §18.7).
			if !m.paused {
				return m, m.interrupt()
			}
			return m, nil
		case "ctrl+x":
			// Hard-terminate the session and return to the menu (spec §12). This
			// is distinct from ctrl+i, which only pauses to steer.
			cmd := m.stopSession()
			m.state = stateMenu
			m.sessionID = ""
			m.paused = false
			m.status = ""
			return m, cmd
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
			m.syncInputHeight()
			m.relayout()
			// While paused, a non-empty Enter steers: send the correction AND
			// resume so the agent continues with it (spec §18.7).
			if m.paused {
				m.follow = true
				return m, tea.Sequence(m.sendInput(text), m.resume())
			}
			// If a question is pending, answer it as free text (option_index -1);
			// otherwise it's a prod handled by SendInput.
			if m.wizActive {
				m.follow = true
				cmd := m.recordWizAnswer(-1, text, false)
				if cmd == nil {
					return m, nil
				}
				return m, cmd
			}
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
	m.syncInputHeight()
	m.relayout()
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
			m.captureLog = nil
			ch := make(chan *v1.Event, 64)
			m.captureEvents = ch
			m.captureInput.SetValue("")
			return m, m.captureSubmit(ch, m.captureDesc, "", "")
		}
		// Stage 1: answering the agent's clarifying question.
		m.captureBusy = true
		m.captureMsg = ""
		m.captureLog = nil
		ch := make(chan *v1.Event, 64)
		m.captureEvents = ch
		ans := val
		m.captureInput.SetValue("")
		return m, m.captureSubmit(ch, m.captureDesc, m.captureQuestion, ans)
	default:
		var c tea.Cmd
		m.captureInput, c = m.captureInput.Update(msg)
		return m, c
	}
}

// captureSubmit opens the streaming CaptureBacklogItem RPC, scoped to the current
// project, and pumps its action-log events into ch. It does not touch the session
// stream. The first event (or an open error) is delivered as the returned msg;
// subsequent events are pulled via waitCaptureEvent.
func (m model) captureSubmit(ch chan *v1.Event, desc, q, a string) tea.Cmd {
	return func() tea.Msg {
		stream, err := m.client.CaptureBacklogItem(m.ctx, connect.NewRequest(&v1.CaptureBacklogItemRequest{
			Project: m.project, Description: desc, PriorQuestion: q, PriorAnswer: a,
		}))
		if err != nil {
			return captureErrMsg{err}
		}
		go func() {
			for stream.Receive() {
				ch <- stream.Msg()
			}
			close(ch)
		}()
		return waitCaptureEvent(ch)()
	}
}

// waitCaptureEvent blocks for the next capture-agent event on ch, mapping a
// closed channel to captureStreamClosedMsg.
func waitCaptureEvent(ch chan *v1.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return captureStreamClosedMsg{}
		}
		return captureEvMsg{ev}
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
	// Stream the capture agent's action log live (task 0049): show the last few
	// events so the user sees progress instead of a blank wait.
	if len(m.captureLog) > 0 {
		b.WriteString("\n")
		const maxLines = 10
		start := 0
		if len(m.captureLog) > maxLines {
			start = len(m.captureLog) - maxLines
		}
		for _, ev := range m.captureLog[start:] {
			line := detailLine(ev)
			if line == "" {
				continue
			}
			b.WriteString("  " + dimStyle.Render(ev.Actor) + " " + line + "\n")
		}
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
	vis := m.visibleBacklogTasks()
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
		if m.backlogCursor < len(vis)-1 {
			m.backlogCursor++
		}
		return m, nil
	case "d":
		m.backlogShowDone = !m.backlogShowDone
		if vis := m.visibleBacklogTasks(); m.backlogCursor >= len(vis) {
			m.backlogCursor = len(vis) - 1
			if m.backlogCursor < 0 {
				m.backlogCursor = 0
			}
		}
		return m, nil
	case "enter":
		if len(vis) > 0 {
			return m, m.fetchTask(vis[m.backlogCursor].Id)
		}
		return m, nil
	}
	return m, nil
}

// visibleBacklogTasks returns the backlog rows to display: all tasks when
// backlogShowDone is set, otherwise only non-done (actionable) tasks. This keeps
// the overlay focused on open work by default while letting done tasks be revealed.
func (m model) visibleBacklogTasks() []*v1.BacklogTaskSummary {
	if m.backlogShowDone {
		return m.backlogTasks
	}
	out := make([]*v1.BacklogTaskSummary, 0, len(m.backlogTasks))
	for _, t := range m.backlogTasks {
		if t.Status != "done" {
			out = append(out, t)
		}
	}
	return out
}

// backlogView renders the modal backlog browser (list or detail).
func (m model) backlogView() string {
	if m.backlogDetail != nil {
		return m.taskDetailView(m.backlogDetail)
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render(" ycc — backlog ") + "\n\n")
	vis := m.visibleBacklogTasks()
	if len(vis) == 0 {
		b.WriteString("  " + dimStyle.Render("(no backlog tasks)") + "\n")
	}
	for i, t := range vis {
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
	b.WriteString("\n" + dimStyle.Render("  ↑/↓ select · enter inspect · d show/hide done · esc close"))
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
	ovBackends
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
		// Live-switch the palette so the open menu/session repaints in the new
		// theme without a restart.
		applyTheme(themeByName(m.prefs.Theme))
		m.makeRenderer()
		m.bodyCache = map[int]string{}
		m.rebuild()
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
	case ovBackends:
		// Open the model-backends management modal (task 0044) and refresh the
		// model list so it lists the current backends.
		m.overlay = false
		m.mbOpen = true
		m.mbView = 0
		m.mbCursor = 0
		m.mbErr = ""
		return m, m.fetchModels
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
		{"model backends", "add / edit / remove…"},
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

// --- model-backends management modal (spec §18.2, task 0044) ---

// form mode for the add/edit/duplicate form.
const (
	mbAdd = iota
	mbEdit
	mbDuplicate
)

// form field indices (focus order). backend/thinking/effort/display/persist are
// focusable non-text fields cycled with ←/→; the rest are text inputs.
const (
	mbFieldName = iota
	mbFieldBackend
	mbFieldBaseURL
	mbFieldModel
	mbFieldKeyEnv
	mbFieldThinking
	mbFieldEffort
	mbFieldDisplay
	mbFieldPriceIn
	mbFieldPriceOut
	mbFieldPriceCacheRead
	mbFieldPriceCacheWrite
	mbFieldPersist
	mbNumFields
)

var (
	mbBackendList  = []string{"anthropic", "openai", "ollama"}
	mbThinkingList = []string{"", "adaptive", "off"}
	mbEffortList   = []string{"", "low", "medium", "high", "xhigh", "max"}
	mbDisplayList  = []string{"", "summarized", "omitted"}

	// mbModelPresets offers a small built-in list of common model ids per backend
	// as suggestions in the model field (spec §13, task 0042). They are
	// suggestions only — free-text entry is always retained, so any id works. The
	// model field stays a normal text input; ctrl+n/ctrl+p just fill it with the
	// next/previous preset for the current backend.
	mbModelPresets = map[string][]string{
		"anthropic": {"claude-opus-4-8", "claude-sonnet-4-5", "claude-haiku-4-5"},
		"openai":    {"gpt-5.5", "gpt-5-mini", "gpt-4o", "o3"},
		"ollama":    {"qwen2.5-coder", "llama3.3", "deepseek-r1"},
	}
)

func mbIsText(i int) bool {
	switch i {
	case mbFieldName, mbFieldBaseURL, mbFieldModel, mbFieldKeyEnv,
		mbFieldPriceIn, mbFieldPriceOut, mbFieldPriceCacheRead, mbFieldPriceCacheWrite:
		return true
	}
	return false
}

func mbLabel(i int) string {
	switch i {
	case mbFieldName:
		return "name"
	case mbFieldBackend:
		return "backend"
	case mbFieldBaseURL:
		return "base url"
	case mbFieldModel:
		return "model"
	case mbFieldKeyEnv:
		return "key env"
	case mbFieldThinking:
		return "thinking"
	case mbFieldEffort:
		return "effort"
	case mbFieldDisplay:
		return "thinking disp"
	case mbFieldPriceIn:
		return "price in"
	case mbFieldPriceOut:
		return "price out"
	case mbFieldPriceCacheRead:
		return "price c.read"
	case mbFieldPriceCacheWrite:
		return "price c.write"
	case mbFieldPersist:
		return "persist toml"
	}
	return ""
}

// mbNewInputs (re)initializes the form's text inputs with the wizard's
// CharLimit/Width so the form reads consistently with first-run setup.
func (m *model) mbNewInputs() {
	for i := range m.mbInputs {
		ti := textinput.New()
		ti.CharLimit = 200
		ti.Width = 50
		m.mbInputs[i] = ti
	}
	m.mbInputs[mbFieldName].Placeholder = "logical name (e.g. claude)"
	m.mbInputs[mbFieldBaseURL].Placeholder = "base url"
	m.mbInputs[mbFieldModel].Placeholder = "model id"
	m.mbInputs[mbFieldKeyEnv].Placeholder = "API key env var name (never the key)"
	m.mbInputs[mbFieldPriceIn].Placeholder = "$/Mtok (optional)"
	m.mbInputs[mbFieldPriceOut].Placeholder = "$/Mtok (optional)"
	m.mbInputs[mbFieldPriceCacheRead].Placeholder = "$/Mtok (optional)"
	m.mbInputs[mbFieldPriceCacheWrite].Placeholder = "$/Mtok (optional)"
}

// mbStartAdd opens a blank add form (backend defaults to anthropic).
func (m *model) mbStartAdd() {
	m.mbNewInputs()
	m.mbBackends = append([]string(nil), mbBackendList...)
	m.mbBackendIdx = 0
	m.mbThinkIdx, m.mbEffortIdx, m.mbDisplayIdx = 0, 0, 0
	m.mbPresetIdx = -1
	m.mbFormMode = mbAdd
	m.mbOrigName = ""
	m.mbErr = ""
	m.mbFocus = mbFieldName
	m.mbView = 1
	m.mbFocusInputs()
}

// mbPrefill fills the form from a loaded ModelConfig for edit/duplicate.
func (m *model) mbPrefill(cfg *v1.ModelConfig, mode int) {
	m.mbNewInputs()
	m.mbFormMode = mode
	m.mbOrigName = cfg.Name
	name := cfg.Name
	if mode == mbDuplicate {
		name = cfg.Name + "-copy"
	}
	m.mbInputs[mbFieldName].SetValue(name)
	m.mbInputs[mbFieldBaseURL].SetValue(cfg.BaseUrl)
	m.mbInputs[mbFieldModel].SetValue(cfg.Model)
	m.mbInputs[mbFieldKeyEnv].SetValue(cfg.KeyEnv)
	m.mbInputs[mbFieldPriceIn].SetValue(fmtPrice(cfg.PriceInput))
	m.mbInputs[mbFieldPriceOut].SetValue(fmtPrice(cfg.PriceOutput))
	m.mbInputs[mbFieldPriceCacheRead].SetValue(fmtPrice(cfg.PriceCacheRead))
	m.mbInputs[mbFieldPriceCacheWrite].SetValue(fmtPrice(cfg.PriceCacheWrite))
	// Preserve a loaded backend that isn't one of the built-in choices (e.g. "glm").
	m.mbBackends = append([]string(nil), mbBackendList...)
	m.mbBackendIdx = mbIndexOrAppend(&m.mbBackends, cfg.Backend)
	m.mbThinkIdx = mbIndexOf(mbThinkingList, cfg.Thinking)
	m.mbEffortIdx = mbIndexOf(mbEffortList, cfg.Effort)
	m.mbDisplayIdx = mbIndexOf(mbDisplayList, cfg.ThinkingDisplay)
	m.mbPresetIdx = -1
	m.mbErr = ""
	m.mbView = 1
	if mode == mbEdit {
		// The name is read-only in edit mode (rename via duplicate+remove).
		m.mbFocus = mbFieldBackend
	} else {
		m.mbFocus = mbFieldName
	}
	m.mbFocusInputs()
}

func fmtPrice(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

func mbIndexOf(vals []string, cur string) int {
	for i, v := range vals {
		if v == cur {
			return i
		}
	}
	return 0
}

func mbIndexOrAppend(vals *[]string, cur string) int {
	for i, v := range *vals {
		if v == cur {
			return i
		}
	}
	*vals = append(*vals, cur)
	return len(*vals) - 1
}

func parsePrice(s string) (*float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (m *model) mbFocusInputs() {
	for j := range m.mbInputs {
		m.mbInputs[j].Blur()
	}
	if mbIsText(m.mbFocus) {
		m.mbInputs[m.mbFocus].Focus()
	}
}

// mbMoveFocus advances the form focus, skipping the read-only name field in edit
// mode so renaming is only possible via duplicate+remove.
func (m *model) mbMoveFocus(dir int) {
	for i := 0; i < mbNumFields; i++ {
		m.mbFocus = (m.mbFocus + dir + mbNumFields) % mbNumFields
		if m.mbFormMode == mbEdit && m.mbFocus == mbFieldName {
			continue
		}
		break
	}
	m.mbFocusInputs()
}

// mbCycleFocused cycles the focused non-text field (backend/thinking/effort/
// display) or toggles persist with ←/→.
func (m *model) mbCycleFocused(d int) {
	switch m.mbFocus {
	case mbFieldBackend:
		m.mbBackendIdx = (m.mbBackendIdx + d + len(m.mbBackends)) % len(m.mbBackends)
	case mbFieldThinking:
		m.mbThinkIdx = (m.mbThinkIdx + d + len(mbThinkingList)) % len(mbThinkingList)
	case mbFieldEffort:
		m.mbEffortIdx = (m.mbEffortIdx + d + len(mbEffortList)) % len(mbEffortList)
	case mbFieldDisplay:
		m.mbDisplayIdx = (m.mbDisplayIdx + d + len(mbDisplayList)) % len(mbDisplayList)
	case mbFieldPersist:
		m.mbPersist = !m.mbPersist
	}
}

// updateModelBackends handles input while the model-backends modal is open.
func (m model) updateModelBackends(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		if m.mbView == 1 && mbIsText(m.mbFocus) {
			var cmd tea.Cmd
			m.mbInputs[m.mbFocus], cmd = m.mbInputs[m.mbFocus].Update(msg)
			return m, cmd
		}
		return m, nil
	}
	switch m.mbView {
	case 1:
		return m.mbUpdateForm(key)
	case 2:
		return m.mbUpdateConfirm(key)
	default:
		return m.mbUpdateList(key)
	}
}

func (m model) mbUpdateList(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		// Back to the settings overlay.
		m.mbOpen = false
		m.overlay = true
		return m, nil
	case "up":
		if m.mbCursor > 0 {
			m.mbCursor--
		}
		return m, nil
	case "down":
		if m.mbCursor < len(m.models)-1 {
			m.mbCursor++
		}
		return m, nil
	case "a":
		m.mbStartAdd()
		return m, nil
	case "e", "enter":
		if m.mbCursor >= len(m.models) {
			return m, nil
		}
		m.mbErr = ""
		return m, m.mbFetchConfig(m.models[m.mbCursor].Name, mbEdit)
	case "d":
		if m.mbCursor >= len(m.models) {
			return m, nil
		}
		m.mbErr = ""
		return m, m.mbFetchConfig(m.models[m.mbCursor].Name, mbDuplicate)
	case "x":
		if m.mbCursor >= len(m.models) {
			return m, nil
		}
		m.mbErr = ""
		m.mbView = 2
		return m, nil
	case "p":
		m.mbPersist = !m.mbPersist
		return m, nil
	}
	return m, nil
}

func (m model) mbUpdateForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mbView = 0
		m.mbErr = ""
		return m, nil
	case "tab", "down":
		m.mbMoveFocus(1)
		return m, nil
	case "shift+tab", "up":
		m.mbMoveFocus(-1)
		return m, nil
	case "left":
		m.mbCycleFocused(-1)
		return m, nil
	case "right":
		m.mbCycleFocused(1)
		return m, nil
	case "enter":
		return m.mbSubmitForm()
	case "ctrl+n":
		// On the model field, ctrl+n/ctrl+p cycle the backend's id presets while
		// keeping the field free-text. Elsewhere they fall through unchanged.
		if m.mbFocus == mbFieldModel {
			m.mbCyclePreset(1)
			return m, nil
		}
	case "ctrl+p":
		if m.mbFocus == mbFieldModel {
			m.mbCyclePreset(-1)
			return m, nil
		}
	}
	if mbIsText(m.mbFocus) {
		var cmd tea.Cmd
		m.mbInputs[m.mbFocus], cmd = m.mbInputs[m.mbFocus].Update(key)
		return m, cmd
	}
	return m, nil
}

// mbCyclePreset fills the model field with the next/previous built-in id preset
// for the current backend (task 0042). It is a convenience over free text — the
// field remains a normal text input the user can overtype.
func (m *model) mbCyclePreset(d int) {
	presets := mbModelPresets[m.mbBackends[m.mbBackendIdx]]
	if len(presets) == 0 {
		return
	}
	m.mbPresetIdx = (m.mbPresetIdx + d + len(presets)) % len(presets)
	m.mbInputs[mbFieldModel].SetValue(presets[m.mbPresetIdx])
	m.mbInputs[mbFieldModel].CursorEnd()
}

// mbSubmitForm validates and builds a *v1.ModelConfig and issues UpsertModel.
func (m model) mbSubmitForm() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.mbInputs[mbFieldName].Value())
	if m.mbFormMode == mbEdit {
		name = m.mbOrigName // name is fixed in edit mode
	}
	backend := m.mbBackends[m.mbBackendIdx]
	mdl := strings.TrimSpace(m.mbInputs[mbFieldModel].Value())
	if name == "" || backend == "" || mdl == "" {
		m.mbErr = "name, backend and model are required"
		return m, nil
	}
	cfg := &v1.ModelConfig{
		Name:            name,
		Backend:         backend,
		BaseUrl:         strings.TrimSpace(m.mbInputs[mbFieldBaseURL].Value()),
		Model:           mdl,
		KeyEnv:          strings.TrimSpace(m.mbInputs[mbFieldKeyEnv].Value()),
		Thinking:        mbThinkingList[m.mbThinkIdx],
		Effort:          mbEffortList[m.mbEffortIdx],
		ThinkingDisplay: mbDisplayList[m.mbDisplayIdx],
	}
	prices := []struct {
		idx   int
		dst   **float64
		label string
	}{
		{mbFieldPriceIn, &cfg.PriceInput, "price in"},
		{mbFieldPriceOut, &cfg.PriceOutput, "price out"},
		{mbFieldPriceCacheRead, &cfg.PriceCacheRead, "price cache read"},
		{mbFieldPriceCacheWrite, &cfg.PriceCacheWrite, "price cache write"},
	}
	for _, p := range prices {
		v, err := parsePrice(m.mbInputs[p.idx].Value())
		if err != nil {
			m.mbErr = p.label + " must be a number"
			return m, nil
		}
		*p.dst = v
	}
	m.mbErr = ""
	return m, m.mbUpsert(cfg, m.mbPersist)
}

func (m model) mbUpdateConfirm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mbView = 0
		return m, nil
	case "enter":
		if m.mbCursor >= len(m.models) {
			m.mbView = 0
			return m, nil
		}
		return m, m.mbRemove(m.models[m.mbCursor].Name, m.mbPersist)
	}
	return m, nil
}

// mbFetchConfig loads a model backend's full record for the edit/duplicate form.
func (m model) mbFetchConfig(name string, mode int) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.GetModelConfig(m.ctx, connect.NewRequest(&v1.GetModelConfigRequest{Name: name}))
		if err != nil {
			return mbPrefillMsg{err: err, mode: mode}
		}
		return mbPrefillMsg{cfg: resp.Msg.Model, mode: mode}
	}
}

// mbUpsert adds or replaces a logical model backend (persist => also ycc.toml).
func (m model) mbUpsert(cfg *v1.ModelConfig, persist bool) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.UpsertModel(m.ctx, connect.NewRequest(&v1.UpsertModelRequest{
			Model: cfg, Persist: persist,
		})); err != nil {
			return mbWriteMsg{err: err}
		}
		return mbWriteMsg{}
	}
}

// mbRemove deletes a logical model backend; rejected if a role still references it.
func (m model) mbRemove(name string, persist bool) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.RemoveModel(m.ctx, connect.NewRequest(&v1.RemoveModelRequest{
			Name: name, Persist: persist,
		})); err != nil {
			return mbWriteMsg{err: err}
		}
		return mbWriteMsg{}
	}
}

func (m model) modelBackendsView() string {
	switch m.mbView {
	case 1:
		return m.mbFormView()
	case 2:
		return m.mbConfirmView()
	default:
		return m.mbListView()
	}
}

func (m model) mbListView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" model backends ") + "\n\n")
	if len(m.models) == 0 {
		b.WriteString("  " + dimStyle.Render("(no model backends configured)") + "\n")
	}
	for i, mm := range m.models {
		cursor := "  "
		row := fmt.Sprintf("%-16s %-12s %s", mm.Name, mm.Backend, mm.Model)
		if i == m.mbCursor {
			cursor = selStyle.Render("▸ ")
			row = selStyle.Render(row)
		}
		b.WriteString("  " + cursor + row + "\n")
	}
	if m.mbErr != "" {
		b.WriteString("\n  " + errStyle.Render(m.mbErr) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("  persist to ycc.toml: "+boolStr(m.mbPersist)))
	b.WriteString("\n" + dimStyle.Render("  a add · e/enter edit · d duplicate · x remove · p toggle persist · esc back"))
	return b.String()
}

func (m model) mbFormView() string {
	var b strings.Builder
	title := "add model backend"
	switch m.mbFormMode {
	case mbEdit:
		title = "edit model backend"
	case mbDuplicate:
		title = "duplicate model backend"
	}
	b.WriteString(titleStyle.Render(" "+title+" ") + "\n\n")
	order := []int{
		mbFieldName, mbFieldBackend, mbFieldBaseURL, mbFieldModel, mbFieldKeyEnv,
		mbFieldThinking, mbFieldEffort, mbFieldDisplay,
		mbFieldPriceIn, mbFieldPriceOut, mbFieldPriceCacheRead, mbFieldPriceCacheWrite,
		mbFieldPersist,
	}
	for _, f := range order {
		cursor := "  "
		if m.mbFocus == f {
			cursor = selStyle.Render("▸ ")
		}
		label := fmt.Sprintf("%-14s", mbLabel(f)+":")
		var val string
		switch f {
		case mbFieldName:
			if m.mbFormMode == mbEdit {
				val = dimStyle.Render(m.mbInputs[mbFieldName].Value() + "  (rename via duplicate)")
			} else {
				val = m.mbInputs[f].View()
			}
		case mbFieldBackend:
			val = "◂ " + m.mbBackends[m.mbBackendIdx] + " ▸"
		case mbFieldThinking:
			val = "◂ " + mbShow(mbThinkingList[m.mbThinkIdx]) + " ▸"
		case mbFieldEffort:
			val = "◂ " + mbShow(mbEffortList[m.mbEffortIdx]) + " ▸"
		case mbFieldDisplay:
			val = "◂ " + mbShow(mbDisplayList[m.mbDisplayIdx]) + " ▸"
		case mbFieldPersist:
			val = "◂ " + boolStr(m.mbPersist) + " ▸"
		default:
			val = m.mbInputs[f].View()
		}
		b.WriteString("  " + cursor + label + " " + val + "\n")
		// Under the focused model field, hint the current backend's id presets.
		// Free text still works; this just advertises the ctrl+n/p suggestions.
		if f == mbFieldModel && m.mbFocus == mbFieldModel {
			if presets := mbModelPresets[m.mbBackends[m.mbBackendIdx]]; len(presets) > 0 {
				b.WriteString("      " + dimStyle.Render("presets: "+strings.Join(presets, " · ")+"  (ctrl+n/p)") + "\n")
			}
		}
	}
	if m.mbErr != "" {
		b.WriteString("\n  " + errStyle.Render(m.mbErr) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("  Tab/↑↓ move · ←/→ change · enter save · esc back"))
	b.WriteString("\n" + dimStyle.Render("  (keys are env-var references only — never paste a secret)"))
	return b.String()
}

func (m model) mbConfirmView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" remove model backend ") + "\n\n")
	name := ""
	if m.mbCursor < len(m.models) {
		name = m.models[m.mbCursor].Name
	}
	b.WriteString("  remove " + selStyle.Render(name) + "?\n")
	b.WriteString("\n" + dimStyle.Render("  persist to ycc.toml: "+boolStr(m.mbPersist)))
	if m.mbErr != "" {
		b.WriteString("\n\n  " + errStyle.Render(m.mbErr) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("  enter confirm · esc cancel"))
	return b.String()
}

// mbShow renders an empty cycle value as "(none)" for readability.
func mbShow(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
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
	// Skip folded tool_result rows: they share their call's combined row, so
	// selection should land on the call, never the result. Travel in the move
	// direction past any merged result, then snap back to the call if needed.
	dir := d
	if dir == 0 {
		dir = 1
	}
	for m.isMergedResult(m.selected) {
		next := m.selected + dir
		if next < 0 || next >= len(m.evs) {
			break
		}
		m.selected = next
	}
	if m.isMergedResult(m.selected) {
		m.selected--
	}
	m.follow = m.selected == len(m.evs)-1
	m.rebuild()
	m.ensureVisible()
}

func (m *model) toggle(i int) {
	if i < 0 || i >= len(m.evs) {
		return
	}
	// A folded tool_result toggles its call's combined row.
	if m.isMergedResult(i) {
		i--
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
		if qs := dataQuestions(ev); len(qs) > 0 {
			// Multi-question form: start the questionnaire wizard.
			m.startWizard(qs)
			break
		}
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
		m.clearWizard()
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

// mergedResultIdx reports the index of the tool_result that should be folded
// into the tool_call at index i (rendered as one combined row), or -1 when the
// call at i has no adjacent matching result. Pairing is by adjacency (result at
// i+1) which naturally excludes spawn-style tools whose subagent events appear
// between the parent's call and result.
func (m *model) mergedResultIdx(i int) int {
	if i < 0 || i+1 >= len(m.evs) {
		return -1
	}
	call, res := m.evs[i], m.evs[i+1]
	if call.Type != "tool_call" || res.Type != "tool_result" {
		return -1
	}
	if call.Actor != res.Actor {
		return -1
	}
	cid, rid := dataField(call, "id"), dataField(res, "id")
	if cid != "" && rid != "" && cid != rid {
		return -1
	}
	return i + 1
}

// isMergedResult reports whether the event at index j is a tool_result that has
// been folded into its preceding tool_call's combined row.
func (m *model) isMergedResult(j int) bool {
	return j > 0 && m.mergedResultIdx(j-1) == j
}

// eventAt returns the index of the event whose rendered block contains content
// line `row`, or -1.
func (m *model) eventAt(row int) int {
	if row < 0 {
		return -1
	}
	for i := len(m.eventStart) - 1; i >= 0; i-- {
		if row >= m.eventStart[i] {
			idx := i
			if m.isMergedResult(idx) {
				idx--
			}
			return idx
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
	// event loop and freezes the UI. The style is chosen from the user's explicit
	// theme pref (never by querying the terminal).
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(themeByName(m.prefs.Theme).glamourStyle), glamour.WithWordWrap(w))
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
		// A tool_result folded into its preceding tool_call's combined row
		// shares that call's start line and emits no block of its own.
		if m.isMergedResult(i) {
			m.eventStart[i] = m.eventStart[i-1]
			continue
		}
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
	if m.mbOpen {
		return m.modelBackendsView()
	}
	if m.overlay {
		return m.overlayView()
	}
	if m.state == statePicker {
		return m.pickerScreenView()
	}
	if m.state == stateHistory {
		return m.historyView()
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
	b.WriteString("\n" + dimStyle.Render("  ↑/↓ choose mode · type a prompt · enter start · ctrl+r previous sessions · esc settings · ctrl+b backlog · ctrl+n new task"))
	return b.String()
}

// historyView renders the previous-sessions screen (spec §18.6): a navigable
// list of persisted sessions, most-recent first, that can be reopened.
func (m model) historyView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" ycc — previous sessions ") + "\n\n")
	if len(m.history) == 0 {
		msg := m.historyMsgTxt
		if msg == "" {
			msg = "no previous sessions"
		}
		b.WriteString("  " + dimStyle.Render(msg) + "\n")
		b.WriteString("\n" + dimStyle.Render("  r refresh · esc/q back"))
		return b.String()
	}
	for i, s := range m.history {
		// Prefer a derived title; fall back to the short id.
		title := strings.TrimSpace(s.Title)
		if title == "" {
			title = short(s.SessionId)
		}
		meta := s.Mode + " · " + s.Status
		if s.Live {
			meta += " · live"
		}
		if when := historyWhen(s); when != "" {
			meta += " · " + when
		}
		// Clamp the title so the row stays on a single physical line.
		tw := 48
		if m.w > 0 && m.w-4 < tw {
			tw = m.w - 4
		}
		line := fmt.Sprintf("%-*s  %s", tw, trunc(title, tw), dimStyle.Render(meta))
		cursor := "  "
		if i == m.historyCursor {
			cursor = selStyle.Render("▸ ")
			line = selStyle.Render(fmt.Sprintf("%-*s  ", tw, trunc(title, tw))) + dimStyle.Render(meta)
		}
		b.WriteString("  " + cursor + line + "\n")
	}
	if m.historyMsgTxt != "" {
		b.WriteString("\n  " + dimStyle.Render(m.historyMsgTxt))
	}
	b.WriteString("\n\n" + dimStyle.Render("  ↑/↓ choose · enter reopen · r refresh · esc/q back"))
	return b.String()
}

// historyWhen renders a session's last-activity (or start) timestamp compactly
// for the previous-sessions list, returning "" when neither is available.
func historyWhen(s *v1.SessionSummary) string {
	ts := s.LastActivity
	if ts == "" {
		ts = s.StartedAt
	}
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2006-01-02 15:04")
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

// statusBar renders the single-row session status line: a set of colored,
// glyph-prefixed segments (status, mode, model, thinking level, project, id)
// joined by dim chevrons, in the spirit of the reference LSP UI's bottom bar.
// It is always exactly one physical row — clamped to the terminal width with an
// ANSI-aware truncation so styling never causes a wrap (which would corrupt the
// fixed-height frame; see TestSessionViewFitsTerminal).
func (m model) statusBar() string {
	var segs []string

	// status with a state-colored dot.
	dot := dimStyle
	switch m.status {
	case "running":
		dot = successStyle
	case "paused":
		dot = recoStyle
	case "error":
		dot = errStyle
	case "idle", "waiting for your answer":
		dot = pathStyle
	}
	segs = append(segs, dot.Render("●")+" "+typeStyle.Render(m.status))

	if m.mode != "" {
		segs = append(segs, dimStyle.Render("mode ")+typeStyle.Render(m.mode))
	}
	if m.roleCoord != "" {
		segs = append(segs, actorStyle("coordinator").Render(m.roleCoord))
	}
	if lvl := m.thinkLevels["coordinator"]; lvl != "" {
		segs = append(segs, pathStyle.Render("◆")+" "+dimStyle.Render(lvl))
	}
	if loc := m.locationLabel(); loc != "" {
		segs = append(segs, dimStyle.Render(loc))
	}
	if m.sessionID != "" {
		segs = append(segs, dimStyle.Render(short(m.sessionID)))
	}

	bar := " " + strings.Join(segs, dimStyle.Render(" › ")) + " "
	if m.pending != "" {
		bar = askStyle.Render(" ? answer below ") + bar
	}
	if m.w > 0 {
		bar = ansi.Truncate(bar, m.w, dimStyle.Render("…"))
	}
	return bar
}

// locationLabel is the project name when attached to a daemon registry, else the
// basename of the workspace path — the bar's "where am I" segment.
func (m model) locationLabel() string {
	if m.project != "" {
		return m.project
	}
	if m.workspace != "" {
		return filepath.Base(m.workspace)
	}
	return ""
}

func (m model) sessionView() string {
	top := m.statusBar()
	body := ""
	if m.ready {
		body = m.vp.View()
	}
	if m.wizActive {
		overview := m.wizardView()
		if m.picking {
			help := m.footer(" ↑↓ choose · enter select · esc settings")
			return top + "\n" + body + "\n" + overview + "\n" + m.pickerView() + "\n" + help
		}
		help := m.footer(" type your answer + enter · esc settings")
		return top + "\n" + body + "\n" + overview + "\n " + m.input.View() + "\n" + help
	}
	if m.picking {
		help := m.footer(" ↑↓ choose · enter select · esc settings")
		return top + "\n" + body + "\n" + m.pickerView() + "\n" + help
	}
	if m.paused {
		help := m.footer(" ⏸ paused — type a correction + enter to steer · enter to resume · esc settings")
		return top + "\n" + body + "\n " + m.input.View() + "\n" + help
	}
	help := m.footer(" enter send/expand · shift+enter newline · ↑↓ select · click expand · pgup/pgdn scroll · ctrl+i interrupt · ctrl+x stop · esc settings · ctrl+b backlog · ctrl+n new task")
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

// wizardView renders an overview of all questions in a multi-question ask_user
// call alongside each collected answer, marking the question currently being
// answered. The active question's picker/textarea is rendered below it.
func (m model) wizardView() string {
	var b strings.Builder
	b.WriteString(" " + askStyle.Render(" ? ") + " " +
		dimStyle.Render(fmt.Sprintf("question %d of %d", m.wizIdx+1, len(m.wizQuestions))) + "\n")
	for i, q := range m.wizQuestions {
		marker := "  "
		if i == m.wizIdx {
			marker = selStyle.Render("▸ ")
		}
		num := fmt.Sprintf("%d. ", i+1)
		prompt := q.prompt
		if m.w > 0 {
			prompt = trunc(prompt, m.w-len(num)-4)
		}
		line := num + prompt
		if i == m.wizIdx {
			line = selStyle.Render(line)
		}
		b.WriteString("  " + marker + line + "\n")
		// Show the collected answer (or a pending marker) under each question.
		var ansTxt string
		if a := m.wizAnswers[i]; a.done {
			if a.idx >= 0 && a.idx < len(q.options) {
				ansTxt = "→ " + q.options[a.idx]
			} else {
				ansTxt = "→ " + a.text
			}
		} else if i == m.wizIdx {
			ansTxt = "→ (answer below)"
		} else {
			ansTxt = "→ …"
		}
		if m.w > 0 {
			ansTxt = trunc(ansTxt, m.w-6)
		}
		b.WriteString("     " + dimStyle.Render(ansTxt) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- per-event rendering ---
func autoExpand(t string) bool { return t == "session_idle" || t == "question_asked" }

func (m *model) renderBlock(i int, ev *v1.Event) string {
	// An actor's name is spelled out only when it FIRST starts acting in a run
	// of consecutive rows; continuation rows show just its glyph (color + glyph
	// carry the identity), which declutters long single-actor stretches.
	first := m.firstOfRun(i)
	// Tool calls render as LSP-style "cards": a bordered frame whose title is
	// inset into the top border, with a status glyph and a nested Response box.
	// A tool_call is combined with its adjacent tool_result into one card.
	if ev.Type == "tool_call" {
		var res *v1.Event
		if ri := m.mergedResultIdx(i); ri >= 0 {
			res = m.evs[ri]
		}
		return m.renderToolCall(i, ev, res, first)
	}
	body := m.bodyFor(ev)
	hasBody := strings.TrimSpace(body) != ""
	exp := m.expanded[int(ev.Seq)] || autoExpand(ev.Type)
	header := m.renderHeader(ev, i == m.selected, exp && hasBody, hasBody, first)
	if exp && hasBody {
		return header + "\n" + body
	}
	return header
}

// firstOfRun reports whether event i begins a new run of rows by its actor: true
// when the previous *rendered* row (skipping tool_results folded into their
// call) belongs to a different actor. Used to spell out the actor's name only at
// the start of each run.
func (m *model) firstOfRun(i int) bool {
	j := i - 1
	for j >= 0 && m.isMergedResult(j) {
		j--
	}
	if j < 0 {
		return true
	}
	return m.evs[j].Actor != m.evs[i].Actor
}

// renderToolCall renders a tool_call (optionally with its folded tool_result) as
// either a compact one-line summary (collapsed) or a bordered card (expanded).
// res is nil while the call is still in flight.
func (m *model) renderToolCall(i int, call, res *v1.Event, first bool) string {
	exp := m.expanded[int(call.Seq)] || autoExpand(call.Type)
	selected := i == m.selected || (res != nil && i+1 == m.selected)

	paramsBody := m.cardParams(call)
	var resultBody string
	if res != nil {
		resultBody = m.cardResult(res)
	}
	hasBody := strings.TrimSpace(paramsBody) != "" || strings.TrimSpace(resultBody) != "" || res == nil

	if !(exp && hasBody) {
		return m.toolCollapsed(call, res, selected, hasBody, first)
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
func (m *model) toolCollapsed(call, res *v1.Event, selected, hasBody, first bool) string {
	bar := "  "
	if selected {
		bar = selBarStyle.Render("▌ ")
	}
	indent := ""
	if isSub(call.Actor) {
		indent = "  "
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
		avail := m.w - len(indent) - 8 - lipgloss.Width(line)
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

func (m *model) renderHeader(ev *v1.Event, selected, expanded, hasBody, first bool) string {
	return m.renderHeaderDetail(ev, selected, expanded, hasBody, detailLine(ev), first)
}

func (m *model) renderHeaderDetail(ev *v1.Event, selected, expanded, hasBody bool, detail string, first bool) string {
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
	// model_turn is the agent's own narration — it frames the surrounding tool
	// activity, so we drop the redundant "model_turn" type label and let the
	// words read as prose.
	typeSeg := typeStyle.Render(ev.Type) + " "
	if ev.Type == "model_turn" {
		typeSeg = ""
	}
	return fmt.Sprintf("%s%s%s%s %s",
		bar, indent, dimStyle.Render(tri),
		m.actorColumn(ev.Actor, first),
		typeSeg+trunc(detail, avail))
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
	case "question_asked":
		if qs := dataQuestions(ev); len(qs) > 0 {
			var b strings.Builder
			for i, q := range qs {
				fmt.Fprintf(&b, "%d. %s\n", i+1, q.prompt)
				for _, o := range q.options {
					b.WriteString("   - " + o + "\n")
				}
			}
			return indentLines(m.markdown(strings.TrimRight(b.String(), "\n")), "  ")
		}
		txt := firstField(ev, "question")
		if txt == "" {
			return ""
		}
		return indentLines(m.markdown(txt), "  ")
	case "question_answered":
		if ans := dataList(ev, "answers"); len(ans) > 0 {
			var b strings.Builder
			for i, a := range ans {
				fmt.Fprintf(&b, "A%d: %s\n", i+1, a)
			}
			return indentLines(m.markdown(strings.TrimRight(b.String(), "\n")), "  ")
		}
		txt := firstField(ev, "answer")
		if txt == "" {
			return ""
		}
		return indentLines(m.markdown(txt), "  ")
	case "model_turn", "session_idle", "user_input":
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
		// Error output keeps the existing plain/diff/cat-n behavior — we don't
		// language-highlight error text (it's usually not source code).
		if dataField(ev, "error") == "true" {
			return indentLines(highlightResult(r), bodyBar)
		}
		return indentLines(m.highlightToolResult(r, ev), bodyBar)
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


func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// --- styles ---

// These package-level styles are (re)built from the active theme by applyTheme
// (see theme.go); init() populates them with the dark theme. No raw color
// literals live here — every color is a named role in theme.go.
var (
	titleStyle    lipgloss.Style
	headerStyle   lipgloss.Style
	selStyle      lipgloss.Style
	recoStyle     lipgloss.Style
	selBarStyle   lipgloss.Style
	dimStyle      lipgloss.Style
	thinkStyle    lipgloss.Style
	typeStyle     lipgloss.Style
	askStyle      lipgloss.Style
	errStyle      lipgloss.Style
	diffAddStyle  lipgloss.Style
	diffDelStyle  lipgloss.Style
	diffHunkStyle lipgloss.Style
	diffMetaStyle lipgloss.Style

	borderStyle    lipgloss.Style
	borderSelStyle lipgloss.Style
	successStyle   lipgloss.Style
	pathStyle      lipgloss.Style
	cardTitleStyle lipgloss.Style
)

func actorStyle(actor string) lipgloss.Style {
	switch {
	case actor == "coordinator":
		return lipgloss.NewStyle().Foreground(activeTheme.actorCoord)
	case actor == "implementer":
		return lipgloss.NewStyle().Foreground(activeTheme.actorImpl)
	case strings.HasPrefix(actor, "reviewer"):
		return lipgloss.NewStyle().Foreground(activeTheme.actorReviewer)
	case actor == "user":
		return lipgloss.NewStyle().Foreground(activeTheme.actorUser)
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

func detailLine(ev *v1.Event) string {
	switch ev.Type {
	case "tool_call":
		return fmt.Sprintf("%s(%s)", dataField(ev, "name"), oneLine(dataField(ev, "args"), 70))
	case "tool_result":
		return appendDur(ev, oneLine(dataField(ev, "result"), 90))
	case "model_turn":
		return appendDur(ev, oneLine(dataField(ev, "text"), 120))
	case "thinking":
		return dimStyle.Render("(reasoning) " + oneLine(dataField(ev, "text"), 110))
	case "user_input":
		return "› " + oneLine(dataField(ev, "text"), 120)
	case "question_asked":
		if qs := dataQuestions(ev); len(qs) > 0 {
			prompts := make([]string, len(qs))
			for i, q := range qs {
				prompts[i] = q.prompt
			}
			return "? " + oneLine(fmt.Sprintf("%d questions: %s", len(qs), strings.Join(prompts, " · ")), 120)
		}
		return "? " + oneLine(dataField(ev, "question"), 120)
	case "question_answered":
		if ans := dataList(ev, "answers"); len(ans) > 0 {
			return oneLine(strings.Join(ans, " · "), 120)
		}
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

// appendDur appends a compact, dim-styled elapsed-duration suffix to a row's
// detail line when the event carries a positive duration_ms, so per-turn and
// per-tool-call timing is visible when scanning the chat log.
func appendDur(ev *v1.Event, s string) string {
	ms := durationMSField(ev)
	if ms <= 0 {
		return s
	}
	return s + dimStyle.Render(" "+fmtDurMS(ms))
}

// durationMSField reads the numeric duration_ms field from an event's data JSON,
// returning 0 when absent or unparsable.
func durationMSField(ev *v1.Event) int64 {
	if ev.DataJson == "" {
		return 0
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return 0
	}
	if v, ok := mp["duration_ms"].(float64); ok {
		return int64(v)
	}
	return 0
}

// fmtDurMS renders a millisecond duration compactly: sub-second as "340ms",
// otherwise one-decimal seconds like "1.2s".
func fmtDurMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
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
	case bool:
		return fmt.Sprintf("%t", v)
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

// wizQuestion is one parsed question in a multi-question ask_user call.
type wizQuestion struct {
	prompt  string
	options []string
}

// wizAnswer is the user's collected answer to one wizard question. idx >= 0
// selects an option (resolved to its text on the daemon); idx == -1 means the
// free-text field holds the answer.
type wizAnswer struct {
	idx     int
	text    string
	done    bool
	picking bool // chosen via the picker (vs. typed) — for the overview display
}

// dataQuestions parses the `questions` field of a question_asked event into a
// slice of wizQuestion. Returns nil when absent or empty (single-question form).
func dataQuestions(ev *v1.Event) []wizQuestion {
	if ev.DataJson == "" {
		return nil
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return nil
	}
	raw, ok := mp["questions"].([]any)
	if !ok {
		return nil
	}
	var out []wizQuestion
	for _, item := range raw {
		qm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		prompt, _ := qm["question"].(string)
		if strings.TrimSpace(prompt) == "" {
			continue
		}
		q := wizQuestion{prompt: prompt}
		if opts, ok := qm["options"].([]any); ok {
			for _, o := range opts {
				if s, ok := o.(string); ok && strings.TrimSpace(s) != "" {
					q.options = append(q.options, s)
				}
			}
		}
		out = append(out, q)
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
