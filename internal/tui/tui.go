// Package tui is the Bubble Tea home-menu + session client for ycc (spec §3).
// It lists modes, starts a session, and renders the live event stream with
// click-to-expand turns, auto-expanded final responses, and syntax highlighting
// (markdown via glamour, colorized diffs, dimmed cat -n line numbers).
package tui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/whyrusleeping/ycc/internal/clientconfig"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
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

// quitGuardWindow is how long the first ctrl+c stays "armed": a second ctrl+c
// within this window quits, otherwise the guard disarms silently (task 0109).
const quitGuardWindow = 2 * time.Second

// quitGuardHint is the warning shown while the quit guard is armed (task 0109).
const quitGuardHint = "agent running — ctrl+c again to quit"

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
	prompt  textarea.Model

	// "work (loop)" is daemon-owned. loop is the home-menu toggle; looping mirrors
	// a running/stopping WorkLoopInfo, loopInfo is the latest daemon snapshot, and
	// loopArmed defers starting a loop until an attended work session has ended.
	loop         bool
	looping      bool
	loopArmed    bool
	loopArmStop  bool
	loopInfo     *v1.WorkLoopInfo
	loopSeq      int
	loopDigest   *loopDigest
	digest       bool
	digestCursor int

	// session browser / previous-sessions screen (spec §18.6): a navigable list of
	// persisted + live sessions reached from the menu (ctrl+r) or the browse
	// selector. Enter drills into a read-only replayed transcript; `o` reopens the
	// selected session via ResumeSession ("resume = replay").
	history       []*v1.SessionSummary
	historyCursor int
	historyMsgTxt string // status/error line for the session list
	// historyTranscript gates the read-only transcript drill-in: when true the
	// session browser shows the selected session's replayed event log (loaded into
	// the shared event-rendering pipeline: m.evs + m.vp) instead of the list.
	historyTranscript bool
	historyTransID    string // session id whose transcript is currently shown
	// historyWaitingOnly restricts the session browser to live sessions that need
	// the user (pending question or paused). Set when the browser is opened from
	// the home menu's "session waiting for you" indicator (task 0107).
	historyWaitingOnly bool

	// histModal is the session browser opened as a modal OVER a live session
	// (ctrl+r / browse selector → sessions from within a session, task 0112).
	// Unlike stateHistory (a full state reached from the menu) it never touches
	// the live session's event pipeline (m.evs/m.vp), so browsing here is strictly
	// read-only: transcripts render into a separate viewport and reopen is disabled
	// (no reopen-over-live-session footgun). It reuses the shared m.history list
	// and m.historyCursor for navigation.
	histModal           bool
	histModalTranscript bool           // true => a transcript is drilled into (over the list)
	histModalID         string         // session id whose transcript is shown
	histModalVP         viewport.Model // scroll viewport for the modal transcript (never m.vp)
	// Line-based search + jump navigation for the modal transcript (task 0119).
	// It mirrors the live-session transcript's / n/N and {}()<>[] keys but operates
	// over the rendered string lines — it MUST NOT touch the live session's
	// m.evs/m.vp or live search state (m.searching/m.searchQuery). histModalEvents
	// retains the replayed events so a resize can re-render at the new width.
	histModalEvents     []*v1.Event     // replayed events kept alongside the viewport
	histModalLines      []string        // rendered content lines (with ansi) for matching/highlight
	histModalEventLines []histEventLine // per visible event: start line + Type (jump targets)
	histModalSearching  bool            // true while the `/` search bar owns input
	histModalQuery      string          // active search query (kept for n/N after enter)
	histModalCurLine    int             // current match/cursor line, or -1 for none

	// waitingSessions holds the live sessions that need the user right now — a
	// pending ask_user question or a paused-mid-steer session (task 0107). The home
	// menu surfaces a count + a one-key route in ("s"); refreshed on entry to the
	// menu and on a modest tick so a background question appears without a keypress.
	waitingSessions []*v1.SessionSummary
	// waitingSeq guards the menu refresh tick so a stale tick from a previous menu
	// visit can't multiply the in-flight timers.
	waitingSeq int

	// Home-menu project-context header (task 0139): orientation data shown as a
	// one-line segment strip beneath the title — git branch/dirtiness, and today's
	// spend. Every segment degrades gracefully (drops out) when its data is
	// unavailable (non-git workspace, no priced usage, etc.).
	gitBranch string // current branch name; "" => not a git workspace / unknown
	gitDirty  bool   // working tree has uncommitted changes
	// today's spend, populated from a throttled GetUsage over just today's log.
	todaySpend       float64   // total $ spent today (0 => segment dropped)
	todaySpendStatus string    // priced | partial | "" (unknown/loaded-but-empty)
	todaySpendLoaded bool      // a spend fetch has completed at least once
	lastSpendFetch   time.Time // throttle: refetch today's spend at most ~once/60s
	// lastSession is the most recent resumable session, used for the "c continue
	// last session" one-key affordance. nil => no session to continue.
	lastSession *v1.SessionSummary

	// browse selector (spec §18.6/§20.5): a small modal routing to the list+detail
	// browsers — backlog, sessions, and cost (spec §18.6/§20.5).
	browse       bool
	browseCursor int

	// help modal (task 0111): a scrollable keybinding cheat-sheet, modal over both
	// the menu and a session. See help.go for the binding catalog.
	helpOpen   bool
	helpScroll int

	sessionID string
	mode      string
	events    chan *v1.Event

	evs       []*v1.Event
	expanded  map[int]bool   // seq -> manually expanded
	bodyCache map[int]string // seq -> rendered multi-line body
	// blockCache holds each event's FULLY rendered block (header + body/card,
	// keyed by event index) so rebuild() doesn't re-render the whole transcript
	// on every keypress/event — for a long session that repeated per-row work
	// (JSON re-parsing via dataField, diff/highlight rendering, lipgloss framing)
	// is what made the log view slow. Rows rendered in their selected state are
	// never stored (selection moves constantly), and entries are invalidated
	// surgically when a later event changes an earlier row's rendering (see
	// appendEvent) or wholesale when a global input changes (invalidateRender).
	blockCache map[int]string
	// hiddenCache memoizes hiddenRow(i) by event index: the fold scans behind it
	// (ask_user plumbing pairing, echoed-idle detection) re-parse event JSON and
	// can walk the whole log, so computing them for every row on every rebuild
	// was O(N²). Invalidated together with blockCache.
	hiddenCache map[int]bool
	eventStart  []int // content line index where each event begins
	selected    int   // index into evs, or -1
	follow      bool  // auto-scroll + auto-select latest
	// liveTails holds the in-progress streamed output per actor, keyed by actor,
	// fed by transient turn_delta events (spec §5.2/§18.4, task 0114/0129). Values
	// are SNAPSHOTS (the full accumulated turn text so far), so a new delta simply
	// replaces the actor's entry. An entry is cleared by a done/empty delta or when
	// that actor's persisted model_turn / session_error arrives (the durable event
	// supersedes the live row). Transient turn_deltas NEVER enter m.evs / reducers /
	// seq tracking — they only drive this ephemeral tail row.
	liveTails map[string]string
	// retryNotes holds a per-actor "retrying…" note fed by transient retry
	// events (engine loop backoff on a transient API failure, spec §7.2). Like
	// liveTails it is ephemeral live state: a note is replaced by the next retry
	// event for the actor, and cleared when a fresh attempt starts streaming
	// (non-empty turn_delta) or the actor's persisted model_turn / session_error
	// arrives (the durable outcome supersedes the wait).
	retryNotes map[string]string
	// deliveredSeqs holds the seqs of queued mid-run user_input echoes that a
	// later user_input_delivered event has marked as delivered (spec §18.7), so a
	// queued echo renders "(queued)" only until its delivery point.
	deliveredSeqs map[int64]bool

	// transcript search (task 0116): `/` starts an incremental case-insensitive
	// search over the rendered event stream (headlines + expanded bodies), shared
	// by the live session view and the read-only history transcript. searching is
	// true while the query is being typed in the footer search bar; searchQuery
	// stays non-empty after enter so n/N can keep cycling matches. Matches are
	// computed on demand (never cached as indices) so appended live events can
	// never leave stale positions behind. esc clears it.
	searching   bool
	searchQuery string

	vp viewport.Model
	// mouse drag-select-to-copy (select.go): press-drag-release over the
	// transcript viewport highlights a region and copies its plain text to the
	// system clipboard via OSC 52 on release; a press+release without motion
	// stays a plain click (expand/collapse). Coordinates are content-relative
	// (content line index / cell column) so the highlight stays glued to the
	// text while the viewport scrolls mid-drag.
	selDrag      bool // left button is down; a drag may be in progress
	selRegion    bool // the pointer moved while down: a selection exists
	selAnchorRow int  // press point (content coords)
	selAnchorCol int
	selHeadRow   int // latest drag point (content coords)
	selHeadCol   int
	// vpContent mirrors the exact content string last handed to m.vp.SetContent,
	// used to extract selected plain text (the viewport doesn't expose content).
	vpContent string
	input     textarea.Model
	glam      *glamour.TermRenderer
	pending   string
	// pendingSeq is the seq of the question_asked event whose (single) question
	// is currently awaiting an answer; 0 when none. It lets the transcript row
	// collapse to a pointer while the footer picker shows the same prompt, so
	// the question is never rendered twice on screen at once.
	pendingSeq int64
	status     string
	paused     bool // session is paused-to-steer (spec §18.7)

	// live status-bar state (task 0062): a running per-model token tally summed
	// from model_turn usage blocks, per-model pricing surfaced via ListModels, the
	// session/turn start used for the elapsed clock, and an activity spinner that
	// ticks via the Bubble Tea command loop while the session is running (or a
	// quick-capture RPC is in flight).
	usageByModel map[string]event.Usage    // logical model_name -> summed usage
	pricing      map[string]config.Pricing // logical model_name -> pricing ($/Mtok)
	sessionStart time.Time                 // when the current session view started
	notifyAfter  time.Time                 // events with Ts before this are replays; no bell
	spin         spinner.Model
	spinning     bool // a spinner.Tick command is already in flight

	// Spend guard status (task 0137, spec §20.6). budgetPct is the highest
	// fraction-of-cap the current session has crossed (from budget_warning
	// events); budgetExceeded is set once a budget_exceeded event is seen. Both
	// feed a visually distinct status-bar segment and reset per session view.
	budgetPct      float64
	budgetExceeded bool
	// Focused backlog task: the most recent task_focus event's task id (and its
	// title, when the event carried one). Feeds a status-bar segment so the
	// header shows which task the work agent is on; reset per session view.
	focusTask      string
	focusTaskTitle string
	// lastMouse records when we last saw a mouse event. bubbletea v1's input
	// parser leaks the bytes of a split SGR mouse report (common during rapid
	// scroll, when the 256-byte read buffer fills and cuts an event in half) as
	// stray keypresses into the focused input. We swallow key messages that look
	// like such fragments when they arrive right on the heels of mouse activity
	// (see dropMouseFragment).
	lastMouse time.Time

	// keyEnhanced is true once the terminal reports support for the kitty
	// keyboard protocol's disambiguation (bubbletea delivers a
	// KeyboardEnhancementsMsg at startup). Only then can ctrl+i be told apart
	// from Tab (both are byte 0x09); we use this to show the effective interrupt
	// hint in the footer (ctrl+i where distinguishable, ctrl+x everywhere else).
	keyEnhanced bool

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
	wizSeq       int64         // seq of the question_asked event whose batch the wizard is collecting

	// err holds a FATAL, unrecoverable error (e.g. the daemon is unreachable at
	// startup and no screen has any data yet). render() short-circuits to a
	// full-screen error with a retry/quit affordance only when err != nil. All
	// other (transient) RPC failures surface via flashErr instead.
	err error
	// flashErr is a transient, self-clearing inline error shown in the status
	// bar / menu notice while the live view keeps rendering. flashSeq guards the
	// clear timer so a stale timeout never wipes a newer error (task 0104).
	flashErr string
	flashSeq int
	// flashNote is a parallel transient, self-clearing inline *notice* (e.g.
	// "copied ✓" after an OSC 52 yank) shown in the status bar. It shares
	// flashSeq's clear-timer guard with flashErr (task 0141).
	flashNote string
	// quitArmed is set by the first ctrl+c while a one-shot daemon has live agent
	// work (a running/paused/pending session, a loop, a waiting background session,
	// or a capture in flight). A second ctrl+c within quitGuardWindow quits; the
	// quitSeq-guarded disarm timer clears it otherwise, so an accidental keypress
	// can't tear down in-flight work (task 0109).
	quitArmed bool
	quitSeq   int
	// connected records that the client has successfully talked to the daemon at
	// least once. Until then an RPC error is treated as a fatal startup failure;
	// afterwards every RPC failure is transient (task 0104).
	connected bool
	ready     bool
	w, h      int

	// settings overlay (spec §18.2): modal over both menu and session, opened by
	// Esc. It exposes per-role model config, UI prefs, and Quit.
	overlay      bool
	ovCursor     int
	models       []*v1.ModelInfo   // populated from ListModels
	thinkLevels  map[string]string // per-role thinking levels (coordinator|implementer|reviewers)
	roleCoord    string            // logical model driving the coordinator
	roleImpl     string            // logical model for the implementer
	roleReviewrs []string          // logical models for reviewers (multi-select)
	reviewerSub  int               // visible sub-cursor: which reviewer chip the next toggle affects
	prefs        clientconfig.Prefs

	// backlog browser (spec §18.5): modal over menu/session, opened with ctrl+b.
	// Read-only: lists tasks, drills into one task's full detail.
	backlog         bool
	backlogTasks    []*v1.BacklogTaskSummary
	backlogCursor   int
	backlogDetail   *v1.TaskDetail // nil => list view; set => detail view
	backlogShowDone bool           // when false (default), done tasks are hidden in the list view
	// backlogBlockedOnly restricts the list to blocked tasks. Set when the browser
	// is opened from the home menu's "blocked — waiting on you" indicator (task 0101).
	backlogBlockedOnly bool
	backlogVP          viewport.Model // scrollable viewport for the detail view
	// backlogStatusPrompt is set while the browser waits for a status-choice digit
	// (spec §18.5 grooming, task 0099): 1..6 map to todo/in_progress/in_review/done/blocked/proposed.
	backlogStatusPrompt bool
	// backlogNotice is a transient message shown in the browser footer (update
	// errors, "workspace not local", etc.); cleared on the next successful action.
	backlogNotice string

	// plan library browser (task 0020/0077): modal over menu/session, reached
	// from the browse selector (ctrl+o). Read-only: lists saved plans (plans/*.md)
	// and views one plan's markdown.
	plans       bool
	plansCursor int
	plansList   []*v1.PlanSummary
	planDetail  *v1.GetPlanResponse // nil => list view; set => detail view
	plansVP     viewport.Model      // scroll viewport for the plan detail markdown

	// cost view (spec §20.5, task 0039): modal over menu/session, reached from the
	// browse selector (ctrl+o). Read-only: shows the GetUsage token/cost breakdown
	// for the selected project, grouped by a single dimension cycled with "g".
	cost          bool
	costRows      []*v1.UsageRow
	costTotal     *v1.UsageRow
	costWorkspace string
	costGroupBy   []string // single dimension today: task|model|session|day|agent
	costCursor    int
	// costTask scopes the §20.5 table after a task drill-down; costTaskCursor
	// preserves the parent-row selection while that task 0174 detail is open.
	costTask       string
	costTaskCursor int
	// costGen guards task 0174 state against out-of-order GetUsage responses.
	costGen          int
	costMsg          string // status/empty line (loading…, (no usage recorded))
	subUsageAccounts []*v1.SubscriptionUsageAccount

	// quick-add backlog capture overlay (spec §18.2, task 0016): modal over
	// menu/session, opened with ctrl+n. It runs a lightweight, off-stream capture
	// agent server-side so the running session is undisturbed.
	capture         bool
	captureInput    textarea.Model
	captureStage    int            // 0 describe · 1 answer clarification · 2 created (dismiss)
	captureQuestion string         // the agent's clarifying question (stage 1)
	captureDesc     string         // the original description (carried into stage 1)
	captureMsg      string         // status/result/error line
	captureBusy     bool           // a CaptureBacklogItem RPC is in flight
	captureEvents   chan *v1.Event // live capture agent action-log stream
	captureLog      []*v1.Event    // accumulated capture agent events for display

	// workstreams panel (task 0085, design §8): a modal browser over menu/session,
	// reached from the browse selector and opened after a multi-select spawn. It
	// lists a project's workstreams with live per-workstream status, drills into
	// the session view (reusing reopenSession), and hosts the merge/accept + discard
	// overlays.
	ws       bool
	wsList   []*v1.WorkstreamInfo
	wsCursor int
	// wsNotice is a transient footer message (spawn result, merge outcome, RPC
	// errors); cleared on the next successful action.
	wsNotice string
	// wsLocal overlays a locally-known state on a workstream row keyed by id:
	// "conflict" (a preview/merge returned conflicts) or "awaiting-review" (a clean
	// but review-gated merge). Fed by PreviewMerge/MergeWorkstream responses so a
	// conflict is a loud, sticky row state rather than a silent failure.
	wsLocal map[string]string
	// wsTick guards the panel's live-refresh tick so a stale tick from a previous
	// panel visit is dropped rather than compounding timers (mirrors waitingSeq).
	wsTick int
	// merge/accept overlay: wsMerge holds the PreviewMerge result for wsMergeID
	// (nil => no overlay). wsMergeVP scrolls the integrated diff / conflict list.
	wsMerge   *v1.PreviewMergeResponse
	wsMergeID string
	wsMergeVP viewport.Model
	// wsDiscardID is the workstream awaiting a two-step discard confirm (footer
	// prompt); "" => no pending confirm.
	wsDiscardID string

	// commit-diff drill-in overlay (task 0140): enter on a selected commit_made
	// transcript row opens a full-screen `git show` overlay (its own viewport, so
	// the §18.9 render caches are never touched). It draws over the live session,
	// the read-only history transcript, and the histModal transcript alike.
	cdiffOpen        bool
	cdiffSha         string // sha being shown (guards a late/racy fetch reply)
	cdiffMsgTxt      string // commit message for the title bar
	cdiffLoading     bool
	cdiffErr         string
	cdiffTruncated   bool
	cdiffVP          viewport.Model
	cdiffFiles       []cdiffFile
	cdiffPreamble    string // commit header + --stat block (rendered above the files)
	cdiffFold        []bool // per-file fold state (parallel to cdiffFiles)
	cdiffCursor      int    // file cursor index
	cdiffHeaderLines []int  // content line offset of each file's header (for scroll-into-view)

	// backlog multi-select spawn (task 0085): the set of selected task ids in the
	// backlog browser LIST view (todo tasks only), toggled with space and cleared
	// when the browser closes. `P` spawns one workstream per selected task.
	backlogSelected map[string]bool

	// model-backends management modal (spec §18.2, task 0044): list / add / edit /
	// duplicate / remove logical model backends, wired to the 0041 RPCs
	// (ListModels/GetModelConfig/UpsertModel/RemoveModel). Opened from the settings
	// overlay's "model backends" row; modal over both menu and session.
	mbOpen       bool
	mbView       int    // 0=list · 1=form · 2=confirm-remove
	mbCursor     int    // cursor into m.models in the list view
	mbErr        string // inline error/validation message
	mbInfo       string // inline non-error status (e.g. model-discovery result)
	mbBusy       bool   // a discovery RPC is in flight
	mbFormMode   int    // mbAdd | mbEdit | mbDuplicate
	mbOrigName   string // name of the model loaded for edit/duplicate
	mbOrigModel  string // model id of the model loaded for edit (to keep its name)
	mbAuthIdx    int    // index into mbAuthList: api-key (default) vs oauth subscription auth
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
// project-picker screen for persistent/remote daemons. A one-shot daemon still
// exposes cwd as a normal project, but it is the unambiguous sole choice.
func Run(ctx context.Context, client yccv1connect.SessionServiceClient, workspace string, showPicker bool) error {
	p := tea.NewProgram(initialModel(ctx, client, workspace, showPicker))
	_, err := p.Run()
	return err
}

func initialModel(ctx context.Context, client yccv1connect.SessionServiceClient, workspace string, showPicker bool) model {
	prefs := clientconfig.Load()
	// Apply the persisted theme to the package-level palette/chroma at launch so
	// the lipgloss palette and syntax style match the saved pref (glamour already
	// reads prefs.Theme in makeRenderer).
	applyTheme(themeByName(prefs.Theme))
	prompt := newChatInput("what should the agent do? (optional for 'work')")
	prompt.Focus()

	input := newSessionInput()

	captureInput := newChatInput("describe a new backlog item…")

	// Activity spinner (task 0062): a small dot animation tinted with the palette's
	// success role; it ticks via the Bubble Tea command loop while the session is
	// running or a quick-capture RPC is in flight.
	spin := spinner.New(spinner.WithSpinner(spinner.Dot))
	spin.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(activeTheme.success))

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
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		blockCache: map[int]string{}, hiddenCache: map[int]bool{},
		selected: -1, follow: prefs.Follow,
		deliveredSeqs: map[int64]bool{},
		liveTails:     map[string]string{},
		prefs:         prefs,
		thinkLevels:   map[string]string{"coordinator": "high", "implementer": "high", "reviewers": "high"},
		spin:          spin,
		usageByModel:  map[string]event.Usage{},
		pricing:       map[string]config.Pricing{},
	}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.fetchModes, m.fetchModels, m.fetchProjects, m.menuRefreshTick()}
	// A persistent multi-project client must pick a project before issuing scoped
	// RPCs. A one-shot daemon has one project, so omission is unambiguous while the
	// ListProjects response is still in flight.
	if !m.showPicker {
		cmds = append(cmds, m.fetchBacklog, m.fetchWaitingSessions)
	}
	return tea.Batch(cmds...)
}

// flash arms a transient, self-clearing inline error (shown in the status bar /
// menu notice) while the live view keeps rendering, and returns a command that
// clears it after a timeout unless a newer error supersedes it. When the client
// has never reached the daemon it is a fatal startup failure instead: render()
// short-circuits to the full-screen error with a retry affordance (task 0104).
func (m *model) flash(err error) tea.Cmd {
	if err == nil {
		return nil
	}
	if !m.connected {
		m.err = err
		return nil
	}
	m.flashSeq++
	m.flashErr = err.Error()
	seq := m.flashSeq
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return flashClearMsg{seq} })
}

// clearFlash dismisses any transient inline error. Bumping flashSeq also disarms
// the pending clear timer so it can't wipe a future error (task 0104).
func (m *model) clearFlash() {
	m.flashErr = ""
	m.flashNote = ""
	m.flashSeq++
}

// noteFlash arms a transient, self-clearing inline notice (e.g. "copied ✓" after
// a clipboard yank), mirroring flash() but for a success/info message instead of
// an error. It clears any pending error, bumps flashSeq (disarming stale clear
// timers), and arms a shorter clear tick (task 0141).
func (m *model) noteFlash(msg string) tea.Cmd {
	m.flashSeq++
	m.flashErr = ""
	m.flashNote = msg
	seq := m.flashSeq
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return flashClearMsg{seq} })
}

// quitGuardActive reports whether quitting right now would tear down live agent
// work on a one-shot in-process daemon. On a persistent daemon (showPicker) the
// work survives the client disconnecting, so the guard never applies (task 0109).
func (m *model) quitGuardActive() bool {
	if m.showPicker {
		return false
	}
	if m.looping || m.captureBusy || len(m.waitingSessions) > 0 {
		return true
	}
	if m.sessionID != "" {
		switch m.status {
		case "running", "paused", "waiting for your answer":
			return true
		}
	}
	return false
}

// confirmQuit implements the two-step ctrl+c guard: when live agent work would
// be killed, the first press arms the guard (and shows a warning) while a second
// press within quitGuardWindow quits. When no work is at risk it quits at once
// (task 0109).
func (m model) confirmQuit() (tea.Model, tea.Cmd) {
	if !m.quitGuardActive() || m.quitArmed {
		return m, tea.Quit
	}
	m.quitArmed = true
	m.quitSeq++
	seq := m.quitSeq
	return m, tea.Tick(quitGuardWindow, func(time.Time) tea.Msg { return quitDisarmMsg{seq} })
}

// markConnected records that the client has reached the daemon at least once,
// so subsequent RPC failures are treated as transient rather than a fatal
// startup failure (task 0104). It does not touch the visible flash.
func (m *model) markConnected() {
	m.connected = true
}

// rpcOK marks the client connected and clears any lingering transient error — a
// successful user-facing RPC/action/fetch dismisses the previous flash (task
// 0104). It also clears a lingering fatal startup error: Init fires several
// fetches concurrently, so one may fail (setting m.err while not yet connected)
// just before another succeeds — proof the daemon is reachable after all.
func (m *model) rpcOK() {
	m.connected = true
	m.err = nil
	m.clearFlash()
}

// mouseFragmentRe matches the printable remnants of a split SGR mouse report
// ("<Cb;Cx;Cy" optionally with a trailing M/m). The required digit-then-';'
// shape keeps it from matching ordinary typed text — real chat input is
// virtually never a bare run of digits and semicolons.
var mouseFragmentRe = regexp.MustCompile(`^<?[0-9]+;[0-9;]*[Mm<]?$`)

// dropMouseFragment reports whether a key message is actually a leaked fragment
// of a mouse escape sequence that bubbletea v1's parser failed to reassemble
// (see the lastMouse field). We only drop when the keystroke arrives hard on the
// heels of genuine mouse activity AND looks like SGR mouse bytes, so it cannot
// eat real typing during normal use.
func (m model) dropMouseFragment(k tea.KeyMsg) bool {
	if time.Since(m.lastMouse) > 150*time.Millisecond {
		return false
	}
	key := k.Key()
	// "\x1b[" from a split report surfaces as alt+[.
	if key.Mod&tea.ModAlt != 0 && key.Text == "[" {
		return true
	}
	if key.Text == "" {
		return false
	}
	return mouseFragmentRe.MatchString(key.Text)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Track mouse activity and swallow keystrokes that are really the leaked
	// bytes of a split mouse report (bubbletea v1 input-parser bug). This runs
	// ahead of all state dispatch so it protects every input box uniformly.
	switch msg := msg.(type) {
	case tea.MouseMsg:
		m.lastMouse = time.Now()
	case tea.KeyMsg:
		if m.dropMouseFragment(msg) {
			return m, nil
		}
	case tea.KeyboardEnhancementsMsg:
		// The terminal told us whether it can disambiguate keys via the kitty
		// keyboard protocol. Remember it so the footer advertises the interrupt
		// chord that actually works: ctrl+i (== Tab byte-wise) only survives
		// where disambiguation is available; ctrl+x works everywhere.
		m.keyEnhanced = msg.SupportsKeyDisambiguation()
	}

	// A fatal startup failure owns the screen (render short-circuits to it). Only
	// the retry/quit affordance is live here; retry re-runs the Init fetches and
	// clears the fatal error so a recovered daemon brings the UI back (task 0104).
	if m.err != nil {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "ctrl+c", "q":
				return m, tea.Quit
			case "r":
				m.err = nil
				return m, m.Init()
			}
			return m, nil
		}
	}

	switch msg := msg.(type) {
	case spinner.TickMsg:
		// Advance the activity spinner only while there is activity to indicate.
		// When the session goes idle/paused/error (and no capture RPC is running)
		// we stop ticking so the spinner doesn't resurrect on a stale error state
		// (task 0051): the next start re-arms it via spinnerCmd.
		if m.status != "running" && !m.captureBusy {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case tea.WindowSizeMsg:
		// Reinitialize any zero-value textareas (e.g. a test-constructed model
		// literal): a zero-value textarea has an uninitialized viewport and panics
		// on SetWidth.
		if m.input.MaxHeight == 0 {
			m.input = newSessionInput()
		}
		if m.prompt.MaxHeight == 0 {
			m.prompt = newChatInput("what should the agent do? (optional for 'work')")
			m.prompt.Focus()
		}
		if m.captureInput.MaxHeight == 0 {
			m.captureInput = newChatInput("describe a new backlog item…")
		}
		m.w, m.h = msg.Width, msg.Height
		vpHeight := msg.Height - headerHeight - 2
		if vpHeight < 3 {
			vpHeight = 3
		}
		if !m.ready {
			m.vp = viewport.New(viewport.WithWidth(msg.Width), viewport.WithHeight(vpHeight))
			m.ready = true
		} else {
			m.vp.SetWidth(msg.Width)
			m.vp.SetHeight(vpHeight)
		}
		// Reserve room for the rounded frame (inputFrameStyle) drawn around each
		// input so the framed box fits the same width the bare input used to.
		inputW := msg.Width - 4 - inputFrameStyle.GetHorizontalFrameSize()
		m.prompt.SetWidth(inputW)
		m.input.SetWidth(inputW)
		m.captureInput.SetWidth(inputW)
		m.makeRenderer()
		m.invalidateRender() // re-render bodies at the new width
		m.rebuild()
		m.relayout()
		m.refreshBacklogDetailVP()
		m.refreshPlanDetailVP()
		m.refreshWsMergeVP()
		m.refreshCdiffVP()
		// Keep the modal transcript viewport (task 0112) sized to the terminal, and
		// re-wrap its retained events to the new width (task 0119). Preserve the
		// current line highlight / scroll position when a search or jump is active.
		if m.histModalVP.Height() != 0 || m.histModalVP.Width() != 0 {
			h := msg.Height - 2
			if h < 3 {
				h = 3
			}
			m.histModalVP.SetWidth(msg.Width)
			m.histModalVP.SetHeight(h)
			if m.histModalTranscript && m.histModalEvents != nil {
				off := m.histModalVP.YOffset()
				content, lines, eventLines := m.renderTranscript(m.histModalEvents)
				m.histModalLines = lines
				m.histModalEventLines = eventLines
				// A re-wrap shifts every line offset, so a stale highlight line would
				// point at the wrong text; clear the cursor and restore raw scroll.
				m.histModalCurLine = -1
				m.histModalVP.SetContent(content)
				m.histModalVP.SetYOffset(off)
			}
		}
		return m, nil

	case modesMsg:
		m.rpcOK()
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
		m.rpcOK()
		m.models = msg.models
		// The reviewer sub-cursor indexes m.models; the backend set can shrink,
		// so clamp it back into range defensively.
		if m.reviewerSub >= len(m.models) {
			m.reviewerSub = 0
		}
		// Seed the per-role pickers with the daemon's CURRENT default assignment
		// (config.Roles) so the settings overlay shows the real selection — even
		// when opened from the home menu with no live session. A live session keeps
		// these in sync via role_config_changed events.
		if msg.coordinator != "" {
			m.roleCoord = msg.coordinator
		}
		if msg.implementer != "" {
			m.roleImpl = msg.implementer
		}
		if len(msg.reviewers) > 0 {
			m.roleReviewrs = msg.reviewers
		}
		// Seed the thinking pickers with the daemon's current default levels too.
		if msg.coordThink != "" {
			m.thinkLevels["coordinator"] = msg.coordThink
		}
		if msg.implThink != "" {
			m.thinkLevels["implementer"] = msg.implThink
		}
		if msg.revThink != "" {
			m.thinkLevels["reviewers"] = msg.revThink
		}
		// Build the per-model pricing table (task 0062) used by the live status
		// bar's token/cost readout. Only models flagged priced get an entry, so an
		// unpriced model is absent from the map and sessionUsage renders tokens-only
		// rather than inventing a cost.
		m.pricing = map[string]config.Pricing{}
		for _, mi := range msg.models {
			if mi.GetPriced() {
				m.pricing[mi.GetName()] = config.Pricing{
					Input:      mi.GetPriceInput(),
					Output:     mi.GetPriceOutput(),
					CacheRead:  mi.GetPriceCacheRead(),
					CacheWrite: mi.GetPriceCacheWrite(),
					Configured: true,
				}
			}
		}
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
		m.rpcOK()
		m.projects = msg.projects
		if m.projectCur >= len(m.projects) {
			m.projectCur = 0
		}
		// A one-shot daemon has one ordinary named project. Select it
		// automatically so subsequent RPCs never rely on an empty/default project.
		if !m.showPicker && m.project == "" && len(m.projects) == 1 {
			m.project = m.projects[0].Name
			m.workspace = m.projects[0].Path
			return m, m.refreshMenu()
		}
		return m, nil
	case historyMsg:
		if msg.err != nil {
			m.history = nil
			m.historyMsgTxt = "error: " + msg.err.Error()
			return m, nil
		}
		m.rpcOK()
		m.history = msg.sessions
		if m.historyWaitingOnly {
			// Opened from the home-menu "session waiting for you" indicator: show
			// only the live sessions that need the user (task 0107).
			filtered := m.history[:0:0]
			for _, s := range msg.sessions {
				if sessionNeedsUser(s) {
					filtered = append(filtered, s)
				}
			}
			m.history = filtered
		}
		if m.historyCursor >= len(m.history) {
			m.historyCursor = 0
		}
		if len(m.history) == 0 {
			m.historyMsgTxt = "no previous sessions"
		} else {
			m.historyMsgTxt = ""
		}
		return m, nil
	case waitingSessionsMsg:
		// Awareness signal only: on error keep the last-known set and stay quiet
		// (never flash) — a transient RPC hiccup must not blank the menu line.
		if msg.err != nil {
			return m, nil
		}
		m.waitingSessions = msg.sessions
		m.lastSession = msg.recent
		return m, nil
	case menuGitMsg:
		// Awareness signal only (task 0139): on error clear the branch so the git
		// segment drops out of the header rather than showing stale data.
		if msg.err != nil {
			m.gitBranch, m.gitDirty = "", false
			return m, nil
		}
		m.gitBranch, m.gitDirty = msg.branch, msg.dirty
		return m, nil
	case menuSpendMsg:
		// Awareness signal only (task 0139): on error keep the last-known spend and
		// stay quiet — a transient RPC hiccup must not blank the header.
		if msg.err != nil {
			return m, nil
		}
		m.todaySpend, m.todaySpendStatus, m.todaySpendLoaded = msg.cost, msg.status, true
		return m, nil
	case menuRefreshMsg:
		// Drop a stale tick from a previous menu visit (seq guards against
		// compounding timers). Re-poll only while the menu is actually showing.
		if msg.seq != m.waitingSeq {
			return m, nil
		}
		if m.state != stateMenu {
			return m, m.menuRefreshTick()
		}
		cmds := []tea.Cmd{m.fetchWaitingSessions, m.fetchGitInfo, m.menuRefreshTick()}
		if !m.looping {
			cmds = append(cmds, m.fetchWorkLoop())
		}
		if cmd := m.maybeFetchSpend(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	case transcriptMsg:
		if msg.err != nil {
			m.historyMsgTxt = "error: " + msg.err.Error()
			return m, nil
		}
		m.rpcOK()
		m.historyMsgTxt = ""
		// When the session browser is open as a modal over a live session (task
		// 0112), render the replayed transcript statelessly into its own viewport —
		// the live session's event pipeline (m.evs/m.vp/caches) must be left intact.
		if m.histModal {
			m.histModalTranscript = true
			m.histModalID = msg.id
			m.resetHistModalNav()
			m.refreshHistModalVP(msg.events)
			return m, nil
		}
		// Load the replayed transcript into the shared event-rendering pipeline so
		// it renders identically to the live session view (reasoning, tool-calls,
		// folding all match), but read-only and starting at the top.
		m.historyTranscript = true
		m.historyTransID = msg.id
		m.evs = msg.events
		m.expanded = map[int]bool{}
		m.invalidateRender()
		m.deliveredSeqs = deliveredSeqSet(msg.events)
		m.liveTails = map[string]string{}
		m.eventStart = nil
		m.selected = -1
		m.follow = false
		m.clearSearch()
		if m.ready {
			m.rebuild()
			m.vp.GotoTop()
		}
		return m, nil
	case errMsg:
		return m, m.flash(msg.err)
	case flashClearMsg:
		if msg.seq == m.flashSeq {
			m.flashErr = ""
			m.flashNote = ""
		}
		return m, nil
	case quitDisarmMsg:
		if msg.seq == m.quitSeq {
			m.quitArmed = false
		}
		return m, nil
	case startedMsg:
		m.rpcOK()
		// Reset any stale event/view state from a prior session so a reopened
		// session renders cleanly from its replayed log (spec §18.6).
		m.evs = nil
		m.expanded = map[int]bool{}
		m.invalidateRender()
		m.deliveredSeqs = map[int64]bool{}
		m.liveTails = map[string]string{}
		m.eventStart = nil
		m.selected = -1
		m.follow = m.prefs.Follow
		m.pending, m.paused, m.picking = "", false, false
		m.pickerOpts, m.pickerCursor = nil, 0
		m.clearWizard()
		m.clearSearch()
		// Allocate a fresh event channel for this session. The subscribe goroutine
		// closes its channel when the stream ends; in a loop run the next session
		// must not reuse (and send on) that already-closed channel — doing so panics
		// with "send on closed channel" and crashes the TUI back to the shell.
		m.events = make(chan *v1.Event, 256)
		m.sessionID, m.mode, m.state, m.status = msg.id, msg.mode, stateSession, "running"
		// Reset the running usage tally and start the elapsed clock for the new (or
		// reopened) session — usage accumulates only over the current view (task 0062).
		m.usageByModel = map[string]event.Usage{}
		m.sessionStart = time.Now()
		// Reset the per-session spend-guard status for the new/reopened session
		// view (task 0137). A reopened session that already crossed the line
		// re-emits its budget_warning/budget_exceeded on replay, re-setting these.
		m.budgetPct, m.budgetExceeded = 0, false
		// Reset the focused-task readout: a reopened session that already focused
		// a task re-emits its task_focus on replay, re-setting these.
		m.focusTask, m.focusTaskTitle = "", ""
		// Events already persisted before we subscribed are replayed by the daemon
		// on reopen; only events genuinely newer than this instant should ring the
		// terminal bell / raise a desktop notification (task 0108).
		m.notifyAfter = m.sessionStart
		m.input.SetValue("")
		fc := m.input.Focus()
		m.relayout()
		spin := m.spinnerCmd() // arm the activity spinner (mutates m.spinning) before returning m
		return m, tea.Batch(m.subscribe(), fc, spin)
	case streamClosedMsg:
		m.loopArmStop = false
		if m.loopArmed {
			// An attended work session must be completely gone before asking the daemon
			// to start its own session; otherwise two coordinators could work the same
			// backlog concurrently.
			m.loopArmed = false
			m.status = "loop starting…"
			return m, m.startWorkLoop()
		}
		if m.looping {
			m.status = "loop: session ended — waiting for the daemon to pick the next task"
			return m, nil
		}
		m.status = "stream closed"
		return m, nil
	case workLoopMsg:
		// Historical digest reads are modal-only: they deliberately bypass lifecycle
		// and generation handling so Browse → digest cannot detach a running loop or
		// eject a live session back to the menu.
		if msg.openDigest {
			if msg.err != nil {
				return m, m.flash(msg.err)
			}
			m.rpcOK()
			m.loopDigest = digestFromWorkLoop(msg.info)
			m.digest, m.digestCursor = true, 0
			return m, nil
		}
		// GetWorkLoop can race a newer Start/Stop response. Background responses are
		// valid only in the generation in which their RPC was issued; start/stop
		// actions always apply and invalidate every older poll/timer chain.
		if !msg.initiated && msg.seq != m.loopSeq {
			return m, nil
		}
		if msg.initiated {
			m.loopSeq++
		}
		if msg.err != nil {
			cmd := m.flash(msg.err)
			if m.looping || msg.continuePoll {
				m.looping = true
				return m, tea.Batch(cmd, m.loopRefreshTick())
			}
			return m, cmd
		}
		m.rpcOK()
		wasLooping := m.looping
		m.loopInfo = msg.info
		if msg.info == nil {
			m.looping = false
			if !msg.initiated {
				m.loopSeq++
			}
			return m, nil
		}
		switch msg.info.State {
		case "running", "stopping":
			m.looping = true
			if !msg.initiated && !wasLooping {
				// A menu discovery is the root of a new single poll chain.
				m.loopSeq++
			}
			if msg.alreadyRunning {
				m.status = "loop already running — attached"
			} else if msg.info.State == "stopping" {
				m.status = "loop stopping: current task finishes, next not picked"
			} else if !wasLooping {
				m.status = "loop started"
			}
			cmds := []tea.Cmd{m.loopRefreshTick()}
			if id := msg.info.CurrentSessionId; id != "" && id != m.sessionID {
				cmds = append(cmds, m.reopenSession(id))
			} else if id == "" && wasLooping {
				m.status = "loop: waiting for the next task"
			}
			return m, tea.Batch(cmds...)
		case "finished":
			m.looping = false
			if !msg.initiated {
				m.loopSeq++
			}
			m.loopDigest = digestFromWorkLoop(msg.info)
			if msg.initiated || wasLooping {
				m.digest, m.digestCursor = true, 0
				m.state, m.status = stateMenu, msg.info.Outcome
				return m, m.refreshMenu()
			}
			return m, nil
		default:
			m.looping = false
			if !msg.initiated {
				m.loopSeq++
			}
			return m, nil
		}
	case loopTickMsg:
		if msg.seq != m.loopSeq || !m.looping {
			return m, nil
		}
		return m, m.fetchWorkLoop()
	case evMsg:
		m.markConnected()
		// Transient events (Seq=0, broadcast-only, e.g. turn_delta) are ephemeral
		// UI hints that are never persisted and carry no sequence number. Route them
		// into live tail state (applyTransient) but NEVER through appendEvent /
		// maybeNotify, so they can't enter the reducers, replay, or seq tracking
		// (task 0129).
		if msg.ev != nil && msg.ev.Transient {
			m.applyTransient(msg.ev)
		} else {
			m.appendEvent(msg.ev)
			m.maybeNotify(msg.ev)
		}
		// Coalesce a burst into one rebuild. On reopen the daemon replays the whole
		// persisted log (N events) which arrive buffered in m.events essentially at
		// once; draining them here and rebuilding a single time keeps reload O(N)
		// instead of O(N^2) (one full re-render per event). Update runs on the Bubble
		// Tea main loop and we only re-arm waitEvent after draining, so there is no
		// concurrent reader of m.events.
		closed := false
	drain:
		for {
			select {
			case ev, ok := <-m.events:
				if !ok {
					closed = true
					break drain
				}
				if ev != nil && ev.Transient {
					m.applyTransient(ev) // live tail only; never persisted (see above)
					continue
				}
				m.appendEvent(ev)
				m.maybeNotify(ev)
			default:
				break drain
			}
		}
		// Events can change the footer stack (question_asked shows the picker /
		// wizard; question_answered dismisses them), so recompute the viewport
		// height BEFORE rebuild — follow-mode's GotoBottom needs the final height
		// or the pending question scrolls off the bottom of the screen.
		m.relayout()
		m.rebuild()
		spin := m.spinnerCmd() // mutates m.spinning; evaluate before returning m
		if closed {
			return m, func() tea.Msg { return streamClosedMsg{} }
		}
		if m.loopArmed && !m.loopArmStop && m.status == "idle" {
			m.loopArmStop = true
			m.status = "loop armed: ending current session…"
			return m, tea.Batch(m.stopSession(), waitEvent(m.events), spin)
		}
		return m, tea.Batch(waitEvent(m.events), spin)
	case backlogMsg:
		m.rpcOK()
		m.backlogTasks = msg.tasks
		if m.backlogCursor >= len(m.backlogTasks) {
			m.backlogCursor = 0
		}
		return m, nil
	case taskDetailMsg:
		m.rpcOK()
		m.backlogDetail = msg.task
		m.refreshBacklogDetailVP()
		m.backlogVP.GotoTop()
		return m, nil
	case taskUpdatedMsg:
		// Backlog grooming result (task 0099): surface failures in the browser
		// footer, otherwise adopt the refreshed detail and re-read the list.
		if msg.err != nil {
			m.backlogNotice = "update failed: " + msg.err.Error()
			return m, nil
		}
		m.rpcOK()
		m.backlogNotice = ""
		if m.backlogDetail != nil && msg.task != nil && m.backlogDetail.Id == msg.task.Id {
			m.backlogDetail = msg.task
			m.refreshBacklogDetailVP()
		}
		return m, m.fetchBacklog
	case editorClosedMsg:
		// The external $EDITOR exited (task 0099): reload the task (a no-mutation
		// UpdateTask re-reads the file) and the list.
		if msg.err != nil {
			m.backlogNotice = "editor: " + msg.err.Error()
		} else {
			m.backlogNotice = ""
		}
		return m, tea.Batch(m.updateTaskCmd(msg.id, nil, nil), m.fetchBacklog)
	case plansMsg:
		m.rpcOK()
		m.plansList = msg.plans
		if m.plansCursor >= len(m.plansList) {
			m.plansCursor = 0
		}
		return m, nil
	case planDetailMsg:
		m.rpcOK()
		m.planDetail = msg.plan
		m.refreshPlanDetailVP()
		m.plansVP.GotoTop()
		return m, nil
	case commitDiffMsg:
		// Drop a reply that arrived after the overlay closed or moved on (task 0140).
		if !m.cdiffOpen || msg.sha != m.cdiffSha {
			return m, nil
		}
		m.rpcOK()
		m.cdiffLoading = false
		if msg.err != nil {
			m.cdiffErr = msg.err.Error()
			return m, nil
		}
		m.cdiffErr = ""
		m.cdiffTruncated = msg.truncated
		pre, files := parseCommitDiff(msg.diff)
		m.cdiffPreamble = pre
		m.cdiffFiles = files
		m.cdiffFold = make([]bool, len(files))
		// Large-commit safety (§18.9): open with everything folded so the overlay
		// renders instantly; the user unfolds what they want.
		if len(files) > cdiffFoldAllFiles || strings.Count(msg.diff, "\n") > cdiffFoldAllLines {
			for i := range m.cdiffFold {
				m.cdiffFold[i] = true
			}
		}
		m.cdiffCursor = 0
		m.refreshCdiffVP()
		m.cdiffVP.GotoTop()
		return m, nil
	case usageMsg:
		if msg.gen != m.costGen {
			return m, nil
		}
		m.rpcOK()
		m.costRows = msg.rows
		m.costTotal = msg.total
		m.costWorkspace = msg.workspace
		m.subUsageAccounts = msg.accounts
		m.costCursor = clampCursor(m.costCursor, len(m.costRows))
		if len(m.costRows) == 0 {
			m.costMsg = "(no usage recorded)"
		} else {
			m.costMsg = ""
		}
		return m, nil
	case workstreamsMsg:
		if msg.err != nil {
			m.wsNotice = "list failed: " + msg.err.Error()
			return m, nil
		}
		m.rpcOK()
		m.wsList = msg.list
		m.wsCursor = clampCursor(m.wsCursor, len(m.wsList))
		return m, nil
	case wsTickMsg:
		// Drop a stale tick from a previous panel visit; re-poll only while the
		// panel is open (guards against compounding timers, task 0085).
		if msg.seq != m.wsTick {
			return m, nil
		}
		if !m.ws {
			return m, nil
		}
		return m, tea.Batch(m.fetchWorkstreams, m.wsRefreshTick())
	case wsSpawnedMsg:
		if msg.err != nil {
			m.wsNotice = fmt.Sprintf("spawned %d, then failed: %s", msg.count, msg.err.Error())
		} else {
			m.wsNotice = fmt.Sprintf("spawned %d workstream(s)", msg.count)
		}
		// Open the Workstreams panel to monitor what was spawned.
		m.backlog = false
		m.backlogSelected = nil
		m.openWorkstreams()
		return m, tea.Batch(m.fetchWorkstreams, m.wsRefreshTick())
	case wsPreviewMsg:
		if msg.err != nil {
			m.wsMerge, m.wsMergeID = nil, ""
			m.wsNotice = "preview failed: " + msg.err.Error()
			return m, nil
		}
		m.rpcOK()
		m.wsMerge, m.wsMergeID = msg.preview, msg.id
		if m.wsLocal == nil {
			m.wsLocal = map[string]string{}
		}
		if msg.preview.GetClean() {
			delete(m.wsLocal, msg.id)
		} else {
			// A conflict is a loud, sticky row state — never a silent failure.
			m.wsLocal[msg.id] = "conflict"
		}
		m.refreshWsMergeVP()
		return m, nil
	case wsMergedMsg:
		if msg.err != nil {
			m.wsNotice = "merge failed: " + msg.err.Error()
			return m, nil
		}
		m.rpcOK()
		if m.wsLocal == nil {
			m.wsLocal = map[string]string{}
		}
		switch {
		case msg.res.GetMerged():
			delete(m.wsLocal, msg.id)
			m.wsMerge, m.wsMergeID = nil, ""
			m.wsNotice = "merged " + short(msg.id) + " → " + msg.res.GetCommit()
		case len(msg.res.GetConflicts()) > 0:
			m.wsLocal[msg.id] = "conflict"
			m.wsNotice = "conflict merging " + short(msg.id) + ": " + strings.Join(msg.res.GetConflicts(), ", ")
			// Reflect the conflict in the open overlay too.
			m.wsMerge = &v1.PreviewMergeResponse{Clean: false, Conflicts: msg.res.GetConflicts()}
			m.refreshWsMergeVP()
		case msg.res.GetNeedsAccept():
			m.wsLocal[msg.id] = "awaiting-review"
			m.wsNotice = "awaiting review for " + short(msg.id)
		}
		return m, m.fetchWorkstreams
	case wsDiscardedMsg:
		if msg.err != nil {
			m.wsNotice = "discard failed: " + msg.err.Error()
			return m, nil
		}
		m.rpcOK()
		if m.wsLocal != nil {
			delete(m.wsLocal, msg.id)
		}
		m.wsNotice = "discarded " + short(msg.id)
		return m, m.fetchWorkstreams
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
	case mbDiscoverMsg:
		m.mbBusy = false
		if msg.err != nil {
			m.mbErr = "discover failed: " + msg.err.Error()
			return m, nil
		}
		if len(msg.ids) > 0 {
			m.mbInputs[mbFieldModel].SetValue(strings.Join(msg.ids, " "))
			m.mbInputs[mbFieldModel].CursorEnd()
		}
		m.mbErr = ""
		m.mbInfo = msg.note
		return m, nil
	}

	// The project picker (spec §3.1) is shown first when attached to a
	// persistent/remote daemon; it owns input until a project is chosen.
	if m.state == statePicker {
		return m.updatePicker(msg)
	}

	// The commit-diff drill-in overlay (task 0140) is modal over EVERYTHING —
	// the live session, the read-only history transcript, and the histModal
	// transcript. This check must precede the stateHistory branch so enter on a
	// commit row in the history transcript opens (and its keys drive) the overlay.
	if m.cdiffOpen {
		return m.updateCommitDiff(msg)
	}

	// The previous-sessions screen (ctrl+r from the menu) owns input until the
	// user reopens a session or returns to the menu (spec §18.6).
	if m.state == stateHistory {
		return m.updateHistory(msg)
	}

	// The keybinding help modal (?) is modal over both the menu and a session
	// (task 0111). It owns input while open — scroll + close.
	if m.helpOpen {
		return m.updateHelp(msg)
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

	// The plan library browser (browse selector → plans) is modal over both the
	// menu and a session (task 0077).
	if m.plans {
		return m.updatePlans(msg)
	}

	// The cost view (browse selector → cost) is modal over both the menu and a
	// session (spec §20.5, task 0039).
	if m.cost {
		return m.updateCost(msg)
	}

	// The Workstreams panel (browse selector → workstreams, or opened after a
	// multi-select spawn) is modal over both the menu and a session (task 0085).
	if m.ws {
		return m.updateWorkstreams(msg)
	}

	// The work-loop batch digest (shown when a loop ends, re-opened from the
	// browse selector) is modal over both the menu and a session (task 0098).
	if m.digest {
		return m.updateDigest(msg)
	}

	// The session browser opened as a modal over a live session (ctrl+r / browse
	// selector → sessions from within a session, task 0112). Read-only: it owns
	// input while open and never disturbs the live session behind it.
	if m.histModal {
		return m.updateHistoryModal(msg)
	}

	// The browse selector (ctrl+o) is modal over the menu (spec §18.6/§20.5): it
	// routes to the backlog / session browsers.
	if m.browse {
		return m.updateBrowse(msg)
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
		// A live transcript search intercepts esc: clear it (and re-focus the
		// input) instead of opening settings (task 0116). A second esc then opens
		// the overlay as usual.
		if m.state == stateSession && (m.searching || m.searchQuery != "") {
			m.clearSearch()
			m.relayout()
			return m, m.input.Focus()
		}
		// Esc opens the overlay rather than leaving the session (spec §18.2).
		m.openOverlay()
		return m, nil
	}

	if m.state == stateMenu {
		return m.updateMenu(msg)
	}
	return m.updateSession(msg)
}

// View renders the model and declares the program-level terminal modes the TUI
// needs (alt screen + cell-motion mouse reporting). In bubbletea v2 these are
// properties of the returned View rather than NewProgram options.
func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m model) render() string {
	if m.err != nil {
		// Fatal, unrecoverable startup failure (e.g. daemon unreachable before any
		// screen has data). Offer a retry as well as quit — a transient RPC hiccup
		// never reaches here; it surfaces inline via flashErr (task 0104).
		return fmt.Sprintf("\n  error: %v\n\n  (r to retry · ctrl+c to quit)\n", m.err)
	}
	if m.helpOpen {
		return m.helpView()
	}
	if m.cdiffOpen {
		return m.commitDiffView()
	}
	if m.capture {
		return m.captureView()
	}
	if m.backlog {
		return m.backlogView()
	}
	if m.plans {
		return m.plansView()
	}
	if m.cost {
		return m.costView()
	}
	if m.ws {
		return m.workstreamsView()
	}
	if m.digest {
		return m.digestView()
	}
	if m.histModal {
		return m.histModalView()
	}
	if m.browse {
		return m.browseView()
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
