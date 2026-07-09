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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/reflow/wrap"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"

	"github.com/whyrusleeping/ycc/internal/clientconfig"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
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

	// "work (loop)" mode (toggled with tab on the work entry): chew through the
	// backlog unattended, starting a fresh work session for each ready task until
	// none remain (every task done, blocked, or in_review). loop is the menu toggle;
	// looping is true while a loop run is in flight.
	//
	// Stall detection is by backlog FINGERPRINT, not by predicting which task the
	// session will work: the coordinator is an LLM that picks its own task, so a
	// driver that guessed task X and then re-derived X afterwards (because the LLM
	// actually worked a different ready task, leaving X pending) would falsely
	// conclude "no progress" and bail after a single completed task. Instead we
	// snapshot the backlog (id+status of every task) before each session and stop
	// only when a finished session leaves that snapshot completely unchanged — i.e.
	// it genuinely advanced nothing. loopStarted marks that at least one session has
	// run, so the very first decision is never judged a stall.
	loop        bool
	looping     bool
	loopStarted bool
	loopPrevFP  string
	// loopStopping guards the idle→stop transition while looping: a finished work
	// session goes idle and blocks (it does not self-terminate), so the loop driver
	// stops it explicitly to close its stream and advance. The flag prevents issuing
	// StopSession more than once for the same idle session.
	loopStopping bool

	// batch digest (task 0098, §9): the "work (loop)" driver accumulates a
	// per-session summary as it runs (loopRun) and rolls it up into an
	// end-of-batch digest (loopDigest) shown when the loop ends — "here's what
	// happened while you were gone": tasks completed / blocked / in_review /
	// created, with commit sha, review verdicts, tokens and cost. loopDigest
	// survives dismissal so the browse selector can reopen it. digest gates the
	// modal digest surface (shared list+detail browser).
	loopRun      *loopRunState
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
	// Work-loop cap state: caps fetched once at loop start via GetBudget, and a
	// flag recording that a loop session's own budget was breached daemon-side so
	// the loop halts at the next decision point with a distinct outcome.
	loopCostCap    float64
	loopTokenCap   int64
	loopSessBreach bool

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
	// Esc. It exposes interaction level, per-role model config, UI prefs, and Quit.
	overlay      bool
	ovCursor     int
	models       []*v1.ModelInfo   // populated from ListModels
	level        string            // current interaction level (session)
	menuLevel    string            // home-menu selector: interaction level for the NEXT session (not persisted; defaults to judgement each launch — §18.2)
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
	costMsg       string // status/empty line (loading…, (no usage recorded))

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
		prefs:         prefs, level: "judgement", menuLevel: "judgement",
		thinkLevels:  map[string]string{"coordinator": "high", "implementer": "high", "reviewers": "high"},
		spin:         spin,
		usageByModel: map[string]event.Usage{},
		pricing:      map[string]config.Pricing{},
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
type modelsMsg struct {
	models      []*v1.ModelInfo
	coordinator string
	implementer string
	reviewers   []string
	coordThink  string
	implThink   string
	revThink    string
}
type projectsMsg struct{ projects []*v1.ProjectInfo }
type startedMsg struct{ id, mode string }

// loopDecisionMsg carries the "work (loop)" driver's decision after a session
// ends: next is the id of the next ready task to work (""=none left), and fp is a
// fingerprint of the whole backlog at this point (id+status of every task). The
// driver compares fp against the snapshot taken before the just-finished session
// to detect a genuine stall (nothing changed) without guessing which task ran.
type loopDecisionMsg struct {
	next  string
	fp    string
	tasks []*v1.BacklogTaskSummary // full backlog snapshot at this decision point
	err   error
}

// loopUsageMsg carries the per-session token/cost breakdown (GetUsage grouped by
// session) used to price the batch digest (task 0098, §20). The handler matches
// rows to the loop run's session ids and fills per-task + total cost.
type loopUsageMsg struct {
	rows []*v1.UsageRow
	err  error
}

// budgetCapsMsg carries the configured loop spend caps fetched once at loop start
// via GetBudget (task 0137, spec §20.6). The loop driver enforces the per-loop-run
// cap client-side; a fetch error just leaves the caps at 0 (unlimited).
type budgetCapsMsg struct {
	loopCost   float64
	loopTokens int64
}

// digestTaskMsg carries one blocked task's full detail so the digest can surface
// the specific reason it is blocked (task 0098; ties into §18.7 semantics).
type digestTaskMsg struct {
	id   string
	task *v1.TaskDetail
	err  error
}

type historyMsg struct {
	sessions []*v1.SessionSummary
	err      error
}

// waitingSessionsMsg carries the live sessions that need the user (pending
// question or paused) for the home-menu awareness line (task 0107). It is an
// awareness signal, not a screen: errors are ignored silently so a transient
// RPC hiccup never flashes on the menu.
type waitingSessionsMsg struct {
	sessions []*v1.SessionSummary
	// recent is the most-recent session overall (ListSessionHistory returns
	// most-recent first), used for the "ctrl+l continue last session" affordance
	// (task 0139). nil when there is no session to continue.
	recent *v1.SessionSummary
	err    error
}

// menuRefreshMsg is the modest tick that re-polls waiting sessions while the
// home menu is showing (task 0107), so a question raised in a background
// session surfaces without the user pressing a key. seq disarms stale ticks.
type menuRefreshMsg struct{ seq int }

// menuGitMsg carries the current git branch + dirtiness for the home-menu
// context header (task 0139). An error (non-git workspace, remote daemon, git
// missing) is delivered as an empty branch so the segment simply drops out.
type menuGitMsg struct {
	branch string
	dirty  bool
	err    error
}

// menuSpendMsg carries today's aggregated spend for the home-menu context
// header (task 0139). Errors are ignored silently — the segment drops out.
type menuSpendMsg struct {
	cost   float64
	status string
	err    error
}

// transcriptMsg carries a session's replayed event log for the read-only
// transcript drill-in (spec §18.6), or an error if the fetch failed.
type transcriptMsg struct {
	id     string
	events []*v1.Event
	err    error
}
type evMsg struct{ ev *v1.Event }
type streamClosedMsg struct{}
type errMsg struct{ err error }

// flashClearMsg fires ~5s after a transient error was shown; the handler clears
// flashErr only when seq still matches the current flash so a stale timer never
// wipes a newer error (task 0104).
type flashClearMsg struct{ seq int }

// quitDisarmMsg fires quitGuardWindow after the first ctrl+c armed the quit
// guard; the handler clears quitArmed only when seq still matches so a stale
// timer never disarms a freshly re-armed guard (task 0109).
type quitDisarmMsg struct{ seq int }
type backlogMsg struct{ tasks []*v1.BacklogTaskSummary }
type taskDetailMsg struct{ task *v1.TaskDetail }

// commitDiffMsg carries the result of a GetCommitDiff RPC for the commit-diff
// drill-in overlay (task 0140). sha guards a late reply arriving after the
// overlay was closed or a different commit was opened.
type commitDiffMsg struct {
	sha       string
	diff      string
	truncated bool
	err       error
}

// taskUpdatedMsg carries the result of an UpdateTask grooming RPC (task 0099):
// a refreshed TaskDetail on success, or an error to surface in the browser footer.
type taskUpdatedMsg struct {
	task *v1.TaskDetail
	err  error
}

// editorClosedMsg fires when the external $EDITOR spawned for a task exits (task
// 0099). The browser then reloads the task so hand-edits are reflected.
type editorClosedMsg struct {
	id  string
	err error
}
type plansMsg struct{ plans []*v1.PlanSummary }
type planDetailMsg struct{ plan *v1.GetPlanResponse }

// usageMsg carries the GetUsage breakdown for the cost view (spec §20.5, task 0039).
type usageMsg struct {
	rows      []*v1.UsageRow
	total     *v1.UsageRow
	workspace string
}

// workstreamsMsg carries the ListWorkstreams result for the panel (task 0085),
// or an error to surface in the panel footer.
type workstreamsMsg struct {
	list []*v1.WorkstreamInfo
	err  error
}

// wsSpawnedMsg reports the result of a multi-select "run in parallel" spawn (task
// 0085): count is how many workstreams were created before err (if any).
type wsSpawnedMsg struct {
	count int
	err   error
}

// wsPreviewMsg carries a PreviewMerge result for the merge overlay (task 0085).
type wsPreviewMsg struct {
	id      string
	preview *v1.PreviewMergeResponse
	err     error
}

// wsMergedMsg carries a MergeWorkstream result (task 0085): merged (with commit),
// still-conflicted (paths), or a review-gated needs_accept.
type wsMergedMsg struct {
	id  string
	res *v1.MergeWorkstreamResponse
	err error
}

// wsDiscardedMsg reports the result of a DiscardWorkstream (task 0085).
type wsDiscardedMsg struct {
	id  string
	err error
}

// wsTickMsg is the panel's live-refresh tick (task 0085); seq guards against
// compounding timers across panel visits.
type wsTickMsg struct{ seq int }

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

// mbDiscoverMsg carries the result of a DiscoverModels RPC (spec §13). On success
// the ids populate the connection form's model-id field; note is a human-readable
// status line (e.g. why a curated fallback was used).
type mbDiscoverMsg struct {
	ids     []string
	note    string
	fromNet bool
	err     error
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.fetchModes, m.fetchModels, m.fetchBacklog, m.fetchWaitingSessions, m.menuRefreshTick()}
	if m.showPicker {
		cmds = append(cmds, m.fetchProjects)
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
	return modelsMsg{
		models:      resp.Msg.Models,
		coordinator: resp.Msg.Coordinator,
		implementer: resp.Msg.Implementer,
		reviewers:   resp.Msg.Reviewers,
		coordThink:  resp.Msg.CoordinatorThinking,
		implThink:   resp.Msg.ImplementerThinking,
		revThink:    resp.Msg.ReviewersThinking,
	}
}

// startSession starts a session in the given mode. An empty level lets the daemon
// apply its default (judgement); the work loop passes "autonomous" so unattended
// runs never block on ask_user (spec §9, §11).
func (m model) startSession(mode, prompt, level string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.StartSession(m.ctx, connect.NewRequest(&v1.StartSessionRequest{
			Mode: mode, Prompt: prompt, Workspace: m.workspace, Project: m.project, InteractionLevel: level,
		}))
		if err != nil {
			return errMsg{err}
		}
		return startedMsg{id: resp.Msg.SessionId, mode: mode}
	}
}

// isWorkEntry reports whether a menu entry is the plain "work" mode (not a preset,
// which would carry an opening prompt). Only this entry supports the loop toggle.
func isWorkEntry(e menuEntry) bool { return e.mode == "work" && e.openingPrompt == "" }

// stopSession hard-terminates the current session via StopSession (spec §12). The
// loop driver uses it to end a finished work session that has gone idle (it blocks
// waiting for input rather than self-terminating): stopping it closes the event
// stream, which surfaces as streamClosedMsg and drives the next loop iteration.
func (m model) stopSession() tea.Cmd {
	id := m.sessionID
	return func() tea.Msg {
		if _, err := m.client.StopSession(m.ctx, connect.NewRequest(&v1.StopSessionRequest{SessionId: id})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// sessionFinished reports whether the current session view has reached a terminal
// state the user should be offered a clean exit from (task 0127): the agent went
// idle after finishing ("idle" — it now blocks in the daemon waiting for input, so
// leaving must StopSession to avoid an orphan) or the event stream already ended
// ("stream closed" — nothing left to stop). It deliberately EXCLUDES looping
// sessions (the work-loop driver already auto-stops idle sessions and advances —
// this must not interfere) and the recoverable "error"/"paused" states (esc →
// settings overlay → "back home" remains the escape hatch there).
func (m model) sessionFinished() bool {
	return m.state == stateSession && !m.looping && (m.status == "idle" || m.status == "stream closed")
}

// loopNext drives the "work (loop)" run: it loads the backlog, picks the next
// ready task, and decides whether to start another work session (spec §9). The
// decision is returned as a loopDecisionMsg so Update can apply it on the main loop.
//
// Investigation note (task 0168): the loop driver keeps NO snapshot or queue of
// tasks captured at loop start. Every iteration re-reads the LIVE backlog via the
// ListBacklog RPC (server → docs Store.List re-reads the backlog dir from disk),
// then topReadyTask picks from that fresh list. So tasks added mid-loop — via the
// coordinator's create_task, `ycc task add`, or ctrl+n quick capture — are already
// considered by the next pick, under the same eligibility rules as the initial
// pick (topReadyTask requires status todo/in_progress and Ready; proposed/blocked/
// in_review and dependency-blocked tasks are skipped). No behavioural fix needed.
func (m model) loopNext() tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.ListBacklog(m.ctx, connect.NewRequest(&v1.ListBacklogRequest{Project: m.project}))
		if err != nil {
			return loopDecisionMsg{err: err}
		}
		return loopDecisionMsg{
			next:  topReadyTask(resp.Msg.Tasks),
			fp:    backlogFingerprint(resp.Msg.Tasks),
			tasks: resp.Msg.Tasks,
		}
	}
}

// --- work-loop batch digest (task 0098, §9/§20) ---
//
// The loop driver accumulates a per-session summary as it runs and rolls it up
// into an end-of-batch digest when the loop stops. Everything is projected from
// data that already exists — the backlog snapshot at loop start (baseline), the
// live event log of each session (task_focus / commit_made / review_submitted),
// the running per-model token tally, and the GetUsage cost aggregator — rather
// than new bookkeeping.

// loopCommit is one commit made during a loop session.
type loopCommit struct{ task, sha, message string }

// loopSessRec is a per-session snapshot captured when a loop session's stream
// closes: its id, focus task, wall-clock duration, summed tokens, the commits it
// made, and the review verdicts it collected. Cost/priceStatus are filled later
// from GetUsage (grouped by session).
type loopSessRec struct {
	id          string
	focus       string
	dur         time.Duration
	tokens      int64
	commits     []loopCommit
	verdicts    []string
	cost        float64
	priceStatus string
}

// loopRunState accumulates across every session a single loop run drives.
// baseline is the backlog (id → summary) at loop start so the finished digest can
// classify each task by how it changed (completed / blocked / in_review / new).
type loopRunState struct {
	startedAt time.Time
	baseline  map[string]*v1.BacklogTaskSummary
	sessions  []loopSessRec
	// Cumulative spend across every session the run has driven, accumulated at
	// each session close (task 0137, spec §20.6). cumCost sums only priced
	// models' dollars; costStatus tracks pricing coverage so an unpriced run never
	// invents a dollar cap breach. Used by applyLoopDecision to enforce loop caps.
	cumTokens  int64
	cumCost    float64
	costStatus string
}

// digestTask is one task row in the finished digest.
type digestTask struct {
	id, title, status string
	sha               string
	verdictTally      string
	tokens            int64
	cost              float64
	priceStatus       string
	reason            string // blocked reason (filled from the task's work log)
}

// loopDigest is the finished, re-openable batch digest surface.
type loopDigest struct {
	outcome     string
	startedAt   time.Time
	dur         time.Duration
	sessions    []loopSessRec
	completed   []digestTask
	blocked     []digestTask
	inReview    []digestTask
	created     []digestTask
	totalTokens int64
	totalCost   float64
	costStatus  string
}

// notifyLoopDigest fires a fire-and-forget daemon push notification summarising a
// finished work-loop run (task 0142): "work loop finished: N completed, M blocked,
// K in review". Delivery is best-effort — the daemon may have notifications
// disabled or the "digest" kind muted, and an older daemon may lack the Notify RPC
// — so any error is ignored and the command produces no message.
func (m *model) notifyLoopDigest(d *loopDigest) tea.Cmd {
	if d == nil {
		return nil
	}
	line := fmt.Sprintf("work loop finished: %d completed, %d blocked, %d in review",
		len(d.completed), len(d.blocked), len(d.inReview))
	project, sessionID, client, ctx := m.project, m.sessionID, m.client, m.ctx
	return func() tea.Msg {
		_, _ = client.Notify(ctx, connect.NewRequest(&v1.NotifyRequest{
			Kind: "digest", Line: line, Project: project, SessionId: sessionID,
		}))
		return nil
	}
}

// buildLoopDigest rolls a finished loop run up into the digest artifact: it
// aggregates commits/verdicts/tokens per task from the session records and
// classifies every final backlog task against the run's baseline. It is pure so
// the roll-up is unit-testable. Cost is left "unpriced" until fetchLoopUsage fills
// it (§20.4: cost renders "—" while unpriced).
func buildLoopDigest(run *loopRunState, final []*v1.BacklogTaskSummary, outcome string) *loopDigest {
	d := &loopDigest{outcome: outcome, costStatus: "unpriced"}
	baseline := map[string]*v1.BacklogTaskSummary{}
	if run != nil {
		d.startedAt = run.startedAt
		if !run.startedAt.IsZero() {
			d.dur = time.Since(run.startedAt)
		}
		d.sessions = run.sessions
		baseline = run.baseline
	}

	shaByTask := map[string]string{}
	verdictsByTask := map[string][]string{}
	tokensByTask := map[string]int64{}
	for _, s := range d.sessions {
		d.totalTokens += s.tokens
		for _, c := range s.commits {
			if c.task != "" {
				shaByTask[c.task] = c.sha
			}
		}
		if s.focus != "" {
			verdictsByTask[s.focus] = append(verdictsByTask[s.focus], s.verdicts...)
			tokensByTask[s.focus] += s.tokens
		}
	}

	sorted := append([]*v1.BacklogTaskSummary(nil), final...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Id < sorted[j].Id })
	for _, t := range sorted {
		dt := digestTask{
			id: t.Id, title: t.Title, status: t.Status,
			sha:          shaByTask[t.Id],
			verdictTally: tallyVerdicts(verdictsByTask[t.Id]),
			tokens:       tokensByTask[t.Id],
			priceStatus:  "unpriced",
		}
		base, existed := baseline[t.Id]
		if !existed {
			d.created = append(d.created, dt)
			continue
		}
		switch t.Status {
		case "done":
			if base.Status != "done" {
				d.completed = append(d.completed, dt)
			}
		case "blocked":
			d.blocked = append(d.blocked, dt)
		case "in_review":
			d.inReview = append(d.inReview, dt)
		}
	}
	return d
}

// tallyVerdicts summarises a task's review verdicts as "approve×2 reject×1"
// (insertion order preserved) for the completed-task suffix.
func tallyVerdicts(verdicts []string) string {
	if len(verdicts) == 0 {
		return ""
	}
	counts := map[string]int{}
	var order []string
	for _, v := range verdicts {
		if _, ok := counts[v]; !ok {
			order = append(order, v)
		}
		counts[v]++
	}
	parts := make([]string, 0, len(order))
	for _, v := range order {
		parts = append(parts, fmt.Sprintf("%s×%d", v, counts[v]))
	}
	return strings.Join(parts, " ")
}

// mergeCostStatus combines two price statuses: unset ("") adopts the other; a
// disagreement is "partial" (some priced, some not).
func mergeCostStatus(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" || a == b {
		return a
	}
	return "partial"
}

// applyUsage prices the digest from GetUsage rows grouped by session: each row is
// matched to a session record (and, via its focus task, to a digest task), and
// per-task + total cost/priceStatus are recomputed (task 0098, §20).
func (d *loopDigest) applyUsage(rows []*v1.UsageRow) {
	bySession := map[string]*v1.UsageRow{}
	for _, r := range rows {
		bySession[r.Session] = r
	}
	costByTask := map[string]float64{}
	statusByTask := map[string]string{}
	var total float64
	totalStatus := ""
	for i := range d.sessions {
		s := &d.sessions[i]
		r := bySession[s.id]
		if r == nil {
			continue
		}
		s.cost, s.priceStatus = r.Cost, r.PriceStatus
		total += r.Cost
		totalStatus = mergeCostStatus(totalStatus, r.PriceStatus)
		if s.focus != "" {
			costByTask[s.focus] += r.Cost
			statusByTask[s.focus] = mergeCostStatus(statusByTask[s.focus], r.PriceStatus)
		}
	}
	d.totalCost = total
	if totalStatus != "" {
		d.costStatus = totalStatus
	}
	assign := func(list []digestTask) {
		for i := range list {
			if st, ok := statusByTask[list[i].id]; ok {
				list[i].cost = costByTask[list[i].id]
				list[i].priceStatus = st
			}
		}
	}
	assign(d.completed)
	assign(d.blocked)
	assign(d.inReview)
	assign(d.created)
}

// blockedReasonFromBody extracts a one-line reason a task is blocked from its
// markdown body: the last "## Work log" bullet mentioning "blocked", else the
// last bullet (task 0098; ties into §18.7). Empty when there is no work log.
func blockedReasonFromBody(body string) string {
	lines := strings.Split(body, "\n")
	inLog := false
	var bullets []string
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") {
			inLog = strings.Contains(strings.ToLower(t), "work log")
			continue
		}
		if inLog && (strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")) {
			bullets = append(bullets, strings.TrimSpace(t[2:]))
		}
	}
	if len(bullets) == 0 {
		return ""
	}
	for i := len(bullets) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(bullets[i]), "blocked") {
			return bullets[i]
		}
	}
	return bullets[len(bullets)-1]
}

// snapshotLoopSession captures the just-finished session's summary from live TUI
// state (event log + running token tally) for the batch digest roll-up.
func (m model) snapshotLoopSession() loopSessRec {
	rec := loopSessRec{id: m.sessionID, priceStatus: "unpriced"}
	if !m.sessionStart.IsZero() {
		rec.dur = time.Since(m.sessionStart)
	}
	for _, u := range m.usageByModel {
		rec.tokens += int64(u.Total)
	}
	for _, ev := range m.evs {
		switch ev.Type {
		case "task_focus":
			if t := dataField(ev, "task"); t != "" {
				rec.focus = t
			}
		case "commit_made":
			rec.commits = append(rec.commits, loopCommit{
				task: dataField(ev, "task"), sha: dataField(ev, "sha"), message: dataField(ev, "message"),
			})
		case "review_submitted":
			if v := dataField(ev, "verdict"); v != "" {
				rec.verdicts = append(rec.verdicts, v)
			}
		}
	}
	return rec
}

// fetchLoopUsage loads the per-session token/cost breakdown to price the batch
// digest (task 0098, §20). Grouped by session so rows match the run's sessions.
func (m model) fetchLoopUsage() tea.Msg {
	resp, err := m.client.GetUsage(m.ctx, connect.NewRequest(&v1.GetUsageRequest{
		Project: m.project, GroupBy: []string{"session"},
	}))
	if err != nil {
		return loopUsageMsg{err: err}
	}
	return loopUsageMsg{rows: resp.Msg.Rows}
}

// fetchBudget loads the configured spend caps once at loop start so the loop
// driver can enforce the per-loop-run cap client-side (task 0137, spec §20.6). A
// fetch error is swallowed to unlimited caps — the guard is best-effort and must
// never block starting a loop.
func (m model) fetchBudget() tea.Msg {
	resp, err := m.client.GetBudget(m.ctx, connect.NewRequest(&v1.GetBudgetRequest{}))
	if err != nil {
		return budgetCapsMsg{}
	}
	return budgetCapsMsg{loopCost: resp.Msg.LoopCost, loopTokens: resp.Msg.LoopTokens}
}

// fetchDigestTask loads one blocked task's detail so the digest can surface the
// specific reason it is blocked and offer a jump-to-task (task 0098).
func (m model) fetchDigestTask(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.GetTask(m.ctx, connect.NewRequest(&v1.GetTaskRequest{Project: m.project, Id: id}))
		if err != nil {
			return digestTaskMsg{id: id, err: err}
		}
		return digestTaskMsg{id: id, task: resp.Msg.Task}
	}
}

// backlogFingerprint is a stable, order-independent summary of the backlog's
// actionable state: the id and status of every task. The loop driver compares
// the fingerprint before and after a session to tell whether that session
// advanced anything at all (a task moving todo→in_progress→done/in_review/blocked,
// or a new task appearing, all change it). Equal fingerprints across a finished
// session mean nothing moved — a genuine stall.
func backlogFingerprint(tasks []*v1.BacklogTaskSummary) string {
	parts := make([]string, 0, len(tasks))
	for _, t := range tasks {
		parts = append(parts, t.Id+":"+t.Status)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// applyLoopDecision acts on the loop driver's decision: stop and return to the
// menu when nothing is actionable, an error occurred, or the just-finished session
// left the backlog completely unchanged (a stall — re-running would spin forever);
// otherwise start the next work session and stay in the loop.
func (m model) applyLoopDecision(msg loopDecisionMsg) (tea.Model, tea.Cmd) {
	// Initialise the run accumulator on the first decision of a loop run: the
	// baseline is the backlog at loop start so the end-of-batch digest can diff
	// against it. loopNext runs before/between every session, so this covers both
	// entry paths (menu enter and shift+tab mid-session).
	if m.loopRun == nil && msg.err == nil {
		m.loopRun = &loopRunState{startedAt: time.Now(), baseline: map[string]*v1.BacklogTaskSummary{}}
		for _, t := range msg.tasks {
			m.loopRun.baseline[t.Id] = t
		}
	}

	// finish stops the loop and, when at least one session actually ran, rolls the
	// run up into the re-openable batch digest ("here's what happened while you
	// were gone") and opens it — otherwise it just shows the status line as before
	// (e.g. starting a loop on an empty backlog).
	finish := func(outcome string) (tea.Model, tea.Cmd) {
		run := m.loopRun
		m.looping, m.loopStarted, m.loopPrevFP = false, false, ""
		m.loopRun = nil
		m.state, m.status = stateMenu, outcome
		if run == nil || len(run.sessions) == 0 {
			return m, m.refreshMenu()
		}
		m.loopDigest = buildLoopDigest(run, msg.tasks, outcome)
		m.digest, m.digestCursor = true, 0
		cmds := []tea.Cmd{m.fetchLoopUsage, m.refreshMenu(), m.notifyLoopDigest(m.loopDigest)}
		for _, bt := range m.loopDigest.blocked {
			cmds = append(cmds, m.fetchDigestTask(bt.id))
		}
		return m, tea.Batch(cmds...)
	}

	switch {
	case msg.err != nil:
		return finish("loop stopped: " + msg.err.Error())
	case msg.next == "":
		return finish("loop complete: no ready tasks remain")
	case m.loopStarted && msg.fp == m.loopPrevFP:
		// A session ran but the backlog is byte-for-byte unchanged: it advanced
		// nothing, so starting another would loop forever on the same state.
		return finish("loop stopped: session made no progress")
	case m.loopStarted && m.loopSessBreach:
		// A loop session's own budget was breached daemon-side (task 0137): halt
		// the loop at this safe decision point (the current session already
		// completed) with a distinct outcome recorded in the digest.
		return finish("loop stopped: session budget reached")
	}
	// Enforce the per-loop-run spend cap (task 0137, spec §20.6): once cumulative
	// tokens or priced cost crosses a configured cap, stop before starting the
	// next session. The just-finished session already completed so nothing is cut
	// off mid-write. Unpriced runs contribute no dollars so a cost cap never
	// breaches on them (§20.4).
	if m.loopStarted && m.loopRun != nil {
		if m.loopTokenCap > 0 && m.loopRun.cumTokens >= m.loopTokenCap {
			return finish(fmt.Sprintf("loop stopped: budget reached (%s tokens, cap %s)",
				fmtTokens(int(m.loopRun.cumTokens)), fmtTokens(int(m.loopTokenCap))))
		}
		if m.loopCostCap > 0 && m.loopRun.cumCost >= m.loopCostCap {
			return finish(fmt.Sprintf("loop stopped: budget reached ($%.2f, cap $%.2f)",
				m.loopRun.cumCost, m.loopCostCap))
		}
	}
	m.loopStarted, m.loopPrevFP = true, msg.fp
	// Loop sessions run autonomously: ask_user must never block an unattended run.
	// A genuinely stuck task is marked blocked (and skipped) rather than waited on.
	return m, m.startSession("work", "", "autonomous")
}

// blockedTaskCount reports how many backlog tasks are currently marked "blocked"
// (an autonomous/loop session set them aside pending user input — spec §10/§11).
// The home menu uses it to surface a "waiting on you" indicator (task 0101).
func (m model) blockedTaskCount() int {
	n := 0
	for _, t := range m.backlogTasks {
		if t.Status == "blocked" {
			n++
		}
	}
	return n
}

// topReadyTask returns the id of the task a work session would pick next: the
// highest-priority (lowest priority number) actionable task that is ready and not
// yet done/blocked/in-review — i.e. status "todo" or a resumable "in_progress".
// Ties break by id so the choice is stable. Returns "" when nothing is ready.
func topReadyTask(tasks []*v1.BacklogTaskSummary) string {
	best := ""
	bestPrio := int32(0)
	for _, t := range tasks {
		if !t.Ready || (t.Status != "todo" && t.Status != "in_progress") {
			continue
		}
		if best == "" || t.Priority < bestPrio || (t.Priority == bestPrio && t.Id < best) {
			best, bestPrio = t.Id, t.Priority
		}
	}
	return best
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

// fetchWaitingSessions loads the session history and delivers just the live
// sessions that need the user — a pending ask_user question or a paused
// (mid-steer) session (task 0107). It reuses ListSessionHistory (which carries
// status + waiting_input for both one-shot and persistent daemons) but delivers
// to its own message so the session-browser list state is never clobbered.
func (m model) fetchWaitingSessions() tea.Msg {
	resp, err := m.client.ListSessionHistory(m.ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{Project: m.project}))
	if err != nil {
		return waitingSessionsMsg{err: err}
	}
	var waiting []*v1.SessionSummary
	for _, s := range resp.Msg.Sessions {
		if sessionNeedsUser(s) {
			waiting = append(waiting, s)
		}
	}
	// The most-recent session (list is most-recent first) backs the "c continue
	// last session" affordance on the home menu (task 0139).
	var recent *v1.SessionSummary
	if len(resp.Msg.Sessions) > 0 {
		recent = resp.Msg.Sessions[0]
	}
	return waitingSessionsMsg{sessions: waiting, recent: recent}
}

// sessionNeedsUser reports whether a live session is waiting on the user: it is
// blocked on an unanswered ask_user question, or it is paused mid-steer. Only
// live sessions can hold either state (task 0107).
func sessionNeedsUser(s *v1.SessionSummary) bool {
	return s.Live && (s.WaitingInput || s.Status == "paused")
}

// menuRefreshTick arms the next home-menu refresh of waiting sessions, tagged
// with the current waitingSeq so a stale tick (from a previous menu visit) is
// dropped rather than compounding timers (task 0107).
func (m model) menuRefreshTick() tea.Cmd {
	seq := m.waitingSeq
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return menuRefreshMsg{seq} })
}

// refreshMenu re-polls the home-menu awareness data (backlog + waiting sessions)
// and (re)arms the waiting-session refresh tick, bumping waitingSeq so an older
// tick can't multiply the in-flight timers (task 0107).
func (m *model) refreshMenu() tea.Cmd {
	m.waitingSeq++
	cmds := []tea.Cmd{m.fetchBacklog, m.fetchWaitingSessions, m.fetchGitInfo, m.menuRefreshTick()}
	// Today's spend is throttled: the aggregator scans usage logs, so re-issuing
	// it on every 5s tick would be wasteful. Refetch at most ~once a minute.
	if cmd := m.maybeFetchSpend(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// maybeFetchSpend returns the today's-spend fetch cmd when it hasn't run within
// the throttle window (~60s), and records the attempt so subsequent menu ticks
// don't hammer the log-scanning aggregator (task 0139). Returns nil otherwise.
func (m *model) maybeFetchSpend() tea.Cmd {
	if !m.lastSpendFetch.IsZero() && time.Since(m.lastSpendFetch) < 60*time.Second {
		return nil
	}
	m.lastSpendFetch = time.Now()
	return m.fetchTodaySpend
}

// fetchGitInfo reads the current git branch and working-tree dirtiness of the
// (local) workspace for the home-menu context header (task 0139). It shells out
// to git directly (no shell) and returns an empty branch on any error — a
// non-git workspace, a remote daemon whose workspace isn't local here, or git
// being absent — so the header segment simply drops out.
func (m model) fetchGitInfo() tea.Msg {
	ws := m.workspace
	if ws == "" {
		return menuGitMsg{err: fmt.Errorf("no workspace")}
	}
	branchOut, err := exec.Command("git", "-C", ws, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return menuGitMsg{err: err}
	}
	branch := strings.TrimSpace(string(branchOut))
	if branch == "" {
		return menuGitMsg{err: fmt.Errorf("no branch")}
	}
	dirty := false
	if statusOut, err := exec.Command("git", "-C", ws, "status", "--porcelain").Output(); err == nil {
		dirty = strings.TrimSpace(string(statusOut)) != ""
	}
	return menuGitMsg{branch: branch, dirty: dirty}
}

// fetchTodaySpend aggregates today's token spend for the home-menu context
// header (task 0139) via GetUsage scoped to today (Since=Until=today), grouped
// by day. Any error is delivered so the segment drops out silently.
func (m model) fetchTodaySpend() tea.Msg {
	today := time.Now().Format("2006-01-02")
	resp, err := m.client.GetUsage(m.ctx, connect.NewRequest(&v1.GetUsageRequest{
		Project: m.project, GroupBy: []string{"day"}, Since: today, Until: today,
	}))
	if err != nil {
		return menuSpendMsg{err: err}
	}
	if resp.Msg.Total == nil {
		return menuSpendMsg{cost: 0, status: ""}
	}
	return menuSpendMsg{cost: resp.Msg.Total.Cost, status: resp.Msg.Total.PriceStatus}
}

// fetchTranscript loads a session's full replayed event log for the read-only
// transcript drill-in (spec §18.6) via the GetSessionTranscript RPC.
func (m model) fetchTranscript(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.GetSessionTranscript(m.ctx, connect.NewRequest(&v1.GetSessionTranscriptRequest{
			Project: m.project, SessionId: id,
		}))
		if err != nil {
			return transcriptMsg{id: id, err: err}
		}
		return transcriptMsg{id: id, events: resp.Msg.Events}
	}
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
func (m *model) startWizard(qs []wizQuestion, seq int64) {
	m.wizActive = true
	m.wizQuestions = qs
	m.wizAnswers = make([]wizAnswer, len(qs))
	for i := range m.wizAnswers {
		m.wizAnswers[i] = wizAnswer{idx: -1}
	}
	m.wizIdx = 0
	m.wizSeq = seq
	m.status = "waiting for your answer"
	// Invalidate the body cache so the active question_asked entry re-renders in
	// its condensed form (the wizard below is now the focal point, not the inline
	// log dump of every question).
	m.invalidateRender()
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
		m.relayout() // picker rows replace the input box; resize the viewport
		return nil
	}
	// Free-text question: re-focus the textarea so the user can type, even when a
	// preceding picker question blurred it. Focus() sets the focused state
	// synchronously (what matters for typing) and returns the cosmetic blink cmd.
	m.picking = false
	m.relayout() // input box replaces the picker rows; resize the viewport
	return m.input.Focus()
}

// clearWizard exits the questionnaire wizard and resets its state.
func (m *model) clearWizard() {
	m.wizActive = false
	m.wizQuestions = nil
	m.wizAnswers = nil
	m.wizIdx = 0
	m.wizSeq = 0
	// Invalidate the body cache so the (now answered) entry re-renders its full
	// enumerated form once the wizard is dismissed.
	m.invalidateRender()
	m.relayout() // the wizard overview rows are gone; give them back to the viewport
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
	m.relayout() // picker rows collapse back to the input box while awaiting question_answered
	// Re-focus the textarea: when the final question was a picker it blurred the
	// input, and nothing else focuses it again — leaving the session's input box
	// dead once the agent finishes. Focus() flips the typable state synchronously;
	// the returned cmd is only the cosmetic cursor blink.
	fc := m.input.Focus()
	return tea.Batch(fc, m.answerQuestions(answers))
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

// choosePickerOption commits the picker selection at idx (a valid index into
// m.pickerOpts). In a wizard it records the answer and advances; otherwise it
// clears the pending question and sends the answer. Shared by the enter and
// number-key paths.
func (m *model) choosePickerOption(idx int) tea.Cmd {
	m.picking = false
	if m.wizActive {
		return m.recordWizAnswer(idx, m.pickerOpts[idx], true)
	}
	m.pending = ""
	m.pickerOpts = nil
	m.follow = true
	m.relayout()
	// The picker blurred the textarea when the question arrived; give focus back
	// now that the picker collapses into the input box, or typing stays dead
	// after the agent moves on (the blurred textarea drops every key).
	fc := m.input.Focus()
	return tea.Batch(fc, m.answerQuestion(idx, ""))
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

// setThinking issues SetThinking per role (spec §7.4, §18.2). With a live session
// it applies to that session and persists; with no session an empty session_id
// just persists the new default. An empty role updates all roles. Either way the
// level is written to ycc.toml so it survives a restart.
func (m model) setThinking(role, level string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.SetThinking(m.ctx, connect.NewRequest(&v1.SetThinkingRequest{
			SessionId: m.sessionID, Level: level, Role: role,
		})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// setRoleConfig issues SetRoleConfig (spec §18.2). With a live session it applies
// the change to that session and persists it; with no session (changed from the
// home menu) an empty session_id just persists the new default. Either way the
// selection is written to ycc.toml so it survives a restart.
func (m model) setRoleConfig(coord, impl string, reviewers []string) tea.Cmd {
	return func() tea.Msg {
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

// updateTaskCmd grooms a backlog task via the daemon UpdateTask RPC (spec §18.5,
// task 0099). status/priority are nil-passthrough (leave field untouched); a call
// with both nil is a "refresh" that re-reads the file.
func (m model) updateTaskCmd(id string, status *string, priority *int32) tea.Cmd {
	return func() tea.Msg {
		req := &v1.UpdateTaskRequest{Project: m.project, Id: id, Status: status, Priority: priority}
		resp, err := m.client.UpdateTask(m.ctx, connect.NewRequest(req))
		if err != nil {
			return taskUpdatedMsg{err: err}
		}
		return taskUpdatedMsg{task: resp.Msg.Task}
	}
}

// editorCommand resolves the user's preferred editor: $EDITOR, then $VISUAL, then
// "vi" (task 0099). Kept small and side-effect-free so it is unit-testable.
func editorCommand() string {
	if e := strings.TrimSpace(os.Getenv("EDITOR")); e != "" {
		return e
	}
	if e := strings.TrimSpace(os.Getenv("VISUAL")); e != "" {
		return e
	}
	return "vi"
}

// openEditorCmd suspends the Bubble Tea program and opens path in the user's
// $EDITOR, returning an editorClosedMsg when it exits (task 0099).
func (m model) openEditorCmd(id, path string) tea.Cmd {
	fields := strings.Fields(editorCommand())
	name := fields[0]
	args := append(append([]string{}, fields[1:]...), path)
	return tea.ExecProcess(exec.Command(name, args...), func(err error) tea.Msg {
		return editorClosedMsg{id: id, err: err}
	})
}

// fetchPlans loads the saved plan library list for the plans browser (task 0077).
func (m model) fetchPlans() tea.Msg {
	resp, err := m.client.ListPlans(m.ctx, connect.NewRequest(&v1.ListPlansRequest{Project: m.project}))
	if err != nil {
		return errMsg{err}
	}
	return plansMsg{resp.Msg.Plans}
}

// fetchPlan loads one saved plan's markdown for the plans browser (task 0077).
func (m model) fetchPlan(name string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.GetPlan(m.ctx, connect.NewRequest(&v1.GetPlanRequest{Project: m.project, Name: name}))
		if err != nil {
			return errMsg{err}
		}
		return planDetailMsg{resp.Msg}
	}
}

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

// fetchUsage loads the token/cost breakdown for the cost view (spec §20.5, task
// 0039). It respects the selected project and the chosen group-by dimension.
func (m model) fetchUsage() tea.Msg {
	resp, err := m.client.GetUsage(m.ctx, connect.NewRequest(&v1.GetUsageRequest{
		Project: m.project, GroupBy: m.costGroupBy,
	}))
	if err != nil {
		return errMsg{err}
	}
	return usageMsg{rows: resp.Msg.Rows, total: resp.Msg.Total, workspace: resp.Msg.Workspace}
}

// fetchWorkstreams loads the workstreams for the current project for the panel
// (task 0085, design §8).
func (m model) fetchWorkstreams() tea.Msg {
	resp, err := m.client.ListWorkstreams(m.ctx, connect.NewRequest(&v1.ListWorkstreamsRequest{Project: m.project}))
	if err != nil {
		return workstreamsMsg{err: err}
	}
	return workstreamsMsg{list: resp.Msg.Workstreams}
}

// wsRefreshTick arms the next workstreams-panel refresh, tagged with the current
// wsTick so a stale tick (from a previous panel visit) is dropped rather than
// compounding timers (mirrors menuRefreshTick, task 0085).
func (m model) wsRefreshTick() tea.Cmd {
	seq := m.wsTick
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return wsTickMsg{seq} })
}

// spawnWorkstreamsCmd fires one SpawnWorkstream per selected backlog task (task
// 0085, design §8), stopping at the first error. Each session is seeded with a
// prompt naming the task and runs at the default (judgement) interaction level.
func (m model) spawnWorkstreamsCmd(tasks []*v1.BacklogTaskSummary) tea.Cmd {
	project := m.project
	ctx := m.ctx
	client := m.client
	return func() tea.Msg {
		n := 0
		for _, t := range tasks {
			prompt := fmt.Sprintf("Work on backlog task %s: %s", t.Id, t.Title)
			if _, err := client.SpawnWorkstream(ctx, connect.NewRequest(&v1.SpawnWorkstreamRequest{
				Project: project, TaskId: t.Id, Prompt: prompt, InteractionLevel: "judgement",
			})); err != nil {
				return wsSpawnedMsg{count: n, err: err}
			}
			n++
		}
		return wsSpawnedMsg{count: n}
	}
}

// previewMergeCmd trial-merges a workstream for the merge overlay (task 0085,
// design §6 step 1): clean + integrated diff, or the conflicted paths.
func (m model) previewMergeCmd(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.PreviewMerge(m.ctx, connect.NewRequest(&v1.PreviewMergeRequest{WorkstreamId: id}))
		if err != nil {
			return wsPreviewMsg{id: id, err: err}
		}
		return wsPreviewMsg{id: id, preview: resp.Msg}
	}
}

// mergeWorkstreamCmd integrates a workstream's branch back to base with accept=true
// (task 0085, design §6). A conflict returns the conflicted paths; base untouched.
func (m model) mergeWorkstreamCmd(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.MergeWorkstream(m.ctx, connect.NewRequest(&v1.MergeWorkstreamRequest{WorkstreamId: id, Accept: true}))
		if err != nil {
			return wsMergedMsg{id: id, err: err}
		}
		return wsMergedMsg{id: id, res: resp.Msg}
	}
}

// discardWorkstreamCmd abandons a workstream without merging (task 0085, design §6).
func (m model) discardWorkstreamCmd(id string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.DiscardWorkstream(m.ctx, connect.NewRequest(&v1.DiscardWorkstreamRequest{WorkstreamId: id})); err != nil {
			return wsDiscardedMsg{id: id, err: err}
		}
		return wsDiscardedMsg{id: id}
	}
}

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
		m.loopStopping = false
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
		m.status = "stream closed"
		// In a loop run, a closed stream means the work session finished. Record a
		// per-session summary for the batch digest before deciding whether to start
		// the next task rather than dropping back to an idle view.
		if m.looping {
			if m.loopRun != nil {
				rec := m.snapshotLoopSession()
				m.loopRun.sessions = append(m.loopRun.sessions, rec)
				// Accumulate loop-wide spend for the loop cost/token cap (task 0137).
				// Cost is the priced estimate from the live per-model tally; unpriced
				// models contribute tokens only (never invented dollars, §20.4).
				_, cost, status := m.sessionUsage()
				m.loopRun.cumTokens += rec.tokens
				m.loopRun.cumCost += cost
				m.loopRun.costStatus = mergeCostStatus(m.loopRun.costStatus, status)
				// A daemon-side per-session budget breach observed while looping stops
				// the loop at the next decision point with a distinct outcome.
				if m.budgetExceeded {
					m.loopSessBreach = true
				}
			}
			m.status = "loop: session ended — checking backlog…"
			return m, m.loopNext()
		}
		return m, nil
	case loopDecisionMsg:
		return m.applyLoopDecision(msg)
	case budgetCapsMsg:
		// Loop spend caps fetched at loop start (task 0137). May arrive after the
		// first session already started — that's fine, the caps apply from here on.
		m.loopCostCap, m.loopTokenCap = msg.loopCost, msg.loopTokens
		return m, nil
	case loopUsageMsg:
		// Price the batch digest from the per-session usage breakdown (task 0098).
		if msg.err == nil && m.loopDigest != nil {
			m.loopDigest.applyUsage(msg.rows)
		}
		return m, nil
	case digestTaskMsg:
		// Fill a blocked digest task's specific reason from its work log (task 0098).
		if msg.err == nil && m.loopDigest != nil && msg.task != nil {
			reason := blockedReasonFromBody(msg.task.Body)
			for i := range m.loopDigest.blocked {
				if m.loopDigest.blocked[i].id == msg.id {
					m.loopDigest.blocked[i].reason = reason
				}
			}
		}
		return m, nil
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
		// In a loop run a finished work session goes idle and blocks waiting for
		// input rather than self-terminating, so its stream never closes on its own.
		// When that happens, stop it explicitly: closing its stream surfaces as
		// streamClosedMsg, which advances the loop to the next ready task. The guard
		// ensures we issue StopSession only once for this idle session.
		if m.looping && !m.loopStopping && m.status == "idle" {
			m.loopStopping = true
			m.status = "loop: task finished — advancing…"
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
		m.rpcOK()
		m.costRows = msg.rows
		m.costTotal = msg.total
		m.costWorkspace = msg.workspace
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
		return m.confirmQuit()
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
		return m, m.refreshMenu()
	}
	return m, nil
}

// updateHistory handles the session browser (spec §18.6): navigate the list of
// persisted + live sessions, Enter drills into a read-only replayed transcript,
// `o` reopens the selected session via ResumeSession, `r` refreshes, Esc/q backs
// out (transcript → list, list → menu).
func (m model) updateHistory(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Transcript drill-in: a read-only replayed view that scrolls via the viewport.
	if m.historyTranscript {
		// Mouse: wheel scrolls, and a left-button drag selects + copies the
		// region to the clipboard on release (select.go), matching the live
		// session view. A plain click is a no-op here (rows expand via enter).
		switch mm := msg.(type) {
		case tea.MouseWheelMsg:
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		case tea.MouseClickMsg:
			if mm.Button == tea.MouseLeft {
				m.selMouseDown(mm.X, mm.Y)
			}
			return m, nil
		case tea.MouseMotionMsg:
			m.selMouseMotion(mm.X, mm.Y)
			return m, nil
		case tea.MouseReleaseMsg:
			if text, dragged := m.selMouseUp(); dragged && text != "" {
				return m, tea.Batch(tea.SetClipboard(text), m.noteFlash("copied ✓"))
			}
			return m, nil
		}
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			return m, nil
		}
		// While the search bar owns input (task 0116), keystrokes edit the query
		// and incrementally re-jump the selection. Unconditional here (no input
		// textarea to protect).
		if m.searching {
			switch key.String() {
			case "ctrl+c":
				return m.confirmQuit()
			case "esc":
				m.clearSearch()
				return m, nil
			case "enter":
				// Confirm: keep the query active for n/N.
				m.searching = false
				return m, nil
			case "backspace":
				if r := []rune(m.searchQuery); len(r) > 0 {
					m.searchQuery = string(r[:len(r)-1])
				}
				m.runSearch()
				return m, nil
			default:
				if t := key.Key().Text; t != "" {
					m.searchQuery += t
					m.runSearch()
				}
				return m, nil
			}
		}
		switch key.String() {
		case "ctrl+c":
			return m.confirmQuit()
		case "esc":
			// A transcript search intercepts the first esc: clear it and stay in
			// the transcript. A second esc backs out to the list.
			if m.searchQuery != "" {
				m.clearSearch()
				return m, nil
			}
			fallthrough
		case "q", "backspace", "left":
			// Back to the list: drop the transient transcript event state.
			m.historyTranscript = false
			m.historyTransID = ""
			m.evs = nil
			m.expanded = map[int]bool{}
			m.invalidateRender()
			m.deliveredSeqs = map[int64]bool{}
			m.eventStart = nil
			m.selected = -1
			m.clearSearch()
			if m.ready {
				m.rebuild()
			}
			return m, nil
		case "/":
			// Enter transcript search (unconditional in the read-only transcript).
			m.searching = true
			m.searchQuery = ""
			return m, nil
		case "n":
			m.searchStep(1, m.selected+1)
			return m, nil
		case "N":
			m.searchStep(-1, m.selected-1)
			return m, nil
		case "{":
			m.jumpToEvent(-1, "question_asked")
			return m, nil
		case "}":
			m.jumpToEvent(1, "question_asked")
			return m, nil
		case "(":
			m.jumpToEvent(-1, "review_submitted")
			return m, nil
		case ")":
			m.jumpToEvent(1, "review_submitted")
			return m, nil
		case "<":
			m.jumpToEvent(-1, "commit_made")
			return m, nil
		case ">":
			m.jumpToEvent(1, "commit_made")
			return m, nil
		case "[":
			m.jumpToEvent(-1, "session_error")
			return m, nil
		case "]":
			m.jumpToEvent(1, "session_error")
			return m, nil
		case "enter":
			// Enter on a selected commit_made row drills into that commit's diff
			// overlay (task 0140); otherwise it reopens the session (like `o`).
			if m.selected >= 0 && m.selected < len(m.evs) && m.evs[m.selected].Type == "commit_made" {
				ev := m.evs[m.selected]
				if cmd := m.openCommitDiff(dataField(ev, "sha"), dataField(ev, "message")); cmd != nil {
					return m, cmd
				}
			}
			fallthrough
		case "o":
			// Reopen the session whose transcript we're viewing (resume = replay).
			m.historyTranscript = false
			m.clearSearch()
			m.historyMsgTxt = "reopening " + short(m.historyTransID) + "…"
			return m, m.reopenSession(m.historyTransID)
		}
		// Everything else (↑/↓, pgup/pgdn, wheel) scrolls the transcript viewport.
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.state = stateMenu
		m.historyWaitingOnly = false
		return m, m.refreshMenu()
	case "r":
		m.historyMsgTxt = "loading…"
		return m, m.fetchHistory
	case "up":
		m.historyCursor = navUp(m.historyCursor)
		return m, nil
	case "down":
		m.historyCursor = navDown(m.historyCursor, len(m.history))
		return m, nil
	case "enter":
		// Drill into a read-only replayed transcript of the selected session.
		if len(m.history) == 0 {
			return m, nil
		}
		sel := m.history[m.historyCursor]
		m.historyMsgTxt = "loading transcript…"
		return m, m.fetchTranscript(sel.SessionId)
	case "o":
		// Reopen the selected session directly from the list (resume = replay).
		if len(m.history) == 0 {
			return m, nil
		}
		sel := m.history[m.historyCursor]
		m.historyMsgTxt = "reopening " + short(sel.SessionId) + "…"
		return m, m.reopenSession(sel.SessionId)
	}
	return m, nil
}

// openHistModal opens the session browser as a read-only modal over the current
// live session (task 0112). It reuses the shared history list/cursor but keeps
// the live session's event pipeline untouched. Callers should return
// m.fetchHistory to populate the list.
func (m *model) openHistModal() {
	m.histModal = true
	m.histModalTranscript = false
	m.histModalID = ""
	m.historyCursor = 0
	m.history = nil
	m.historyWaitingOnly = false
	m.historyMsgTxt = "loading…"
}

// updateHistoryModal handles the session browser when it is open as a modal over
// a live session (task 0112). It mirrors updateHistory's navigation but is
// strictly read-only: there is no `o`/enter reopen (reopening over a live session
// is a footgun). Transcripts scroll a separate viewport and support line-based
// `/` search (n/N, esc) plus {}()<>[] jump-to-event keys (task 0119), so the live
// session behind the modal is never disturbed.
func (m model) updateHistoryModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Transcript drill-in: a read-only replayed view that scrolls its own viewport.
	// It supports the same `/` search (n/N, esc) and {}()<>[] jump-to-event keys as
	// the live transcript, but line-based over the rendered content so the live
	// session behind the modal (m.evs/m.vp/search state) is never touched (0119).
	if m.histModalTranscript {
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			return m, nil
		}
		// While the search bar owns input, keystrokes edit the query and
		// incrementally re-jump the current line.
		if m.histModalSearching {
			switch key.String() {
			case "ctrl+c":
				return m.confirmQuit()
			case "esc":
				m.histModalSearching = false
				m.histModalQuery = ""
				m.histModalCurLine = -1
				m.applyHistModalContent()
				return m, nil
			case "enter":
				// Confirm: keep the query active for n/N.
				m.histModalSearching = false
				return m, nil
			case "backspace":
				if r := []rune(m.histModalQuery); len(r) > 0 {
					m.histModalQuery = string(r[:len(r)-1])
				}
				m.histRunSearch()
				return m, nil
			default:
				if t := key.Key().Text; t != "" {
					m.histModalQuery += t
					m.histRunSearch()
				}
				return m, nil
			}
		}
		switch key.String() {
		case "ctrl+c":
			return m.confirmQuit()
		case "esc":
			// A transcript search intercepts the first esc: clear it and stay in the
			// transcript. A second esc backs out to the list.
			if m.histModalQuery != "" {
				m.histModalQuery = ""
				m.histModalCurLine = -1
				m.applyHistModalContent()
				return m, nil
			}
			fallthrough
		case "q", "backspace", "left":
			// Back to the list; drop the transient transcript + nav state.
			m.histModalTranscript = false
			m.histModalID = ""
			m.resetHistModalNav()
			return m, nil
		case "/":
			// Enter transcript search (unconditional in the read-only transcript).
			m.histModalSearching = true
			m.histModalQuery = ""
			return m, nil
		case "n":
			m.histSearchStep(1, m.histModalCurLine+1)
			return m, nil
		case "N":
			m.histSearchStep(-1, m.histModalCurLine-1)
			return m, nil
		case "{":
			m.histJump(-1, "question_asked")
			return m, nil
		case "}":
			m.histJump(1, "question_asked")
			return m, nil
		case "(":
			m.histJump(-1, "review_submitted")
			return m, nil
		case ")":
			m.histJump(1, "review_submitted")
			return m, nil
		case "<":
			m.histJump(-1, "commit_made")
			return m, nil
		case ">":
			m.histJump(1, "commit_made")
			return m, nil
		case "[":
			m.histJump(-1, "session_error")
			return m, nil
		case "]":
			m.histJump(1, "session_error")
			return m, nil
		case "enter":
			// Drill into a commit's diff when the current line is a commit_made
			// event block (task 0140). Otherwise fall through to viewport handling.
			for _, el := range m.histModalEventLines {
				if el.line == m.histModalCurLine && el.typ == "commit_made" && el.idx >= 0 && el.idx < len(m.histModalEvents) {
					ev := m.histModalEvents[el.idx]
					if cmd := m.openCommitDiff(dataField(ev, "sha"), dataField(ev, "message")); cmd != nil {
						return m, cmd
					}
				}
			}
		}
		// Everything else (↑/↓, pgup/pgdn, wheel) scrolls the transcript viewport.
		// No `o`/enter reopen: browsing from a live session is read-only.
		var cmd tea.Cmd
		m.histModalVP, cmd = m.histModalVP.Update(msg)
		return m, cmd
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		// Close the modal and return to the live session behind it.
		m.histModal = false
		m.resetHistModalNav()
		return m, nil
	case "r":
		m.historyMsgTxt = "loading…"
		return m, m.fetchHistory
	case "up":
		m.historyCursor = navUp(m.historyCursor)
		return m, nil
	case "down":
		m.historyCursor = navDown(m.historyCursor, len(m.history))
		return m, nil
	case "enter":
		// Drill into a read-only replayed transcript of the selected session.
		if len(m.history) == 0 {
			return m, nil
		}
		sel := m.history[m.historyCursor]
		m.historyMsgTxt = "loading transcript…"
		return m, m.fetchTranscript(sel.SessionId)
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
	m.captureLog = nil
	m.captureInput.SetValue("")
	m.captureInput.Focus()
}

func (m model) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m.confirmQuit()
		case "ctrl+n":
			// Quick-add a backlog item (spec §18.2, task 0016).
			m.openCapture()
			return m, nil
		case "ctrl+b":
			// Open the read-only backlog browser (spec §18.5).
			m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
			m.backlogShowDone = false
			m.backlogBlockedOnly = false
			return m, m.fetchBacklog
		case "ctrl+w":
			// Jump to the blocked tasks the agent is waiting on (task 0101). Menu
			// affordances are ctrl-chords so a naked letter never triggers anything;
			// still gated on an empty prompt because the textarea binds ctrl+w to
			// delete-word-backward — mid-composition it must keep deleting.
			if m.blockedTaskCount() > 0 && strings.TrimSpace(m.prompt.Value()) == "" {
				m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
				m.backlogShowDone = false
				m.backlogBlockedOnly = true
				return m, m.fetchBacklog
			}
		case "ctrl+s":
			// Jump straight to a live session that needs the user — a pending
			// ask_user question or a paused-mid-steer session (task 0107). Same
			// gating as ctrl+w: only intercept when a session actually needs the
			// user AND the prompt is empty, so a jump never abandons a drafted prompt.
			if len(m.waitingSessions) > 0 && strings.TrimSpace(m.prompt.Value()) == "" {
				if len(m.waitingSessions) == 1 {
					// Exactly one: attach directly (ResumeSession is idempotent for a
					// live session, so this reopens/attaches rather than restarts).
					id := m.waitingSessions[0].SessionId
					m.status = "reopening " + short(id) + "…"
					return m, m.reopenSession(id)
				}
				// Several: open the session browser filtered to just the waiting
				// sessions so the user picks which to attach.
				m.state = stateHistory
				m.historyCursor = 0
				m.history = nil
				m.historyTranscript = false
				m.historyWaitingOnly = true
				m.historyMsgTxt = "loading…"
				return m, m.fetchHistory
			}
		case "ctrl+l":
			// One-key "continue last session" (task 0139): reopen the most recent
			// session (resume = replay). ctrl+l = "last" (ctrl+c is quit). Same
			// gating as ctrl+w/ctrl+s: only intercept when a session exists AND the
			// prompt is empty, so the jump never abandons a drafted prompt.
			if m.lastSession != nil && strings.TrimSpace(m.prompt.Value()) == "" {
				id := m.lastSession.SessionId
				m.status = "reopening " + short(id) + "…"
				return m, m.reopenSession(id)
			}
		case "ctrl+r":
			// Open the session browser to inspect/reopen a session (spec §18.6).
			m.state = stateHistory
			m.historyCursor = 0
			m.history = nil
			m.historyTranscript = false
			m.historyWaitingOnly = false
			m.historyMsgTxt = "loading…"
			return m, m.fetchHistory
		case "ctrl+o":
			// Open the browse selector (backlog / sessions / cost) — spec §18.6/§20.5.
			m.openBrowse()
			return m, nil
		case "?", "ctrl+h":
			// Open the keybinding help modal (task 0111). Gated on an empty prompt so
			// a bare "?" still types into a composition and ctrl+h (== the legacy BS
			// byte 0x08, bound by the textarea to delete-char-backward) keeps deleting
			// mid-edit; fall through to the textarea otherwise. ctrl+_ is unconditional.
			if strings.TrimSpace(m.prompt.Value()) == "" {
				m.openHelp()
				return m, nil
			}
		case "ctrl+_":
			m.openHelp()
			return m, nil
		case "left", "right":
			// Cycle the interaction level for the next session, but only when the
			// prompt is empty so ←/→ still move the cursor mid-edit (same gating as
			// the "?" help key). Not persisted — resets to judgement each launch.
			if strings.TrimSpace(m.prompt.Value()) == "" {
				d := 1
				if key.String() == "left" {
					d = -1
				}
				m.menuLevel = cycle(levels, m.menuLevel, d)
				return m, nil
			}
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
		case "tab":
			// Toggle "work (loop)" on the work entry: an unattended run that keeps
			// starting fresh work sessions for each ready backlog task until none
			// remain. Only the work mode supports it; tab is a no-op elsewhere.
			if len(m.entries) > 0 && isWorkEntry(m.entries[m.cursor]) {
				m.loop = !m.loop
			}
			return m, nil
		case "enter":
			if len(m.entries) == 0 {
				return m, nil
			}
			e := m.entries[m.cursor]
			// "work (loop)": drive the backlog unattended. Hand off to the loop
			// driver, which picks the next ready task, starts a session, and repeats
			// when it ends — ignoring any typed prompt so every iteration auto-picks.
			if m.loop && isWorkEntry(e) {
				m.looping, m.loopStarted, m.loopPrevFP = true, false, ""
				m.loopRun = nil // start a fresh batch-digest accumulation (task 0098)
				m.loopSessBreach, m.loopCostCap, m.loopTokenCap = false, 0, 0
				return m, tea.Batch(m.fetchBudget, m.loopNext())
			}
			// Compose the preset's opening prompt with any typed text: choosing a
			// preset AND typing details means both — the preset supplies the
			// framing and the typed text is the user's upfront context. A typed
			// prompt on a plain mode entry is sent as-is; an empty prompt falls
			// back to the preset's opening prompt alone.
			prompt := strings.TrimSpace(m.prompt.Value())
			switch {
			case prompt == "":
				prompt = e.openingPrompt
			case e.openingPrompt != "":
				prompt = e.openingPrompt + "\n\nContext from the user (supplied upfront with this request):\n" + prompt
			}
			return m, m.startSession(e.mode, prompt, m.menuLevel)
		}
	}
	var cmd tea.Cmd
	m.prompt, cmd = m.prompt.Update(msg)
	return m, cmd
}

func (m model) updateSession(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelUp || msg.Button == tea.MouseWheelDown {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			m.follow = m.vp.AtBottom()
			return m, cmd
		}
		return m, nil

	case tea.MouseClickMsg:
		// A left press is ambiguous until released: motion turns it into a
		// drag-selection (select.go), an immediate release stays a click whose
		// expand/collapse toggle is applied by the MouseReleaseMsg case below.
		if msg.Button == tea.MouseLeft {
			m.selMouseDown(msg.X, msg.Y)
		}
		return m, nil

	case tea.MouseMotionMsg:
		m.selMouseMotion(msg.X, msg.Y)
		return m, nil

	case tea.MouseReleaseMsg:
		// wasPress (a left press recorded by selMouseDown) rather than
		// msg.Button gates the click fallback: legacy X10 mouse encoding
		// reports every release as Button==MouseNone.
		wasPress := m.selDrag
		if text, dragged := m.selMouseUp(); dragged {
			// A drag-selection: copy it to the system clipboard (OSC 52) and
			// confirm with the same transient flash the row-yank uses.
			if text == "" {
				return m, nil
			}
			return m, tea.Batch(tea.SetClipboard(text), m.noteFlash("copied ✓"))
		} else if wasPress {
			// A plain click (no drag): select + expand/collapse the row under
			// the pointer, as before.
			row := msg.Y - headerHeight + m.vp.YOffset()
			if i := m.eventAt(row); i >= 0 {
				m.selected = i
				m.toggle(i)
				m.follow = false
			}
		}
		return m, nil

	case tea.KeyMsg:
		// While the transcript search bar owns input (task 0116), keystrokes edit
		// the query and incrementally re-jump the selection to the nearest match.
		// It is entered by `/` below (only when the input textarea is empty) and
		// blurs the input; esc/enter (handled at the top level / here) leave it.
		if m.searching {
			switch msg.String() {
			case "ctrl+c":
				return m.confirmQuit()
			case "enter":
				// Confirm: keep the query active for n/N, restore the input row.
				m.searching = false
				m.relayout()
				return m, m.input.Focus()
			case "backspace":
				if r := []rune(m.searchQuery); len(r) > 0 {
					m.searchQuery = string(r[:len(r)-1])
				}
				m.runSearch()
				return m, nil
			default:
				if t := msg.Key().Text; t != "" {
					m.searchQuery += t
					m.runSearch()
				}
				return m, nil
			}
		}
		// When a question with options is pending, the footer is a picker that
		// owns ↑/↓/enter until the user chooses "other…" to free-type.
		if m.picking {
			switch msg.String() {
			case "ctrl+c":
				return m.confirmQuit()
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
					// "other…": drop into the free-text textarea. The question row's
					// body was collapsed to an "answer below ↓" pointer while the
					// picker echoed the prompt; the plain textarea doesn't, so
					// restore the full question in the transcript.
					m.picking = false
					m.invalidateRender()
					m.relayout()
					m.rebuild()
					return m, m.input.Focus()
				}
				return m, m.choosePickerOption(m.pickerCursor)
			case "1", "2", "3", "4", "5", "6", "7", "8", "9":
				// Number keys select an option directly (spec §18.3); digits past
				// the option count are ignored so a stray press stays on the picker.
				if idx := int(msg.String()[0] - '1'); idx < len(m.pickerOpts) {
					return m, m.choosePickerOption(idx)
				}
				return m, nil
			case "pgup":
				// Keep the transcript scrollable so the question's context can be
				// re-read without dismissing the picker.
				m.vp.HalfPageUp()
				m.follow = m.vp.AtBottom()
				return m, nil
			case "pgdown":
				m.vp.HalfPageDown()
				m.follow = m.vp.AtBottom()
				return m, nil
			case "ctrl+n":
				// Quick-add a backlog item without answering yet (task 0016); the
				// picker re-renders once the capture overlay closes.
				m.openCapture()
				return m, nil
			case "ctrl+b":
				// Open the read-only backlog browser (spec §18.5) — often exactly
				// what's needed to answer "which task next?". m.picking is left set
				// so sessionView restores the picker on return.
				m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
				m.backlogShowDone = false
				m.backlogBlockedOnly = false
				return m, m.fetchBacklog
			case "ctrl+o":
				// Open the browse selector (backlog / sessions / cost) — parity with
				// the menu (task 0112). m.picking stays set so the picker restores.
				m.openBrowse()
				return m, nil
			case "ctrl+r":
				// Open the read-only session browser modal (task 0112).
				m.openHistModal()
				return m, m.fetchHistory
			case "?", "ctrl+h", "ctrl+_":
				// Open the keybinding help modal (task 0111). No free-text input is
				// focused in the picker, so "?"/ctrl+h open unconditionally here.
				m.openHelp()
				return m, nil
			}
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c":
			return m.confirmQuit()
		case "ctrl+n":
			// Quick-add a backlog item without pausing the session (task 0016).
			m.openCapture()
			return m, nil
		case "ctrl+b":
			// Open the read-only backlog browser (spec §18.5).
			m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
			m.backlogShowDone = false
			m.backlogBlockedOnly = false
			return m, m.fetchBacklog
		case "ctrl+o":
			// Open the browse selector (backlog / plans / sessions / cost) from a
			// session — parity with the menu (task 0112).
			m.openBrowse()
			return m, nil
		case "ctrl+r":
			// Open the read-only session browser modal over the session (task 0112).
			m.openHistModal()
			return m, m.fetchHistory
		case "?", "ctrl+h":
			// Open the keybinding help modal (task 0111). Gated on empty input so a
			// bare "?" still types and ctrl+h (== legacy BS byte 0x08, bound by the
			// textarea to delete-char-backward) keeps deleting mid-edit; fall through
			// to the textarea otherwise. ctrl+_ is the unconditional chord.
			if strings.TrimSpace(m.input.Value()) == "" {
				m.openHelp()
				return m, nil
			}
		case "ctrl+_":
			m.openHelp()
			return m, nil
		case "ctrl+i", "ctrl+x":
			// Gracefully interrupt the running agent to steer it (spec §18.7).
			// ctrl+i is the historical chord but is byte-identical to Tab (0x09)
			// and only distinguishable on terminals with the kitty keyboard
			// protocol; ctrl+x (0x18) is a distinct control byte delivered on
			// every terminal (and unused by the textarea keymap), so it is the
			// universal fallback.
			if !m.paused {
				return m, m.interrupt()
			}
			return m, nil
		case "shift+tab":
			// Toggle the unattended "work (loop)" run mid-session (spec §9). Halting
			// it is graceful: the current task runs to completion (commit/blocked/
			// in_review) and the loop simply doesn't pick up the next one. Toggling it
			// on lets an ordinary work session roll into a loop once it finishes. Only
			// meaningful for work-mode sessions.
			if m.mode == "work" {
				m.looping = !m.looping
				m.loopStarted, m.loopPrevFP = false, ""
				if m.looping {
					m.loopRun = nil // fresh batch-digest accumulation (task 0098)
					m.loopSessBreach, m.loopCostCap, m.loopTokenCap = false, 0, 0
					return m, m.fetchBudget
				}
			}
			return m, nil
		case "up":
			m.moveSelection(-1)
			return m, nil
		case "down":
			m.moveSelection(1)
			return m, nil
		case "q":
			// Return to the main menu from a finished (idle / stream-closed) session
			// (task 0127). Gated on empty input so a bare "q" still types into the
			// textarea mid-compose; falls through otherwise. Only fires on a finished,
			// non-looping session (sessionFinished): the loop driver owns the idle→stop
			// transition for its own sessions.
			if m.sessionFinished() && strings.TrimSpace(m.input.Value()) == "" {
				// Build the stop command FIRST so it captures the current m.sessionID.
				stop := m.stopSession()
				status := m.status
				// Clear transient session-footer state so the menu starts clean.
				m.pending, m.pendingSeq = "", 0
				m.pickerOpts, m.picking = nil, false
				m.clearWizard()
				m.clearSearch()
				m.selected = -1
				m.sessionID = ""
				m.status = ""
				m.state = stateMenu
				// StopSession only when the session went idle (it is still alive in the
				// daemon). A "stream closed" session is already gone — stopping it would
				// return NotFound and flash a needless error.
				if status == "idle" {
					return m, tea.Batch(stop, m.refreshMenu())
				}
				return m, m.refreshMenu()
			}
		case "y":
			// Copy the selected transcript row's text to the clipboard via OSC 52
			// (task 0141). Gated on empty input so a bare "y" still types into the
			// textarea mid-compose; falls through otherwise. commit_made → sha,
			// session_error → the error text, otherwise the row's body text.
			if m.selected >= 0 && m.selected < len(m.evs) && strings.TrimSpace(m.input.Value()) == "" {
				text := m.yankText(m.evs[m.selected])
				if text == "" {
					return m, nil
				}
				return m, tea.Batch(tea.SetClipboard(text), m.noteFlash("copied ✓"))
			}
		case "/":
			// Enter transcript search (task 0116). Gated on empty input so a bare
			// "/" still types into the textarea mid-compose; falls through otherwise.
			if strings.TrimSpace(m.input.Value()) == "" {
				m.searching = true
				m.searchQuery = ""
				m.input.Blur()
				m.relayout()
				return m, nil
			}
		case "n":
			// Cycle to the next search match. Only hijacks 'n' when a search is
			// active AND the input is empty; otherwise it types normally.
			if m.searchQuery != "" && strings.TrimSpace(m.input.Value()) == "" {
				m.searchStep(1, m.selected+1)
				return m, nil
			}
		case "N":
			if m.searchQuery != "" && strings.TrimSpace(m.input.Value()) == "" {
				m.searchStep(-1, m.selected-1)
				return m, nil
			}
		case "{":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(-1, "question_asked")
				return m, nil
			}
		case "}":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(1, "question_asked")
				return m, nil
			}
		case "(":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(-1, "review_submitted")
				return m, nil
			}
		case ")":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(1, "review_submitted")
				return m, nil
			}
		case "<":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(-1, "commit_made")
				return m, nil
			}
		case ">":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(1, "commit_made")
				return m, nil
			}
		case "[":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(-1, "session_error")
				return m, nil
			}
		case "]":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(1, "session_error")
				return m, nil
			}
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
				// Empty input: Enter expands/collapses the selected turn — unless the
				// selected row is a commit_made, in which case Enter drills into the
				// commit's diff overlay (task 0140).
				if m.selected >= 0 {
					if m.selected < len(m.evs) && m.evs[m.selected].Type == "commit_made" {
						ev := m.evs[m.selected]
						if cmd := m.openCommitDiff(dataField(ev, "sha"), dataField(ev, "message")); cmd != nil {
							return m, cmd
						}
					}
					m.toggle(m.selected)
				}
				return m, nil
			}
			m.input.SetValue("")
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
		return m.confirmQuit()
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
			// Echo the user's own message into the transcript so the
			// conversation history stays visible after Enter clears the input.
			m.captureLog = append(m.captureLog, userInputEvent(val))
			ch := make(chan *v1.Event, 64)
			m.captureEvents = ch
			m.captureInput.SetValue("")
			spin := m.spinnerCmd() // mutates m.spinning before returning m
			return m, tea.Batch(m.captureSubmit(ch, m.captureDesc, "", ""), spin)
		}
		// Stage 1: answering the agent's clarifying question.
		m.captureBusy = true
		m.captureMsg = ""
		m.captureLog = append(m.captureLog, userInputEvent(val))
		ch := make(chan *v1.Event, 64)
		m.captureEvents = ch
		ans := val
		m.captureInput.SetValue("")
		spin := m.spinnerCmd() // mutates m.spinning before returning m
		return m, tea.Batch(m.captureSubmit(ch, m.captureDesc, m.captureQuestion, ans), spin)
	default:
		var c tea.Cmd
		m.captureInput, c = m.captureInput.Update(msg)
		return m, c
	}
}

// userInputEvent builds a synthetic action-log event echoing the user's own
// submitted text, so the capture overlay transcript shows the conversation
// history (their message alongside the agent's events).
func userInputEvent(text string) *v1.Event {
	var dj string
	if b, err := json.Marshal(map[string]string{"text": text}); err == nil {
		dj = string(b)
	}
	return &v1.Event{Actor: "you", Type: "user_input", DataJson: dj}
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

// captureView renders the quick-add backlog capture overlay as a bordered modal card.
func (m model) captureView() string {
	var b strings.Builder
	w := m.w - 4
	if w < 1 {
		w = 20
	}
	switch m.captureStage {
	case 0:
		b.WriteString("Describe a new backlog item:\n\n")
		b.WriteString(framedInput(m.captureInput, 0) + "\n")
	case 1:
		// Reuse the shared interactive question UI badge the main agents use.
		b.WriteString(questionPrompt(m.captureQuestion, w) + "\n\n")
		b.WriteString("Your answer:\n\n")
		b.WriteString(framedInput(m.captureInput, 0) + "\n")
	case 2:
		b.WriteString(selStyle.Render(m.captureMsg) + "\n")
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
			// Echo the user's own messages in full (wrapped), without the
			// truncation detailLine applies, so the conversation reads cleanly.
			if ev.Actor == "you" || ev.Type == "user_input" {
				text := dataField(ev, "text")
				if strings.TrimSpace(text) == "" {
					continue
				}
				b.WriteString(wrap.String(wordwrap.String("› "+text, w), w) + "\n")
				continue
			}
			line := detailLine(ev)
			if line == "" {
				continue
			}
			composed := dimStyle.Render(ev.Actor) + " " + line
			b.WriteString(wrap.String(wordwrap.String(composed, w), w) + "\n")
		}
	}
	if m.captureBusy {
		// Animate the same activity spinner (task 0062) while the capture RPC streams.
		spin := dimStyle.Render("…")
		if len(m.spin.Spinner.Frames) > 0 {
			spin = m.spin.View()
		}
		b.WriteString("\n" + spin + " " + dimStyle.Render("capturing…"))
	} else if strings.HasPrefix(m.captureMsg, "error:") {
		b.WriteString("\n" + selStyle.Render(m.captureMsg))
	}
	b.WriteString("\n\n" + dimStyle.Render("(the running session keeps going — capture is off-stream)"))
	hint := "enter submit · esc cancel"
	if m.captureStage == 2 {
		hint = "enter/esc close"
	}
	return m.modalCard(" capture backlog item ", strings.TrimRight(b.String(), "\n"), hint)
}

// --- shared list+detail browser surface (spec §18.5/§18.6/§20.5) ---
//
// browser is the reusable modal list+detail component behind every browser
// (backlog today, sessions, and a future cost view): it owns generic list
// navigation (cursor up/down with clamping) and bordered-card rendering via
// modalCard. Each specific browser supplies the rendered row text, footer hint,
// and any extra keybindings — the owner handles enter/extra keys while this
// component handles up/down + cursor clamp + rendering. It deliberately stays
// small: factor the duplicated list+card pattern, don't over-engineer.
type browser struct {
	title  string
	rows   []browserRow
	cursor int
	hint   string
	empty  string // message shown when there are no rows
}

// browserRow is one list entry: text is selection-highlighted when the row is
// under the cursor, while suffix (dim meta/tags) is appended unstyled so a row
// can carry secondary detail without it being swallowed by the selection style.
type browserRow struct {
	text   string
	suffix string
}

// navUp / navDown / clampCursor are the single source of truth for navigable-list
// cursor arithmetic, shared by the browser component AND the specific update
// handlers (backlog/history/browse) so cursor movement is never re-implemented
// inline. navDown/clampCursor take the row count n; an empty list clamps to 0.
func navUp(cursor int) int {
	if cursor > 0 {
		return cursor - 1
	}
	return cursor
}

func navDown(cursor, n int) int {
	if cursor < n-1 {
		return cursor + 1
	}
	return cursor
}

func clampCursor(cursor, n int) int {
	if cursor >= n {
		cursor = n - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	return cursor
}

// listWindow returns the [start,end) bounds of the visible slice of n items
// shown in a scroll window of at most `size` rows, keeping `cursor` visible.
// It center-anchors the cursor and clamps to the list bounds, so the window
// scrolls one row at a time as the cursor moves and the selected item always
// stays on screen. size<=0 or n<=size means no clipping (returns 0,n).
func listWindow(cursor, n, size int) (start, end int) {
	if size <= 0 || n <= size {
		return 0, n
	}
	start = cursor - size/2
	if start < 0 {
		start = 0
	}
	end = start + size
	if end > n {
		end = n
		start = n - size
	}
	return start, end
}

func (b *browser) up() { b.cursor = navUp(b.cursor) }

func (b *browser) down() { b.cursor = navDown(b.cursor, len(b.rows)) }

// clamp keeps the cursor within [0, len-1] after the underlying row set changes
// (e.g. a show/hide-done toggle shrinks the list out from under it).
func (b *browser) clamp() { b.cursor = clampCursor(b.cursor, len(b.rows)) }

// browserCard renders a browser's navigable list as a bordered modal card.
func (m model) browserCard(b browser) string {
	var sb strings.Builder
	if len(b.rows) == 0 {
		empty := b.empty
		if empty == "" {
			empty = "(empty)"
		}
		sb.WriteString(dimStyle.Render(empty) + "\n")
	}
	// Window the rows so the card never overruns the terminal vertically.
	// modalCard's chrome is 6 non-content rows (title + blank + blank + footer
	// + top/bottom border), so the content budget is m.h-6. Before the first
	// WindowSizeMsg (m.h == 0) keep the legacy behaviour of rendering all rows.
	budget := len(b.rows)
	if m.h > 0 {
		budget = m.h - 6
		if budget < 1 {
			budget = 1
		}
	}
	start, end := listWindow(b.cursor, len(b.rows), budget)
	hint := b.hint
	if start > 0 || end < len(b.rows) {
		hint = fmt.Sprintf("%s · %d–%d/%d", b.hint, start+1, end, len(b.rows))
	}
	for i, r := range b.rows[start:end] {
		cursor := "  "
		text := r.text
		if start+i == b.cursor {
			cursor = selStyle.Render("▸ ")
			text = selStyle.Render(text)
		}
		sb.WriteString(cursor + text + r.suffix + "\n")
	}
	return m.modalCard(b.title, strings.TrimRight(sb.String(), "\n"), hint)
}

// --- browse selector (spec §18.6 / §20.5) ---
//
// browseTargets are the routes the browse selector offers. It is the single
// extension point for the shared browser surface: each row maps to a case in
// updateBrowse — no other plumbing is needed (spec §18.6/§20.5).
var browseTargets = []struct{ label, desc string }{
	{"backlog", "tasks · readiness · drill-in detail"},
	{"plans", "saved runbooks · view markdown"},
	{"sessions", "previous + live · transcript · reopen"},
	{"cost", "token/cost breakdown by task × model × day"},
	{"workstreams", "parallel worktrees · status · merge/discard"},
	{"digest", "last work-loop run — done · blocked · cost"},
}

// openBrowse enters the browse selector modal.
func (m *model) openBrowse() {
	m.browse = true
	m.browseCursor = 0
}

// updateBrowse handles the browse selector: navigate the routes and Enter opens
// the chosen browser (backlog / sessions). Esc/q dismisses it.
func (m model) updateBrowse(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.browse = false
		return m, nil
	case "up":
		m.browseCursor = navUp(m.browseCursor)
		return m, nil
	case "down":
		m.browseCursor = navDown(m.browseCursor, len(browseTargets))
		return m, nil
	case "enter":
		m.browse = false
		switch browseTargets[m.browseCursor].label {
		case "backlog":
			m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
			m.backlogShowDone = false
			m.backlogBlockedOnly = false
			return m, m.fetchBacklog
		case "plans":
			m.plans, m.plansCursor, m.planDetail = true, 0, nil
			return m, m.fetchPlans
		case "sessions":
			// From a live session, open the read-only modal variant (task 0112) so
			// browsing never disturbs (or reopens over) the session behind it. From
			// the menu, use the full-state session browser as before.
			if m.state == stateSession {
				m.openHistModal()
				return m, m.fetchHistory
			}
			m.state = stateHistory
			m.historyCursor = 0
			m.history = nil
			m.historyTranscript = false
			m.historyMsgTxt = "loading…"
			return m, m.fetchHistory
		case "cost":
			// The cost view (spec §20.5, task 0039) opens grouped by task.
			m.cost, m.costCursor = true, 0
			m.costGroupBy = []string{"task"}
			m.costRows, m.costTotal = nil, nil
			m.costMsg = "loading…"
			return m, m.fetchUsage
		case "workstreams":
			m.openWorkstreams()
			return m, tea.Batch(m.fetchWorkstreams, m.wsRefreshTick())
		case "digest":
			// Reopen the last work-loop batch digest (task 0098). When no loop has
			// finished yet the digest surface shows its empty-state message.
			m.digest, m.digestCursor = true, 0
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

// browseView renders the browse selector as a bordered modal card via the shared
// list component.
func (m model) browseView() string {
	b := browser{
		title:  " ycc — browse ",
		cursor: m.browseCursor,
		hint:   "↑/↓ choose · enter open · esc back",
	}
	for _, t := range browseTargets {
		b.rows = append(b.rows, browserRow{text: fmt.Sprintf("%-10s", t.label), suffix: dimStyle.Render(t.desc)})
	}
	return m.browserCard(b)
}

// --- workstreams panel (task 0085, design §8) ---

// openWorkstreams enters the modal Workstreams panel, resetting its transient
// state and bumping the refresh-tick generation so an older tick can't multiply
// the in-flight timers.
func (m *model) openWorkstreams() {
	m.ws = true
	m.wsCursor = 0
	m.wsMerge, m.wsMergeID = nil, ""
	m.wsDiscardID = ""
	m.wsTick++
	if m.wsList == nil {
		m.wsNotice = "loading…"
	}
}

// wsRowStatus resolves the status cell for a workstream row. Precedence (design
// §8): a terminal registry status (merged/discarded/stale) wins; then a
// locally-known conflict (loud, distinct); then awaiting-review; else the live
// session status (running/idle/…). Returns the label and whether it is a
// conflict (so the view can render it in the error style).
func (m model) wsRowStatus(w *v1.WorkstreamInfo) (string, bool) {
	switch w.GetStatus() {
	case "merged", "discarded", "stale":
		return w.GetStatus(), false
	}
	switch m.wsLocal[w.GetId()] {
	case "conflict":
		return "conflict", true
	case "awaiting-review":
		return "awaiting-review", false
	}
	if s := w.GetSessionStatus(); s != "" {
		return s, false
	}
	return w.GetStatus(), false
}

// updateWorkstreams handles the modal Workstreams panel: list navigation, drill
// into the session (enter), merge overlay (m), discard confirm (d), refresh (r).
// The merge overlay and the discard confirm own input while active.
func (m model) updateWorkstreams(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// Merge/accept overlay owns input while open (task 0085, design §6).
	if m.wsMerge != nil {
		switch key.String() {
		case "ctrl+c":
			return m.confirmQuit()
		case "esc", "q", "backspace", "left":
			m.wsMerge, m.wsMergeID = nil, ""
			return m, nil
		case "enter", "y":
			if m.wsMerge.GetClean() {
				id := m.wsMergeID
				m.wsNotice = "merging " + short(id) + "…"
				return m, m.mergeWorkstreamCmd(id)
			}
			// A conflicted preview cannot be accepted; keep it surfaced.
			m.wsNotice = "cannot merge: conflicts must be resolved first"
			return m, nil
		}
		var cmd tea.Cmd
		m.wsMergeVP, cmd = m.wsMergeVP.Update(msg)
		return m, cmd
	}

	// Two-step discard confirm (footer prompt).
	if m.wsDiscardID != "" {
		id := m.wsDiscardID
		m.wsDiscardID = ""
		switch key.String() {
		case "y":
			m.wsNotice = "discarding " + short(id) + "…"
			return m, m.discardWorkstreamCmd(id)
		default:
			m.wsNotice = "discard cancelled"
			return m, nil
		}
	}

	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.ws = false
		m.wsTick++ // invalidate any in-flight refresh tick
		m.wsNotice = ""
		return m, nil
	case "up":
		m.wsCursor = navUp(m.wsCursor)
		return m, nil
	case "down":
		m.wsCursor = navDown(m.wsCursor, len(m.wsList))
		return m, nil
	case "r":
		m.wsNotice = "refreshing…"
		return m, m.fetchWorkstreams
	case "enter":
		// Drill into the workstream's session (design §8): ResumeSession is
		// idempotent for a live session, so this attaches rather than restarts.
		if w := m.wsCurrent(); w != nil && w.GetSessionId() != "" {
			m.status = "reopening " + short(w.GetSessionId()) + "…"
			return m, m.reopenSession(w.GetSessionId())
		}
		return m, nil
	case "m":
		if w := m.wsCurrent(); w != nil {
			if w.GetStatus() != "active" {
				m.wsNotice = "cannot merge: workstream is " + w.GetStatus()
				return m, nil
			}
			m.wsNotice = "previewing merge…"
			return m, m.previewMergeCmd(w.GetId())
		}
		return m, nil
	case "d":
		if w := m.wsCurrent(); w != nil {
			m.wsDiscardID = w.GetId()
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

// wsCurrent returns the workstream under the cursor, or nil when the list is empty.
func (m model) wsCurrent() *v1.WorkstreamInfo {
	if m.wsCursor >= 0 && m.wsCursor < len(m.wsList) {
		return m.wsList[m.wsCursor]
	}
	return nil
}

// shortBranch trims the ycc/ws/ namespace prefix for a compact row cell.
func shortBranch(b string) string {
	return strings.TrimPrefix(b, "ycc/ws/")
}

// workstreamsView renders the Workstreams panel: the merge overlay when open,
// otherwise the list of workstreams with a live status cell (conflicts loud).
func (m model) workstreamsView() string {
	if m.wsMerge != nil {
		return m.wsMergeView()
	}
	b := browser{
		title:  " ycc — workstreams ",
		cursor: m.wsCursor,
		hint:   "↑/↓ select · enter open session · m merge · d discard · r refresh · esc close",
		empty:  "(no workstreams — spawn from the backlog with space + P)",
	}
	if m.wsDiscardID != "" {
		b.hint = "discard " + short(m.wsDiscardID) + "? y confirm · any other key cancel"
	} else if m.wsNotice != "" {
		b.hint = m.wsNotice
	}
	for _, w := range m.wsList {
		status, conflict := m.wsRowStatus(w)
		statusCell := fmt.Sprintf("%-15s", status)
		if conflict {
			statusCell = errStyle.Render(fmt.Sprintf("%-15s", "⚠ conflict"))
		}
		task := w.GetTaskId()
		if task == "" {
			task = "—"
		}
		row := fmt.Sprintf("%-10s %-6s %-18s %2d↑ ", short(w.GetId()), task, shortBranch(w.GetBranch()), w.GetCommitCount())
		b.rows = append(b.rows, browserRow{text: row, suffix: statusCell})
	}
	return m.browserCard(b)
}

// refreshWsMergeVP (re)sizes the merge overlay viewport and loads its content:
// a clean integrated diff, or the conflicted paths rendered distinctly.
func (m *model) refreshWsMergeVP() {
	if m.wsMerge == nil || !m.ready {
		return
	}
	h := m.h - 2
	if h < 3 {
		h = 3
	}
	if m.wsMergeVP.Height() == 0 && m.wsMergeVP.Width() == 0 {
		m.wsMergeVP = viewport.New(viewport.WithWidth(m.w), viewport.WithHeight(h))
	} else {
		m.wsMergeVP.SetWidth(m.w)
		m.wsMergeVP.SetHeight(h)
	}
	m.wsMergeVP.SetContent(m.wsMergeContent())
}

// wsMergeContent builds the scrollable body of the merge overlay (task 0085).
func (m model) wsMergeContent() string {
	var b strings.Builder
	if m.wsMerge.GetClean() {
		b.WriteString(successStyle.Render("✓ clean — no conflicts") + "\n\n")
		diff := m.wsMerge.GetDiff()
		if strings.TrimSpace(diff) == "" {
			b.WriteString(dimStyle.Render("(no changes to integrate)"))
		} else {
			b.WriteString(colorizeDiff(diff))
		}
	} else {
		b.WriteString(errStyle.Render("⚠ conflict — merge blocked until resolved") + "\n\n")
		b.WriteString(dimStyle.Render("conflicted paths:") + "\n")
		for _, p := range m.wsMerge.GetConflicts() {
			b.WriteString("  " + errStyle.Render(p) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// wsMergeView renders the merge/accept overlay as a full-screen scrollable view.
func (m model) wsMergeView() string {
	top := m.titleBar(" merge " + short(m.wsMergeID) + " ")
	body := ""
	if m.ready {
		body = m.wsMergeVP.View()
	}
	var hint string
	if m.wsMerge.GetClean() {
		hint = " enter/y merge · ↑↓ scroll · esc/← cancel · ctrl+c quit "
	} else {
		hint = " conflict — resolve in the worktree first · esc/← back · ctrl+c quit "
	}
	if m.wsNotice != "" {
		hint = " " + m.wsNotice + " "
	}
	return top + "\n" + body + "\n" + m.footerBar(hint)
}

// --- commit-diff drill-in overlay (task 0140) ---

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

// --- cost view (spec §20.5, task 0039) ---

// costGroupOrder is the cycle of group-by dimensions the cost view rotates through
// with the "g" key (mirrors the CLI's -by options in cmd/ycc).
var costGroupOrder = []string{"task", "model", "session", "day", "agent"}

// updateCost handles the modal cost view: list navigation plus "g" to cycle the
// group-by dimension (which re-fetches). Esc/q dismisses it.
func (m model) updateCost(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.cost = false
		return m, nil
	case "up":
		m.costCursor = navUp(m.costCursor)
		return m, nil
	case "down":
		m.costCursor = navDown(m.costCursor, len(m.costRows))
		return m, nil
	case "g":
		cur := "task"
		if len(m.costGroupBy) > 0 {
			cur = m.costGroupBy[0]
		}
		next := costGroupOrder[0]
		for i, d := range costGroupOrder {
			if d == cur {
				next = costGroupOrder[(i+1)%len(costGroupOrder)]
				break
			}
		}
		m.costGroupBy = []string{next}
		m.costCursor = 0
		m.costMsg = "loading…"
		return m, m.fetchUsage
	}
	return m, nil
}

// costCellTUI renders the cost column for a usage row, mirroring cmd/ycc's
// costCell: unpriced rows show "—", partial pricing appends "*".
func costCellTUI(r *v1.UsageRow) string {
	switch r.PriceStatus {
	case "unpriced":
		return "—"
	case "partial":
		return fmt.Sprintf("$%.4f*", r.Cost)
	default:
		return fmt.Sprintf("$%.4f", r.Cost)
	}
}

// costTitleTUI capitalises a group-by dimension for the table header (mirrors
// cmd/ycc's costTitle).
func costTitleTUI(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// commasTUI formats an int64 with thousands separators (mirrors cmd/ycc's commas).
func commasTUI(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// costGroupValue resolves the group label for one row + dimension, mirroring the
// CLI's placeholder treatment for unattributed/unknown values.
func costGroupValue(r *v1.UsageRow, dim string) string {
	switch dim {
	case "task":
		if r.Task == "" {
			return "(unattributed)"
		}
		return r.Task
	case "model":
		if r.Model == "" {
			return "(unknown)"
		}
		return r.Model
	case "session":
		return r.Session
	case "agent":
		if r.Agent == "" {
			return "(unknown)"
		}
		return r.Agent
	case "day":
		return r.Day
	}
	return ""
}

// costView renders the token/cost breakdown as a bordered modal card (spec §20.5,
// task 0039). Columns mirror the `ycc cost` CLI table; the row under the cursor is
// selection-highlighted and a dim TOTAL line closes the table.
func (m model) costView() string {
	groupBy := m.costGroupBy
	if len(groupBy) == 0 {
		groupBy = []string{"task"}
	}

	title := " ycc — cost "
	if m.costWorkspace != "" {
		title = " ycc — cost · " + m.costWorkspace + " "
	}
	hint := fmt.Sprintf("g group-by:%s · ↑/↓ select · esc close", groupBy[0])

	if len(m.costRows) == 0 {
		msg := m.costMsg
		if msg == "" {
			msg = "(no usage recorded)"
		}
		return m.modalCard(title, dimStyle.Render(msg), hint)
	}

	// Build aligned columns with a tabwriter, then apply selection styling per
	// rendered line (the writer pads on raw widths, so style after flushing).
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
	header := make([]string, 0, len(groupBy)+5)
	for _, d := range groupBy {
		header = append(header, costTitleTUI(d))
	}
	header = append(header, "Input", "Output", "Cache", "Total", "Cost")
	fmt.Fprintln(tw, "  "+strings.Join(header, "\t"))

	partial := false
	for _, r := range m.costRows {
		cells := make([]string, 0, len(groupBy)+5)
		for _, d := range groupBy {
			cells = append(cells, costGroupValue(r, d))
		}
		cache := r.CacheRead + r.CacheWrite
		cells = append(cells, commasTUI(r.Input), commasTUI(r.Output), commasTUI(cache), commasTUI(r.Total), costCellTUI(r))
		fmt.Fprintln(tw, "  "+strings.Join(cells, "\t"))
		if r.PriceStatus == "partial" {
			partial = true
		}
	}
	if total := m.costTotal; total != nil {
		cells := make([]string, len(groupBy))
		cells[0] = "TOTAL"
		cache := total.CacheRead + total.CacheWrite
		cells = append(cells, commasTUI(total.Input), commasTUI(total.Output), commasTUI(cache), commasTUI(total.Total), costCellTUI(total))
		fmt.Fprintln(tw, "  "+strings.Join(cells, "\t"))
		if total.PriceStatus == "partial" {
			partial = true
		}
	}
	tw.Flush()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	headerLine, dataLines := lines[0], lines[1:]
	totalLine := ""
	if m.costTotal != nil && len(dataLines) > 0 {
		totalLine, dataLines = dataLines[len(dataLines)-1], dataLines[:len(dataLines)-1]
	}
	// Window the data rows around the cursor so the card never overruns the
	// terminal vertically (mirrors browserCard). Fixed chrome: modalCard's 6
	// non-content rows plus the pinned header, TOTAL, and footnote lines.
	budget := len(dataLines)
	if m.h > 0 {
		chrome := 6 + 1 // modalCard chrome + header row
		if totalLine != "" {
			chrome++
		}
		if partial {
			chrome++
		}
		budget = m.h - chrome
		if budget < 1 {
			budget = 1
		}
	}
	start, end := listWindow(m.costCursor, len(dataLines), budget)
	if start > 0 || end < len(dataLines) {
		hint = fmt.Sprintf("%s · %d–%d/%d", hint, start+1, end, len(dataLines))
	}
	var sb strings.Builder
	sb.WriteString(dimStyle.Render(headerLine) + "\n")
	for i, line := range dataLines[start:end] {
		if start+i == m.costCursor {
			// Highlight the cursor row.
			sb.WriteString(selStyle.Render("▸"+line[1:]) + "\n")
			continue
		}
		sb.WriteString(line + "\n")
	}
	if totalLine != "" {
		sb.WriteString(dimStyle.Render(totalLine) + "\n")
	}
	if partial {
		sb.WriteString(dimStyle.Render("  * partial pricing (some models unpriced)") + "\n")
	}
	return m.modalCard(title, strings.TrimRight(sb.String(), "\n"), hint)
}

// --- work-loop batch digest surface (task 0098, §9/§18.6/§20) ---

// shortSHA truncates a commit sha to its conventional 7-char prefix for display.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// digestRows builds the digest's list rows (shared browser surface) alongside a
// parallel nav slice giving each row's task id ("" for informational rows), so
// updateDigest and digestView agree on which row maps to which task.
func (m model) digestRows() (rows []browserRow, nav []string) {
	d := m.loopDigest
	if d == nil {
		return nil, nil
	}
	add := func(text, suffix, id string) {
		rows = append(rows, browserRow{text: text, suffix: suffix})
		nav = append(nav, id)
	}
	add(d.outcome, "", "")
	add(dimStyle.Render(fmt.Sprintf("%d session(s) · %s", len(d.sessions), fmtElapsed(d.dur))), "", "")
	add(dimStyle.Render(fmt.Sprintf("total: %s tok · %s", commasTUI(d.totalTokens),
		costCellTUI(&v1.UsageRow{Cost: d.totalCost, PriceStatus: d.costStatus}))), "", "")

	section := func(title, marker string, tasks []digestTask, suf func(digestTask) string) {
		if len(tasks) == 0 {
			return
		}
		add(dimStyle.Render(fmt.Sprintf("%s (%d)", title, len(tasks))), "", "")
		for _, t := range tasks {
			add(fmt.Sprintf("%s %s  %s", marker, t.id, oneLine(t.title, 48)), suf(t), t.id)
		}
	}
	tokCost := func(t digestTask) string {
		return fmt.Sprintf("%s tok · %s", commasTUI(t.tokens),
			costCellTUI(&v1.UsageRow{Cost: t.cost, PriceStatus: t.priceStatus}))
	}
	section("completed", "✔", d.completed, func(t digestTask) string {
		parts := []string{}
		if t.sha != "" {
			parts = append(parts, shortSHA(t.sha))
		}
		if t.verdictTally != "" {
			parts = append(parts, t.verdictTally)
		}
		parts = append(parts, tokCost(t))
		return "  " + dimStyle.Render(strings.Join(parts, " · "))
	})
	section("blocked", "⛔", d.blocked, func(t digestTask) string {
		reason := t.reason
		if reason == "" {
			reason = "(no reason recorded — open to view)"
		}
		return "  " + dimStyle.Render(oneLine(reason, 60))
	})
	section("in_review / unfinished", "◌", d.inReview, func(t digestTask) string {
		return "  " + dimStyle.Render(tokCost(t))
	})
	section("created during run", "+", d.created, func(t digestTask) string {
		return "  " + dimStyle.Render(t.status)
	})
	return rows, nav
}

// digestView renders the batch digest as a bordered modal card via the shared
// list component (task 0098).
func (m model) digestView() string {
	rows, _ := m.digestRows()
	b := browser{
		title:  " ycc — loop digest ",
		rows:   rows,
		cursor: m.digestCursor,
		hint:   "↑/↓ · enter open task · esc close",
		empty:  "no completed loop run yet",
	}
	return m.browserCard(b)
}

// updateDigest handles the batch digest modal: list navigation, and Enter on a
// task row jumps into the backlog browser's detail for that task — the fast path
// to answer a blocked task + re-queue, or just inspect what happened (task 0098).
func (m model) updateDigest(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	_, nav := m.digestRows()
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.digest = false
		return m, nil
	case "up":
		m.digestCursor = navUp(m.digestCursor)
		return m, nil
	case "down":
		m.digestCursor = navDown(m.digestCursor, len(nav))
		return m, nil
	case "enter":
		if m.digestCursor >= 0 && m.digestCursor < len(nav) && nav[m.digestCursor] != "" {
			id := nav[m.digestCursor]
			m.digest = false
			m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
			m.backlogShowDone = true
			m.backlogBlockedOnly = false
			return m, tea.Batch(m.fetchBacklog, m.fetchTask(id))
		}
		return m, nil
	}
	return m, nil
}

func (m model) updateBacklog(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	// Status-choice mode (spec §18.5 grooming, task 0099): the next digit picks a
	// status; esc/any other key cancels. Applies to the cursor row (list) or the
	// open detail task.
	if m.backlogStatusPrompt {
		m.backlogStatusPrompt = false
		if st, ok := statusForDigit(key.String()); ok {
			if id := m.backlogTargetID(); id != "" {
				return m, m.updateTaskCmd(id, &st, nil)
			}
		}
		return m, nil
	}
	if m.backlogDetail != nil {
		// Detail view: grooming keys, editor escape hatch, then scroll.
		switch key.String() {
		case "ctrl+c", "q":
			return m.confirmQuit()
		case "esc", "backspace", "left":
			m.backlogDetail = nil
			m.backlogNotice = ""
			return m, nil
		case "+", "=":
			return m, m.reprioritizeCmd(m.backlogDetail.Id, int(m.backlogDetail.Priority), -1)
		case "-", "_":
			return m, m.reprioritizeCmd(m.backlogDetail.Id, int(m.backlogDetail.Priority), +1)
		case "s":
			m.backlogStatusPrompt = true
			return m, nil
		case "e":
			return m.openTaskInEditor(m.backlogDetail.Id, m.backlogDetail.Path)
		}
		var cmd tea.Cmd
		m.backlogVP, cmd = m.backlogVP.Update(msg)
		return m, cmd
	}
	// List view.
	vis := m.visibleBacklogTasks()
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.backlog = false
		m.backlogBlockedOnly = false
		m.backlogNotice = ""
		m.backlogSelected = nil
		return m, nil
	case "up":
		m.backlogCursor = navUp(m.backlogCursor)
		return m, nil
	case "down":
		m.backlogCursor = navDown(m.backlogCursor, len(vis))
		return m, nil
	case "d":
		m.backlogShowDone = !m.backlogShowDone
		m.backlogCursor = clampCursor(m.backlogCursor, len(m.visibleBacklogTasks()))
		return m, nil
	case "+", "=":
		if len(vis) > 0 {
			t := vis[m.backlogCursor]
			return m, m.reprioritizeCmd(t.Id, int(t.Priority), -1)
		}
		return m, nil
	case "-", "_":
		if len(vis) > 0 {
			t := vis[m.backlogCursor]
			return m, m.reprioritizeCmd(t.Id, int(t.Priority), +1)
		}
		return m, nil
	case "s":
		if len(vis) > 0 {
			m.backlogStatusPrompt = true
		}
		return m, nil
	case "enter":
		if len(vis) > 0 {
			return m, m.fetchTask(vis[m.backlogCursor].Id)
		}
		return m, nil
	case "space", " ":
		// Toggle multi-select for a spawnable (todo) task (task 0085). Selection is
		// a set of task ids, cleared when the browser closes.
		if len(vis) > 0 {
			t := vis[m.backlogCursor]
			if t.Status == "todo" {
				if m.backlogSelected == nil {
					m.backlogSelected = map[string]bool{}
				}
				if m.backlogSelected[t.Id] {
					delete(m.backlogSelected, t.Id)
				} else {
					m.backlogSelected[t.Id] = true
				}
			}
		}
		return m, nil
	case "P":
		// Run the selected tasks in parallel workstreams (task 0085, design §8).
		// Workstreams need a registered project (daemon-registry mode); a one-shot
		// (empty project) can't spawn them.
		sel := m.selectedBacklogTasks()
		if len(sel) == 0 {
			m.backlogNotice = "select tasks with space, then P to run in parallel"
			return m, nil
		}
		if strings.TrimSpace(m.project) == "" {
			m.backlogNotice = "workstreams need a registered project (open ycc on a project)"
			return m, nil
		}
		m.backlogNotice = fmt.Sprintf("spawning %d workstream(s)…", len(sel))
		return m, m.spawnWorkstreamsCmd(sel)
	}
	return m, nil
}

// selectedBacklogTasks returns the currently multi-selected backlog tasks (task
// 0085), restricted to those still visible and todo.
func (m model) selectedBacklogTasks() []*v1.BacklogTaskSummary {
	if len(m.backlogSelected) == 0 {
		return nil
	}
	var out []*v1.BacklogTaskSummary
	for _, t := range m.backlogTasks {
		if m.backlogSelected[t.Id] {
			out = append(out, t)
		}
	}
	return out
}

// backlogTargetID returns the task the grooming keys act on: the open detail task,
// else the cursor row in the list (task 0099).
func (m model) backlogTargetID() string {
	if m.backlogDetail != nil {
		return m.backlogDetail.Id
	}
	vis := m.visibleBacklogTasks()
	if len(vis) > 0 && m.backlogCursor < len(vis) {
		return vis[m.backlogCursor].Id
	}
	return ""
}

// statusForDigit maps a status-prompt digit to a docs status (task 0099).
func statusForDigit(k string) (string, bool) {
	switch k {
	case "1":
		return "todo", true
	case "2":
		return "in_progress", true
	case "3":
		return "in_review", true
	case "4":
		return "done", true
	case "5":
		return "blocked", true
	case "6":
		return "proposed", true
	}
	return "", false
}

// reprioritizeCmd nudges a task's priority toward p1 (dir<0) or p5 (dir>0), clamped
// to 1..5; it is a no-op at the clamp edge to avoid a needless RPC (task 0099).
func (m model) reprioritizeCmd(id string, cur, dir int) tea.Cmd {
	next := cur + dir
	if next < 1 {
		next = 1
	}
	if next > 5 {
		next = 5
	}
	if next == cur {
		return nil
	}
	p := int32(next)
	return m.updateTaskCmd(id, nil, &p)
}

// openTaskInEditor opens the task's markdown file in $EDITOR when the workspace is
// local to the client (the task file exists on this filesystem — the common
// in-process/loopback case). Remote clients can't reach the workspace editor, so
// the affordance degrades to a footer notice (task 0099).
func (m model) openTaskInEditor(id, path string) (tea.Model, tea.Cmd) {
	if !taskFileLocal(path) {
		m.backlogNotice = "open-in-editor unavailable: workspace not local"
		return m, nil
	}
	m.backlogNotice = ""
	return m, m.openEditorCmd(id, path)
}

// taskFileLocal reports whether the task's file is reachable on the client's
// filesystem (gates the open-in-editor affordance, task 0099).
func taskFileLocal(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// visibleBacklogTasks returns the backlog rows to display: all tasks when
// backlogShowDone is set, otherwise only non-done (actionable) tasks. This keeps
// the overlay focused on open work by default while letting done tasks be revealed.
func (m model) visibleBacklogTasks() []*v1.BacklogTaskSummary {
	if m.backlogBlockedOnly {
		out := make([]*v1.BacklogTaskSummary, 0, len(m.backlogTasks))
		for _, t := range m.backlogTasks {
			if t.Status == "blocked" {
				out = append(out, t)
			}
		}
		return out
	}
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

// backlogView renders the modal backlog browser (list or detail) as a bordered card.
func (m model) backlogView() string {
	if m.backlogDetail != nil {
		return m.taskDetailView(m.backlogDetail)
	}
	b := browser{
		title:  " ycc — backlog ",
		cursor: m.backlogCursor,
		hint:   "↑/↓ select · enter inspect · +/- priority · s status · d show/hide done · esc close",
		empty:  "(no backlog tasks)",
	}
	if m.backlogBlockedOnly {
		b.title = " ycc — blocked tasks "
		b.hint = "↑/↓ select · enter inspect (see why) · +/- priority · s status · esc close"
		b.empty = "(no blocked tasks)"
	}
	if m.backlogStatusPrompt {
		b.hint = "set status: 1 todo · 2 in_progress · 3 in_review · 4 done · 5 blocked · 6 proposed · esc cancel"
	} else if m.backlogNotice != "" {
		b.hint = m.backlogNotice
	} else if n := len(m.backlogSelected); n > 0 {
		b.hint = fmt.Sprintf("%d selected · space toggle · P run in parallel (%d workstreams) · esc close", n, n)
	} else {
		b.hint += " · space select · P run in parallel"
	}
	for _, t := range m.visibleBacklogTasks() {
		// Multi-select checkbox for spawnable (todo) tasks (task 0085).
		mark := "   "
		if t.Status == "todo" {
			if m.backlogSelected[t.Id] {
				mark = "[x]"
			} else {
				mark = "[ ]"
			}
		}
		row := fmt.Sprintf("%s %-5s %-12s p%d  %s", mark, t.Id, t.Status, t.Priority, t.Title)
		var tag string
		if t.Status != "done" {
			if t.Ready {
				tag = " " + dimStyle.Render("[ready]")
			} else {
				tag = " " + dimStyle.Render("[blocked by "+strings.Join(t.BlockedBy, ", ")+"]")
			}
		}
		b.rows = append(b.rows, browserRow{text: row, suffix: tag})
	}
	return m.browserCard(b)
}

// taskDetailContent builds the read-only body shown for a single task: a dim
// meta line followed by the glamour-rendered task body. It is the scrollable
// content placed into the detail viewport (m.backlogVP).
func (m model) taskDetailContent(t *v1.TaskDetail) string {
	var b strings.Builder
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
	b.WriteString(dimStyle.Render(meta) + "\n\n")
	body := t.Body
	if m.glam != nil {
		if out, err := m.glam.Render(body); err == nil {
			body = strings.Trim(out, "\n")
		}
	}
	b.WriteString(body)
	return strings.TrimRight(b.String(), "\n")
}

// refreshBacklogDetailVP (re)sizes the detail viewport to the current terminal
// dimensions and loads the current task's content. It is a no-op when no detail
// task is open or the terminal size is not yet known.
func (m *model) refreshBacklogDetailVP() {
	if m.backlogDetail == nil || !m.ready {
		return
	}
	h := m.h - 2 // one row for the title bar, one for the footer
	if h < 3 {
		h = 3
	}
	if m.backlogVP.Height() == 0 && m.backlogVP.Width() == 0 {
		m.backlogVP = viewport.New(viewport.WithWidth(m.w), viewport.WithHeight(h))
	} else {
		m.backlogVP.SetWidth(m.w)
		m.backlogVP.SetHeight(h)
	}
	m.backlogVP.SetContent(m.taskDetailContent(m.backlogDetail))
}

// refreshHistModalVP (re)sizes the modal session-browser transcript viewport and
// loads a stateless render of the replayed events into it (task 0112). It never
// touches the live session's m.vp/m.evs. It also captures the rendered lines and
// per-event start-line metadata used by the modal's line-based search/jump
// navigation (task 0119).
func (m *model) refreshHistModalVP(events []*v1.Event) {
	if !m.ready {
		return
	}
	h := m.h - 2 // one row for the title bar, one for the footer
	if h < 3 {
		h = 3
	}
	if m.histModalVP.Height() == 0 && m.histModalVP.Width() == 0 {
		m.histModalVP = viewport.New(viewport.WithWidth(m.w), viewport.WithHeight(h))
	} else {
		m.histModalVP.SetWidth(m.w)
		m.histModalVP.SetHeight(h)
	}
	m.histModalEvents = events
	content, lines, eventLines := m.renderTranscript(events)
	m.histModalLines = lines
	m.histModalEventLines = eventLines
	m.histModalVP.SetContent(content)
	m.histModalVP.GotoTop()
}

// histEventLine records, per VISIBLE event block in a modal transcript render,
// the content line its block starts on plus its event Type — the metadata that
// lets the {}()<>[] jump keys land on the right event without the live event
// pipeline (task 0119).
type histEventLine struct {
	line int    // start content line of the event block
	typ  string // event Type
	idx  int    // index into the rendered events slice (for the commit-diff drill-in)
}

// renderTranscriptContent renders a replayed event log to a string using the same
// pipeline as rebuild()/the live session view, WITHOUT mutating the live model
// (task 0112). It works on a scratch copy of the model whose event state and
// caches are freshly allocated, so renderBlock's cache mutations never leak into
// the live session's shared maps.
func (m model) renderTranscriptContent(events []*v1.Event) string {
	content, _, _ := m.renderTranscript(events)
	return content
}

// renderTranscript renders a replayed event log statelessly (like
// renderTranscriptContent) and additionally returns the rendered content lines
// and per-event start-line metadata used by the modal transcript's line-based
// search + jump navigation (task 0119). It never mutates the live model.
func (m model) renderTranscript(events []*v1.Event) (content string, lines []string, eventLines []histEventLine) {
	scratch := m
	scratch.evs = events
	scratch.expanded = map[int]bool{}
	scratch.bodyCache = map[int]string{}
	scratch.blockCache = map[int]string{}
	scratch.hiddenCache = map[int]bool{}
	scratch.deliveredSeqs = deliveredSeqSet(events)
	scratch.eventStart = nil
	scratch.selected = -1
	scratch.follow = false
	// The live session's pending-question UI state (footer picker / wizard) must
	// not leak into a replayed transcript: there is no picker below it, so its
	// question rows always render in full, never as an "answer below ↓" pointer.
	scratch.picking = false
	scratch.pendingSeq = 0
	scratch.wizActive = false
	var b strings.Builder
	line := 0
	for i, ev := range events {
		if scratch.hiddenRow(i) {
			continue
		}
		block := scratch.renderBlock(i, ev)
		eventLines = append(eventLines, histEventLine{line: line, typ: ev.Type, idx: i})
		b.WriteString(block)
		b.WriteByte('\n')
		line += strings.Count(block, "\n") + 1
	}
	content = b.String()
	if content != "" {
		lines = strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	}
	return content, lines, eventLines
}

// taskDetailView renders a single task's full, read-only detail (spec §18.5) as a
// full-screen scrollable viewport (mirroring the transcript drill-in).
func (m model) taskDetailView(t *v1.TaskDetail) string {
	top := m.titleBar(" " + t.Id + " — " + t.Title + " ")
	body := ""
	if m.ready {
		body = m.backlogVP.View()
	}
	// Grooming footer (task 0099): the status prompt and transient notices take
	// precedence; the "e edit" affordance shows only when the file is local.
	hint := " ↑↓/pgup/pgdn scroll · +/- priority · s status"
	if taskFileLocal(t.Path) {
		hint += " · e edit"
	}
	hint += " · esc/← back · ctrl+c quit "
	if m.backlogStatusPrompt {
		hint = " set status: 1 todo · 2 in_progress · 3 in_review · 4 done · 5 blocked · 6 proposed · esc cancel "
	} else if m.backlogNotice != "" {
		hint = " " + m.backlogNotice + " "
	}
	help := m.footerBar(hint)
	return top + "\n" + body + "\n" + help
}

// updatePlans handles the modal plan library browser: a list of saved plans with
// drill-down into one plan's read-only markdown (task 0077).
func (m model) updatePlans(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.planDetail != nil {
		// Detail view: back returns to the list; everything else scrolls the
		// markdown viewport.
		switch key.String() {
		case "ctrl+c", "q":
			return m.confirmQuit()
		case "esc", "backspace", "left":
			m.planDetail = nil
			return m, nil
		}
		var cmd tea.Cmd
		m.plansVP, cmd = m.plansVP.Update(msg)
		return m, cmd
	}
	// List view.
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.plans = false
		return m, nil
	case "up":
		m.plansCursor = navUp(m.plansCursor)
		return m, nil
	case "down":
		m.plansCursor = navDown(m.plansCursor, len(m.plansList))
		return m, nil
	case "enter":
		if len(m.plansList) > 0 {
			return m, m.fetchPlan(m.plansList[m.plansCursor].Name)
		}
		return m, nil
	}
	return m, nil
}

// plansView renders the modal plan library browser (list or detail) as a card.
func (m model) plansView() string {
	if m.planDetail != nil {
		return m.planDetailView(m.planDetail)
	}
	b := browser{
		title:  " ycc — plans ",
		cursor: m.plansCursor,
		hint:   "↑/↓ select · enter view · esc close",
		empty:  "(no saved plans)",
	}
	for _, p := range m.plansList {
		b.rows = append(b.rows, browserRow{
			text:   fmt.Sprintf("%-20s", p.Name),
			suffix: dimStyle.Render(p.Title),
		})
	}
	return m.browserCard(b)
}

// planDetailView renders a single saved plan's markdown content (task 0077) as a
// full-screen scrollable viewport (mirroring the backlog task detail drill-in).
func (m model) planDetailView(p *v1.GetPlanResponse) string {
	top := m.titleBar(" " + p.Name + " — " + p.Title + " ")
	body := ""
	if m.ready {
		body = m.plansVP.View()
	}
	help := m.footerBar(" ↑↓/pgup/pgdn scroll · esc/← back · ctrl+c quit · (ask a session to run it) ")
	return top + "\n" + body + "\n" + help
}

// planDetailContent builds the glamour-rendered markdown body placed into the
// plan detail viewport (m.plansVP).
func (m model) planDetailContent(p *v1.GetPlanResponse) string {
	body := p.Content
	if m.glam != nil {
		if out, err := m.glam.Render(body); err == nil {
			body = strings.Trim(out, "\n")
		}
	}
	return body
}

// refreshPlanDetailVP (re)sizes the plan detail viewport to the current terminal
// dimensions and loads the open plan's content. It is a no-op when no plan is
// open or the terminal size is not yet known.
func (m *model) refreshPlanDetailVP() {
	if m.planDetail == nil || !m.ready {
		return
	}
	h := m.h - 2 // one row for the title bar, one for the footer
	if h < 3 {
		h = 3
	}
	if m.plansVP.Height() == 0 && m.plansVP.Width() == 0 {
		m.plansVP = viewport.New(viewport.WithWidth(m.w), viewport.WithHeight(h))
	} else {
		m.plansVP.SetWidth(m.w)
		m.plansVP.SetHeight(h)
	}
	m.plansVP.SetContent(m.planDetailContent(m.planDetail))
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
	ovAutoExpand
	ovNotifyBell
	ovNotifyDesktop
	ovInterrupt
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
		return m.confirmQuit()
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
			return m.toggleReviewerAndPersist()
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
		// Apply immediately (like interaction level) so the choice sticks without a
		// separate "apply" step — the daemon persists it to ycc.toml.
		return m, m.setRoleConfig(m.roleCoord, "", nil)
	case ovImpl:
		m.roleImpl = cycleModel(m.models, m.roleImpl, d)
		return m, m.setRoleConfig("", m.roleImpl, nil)
	case ovReviewers:
		// Move the visible sub-cursor across the reviewer chips (no toggle, no
		// persist) so the user can see which model the next space/enter affects.
		if n := len(m.models); n > 0 {
			m.reviewerSub = (m.reviewerSub + d + n) % n
		}
		return m, nil
	case ovTheme:
		m.prefs.Theme = cycle(themes, m.prefs.Theme, d)
		clientconfig.Save(m.prefs)
		// Live-switch the palette so the open menu/session repaints in the new
		// theme without a restart.
		applyTheme(themeByName(m.prefs.Theme))
		m.restyleInputs()
		m.makeRenderer()
		m.invalidateRender()
		m.rebuild()
		return m, nil
	case ovFollow:
		m.prefs.Follow = !m.prefs.Follow
		m.follow = m.prefs.Follow
		clientconfig.Save(m.prefs)
		return m, nil
	case ovAutoExpand:
		m.toggleAutoExpand()
		return m, nil
	case ovNotifyBell:
		m.prefs.NotifyBell = !m.prefs.NotifyBell
		clientconfig.Save(m.prefs)
		return m, nil
	case ovNotifyDesktop:
		m.prefs.NotifyDesktop = !m.prefs.NotifyDesktop
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
		return m.toggleReviewerAndPersist()
	case ovBackends:
		// Open the model-backends management modal (task 0044) and refresh the
		// model list so it lists the current backends.
		m.overlay = false
		m.mbOpen = true
		m.mbView = 0
		m.mbCursor = 0
		m.mbErr = ""
		return m, m.fetchModels
	case ovInterrupt:
		// Interrupt the running agent (or resume a paused one) — the overlay
		// route promised by spec §18.7, and the reliable path on terminals where
		// ctrl+i can't be distinguished from tab (ctrl+x is the universal direct
		// chord for the same action). Close the overlay so the user
		// sees the paused/running state and can steer immediately.
		if m.sessionID == "" || m.state != stateSession {
			return m, nil
		}
		if m.paused {
			m.overlay = false
			return m, m.resume()
		}
		if m.status == "running" {
			m.overlay = false
			return m, m.interrupt()
		}
		return m, nil
	case ovBackHome:
		// Explicit, intentional exit from the session (spec §18.2).
		m.overlay = false
		m.state = stateMenu
		return m, m.refreshMenu()
	case ovQuit:
		return m.confirmQuit()
	case ovAutoExpand:
		m.toggleAutoExpand()
		return m, nil
	case ovNotifyBell:
		m.prefs.NotifyBell = !m.prefs.NotifyBell
		clientconfig.Save(m.prefs)
		return m, nil
	case ovNotifyDesktop:
		m.prefs.NotifyDesktop = !m.prefs.NotifyDesktop
		clientconfig.Save(m.prefs)
		return m, nil
	}
	return m, nil
}

// toggleAutoExpand flips the auto-expand-agent-logs preference, persists it, and
// rebuilds the event stream so the new default takes effect immediately.
func (m *model) toggleAutoExpand() {
	m.prefs.AutoExpandLogs = !m.prefs.AutoExpandLogs
	clientconfig.Save(m.prefs)
	m.invalidateRender()
	m.rebuild()
}

// eventExpanded reports whether the event with the given seq/type should render
// expanded. A manual per-row override (m.expanded) always wins; otherwise the
// auto-expand preference and the per-type default decide.
func (m *model) eventExpanded(seq int, typ string) bool {
	if v, ok := m.expanded[seq]; ok {
		return v
	}
	return m.prefs.AutoExpandLogs || autoExpand(typ)
}

// toggleReviewer flips membership of the model under the visible sub-cursor
// (m.reviewerSub). The sub-cursor stays put so the next toggle's target remains
// exactly what the user sees highlighted; it is moved explicitly with ←/→.
func (m *model) toggleReviewer() {
	if len(m.models) == 0 {
		return
	}
	if m.reviewerSub >= len(m.models) {
		m.reviewerSub = 0
	}
	name := m.models[m.reviewerSub].Name
	if m.contains(name) {
		m.roleReviewrs = remove(m.roleReviewrs, name)
	} else {
		m.roleReviewrs = append(m.roleReviewrs, name)
	}
}

// toggleReviewerAndPersist toggles the highlighted reviewer, guards the
// non-empty invariant (a session never points at zero reviewers), and persists
// the new set immediately via SetRoleConfig. Shared by the space and enter key
// paths on the reviewers row.
func (m model) toggleReviewerAndPersist() (tea.Model, tea.Cmd) {
	m.toggleReviewer()
	revs := m.roleReviewrs
	if len(revs) == 0 && len(m.models) > 0 {
		revs = []string{m.models[0].Name}
		m.roleReviewrs = revs
	}
	if len(revs) > 0 {
		return m, m.setRoleConfig("", "", revs)
	}
	return m, nil
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
	// The interrupt row doubles as resume while paused (spec §18.7). It also
	// serves as the fallback route to interrupt on terminals where ctrl+i is
	// indistinguishable from tab (no kitty keyboard protocol).
	interruptLabel, interruptVal := "interrupt agent", "pause at next safe checkpoint"
	switch {
	case m.sessionID == "" || m.state != stateSession:
		interruptVal = "(no active session)"
	case m.paused:
		interruptLabel, interruptVal = "resume agent", "continue from the pause"
	case m.status != "running":
		interruptVal = "(agent is " + m.status + ")"
	}
	rows := []struct{ label, val string }{
		{"interaction level", m.level},
		{"coordinator model", m.roleCoord + " (" + m.thinkLevels["coordinator"] + ")"},
		{"implementer model", m.roleImpl + " (" + m.thinkLevels["implementer"] + ")"},
		{"reviewers", strings.Join(m.roleReviewrs, ", ")},
		{"model backends", "add / edit / remove…"},
		{"theme", m.prefs.Theme},
		{"follow / auto-scroll", boolStr(m.prefs.Follow)},
		{"auto-expand agent logs", boolStr(m.prefs.AutoExpandLogs)},
		{"notify: terminal bell", boolStr(m.prefs.NotifyBell)},
		{"notify: desktop (OSC 9)", boolStr(m.prefs.NotifyDesktop)},
		{interruptLabel, interruptVal},
		{"back to home menu", ""},
		{"quit", ""},
	}
	// Window the rows around the cursor so the card fits short terminals
	// (mirrors browserCard). Fixed chrome: modalCard's 6 non-content rows plus
	// the no-session note (2 lines) when shown.
	budget := len(rows)
	if m.h > 0 {
		chrome := 6
		if m.sessionID == "" {
			chrome += 2
		}
		budget = m.h - chrome
		if budget < 1 {
			budget = 1
		}
	}
	start, end := listWindow(m.ovCursor, len(rows), budget)
	for i, r := range rows[start:end] {
		cursor := "  "
		label := fmt.Sprintf("%-22s", r.label)
		if start+i == m.ovCursor {
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render(label)
		}
		val := r.val
		if start+i == ovReviewers && len(m.models) > 0 {
			// Highlight the chip the next toggle affects only while the cursor is
			// on this row, so the target is always visible before pressing space.
			val = "(" + m.thinkLevels["reviewers"] + ")  " + m.reviewerSummary(start+i == m.ovCursor)
		}
		b.WriteString(cursor + label + dimStyle.Render(val) + "\n")
	}
	if m.sessionID == "" {
		b.WriteString("\n" + dimStyle.Render("(no active session: role changes are saved as defaults; interaction/thinking level apply only within a session)"))
	}
	help := "↑/↓ move · ←/→ change · +/- thinking · enter activate · esc close"
	if m.ovCursor == ovReviewers {
		help = "←/→ highlight model · space/enter toggle · +/- thinking · esc close"
	}
	return m.modalCard(" settings ", strings.TrimRight(b.String(), "\n"), help)
}

func (m model) reviewerSummary(highlight bool) string {
	var parts []string
	for i, mm := range m.models {
		mark := "[ ]"
		if m.contains(mm.Name) {
			mark = "[x]"
		}
		chip := mark + " " + mm.Name
		if highlight && i == m.reviewerSub {
			chip = selStyle.Render(chip)
		}
		parts = append(parts, chip)
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
		"anthropic": {"claude-opus-4-8", "claude-sonnet-4-5", "claude-haiku-4-5", "claude-fable-5"},
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
		return "model id(s)"
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
	}
	return ""
}

// mbNewInputs (re)initializes the form's text inputs with the wizard's
// CharLimit/Width so the form reads consistently with first-run setup.
func (m *model) mbNewInputs() {
	for i := range m.mbInputs {
		ti := textinput.New()
		ti.CharLimit = 200
		ti.SetWidth(50)
		m.mbInputs[i] = ti
	}
	m.mbInputs[mbFieldName].Placeholder = "logical name (optional; defaults to model id)"
	m.mbInputs[mbFieldBaseURL].Placeholder = "base url"
	m.mbInputs[mbFieldModel].Placeholder = "model id(s), space-separated (ctrl+f to fetch)"
	// The model field holds one or more space/comma-separated ids, so it needs a
	// larger char limit than the other single-value inputs.
	m.mbInputs[mbFieldModel].CharLimit = 800
	m.mbInputs[mbFieldModel].SetWidth(60)
	m.mbInputs[mbFieldKeyEnv].Placeholder = "API key env var name (never the key)"
	m.mbInputs[mbFieldPriceIn].Placeholder = "$/Mtok (optional)"
	m.mbInputs[mbFieldPriceOut].Placeholder = "$/Mtok (optional)"
	m.mbInputs[mbFieldPriceCacheRead].Placeholder = "$/Mtok (optional)"
	m.mbInputs[mbFieldPriceCacheWrite].Placeholder = "$/Mtok (optional)"
}

// mbStartAdd opens a blank "add connection" form. The backend defaults to
// anthropic and the model field is prefilled with that backend's curated ids, so
// a single connection produces sibling logical models out of the box (spec §13).
func (m *model) mbStartAdd() {
	m.mbNewInputs()
	m.mbBackends = append([]string(nil), mbBackendList...)
	m.mbBackendIdx = 0
	m.mbThinkIdx, m.mbEffortIdx, m.mbDisplayIdx = 0, 0, 0
	m.mbPresetIdx = -1
	m.mbFormMode = mbAdd
	m.mbOrigName = ""
	m.mbErr, m.mbInfo = "", ""
	m.mbApplyCuratedIDs()
	m.mbFocus = mbFieldName
	m.mbView = 1
	m.mbFocusInputs()
}

// mbApplyCuratedIDs fills the model-id field with the current backend's curated
// default ids (space-separated). Used when opening the add form and when the
// backend is changed in add mode, so switching backend re-seeds sensible ids.
func (m *model) mbApplyCuratedIDs() {
	if presets := mbModelPresets[m.mbBackends[m.mbBackendIdx]]; len(presets) > 0 {
		m.mbInputs[mbFieldModel].SetValue(strings.Join(presets, " "))
		m.mbInputs[mbFieldModel].CursorEnd()
	} else {
		m.mbInputs[mbFieldModel].SetValue("")
	}
}

// mbPrefill fills the form from a loaded ModelConfig for edit/duplicate.
func (m *model) mbPrefill(cfg *v1.ModelConfig, mode int) {
	m.mbNewInputs()
	m.mbFormMode = mode
	m.mbOrigName = cfg.Name
	m.mbOrigModel = cfg.Model
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
	m.mbErr, m.mbInfo = "", ""
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
		m.mbPresetIdx = -1
		// In add mode, re-seed the model-id field with the new backend's curated
		// defaults so switching backend offers sensible ids (spec §13).
		if m.mbFormMode == mbAdd {
			m.mbApplyCuratedIDs()
		}
	case mbFieldThinking:
		m.mbThinkIdx = (m.mbThinkIdx + d + len(mbThinkingList)) % len(mbThinkingList)
	case mbFieldEffort:
		m.mbEffortIdx = (m.mbEffortIdx + d + len(mbEffortList)) % len(mbEffortList)
	case mbFieldDisplay:
		m.mbDisplayIdx = (m.mbDisplayIdx + d + len(mbDisplayList)) % len(mbDisplayList)
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
		return m.confirmQuit()
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
	}
	return m, nil
}

func (m model) mbUpdateForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc":
		m.mbView = 0
		m.mbErr, m.mbInfo = "", ""
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
	case "ctrl+f":
		// Fetch the connection's available model ids into the model-id field.
		m.mbBusy = true
		m.mbErr = ""
		m.mbInfo = "fetching available models…"
		return m, m.mbDiscover()
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

// parseModelIDs splits the model-id field into individual ids on whitespace and
// commas, trimming and de-duplicating while preserving order.
func parseModelIDs(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ','
	})
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// mbSubmitForm validates the connection form and issues UpsertModel for each
// model id entered (spec §13, §18.2). A single connection (backend + base_url +
// key_env + reasoning/pricing) with N model ids becomes N sibling logical models,
// each named after its model id (so the role pickers can select opus vs sonnet vs
// fable within one connection). With a single id an explicit name is honored. In
// edit mode the edited model keeps its logical name for its own model id; any
// extra ids become new siblings on the same connection.
func (m model) mbSubmitForm() (tea.Model, tea.Cmd) {
	explicitName := strings.TrimSpace(m.mbInputs[mbFieldName].Value())
	backend := m.mbBackends[m.mbBackendIdx]
	ids := parseModelIDs(m.mbInputs[mbFieldModel].Value())
	if backend == "" || len(ids) == 0 {
		m.mbErr = "backend and at least one model id are required"
		return m, nil
	}

	// Shared connection fields for every sibling.
	backendURL := strings.TrimSpace(m.mbInputs[mbFieldBaseURL].Value())
	keyEnv := strings.TrimSpace(m.mbInputs[mbFieldKeyEnv].Value())
	thinking := mbThinkingList[m.mbThinkIdx]
	effort := mbEffortList[m.mbEffortIdx]
	display := mbDisplayList[m.mbDisplayIdx]

	var priceIn, priceOut, priceCacheRead, priceCacheWrite *float64
	prices := []struct {
		idx   int
		dst   **float64
		label string
	}{
		{mbFieldPriceIn, &priceIn, "price in"},
		{mbFieldPriceOut, &priceOut, "price out"},
		{mbFieldPriceCacheRead, &priceCacheRead, "price cache read"},
		{mbFieldPriceCacheWrite, &priceCacheWrite, "price cache write"},
	}
	for _, p := range prices {
		v, err := parsePrice(m.mbInputs[p.idx].Value())
		if err != nil {
			m.mbErr = p.label + " must be a number"
			return m, nil
		}
		*p.dst = v
	}

	// Compute the logical name for each id. By default the name is the id itself.
	names := mbModelNames(ids, m.mbFormMode, m.mbOrigName, m.mbOrigModel, explicitName)

	var cfgs []*v1.ModelConfig
	for i, id := range ids {
		cfgs = append(cfgs, &v1.ModelConfig{
			Name:            names[i],
			Backend:         backend,
			BaseUrl:         backendURL,
			Model:           id,
			KeyEnv:          keyEnv,
			Thinking:        thinking,
			Effort:          effort,
			ThinkingDisplay: display,
			PriceInput:      priceIn,
			PriceOutput:     priceOut,
			PriceCacheRead:  priceCacheRead,
			PriceCacheWrite: priceCacheWrite,
		})
	}
	m.mbErr, m.mbInfo = "", ""
	return m, m.mbUpsertMany(cfgs)
}

// mbModelNames assigns a logical name to each model id. Names default to the id
// itself so a connection's model ids become self-named sibling models. Two
// special cases preserve an existing logical name:
//   - add/duplicate with exactly one id and an explicit name → use that name;
//   - edit mode → the edited model keeps its name (origName) for its own id
//     (origModel), or, if that id is gone, for the first id (an id rename). All
//     other ids are new siblings named after themselves.
func mbModelNames(ids []string, formMode int, origName, origModel, explicitName string) []string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = id
	}
	if formMode == mbEdit && origName != "" {
		keep := -1
		for i, id := range ids {
			if id == origModel {
				keep = i
				break
			}
		}
		if keep == -1 {
			keep = 0 // original id was changed → treat the first id as the rename
		}
		names[keep] = origName
	} else if len(ids) == 1 && explicitName != "" {
		names[0] = explicitName
	}
	return names
}

func (m model) mbUpdateConfirm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc":
		m.mbView = 0
		return m, nil
	case "enter":
		if m.mbCursor >= len(m.models) {
			m.mbView = 0
			return m, nil
		}
		return m, m.mbRemove(m.models[m.mbCursor].Name)
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

// mbUpsert adds or replaces a logical model backend. The change always takes
// effect at runtime and is written back to ycc.toml by the daemon.
func (m model) mbUpsert(cfg *v1.ModelConfig) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.UpsertModel(m.ctx, connect.NewRequest(&v1.UpsertModelRequest{
			Model: cfg, Persist: true,
		})); err != nil {
			return mbWriteMsg{err: err}
		}
		return mbWriteMsg{}
	}
}

// mbUpsertMany upserts several sibling logical models (one per model id) that
// share a connection (spec §13). Any failure aborts and is surfaced inline; the
// models upserted before the failure remain (idempotent re-submit fixes it).
func (m model) mbUpsertMany(cfgs []*v1.ModelConfig) tea.Cmd {
	return func() tea.Msg {
		for _, cfg := range cfgs {
			if _, err := m.client.UpsertModel(m.ctx, connect.NewRequest(&v1.UpsertModelRequest{
				Model: cfg, Persist: true,
			})); err != nil {
				return mbWriteMsg{err: fmt.Errorf("%s: %w", cfg.Name, err)}
			}
		}
		return mbWriteMsg{}
	}
}

// mbDiscover queries the backend connection currently entered in the form for its
// available model ids (spec §13). The result populates the model-id field.
func (m model) mbDiscover() tea.Cmd {
	backend := m.mbBackends[m.mbBackendIdx]
	baseURL := strings.TrimSpace(m.mbInputs[mbFieldBaseURL].Value())
	keyEnv := strings.TrimSpace(m.mbInputs[mbFieldKeyEnv].Value())
	return func() tea.Msg {
		resp, err := m.client.DiscoverModels(m.ctx, connect.NewRequest(&v1.DiscoverModelsRequest{
			Backend: backend, BaseUrl: baseURL, KeyEnv: keyEnv,
		}))
		if err != nil {
			return mbDiscoverMsg{err: err}
		}
		return mbDiscoverMsg{ids: resp.Msg.ModelIds, note: resp.Msg.Note, fromNet: resp.Msg.FromNetwork}
	}
}

// mbRemove deletes a logical model backend; rejected if a role still references it.
func (m model) mbRemove(name string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.RemoveModel(m.ctx, connect.NewRequest(&v1.RemoveModelRequest{
			Name: name, Persist: true,
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
	if len(m.models) == 0 {
		b.WriteString(dimStyle.Render("(no model backends configured)") + "\n")
	}
	// Window the rows around the cursor so the card never overruns the terminal
	// vertically (mirrors browserCard). Fixed chrome: modalCard's 6 non-content
	// rows plus the trailing note (2) and the error block (2) when present.
	hint := "a add · e/enter edit · d duplicate · x remove · esc back"
	budget := len(m.models)
	if m.h > 0 {
		chrome := 6 + 2
		if m.mbErr != "" {
			chrome += 2
		}
		budget = m.h - chrome
		if budget < 1 {
			budget = 1
		}
	}
	start, end := listWindow(m.mbCursor, len(m.models), budget)
	if start > 0 || end < len(m.models) {
		hint = fmt.Sprintf("%s · %d–%d/%d", hint, start+1, end, len(m.models))
	}
	for i, mm := range m.models[start:end] {
		cursor := "  "
		row := fmt.Sprintf("%-16s %-12s %s", mm.Name, mm.Backend, mm.Model)
		if start+i == m.mbCursor {
			cursor = selStyle.Render("▸ ")
			row = selStyle.Render(row)
		}
		b.WriteString(cursor + row + "\n")
	}
	if m.mbErr != "" {
		b.WriteString("\n" + errStyle.Render(m.mbErr) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("changes are saved to ycc.toml automatically"))
	return m.modalCard(" model backends ", strings.TrimRight(b.String(), "\n"), hint)
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
	order := []int{
		mbFieldName, mbFieldBackend, mbFieldBaseURL, mbFieldModel, mbFieldKeyEnv,
		mbFieldThinking, mbFieldEffort, mbFieldDisplay,
		mbFieldPriceIn, mbFieldPriceOut, mbFieldPriceCacheRead, mbFieldPriceCacheWrite,
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
		default:
			val = m.mbInputs[f].View()
		}
		b.WriteString(cursor + label + " " + val + "\n")
		// Under the focused model field, hint that multiple ids are allowed and how
		// to fetch/cycle them. Free text always works.
		if f == mbFieldModel && m.mbFocus == mbFieldModel {
			b.WriteString("    " + dimStyle.Render("space-separated ids · ctrl+f fetch from backend · ctrl+n/p cycle presets") + "\n")
			if presets := mbModelPresets[m.mbBackends[m.mbBackendIdx]]; len(presets) > 0 {
				b.WriteString("    " + dimStyle.Render("presets: "+strings.Join(presets, " · ")) + "\n")
			}
		}
	}
	if m.mbInfo != "" {
		b.WriteString("\n" + dimStyle.Render(m.mbInfo) + "\n")
	}
	if m.mbErr != "" {
		b.WriteString("\n" + errStyle.Render(m.mbErr) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("(keys are env-var references only — never paste a secret)"))
	return m.modalCard(" "+title+" ", strings.TrimRight(b.String(), "\n"),
		"Tab/↑↓ move · ←/→ change · ctrl+f fetch models · enter save · esc back")
}

func (m model) mbConfirmView() string {
	var b strings.Builder
	name := ""
	if m.mbCursor < len(m.models) {
		name = m.models[m.mbCursor].Name
	}
	b.WriteString("remove " + selStyle.Render(name) + "?\n")
	b.WriteString("\n" + dimStyle.Render("this is saved to ycc.toml"))
	if m.mbErr != "" {
		b.WriteString("\n\n" + errStyle.Render(m.mbErr) + "\n")
	}
	return m.modalCard(" remove model backend ", strings.TrimRight(b.String(), "\n"),
		"enter confirm · esc cancel")
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
	// Skip hidden rows (folded tool_results and empty model_turns): they share
	// another event's rendered row, so selection should land on the owning
	// visible row, never on them. Travel in the move direction past any hidden
	// row, then snap back if we ran off the end.
	dir := d
	if dir == 0 {
		dir = 1
	}
	for m.hiddenRow(m.selected) {
		next := m.selected + dir
		if next < 0 || next >= len(m.evs) {
			break
		}
		m.selected = next
	}
	for m.hiddenRow(m.selected) && m.selected > 0 {
		m.selected--
	}
	m.follow = m.selected == len(m.evs)-1
	m.rebuild()
	m.ensureVisible()
}

// --- transcript search & jump-to-event navigation (task 0116) ---

// searchableText returns the plain-text (ansi-stripped) rendering of event i as
// it appears on screen: its type/actor labels and detail-line headline plus,
// when the row is expanded, its rendered body. Hidden rows (folded tool_results,
// empty model_turns, echoed idles, delivery markers) return "" so a folded row
// never matches on its own — its text participates via the owning visible row.
// Used for case-insensitive substring matching by the transcript search.
func (m *model) searchableText(i int) string {
	if i < 0 || i >= len(m.evs) || m.hiddenRow(i) {
		return ""
	}
	ev := m.evs[i]
	var b strings.Builder
	b.WriteString(ev.Type)
	b.WriteByte(' ')
	b.WriteString(ev.Actor)
	b.WriteByte(' ')
	b.WriteString(ansi.Strip(m.detailLineFor(ev)))
	if m.eventExpanded(int(ev.Seq), ev.Type) {
		b.WriteByte(' ')
		b.WriteString(ansi.Strip(m.bodyFor(ev)))
	}
	return b.String()
}

// yankText returns the plain text to copy to the clipboard for an event when the
// user presses `y` on the selected transcript row (task 0141). For events whose
// raw source pastes better than the glamour-rendered body (a commit sha, an error
// message, a model's text) it returns that raw value; otherwise it falls back to
// the on-screen expanded content stripped of styling. Returns "" when there's
// nothing worth copying.
func (m *model) yankText(ev *v1.Event) string {
	if ev == nil {
		return ""
	}
	switch ev.Type {
	case "commit_made":
		return dataField(ev, "sha")
	case "session_error":
		head := sessionErrorHead(ev)
		msg := dataField(ev, "msg")
		switch {
		case head != "" && msg != "":
			return head + "\n" + msg
		case head != "":
			return head
		default:
			return msg
		}
	case "model_turn", "user_input":
		if t := firstField(ev, "text", "report", "question", "answer"); t != "" {
			return strings.TrimSpace(t)
		}
	case "session_idle":
		if t := firstField(ev, "report"); t != "" {
			return strings.TrimSpace(t)
		}
	case "tool_result":
		if t := dataField(ev, "result"); t != "" {
			return strings.TrimSpace(t)
		}
	case "tool_call":
		if a := dataField(ev, "args"); a != "" {
			return strings.TrimSpace(prettyArgs(a))
		}
	}
	return strings.TrimSpace(ansi.Strip(m.bodyFor(ev)))
}

// matchesQuery reports whether event i's searchable text contains q, which must
// already be lower-cased.
func (m *model) matchesQuery(i int, q string) bool {
	if q == "" {
		return false
	}
	return strings.Contains(strings.ToLower(m.searchableText(i)), q)
}

// searchStep moves the selection to the nearest event matching the active query,
// scanning in direction dir (+1 forward / -1 backward) from index `from` and
// wrapping around the whole stream. It is a no-op when there is no query or no
// match. It drives both incremental typing (dir +1 from the current selection,
// which includes it) and n/N cycling (dir ±1 from one past the current match).
func (m *model) searchStep(dir, from int) {
	q := strings.ToLower(m.searchQuery)
	n := len(m.evs)
	if q == "" || n == 0 {
		return
	}
	for k := 0; k < n; k++ {
		i := ((from+dir*k)%n + n) % n
		if m.matchesQuery(i, q) {
			m.selected = i
			m.follow = false
			m.rebuild()
			m.ensureVisible()
			return
		}
	}
}

// runSearch re-jumps the selection to the first match at or after the current
// selection after each incremental edit of the query.
func (m *model) runSearch() {
	from := m.selected
	if from < 0 {
		from = 0
	}
	m.searchStep(1, from)
}

// searchCount returns the total number of matches for the active query and the
// 1-based ordinal of the match at or before the current selection (0 when the
// selection isn't on a match). Feeds the footer counter ⌕ "q" k/N.
func (m *model) searchCount() (total, cur int) {
	q := strings.ToLower(m.searchQuery)
	if q == "" {
		return 0, 0
	}
	for i := range m.evs {
		if m.matchesQuery(i, q) {
			total++
			if i <= m.selected {
				cur = total
			}
		}
	}
	return total, cur
}

// jumpToEvent moves the selection to the nearest non-hidden event in direction
// dir (+1 forward / -1 backward) whose Type is one of types. Unlike search it
// does NOT wrap: a no-op when there is no such event past the current selection.
// Drives the {}()<>[] jump keys.
func (m *model) jumpToEvent(dir int, types ...string) {
	if len(m.evs) == 0 {
		return
	}
	start := m.selected
	if start < 0 {
		if dir > 0 {
			start = -1
		} else {
			start = len(m.evs)
		}
	}
	for i := start + dir; i >= 0 && i < len(m.evs); i += dir {
		if m.hiddenRow(i) {
			continue
		}
		for _, t := range types {
			if m.evs[i].Type == t {
				m.selected = i
				m.follow = false
				m.rebuild()
				m.ensureVisible()
				return
			}
		}
	}
}

// clearSearch resets all transcript-search state. Shared by esc-cancel and the
// pipeline resets (started/transcript load, leaving a transcript).
func (m *model) clearSearch() {
	m.searching = false
	m.searchQuery = ""
}

// --- modal transcript search & jump-to-event navigation (task 0119) ---
//
// The session browser opened as a modal OVER a live session (task 0112) renders
// transcripts statelessly into histModalVP and does NOT use the live event
// pipeline. These helpers give it the same `/` search (n/N, esc) and {}()<>[]
// jump keys as the live transcript, but line-based over the rendered content —
// they never touch m.evs/m.vp or the live search state (m.searching/m.searchQuery).

// resetHistModalNav clears all modal-transcript search/jump state. Called when a
// transcript loads, when backing out to the list, and when closing the modal.
func (m *model) resetHistModalNav() {
	m.histModalSearching = false
	m.histModalQuery = ""
	m.histModalCurLine = -1
	m.histModalEvents = nil
	m.histModalLines = nil
	m.histModalEventLines = nil
}

// histLineMatches reports whether content line i (ansi-stripped, lower-cased)
// contains q, which must already be lower-cased.
func (m *model) histLineMatches(i int, q string) bool {
	if q == "" || i < 0 || i >= len(m.histModalLines) {
		return false
	}
	return strings.Contains(strings.ToLower(ansi.Strip(m.histModalLines[i])), q)
}

// histSearchStep moves histModalCurLine to the nearest content line matching the
// active query, scanning in direction dir (+1 forward / -1 backward) from `from`
// and wrapping around all lines. No-op when there is no query or no match. Drives
// both incremental typing (dir +1 from the current line, inclusive) and n/N.
func (m *model) histSearchStep(dir, from int) {
	q := strings.ToLower(m.histModalQuery)
	n := len(m.histModalLines)
	if q == "" || n == 0 {
		return
	}
	if from < 0 {
		from = 0
	}
	for k := 0; k < n; k++ {
		i := ((from+dir*k)%n + n) % n
		if m.histLineMatches(i, q) {
			m.histModalCurLine = i
			m.applyHistModalContent()
			return
		}
	}
}

// histRunSearch re-jumps the current line to the first match at or after the
// current line after each incremental edit of the query.
func (m *model) histRunSearch() {
	from := m.histModalCurLine
	if from < 0 {
		from = 0
	}
	m.histSearchStep(1, from)
}

// histSearchCount returns the total number of matching content lines and the
// 1-based ordinal of the match at or before the current line (0 when the current
// line isn't on a match). Feeds the footer counter ⌕ "q" k/N.
func (m *model) histSearchCount() (total, cur int) {
	q := strings.ToLower(m.histModalQuery)
	if q == "" {
		return 0, 0
	}
	for i := range m.histModalLines {
		if m.histLineMatches(i, q) {
			total++
			if i <= m.histModalCurLine {
				cur = total
			}
		}
	}
	return total, cur
}

// histJump moves histModalCurLine to the nearest event block start line in
// direction dir (+1 forward / -1 backward) whose Type is one of types. Unlike
// search it does NOT wrap: a no-op when there is no such event past the current
// line. Drives the {}()<>[] jump keys.
func (m *model) histJump(dir int, types ...string) {
	if len(m.histModalEventLines) == 0 {
		return
	}
	cur := m.histModalCurLine
	if cur < 0 {
		// No cursor yet: start just before the first / just past the last line so
		// the first forward/backward match is found (mirrors jumpToEvent).
		if dir > 0 {
			cur = -1
		} else {
			cur = len(m.histModalLines)
		}
	}
	if dir > 0 {
		for _, el := range m.histModalEventLines {
			if el.line <= cur {
				continue
			}
			if typeMatches(el.typ, types) {
				m.histModalCurLine = el.line
				m.applyHistModalContent()
				return
			}
		}
		return
	}
	for i := len(m.histModalEventLines) - 1; i >= 0; i-- {
		el := m.histModalEventLines[i]
		if el.line >= cur {
			continue
		}
		if typeMatches(el.typ, types) {
			m.histModalCurLine = el.line
			m.applyHistModalContent()
			return
		}
	}
}

// typeMatches reports whether t is one of types.
func typeMatches(t string, types []string) bool {
	for _, want := range types {
		if t == want {
			return true
		}
	}
	return false
}

// applyHistModalContent re-sets the modal viewport content, highlighting the
// current line (histModalCurLine) with a reverse style and scrolling it roughly
// centered into view. With no current line it renders the plain content.
func (m *model) applyHistModalContent() {
	if len(m.histModalLines) == 0 {
		return
	}
	cur := m.histModalCurLine
	if cur < 0 || cur >= len(m.histModalLines) {
		m.histModalVP.SetContent(strings.Join(m.histModalLines, "\n"))
		return
	}
	lines := make([]string, len(m.histModalLines))
	copy(lines, m.histModalLines)
	// Strip the matched line's own ansi so the reverse highlight reads cleanly.
	lines[cur] = histHighlightStyle.Render(ansi.Strip(lines[cur]))
	m.histModalVP.SetContent(strings.Join(lines, "\n"))
	// Center the current line in the viewport, clamped to valid offsets.
	off := cur - m.histModalVP.Height()/2
	if max := len(m.histModalLines) - m.histModalVP.Height(); off > max {
		off = max
	}
	if off < 0 {
		off = 0
	}
	m.histModalVP.SetYOffset(off)
}

func (m *model) toggle(i int) {
	if i < 0 || i >= len(m.evs) {
		return
	}
	// A hidden row (folded tool_result or empty model_turn) toggles the visible
	// row it shares.
	for m.hiddenRow(i) && i > 0 {
		i--
	}
	seq := int(m.evs[i].Seq)
	cur := m.eventExpanded(seq, m.evs[i].Type)
	m.expanded[seq] = !cur
	m.invalidateRow(i) // expansion changes this row's rendered block
	m.rebuild()
	m.ensureVisible()
}

// applyTransient routes a transient (broadcast-only) event into ephemeral live
// UI state and reports whether that state changed. Transients NEVER enter m.evs,
// the reducers, or seq tracking (task 0129) — they only drive the live tail row.
//
// turn_delta carries {"text": <snapshot>} where text is the FULL accumulated
// turn output so far (snapshot semantics), so a new delta simply replaces the
// actor's tail. A done/empty delta ({"text":"","done":true}) clears it so no
// stale tail survives the end of a turn. retry carries an API-failure backoff
// notice ({attempt, max_attempts, delay_ms, kind, status}) rendered as a
// per-actor note; a non-empty delta (the next attempt streaming) clears it.
// Other (future) transient types are ignored.
func (m *model) applyTransient(ev *v1.Event) bool {
	if ev == nil {
		return false
	}
	if ev.Type == "retry" {
		if m.retryNotes == nil {
			m.retryNotes = map[string]string{}
		}
		m.retryNotes[ev.Actor] = retryNoteText(ev)
		return true
	}
	if ev.Type != "turn_delta" {
		return false
	}
	if m.liveTails == nil {
		m.liveTails = map[string]string{}
	}
	text := dataField(ev, "text")
	done := dataField(ev, "done") == "true"
	if done || strings.TrimSpace(text) == "" {
		if _, ok := m.liveTails[ev.Actor]; ok {
			delete(m.liveTails, ev.Actor)
			return true
		}
		return false
	}
	changed := false
	// A fresh attempt is streaming: any pending retry note for this actor is
	// obsolete.
	if _, ok := m.retryNotes[ev.Actor]; ok {
		delete(m.retryNotes, ev.Actor)
		changed = true
	}
	if m.liveTails[ev.Actor] == text {
		return changed
	}
	m.liveTails[ev.Actor] = text
	return true
}

// retryNoteText renders a transient retry event's data as a one-line note, e.g.
// "rate_limit (429): retrying in 8s — attempt 2/3".
func retryNoteText(ev *v1.Event) string {
	kind := dataField(ev, "kind")
	if kind == "" {
		kind = "api error"
	}
	head := kind
	if st := dataField(ev, "status"); st != "" && st != "0" {
		head += " (" + st + ")"
	}
	note := head + ": retrying"
	if ms := dataField(ev, "delay_ms"); ms != "" {
		if v, err := strconv.ParseFloat(ms, 64); err == nil && v > 0 {
			note += " in " + (time.Duration(v) * time.Millisecond).Round(100*time.Millisecond).String()
		}
	}
	if a, max := dataField(ev, "attempt"), dataField(ev, "max_attempts"); a != "" && max != "" {
		note += fmt.Sprintf(" — attempt %s/%s", a, max)
	}
	return note
}

// sessionErrorHead renders the structured classification a session_error event
// may carry (kind/status/attempts, emitted by the engine loop, spec §7.2) as a
// compact lead line, plus an actionable hint for the kinds a user can act on.
// Returns "" for legacy/unclassified errors so they render exactly as before.
func sessionErrorHead(ev *v1.Event) string {
	kind := dataField(ev, "kind")
	if kind == "" || kind == "unknown" {
		return ""
	}
	head := kind
	if st := dataField(ev, "status"); st != "" && st != "0" {
		head += " (" + st + ")"
	}
	if a := dataField(ev, "attempts"); a != "" && a != "1" {
		head += " · " + a + " attempts"
	}
	switch kind {
	case "auth":
		head += " — check the model's API key / credentials"
	case "rate_limit", "overloaded", "server", "timeout", "network":
		head += " — transient; sending a message retries the turn"
	}
	return head
}

func (m *model) appendEvent(ev *v1.Event) {
	m.evs = append(m.evs, ev)
	n := len(m.evs) - 1
	// A new event can change how the rows just before it render: the previous
	// rendered row's └─/├─ sub-run connector, or a tool_call's collapsed summary
	// once this tool_result folds into it (in-flight ○ becomes ✓/✗ + response).
	// Drop that row's cached block so only IT re-renders, not the whole log.
	for j := n - 1; j >= 0; j-- {
		if !m.hiddenRow(j) {
			m.invalidateRow(j)
			break
		}
	}
	if ev.Type == "user_input_delivered" {
		// Mark the queued echo this delivery pairs with as delivered so it stops
		// rendering "(queued)" once it actually entered the conversation (§18.7).
		if seq, ok := deliveredSeq(ev); ok {
			if m.deliveredSeqs == nil {
				m.deliveredSeqs = map[int64]bool{}
			}
			m.deliveredSeqs[seq] = true
			// The queued echo row (possibly far back) now drops its "(queued)"
			// tag — invalidate its cached block so it re-renders.
			for j := n - 1; j >= 0; j-- {
				if m.evs[j].Seq == seq {
					m.invalidateRow(j)
					break
				}
			}
		}
	}
	switch ev.Type {
	case "model_turn":
		// The durable turn supersedes any live streamed tail for this actor: drop
		// it so the persisted row replaces the in-progress row with no stale tail
		// (task 0129). A clearing turn_delta usually arrives too, but clearing here
		// makes the swap deterministic even if that transient is lost. Any pending
		// retry note is likewise superseded by the turn's outcome.
		delete(m.liveTails, ev.Actor)
		delete(m.retryNotes, ev.Actor)
		// Accumulate the turn's usage into the running per-model tally that feeds
		// the live token/cost readout (task 0062, spec §20.1). Parsing is best-effort:
		// a turn without a usage block contributes nothing.
		if u, name := eventUsage(ev); u != (event.Usage{}) {
			if m.usageByModel == nil {
				m.usageByModel = map[string]event.Usage{}
			}
			cur := m.usageByModel[name]
			cur.Input += u.Input
			cur.Output += u.Output
			cur.CacheRead += u.CacheRead
			cur.CacheWrite += u.CacheWrite
			cur.Total += u.Total
			m.usageByModel[name] = cur
		}
	case "budget_warning":
		// Session crossed ~80% of a configured cap (task 0137, spec §20.6): surface
		// a distinct status-bar warning. Track the highest pct seen.
		if p := floatField(ev, "pct"); p > m.budgetPct {
			m.budgetPct = p
		}
	case "budget_exceeded":
		m.budgetExceeded = true
	case "task_focus":
		// The session's focus moved to a (new) backlog task: surface it in the
		// status bar so the header always names the task being worked on.
		if t := dataField(ev, "task"); t != "" {
			m.focusTask = t
			m.focusTaskTitle = dataField(ev, "title")
		}
	case "question_asked":
		// This question is now the canonical row for its ask_user exchange: the
		// tool_call that produced it (rendered while in flight, possibly with
		// other-actor rows in between) folds away. Drop its cached block/fold
		// state — and its rendered neighbors', whose run boundaries shift.
		for j := len(m.evs) - 2; j >= 0; j-- {
			pv := m.evs[j]
			if pv.Actor != ev.Actor {
				continue
			}
			if pv.Type == "tool_call" && dataField(pv, "name") == "ask_user" {
				m.invalidateRow(j)
				m.invalidateNeighbors(j)
			}
			if pv.Type == "tool_call" || pv.Type == "tool_result" || pv.Type == "question_asked" {
				break
			}
		}
		if qs := dataQuestions(ev); len(qs) > 0 {
			// Multi-question form: start the questionnaire wizard.
			m.startWizard(qs, ev.Seq)
			break
		}
		m.pending = dataField(ev, "question")
		m.pendingSeq = ev.Seq
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
		m.pendingSeq = 0
		m.status = "running"
		m.pickerOpts = nil
		m.picking = false
		// clearWizard also wipes the body cache, which the single-question path
		// needs too: the answer now folds into the question_asked row's body.
		m.clearWizard()
		// Safety net: a picker question blurred the textarea; make sure the
		// confirmed answer hands focus back so the input box is typable again.
		// Focus() flips the state synchronously — the discarded cmd is only the
		// cursor blink. Skip while the transcript search bar owns input.
		if !m.searching {
			m.input.Focus()
		}
	case "session_idle":
		m.status = "idle"
	case "session_reopened":
		// Reopen marker: the daemon reconstructed the model history and repaired
		// any dangling ask_user tool call with a synthetic result (engine replay),
		// so a question_asked replayed just before this marker is stale — no
		// answer can ever be delivered to it. Drop the picker/wizard and give the
		// input box back, or the reopened session starts with dead input.
		if m.pending != "" || m.picking || m.wizActive {
			m.pending = ""
			m.pendingSeq = 0
			m.pickerOpts = nil
			m.picking = false
			m.clearWizard()
			// The stale question also latched "waiting for your answer"; the
			// daemon follows up with its real state (session_idle / activity).
			m.status = "running"
			if !m.searching {
				m.input.Focus()
			}
		}
	case "session_error":
		m.status = "error"
		// A failed turn ends any in-progress stream: drop the actor's live tail so
		// no stale streamed text lingers below the error (task 0129), and drop any
		// pending retry note (the failure is now durable).
		delete(m.liveTails, ev.Actor)
		delete(m.retryNotes, ev.Actor)
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
	// the header must not stay stuck on "error" after recovery. An idle status
	// clears the same way: prodding a finished session emits a user_input echo
	// the moment the daemon accepts it, but the first model event can lag tens
	// of seconds behind (long context + thinking) — without this the header
	// keeps saying "idle", the footer keeps offering "session finished", and no
	// spinner runs, so the accepted follow-up looks like it went nowhere.
	if m.status == "error" || m.status == "idle" {
		switch ev.Type {
		case "model_turn", "tool_call", "tool_result", "thinking", "user_input", "user_input_delivered":
			m.status = "running"
		}
	}
	if m.follow {
		m.selected = len(m.evs) - 1
	}
	// NOTE: appendEvent deliberately does NOT call rebuild() — the caller batches
	// a burst of events (e.g. the persisted log replayed on reopen) and rebuilds
	// once, turning an O(N^2) "rebuild per event" reload into a single O(N) pass.
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

// --- ask_user Q&A folding ---
//
// One ask_user round-trip produces four events: the engine's tool_call, the
// interaction gate's question_asked + question_answered, and the engine's
// tool_result (whose payload repeats the answer). Rendering all four shows the
// question up to three times and the answer up to four. The transcript instead
// treats question_asked as the canonical row for the whole exchange: the answer
// folds into it (see questionBody), while the tool plumbing rows and the
// question_answered row are hidden. The helpers below do the (non-adjacent)
// pairing; every scan matches only same-actor events, so interleaved user input
// or sub-agent activity never breaks a pair.

// askQuestionIdx returns the index of the question_asked event produced by the
// ask_user tool_call at i, or -1. The interaction gate emits question_asked
// while the tool call is executing, so it is the next same-actor question
// event; hitting any other same-actor tool_call/tool_result first means the
// call never asked (e.g. a validation error) and it must stay visible.
func (m *model) askQuestionIdx(i int) int {
	if i < 0 || i >= len(m.evs) {
		return -1
	}
	call := m.evs[i]
	if call.Type != "tool_call" || dataField(call, "name") != "ask_user" {
		return -1
	}
	for j := i + 1; j < len(m.evs); j++ {
		ev := m.evs[j]
		if ev.Actor != call.Actor {
			continue
		}
		switch ev.Type {
		case "question_asked":
			return j
		case "tool_call", "tool_result":
			return -1
		}
	}
	return -1
}

// resultCallIdx returns the index of the tool_call that produced the
// tool_result at i, scanning backward over the interleaved question events an
// ask_user call emits (so, unlike mergedResultIdx, it does not require
// adjacency). A same-actor tool_result encountered first means the result at i
// belongs to some earlier call and pairing fails.
func (m *model) resultCallIdx(i int) int {
	if i < 0 || i >= len(m.evs) || m.evs[i].Type != "tool_result" {
		return -1
	}
	res := m.evs[i]
	for j := i - 1; j >= 0; j-- {
		ev := m.evs[j]
		if ev.Actor != res.Actor {
			continue
		}
		switch ev.Type {
		case "tool_call":
			cid, rid := dataField(ev, "id"), dataField(res, "id")
			if cid != "" && rid != "" && cid != rid {
				return -1
			}
			return j
		case "tool_result":
			return -1
		}
	}
	return -1
}

// answerIdxFor returns the index of the question_answered event that resolved
// the question_asked at qi, or -1 while it is still unanswered. Questions are
// strictly serialized per actor (the interaction gate holds one pending
// question at a time), so the next same-actor question event decides.
func (m *model) answerIdxFor(qi int) int {
	if qi < 0 || qi >= len(m.evs) || m.evs[qi].Type != "question_asked" {
		return -1
	}
	for j := qi + 1; j < len(m.evs); j++ {
		ev := m.evs[j]
		if ev.Actor != m.evs[qi].Actor {
			continue
		}
		switch ev.Type {
		case "question_answered":
			return j
		case "question_asked":
			return -1
		}
	}
	return -1
}

// questionIdxForAnswer is the inverse of answerIdxFor: the index of the
// question_asked that the question_answered at i resolves, or -1.
func (m *model) questionIdxForAnswer(i int) int {
	if i < 0 || i >= len(m.evs) || m.evs[i].Type != "question_answered" {
		return -1
	}
	for j := i - 1; j >= 0; j-- {
		ev := m.evs[j]
		if ev.Actor != m.evs[i].Actor {
			continue
		}
		switch ev.Type {
		case "question_asked":
			return j
		case "question_answered":
			return -1
		}
	}
	return -1
}

// answerEventFor returns the question_answered event paired with the given
// question_asked event, or nil while it is unanswered. Used by questionBody to
// fold the answer into the question's rendered block.
func (m *model) answerEventFor(q *v1.Event) *v1.Event {
	for i, ev := range m.evs {
		if ev.Seq == q.Seq && ev.Type == "question_asked" {
			if ai := m.answerIdxFor(i); ai >= 0 {
				return m.evs[ai]
			}
			return nil
		}
	}
	return nil
}

// isAskUserPlumbing reports whether event i is ask_user tool plumbing already
// represented by its question_asked row: the ask_user tool_call itself (once
// its question_asked exists) or that call's tool_result (whose payload just
// repeats the answer). An errored result (e.g. the ask was cancelled) stays
// visible so failures are never silently swallowed.
func (m *model) isAskUserPlumbing(i int) bool {
	if i < 0 || i >= len(m.evs) {
		return false
	}
	switch m.evs[i].Type {
	case "tool_call":
		return m.askQuestionIdx(i) >= 0
	case "tool_result":
		if dataField(m.evs[i], "error") == "true" {
			return false
		}
		ci := m.resultCallIdx(i)
		return ci >= 0 && m.askQuestionIdx(ci) >= 0
	}
	return false
}

// isFoldedAnswer reports whether event i is a question_answered folded into its
// preceding question_asked row (which renders the Q and the A as one block).
func (m *model) isFoldedAnswer(i int) bool {
	if i < 0 || i >= len(m.evs) || m.evs[i].Type != "question_answered" {
		return false
	}
	return m.questionIdxForAnswer(i) >= 0
}

// isEmptyModelTurn reports whether the event at index i is a model_turn that
// carries no text — i.e. an agent turn whose only payload was tool calls. Such a
// turn would otherwise render as a bare row showing just its timing/usage
// suffix, so we hide it and let the surrounding tool calls stand on their own.
// Per-turn token usage is still accumulated from the raw event stream elsewhere,
// so suppressing the row does not affect cost tracking.
func (m *model) isEmptyModelTurn(i int) bool {
	if i < 0 || i >= len(m.evs) {
		return false
	}
	ev := m.evs[i]
	return ev.Type == "model_turn" && strings.TrimSpace(dataField(ev, "text")) == ""
}

// isEchoedIdle reports whether the event at index i is a session_idle whose
// report merely echoes the preceding final model_turn (so renderBody de-dupes it
// to nothing). Such a row carries no content beyond the status change — and its
// collapsed header would otherwise re-print the full report a second time, right
// below the identical model_turn — so we hide it entirely.
func (m *model) isEchoedIdle(i int) bool {
	if i < 0 || i >= len(m.evs) {
		return false
	}
	ev := m.evs[i]
	return ev.Type == "session_idle" && strings.TrimSpace(m.bodyFor(ev)) == ""
}

// hiddenRow reports whether event i renders no block of its own and instead
// shares the previous rendered row's start line: a tool_result folded into its
// preceding tool_call, an empty (tool-calls-only) model_turn, a session_idle
// whose report just echoes the final model_turn, ask_user tool plumbing (the
// question_asked row is the canonical rendering of the exchange), or a
// question_answered folded into its question_asked row. The result is memoized
// per index (see hiddenCache): the pairing scans behind it re-parse event JSON
// and can walk the log, so recomputing them for every row on every rebuild made
// long sessions quadratic. Entries are invalidated when a later event can flip
// an earlier row's fold (appendEvent) or wholesale via invalidateRender.
func (m *model) hiddenRow(i int) bool {
	if v, ok := m.hiddenCache[i]; ok {
		return v
	}
	h := m.computeHiddenRow(i)
	if m.hiddenCache == nil {
		m.hiddenCache = map[int]bool{}
	}
	m.hiddenCache[i] = h
	return h
}

func (m *model) computeHiddenRow(i int) bool {
	if i >= 0 && i < len(m.evs) && m.evs[i].Type == "user_input_delivered" {
		// The delivery marker is a bookkeeping event, not a message: its text is
		// already shown by the (now-upgraded) queued user_input row, so it renders
		// no block of its own — otherwise the message would appear twice (§18.7).
		return true
	}
	return m.isMergedResult(i) || m.isEmptyModelTurn(i) || m.isEchoedIdle(i) ||
		m.isAskUserPlumbing(i) || m.isFoldedAnswer(i)
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
			for idx > 0 && m.hiddenRow(idx) {
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
	if start < m.vp.YOffset() {
		m.vp.SetYOffset(start)
	} else if start >= m.vp.YOffset()+m.vp.Height() {
		m.vp.SetYOffset(start - m.vp.Height() + 1)
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

// invalidateRender drops every render cache: per-event bodies, whole rendered
// blocks, and memoized hidden-row folds. Called whenever a global rendering
// input changes (width, theme, auto-expand pref, picker/wizard state, or the
// event log being swapped out) so every row re-renders under the new inputs.
func (m *model) invalidateRender() {
	m.bodyCache = map[int]string{}
	m.blockCache = map[int]string{}
	m.hiddenCache = map[int]bool{}
}

// invalidateRow drops the cached block + hidden-fold state for event index i,
// forcing that single row to re-render on the next rebuild.
func (m *model) invalidateRow(i int) {
	delete(m.blockCache, i)
	delete(m.hiddenCache, i)
}

// invalidateNeighbors drops the cached blocks of the rendered rows immediately
// before and after index i. Used when row i's visibility flips (e.g. an
// ask_user tool_call folding away once its question_asked arrives): the
// neighbors' run-boundary rendering (actor name spelled vs glyph-only,
// └─ vs ├─ connectors) depends on which rows around them are visible.
func (m *model) invalidateNeighbors(i int) {
	for j := i - 1; j >= 0; j-- {
		if !m.hiddenRow(j) {
			m.invalidateRow(j)
			break
		}
	}
	for j := i + 1; j < len(m.evs); j++ {
		if !m.hiddenRow(j) {
			m.invalidateRow(j)
			break
		}
	}
}

// rebuild re-renders the whole event stream into the viewport, tracking the line
// offset of each event for click mapping.
func (m *model) rebuild() {
	if !m.ready {
		return
	}
	if m.blockCache == nil {
		m.blockCache = map[int]string{}
	}
	var b strings.Builder
	m.eventStart = m.eventStart[:0]
	line := 0
	for i, ev := range m.evs {
		m.eventStart = append(m.eventStart, line)
		// A hidden row (a tool_result folded into its preceding tool_call, or an
		// empty tool-calls-only model_turn) shares the previous rendered row's
		// start line and emits no block of its own.
		if m.hiddenRow(i) {
			if i > 0 {
				m.eventStart[i] = m.eventStart[i-1]
			}
			continue
		}
		// A row rendered in its selected state (either directly or because the
		// tool_result merged into it is selected) is drawn fresh and never cached:
		// selection moves constantly, and skipping the store means the previously
		// selected row simply re-renders once after the cursor leaves it.
		sel := i == m.selected || (i+1 == m.selected && m.mergedResultIdx(i) == i+1)
		block, ok := m.blockCache[i]
		if !ok || sel {
			block = m.renderBlock(i, ev)
			if !sel {
				m.blockCache[i] = block
			}
		}
		b.WriteString(block)
		b.WriteByte('\n')
		line += strings.Count(block, "\n") + 1
	}
	// Append the in-progress streamed tail rows (transient turn_delta output)
	// after the persisted conversation. They are ephemeral and carry no seq, so
	// they are NOT added to m.eventStart / selection tracking (task 0129).
	if tail := m.renderLiveTails(); tail != "" {
		b.WriteString(tail)
		b.WriteByte('\n')
	}
	m.vpContent = b.String()
	m.vp.SetContent(m.vpContent)
	if m.follow {
		m.vp.GotoBottom()
	}
}

// liveTailMaxLines caps how many trailing lines of an in-progress streamed turn
// the live tail row shows, so a long streaming turn can't dominate the viewport.
const liveTailMaxLines = 6

// renderLiveTails renders the in-progress streamed output (fed by transient
// turn_delta snapshots, task 0129) as dim, visibly in-progress tail rows appended
// after the persisted conversation, followed by any per-actor retry notes (fed
// by transient retry events — an API-failure backoff in progress, spec §7.2).
// Values are the full accumulated turn text so far, so each render just reflects
// the latest snapshot; the durable model_turn replaces the tail seamlessly
// (appendEvent clears the actor's entry when it arrives). Returns "" when
// nothing is streaming or retrying.
func (m *model) renderLiveTails() string {
	if len(m.liveTails) == 0 && len(m.retryNotes) == 0 {
		return ""
	}
	actors := make([]string, 0, len(m.liveTails))
	for a := range m.liveTails {
		actors = append(actors, a)
	}
	sort.Strings(actors)
	var b strings.Builder
	for _, actor := range actors {
		text := strings.TrimRight(m.liveTails[actor], "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		w := m.w - lipgloss.Width(bodyBar)
		if w < 1 {
			w = 1
		}
		lines := strings.Split(wrapTo(text, w), "\n")
		if len(lines) > liveTailMaxLines {
			lines = lines[len(lines)-liveTailMaxLines:]
		}
		body := indentLines(styleLines(strings.Join(lines, "\n"), dimStyle), bodyBar)
		header := "  " + dimStyle.Render("▸ ") +
			actorStyle(actor).Render(fmt.Sprintf("%-13s", actor)) +
			dimStyle.Render("streaming…")
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(header)
		b.WriteByte('\n')
		b.WriteString(body)
	}
	// Retry notes render after the streamed tails: one warn-styled line per
	// actor waiting out an API-failure backoff.
	noteActors := make([]string, 0, len(m.retryNotes))
	for a := range m.retryNotes {
		noteActors = append(noteActors, a)
	}
	sort.Strings(noteActors)
	for _, actor := range noteActors {
		note := m.retryNotes[actor]
		if strings.TrimSpace(note) == "" {
			continue
		}
		line := "  " + warnStyle.Render("⟳ ") +
			actorStyle(actor).Render(fmt.Sprintf("%-13s", actor)) +
			warnStyle.Render(note)
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

// --- shared screen chrome (task 0061) ---
//
// Every screen draws the same shape: a standardized title/breadcrumb bar at the
// top, a content region, and a consistent, width-clamped key-hint footer. Modal
// overlays additionally wrap their content in a bordered, centered card so they
// read as floating modals rather than full-screen replacements.

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

// pickerScreenView renders the project picker (spec §3.1).
func (m model) pickerScreenView() string {
	var b strings.Builder
	b.WriteString(m.titleBar(" ycc — projects ") + "\n\n")
	if m.flashErr != "" {
		b.WriteString("  " + errStyle.Render("✗ "+m.flashErr) + "\n\n")
	}
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
	b.WriteString("\n" + m.footerBar("  ↑/↓ choose · enter open · a add current dir · q quit"))
	b.WriteString("\n" + m.footerBar("  cwd: "+m.workspace))
	return b.String()
}

func (m model) menuView() string {
	var b strings.Builder
	b.WriteString(m.titleBar(" ycc — home ") + "\n")
	b.WriteString(m.menuHeader() + "\n\n")
	if m.quitArmed {
		b.WriteString("  " + errStyle.Render("⚠ "+quitGuardHint) + "\n\n")
	}
	if m.flashErr != "" {
		b.WriteString("  " + errStyle.Render("✗ "+m.flashErr) + "\n\n")
	}
	if n := m.blockedTaskCount(); n > 0 {
		noun := "task"
		if n > 1 {
			noun = "tasks"
		}
		b.WriteString("  " + warnStyle.Render(fmt.Sprintf("⚠ %d %s blocked — waiting on you", n, noun)) +
			dimStyle.Render(" · press ctrl+w to view") + "\n\n")
	}
	if n := len(m.waitingSessions); n > 0 {
		b.WriteString("  " + warnStyle.Render(waitingSessionsLine(m.waitingSessions)) +
			dimStyle.Render(" · press ctrl+s to open") + "\n\n")
	}
	if len(m.entries) == 0 {
		b.WriteString("  loading modes…\n")
	}
	for i, e := range m.entries {
		cursor := "  "
		// Surface the loop toggle on the work entry (tab toggles it).
		lbl, desc := e.label, e.description
		if m.loop && isWorkEntry(e) {
			lbl = e.label + " (loop)"
			desc = "Chew through every ready backlog task unattended (autonomous) — stuck tasks are marked blocked and skipped."
		}
		label := fmt.Sprintf("%-9s %s", lbl, dimStyle.Render(desc))
		switch {
		case i == m.cursor && e.prominent:
			// Selected AND recommended: keep the selection treatment but still
			// surface the ★ marker and "(recommended)" hint so onboarding reads
			// as recommended even when it's the default-selected row.
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render("★ "+fmt.Sprintf("%-7s ", e.label)) + dimStyle.Render(e.description+"  (recommended)")
		case i == m.cursor:
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render(fmt.Sprintf("%-9s ", lbl)) + dimStyle.Render(desc)
		case e.prominent:
			// Surface a recommended entry (e.g. onboarding on an un-onboarded
			// workspace) so it stands out without stealing the cursor highlight.
			label = recoStyle.Render("★ "+fmt.Sprintf("%-7s ", e.label)) + dimStyle.Render(e.description+"  (recommended)")
		}
		b.WriteString("  " + cursor + label + "\n")
	}
	b.WriteString("\n  " + dimStyle.Render("level ") + typeStyle.Render("‹"+m.menuLevel+"›") + dimStyle.Render("  ←/→") + "\n")
	b.WriteString(framedInput(m.prompt, 2) + "\n")
	// One-key affordance to reopen the most recent session (task 0139): resume the
	// last conversation instead of ctrl+r → pick → o.
	if m.lastSession != nil {
		b.WriteString("  " + typeStyle.Render("ctrl+l") + dimStyle.Render(" continue last session · "+lastSessionLabel(m.lastSession)) + "\n")
	}
	// Keep the footer to the essentials — the full keybinding catalog lives in
	// the help modal (?), and the conditional affordances (ctrl+w blocked tasks,
	// ctrl+s waiting session, ctrl+l continue last) are advertised by their own
	// body lines above, so they aren't repeated here.
	footer := "  ? help · ↑/↓ choose mode · enter start · esc settings"
	b.WriteString("\n" + m.footerBar(footer))
	return b.String()
}

// lastSessionLabel renders the compact descriptor for the "ctrl+l continue last
// session" affordance (task 0139): the session's title (or short id when it has
// none) plus its mode.
func lastSessionLabel(s *v1.SessionSummary) string {
	label := strings.TrimSpace(s.Title)
	if label == "" {
		label = short(s.SessionId)
	}
	if s.Mode != "" {
		label += " (" + s.Mode + ")"
	}
	return label
}

// waitingSessionsLine builds the home-menu awareness line for live sessions that
// need the user (task 0107). A single session waiting on an unanswered question
// gets the pointed "waiting for your answer"; a paused session (or a mix) reads
// "waiting for you". For several sessions the line invites a pick.
func waitingSessionsLine(ws []*v1.SessionSummary) string {
	n := len(ws)
	if n == 1 {
		if ws[0].WaitingInput {
			return "⚠ 1 session waiting for your answer"
		}
		return "⚠ 1 session waiting for you"
	}
	return fmt.Sprintf("⚠ %d sessions waiting for you", n)
}

// historyView renders the session browser (spec §18.6): a navigable list of
// persisted + live sessions, most-recent first, that can be inspected (read-only
// transcript) or reopened. When a transcript is open it renders that instead.
func (m model) historyView() string {
	if m.historyTranscript {
		return m.transcriptView()
	}
	emptyMsg := m.historyMsgTxt
	if emptyMsg == "" {
		emptyMsg = "no previous sessions"
	}
	b := browser{
		title:  " ycc — sessions ",
		cursor: m.historyCursor,
		hint:   "↑/↓ choose · enter transcript · o reopen · r refresh · esc/q back",
		empty:  emptyMsg,
	}
	if m.historyWaitingOnly {
		b.title = " ycc — sessions waiting for you "
		if emptyMsg == "no previous sessions" {
			b.empty = "(no sessions waiting)"
		}
	}
	b.rows = m.historyRows()
	return m.browserCard(b)
}

// historyRows builds the session-browser list rows shared by the full-state
// session browser (historyView) and the read-only modal variant (histModalView),
// keeping the row format identical between them (task 0112).
func (m model) historyRows() []browserRow {
	// Clamp the title column so a row stays on a single physical line.
	tw := 48
	if m.w > 0 && m.w-4 < tw {
		tw = m.w - 4
	}
	if tw < 1 {
		tw = 1
	}
	var rows []browserRow
	for _, s := range m.history {
		// Prefer a derived title; fall back to the short id. Sessions that worked
		// backlog tasks are prefixed with those task ids so the list shows at a
		// glance which task each session was on.
		title := strings.TrimSpace(s.Title)
		if title == "" {
			title = short(s.SessionId)
		}
		if len(s.FocusTasks) > 0 {
			title = "[" + strings.Join(s.FocusTasks, ",") + "] " + title
		}
		meta := s.Mode + " · " + s.Status
		if s.Live {
			meta += " · live"
		}
		if when := historyWhen(s); when != "" {
			meta += " · " + when
		}
		rows = append(rows, browserRow{
			text:   fmt.Sprintf("%-*s", tw, trunc(title, tw)),
			suffix: "  " + dimStyle.Render(meta),
		})
	}
	return rows
}

// histModalView renders the read-only session browser modal shown over a live
// session (task 0112). When a transcript is drilled into it shows that instead.
// Unlike historyView it advertises no `o reopen` — browsing from a live session
// is strictly read-only.
func (m model) histModalView() string {
	if m.histModalTranscript {
		title := short(m.histModalID)
		if m.historyCursor < len(m.history) {
			if t := strings.TrimSpace(m.history[m.historyCursor].Title); t != "" {
				title = t
			}
		}
		top := m.titleBar(" ycc — transcript · " + title + " ")
		body := ""
		if m.ready {
			body = m.histModalVP.View()
		}
		var help string
		switch {
		case m.histModalSearching:
			help = m.histModalSearchBar()
		case m.histModalQuery != "":
			total, cur := m.histSearchCount()
			help = m.footerBar(fmt.Sprintf(" ⌕ %q %d/%d · n/N next/prev · esc clear · esc/q back", m.histModalQuery, cur, total))
		default:
			help = m.footerBar(" ↑↓/pgup/pgdn scroll · / search · {}()<>[] jump · <> commit · enter diff · esc/q back · read-only")
		}
		return top + "\n" + body + "\n" + help
	}
	emptyMsg := m.historyMsgTxt
	if emptyMsg == "" {
		emptyMsg = "no previous sessions"
	}
	b := browser{
		title:  " ycc — sessions ",
		cursor: m.historyCursor,
		hint:   "↑/↓ choose · enter transcript · r refresh · esc/q back",
		empty:  emptyMsg,
	}
	b.rows = m.historyRows()
	return m.browserCard(b)
}

// transcriptView renders the read-only replayed transcript of a session (spec
// §18.6): the same scrollable event viewport as the live session view, but with
// no input box and read-only.
func (m model) transcriptView() string {
	title := short(m.historyTransID)
	if m.historyCursor < len(m.history) {
		if t := strings.TrimSpace(m.history[m.historyCursor].Title); t != "" {
			title = t
		}
	}
	top := m.titleBar(" ycc — transcript · " + title + " ")
	body := ""
	if m.ready {
		body = m.overlaySelection(m.vp.View())
	}
	var help string
	switch {
	case m.searching:
		help = m.searchBar()
	case m.searchQuery != "":
		total, cur := m.searchCount()
		help = m.footerBar(fmt.Sprintf(" ⌕ %q %d/%d · n/N next/prev · esc clear · o reopen · esc/q back", m.searchQuery, cur, total))
	default:
		help = m.footerBar(" ↑↓/pgup/pgdn scroll · / search · {}()<>[] jump · <> commit · enter diff/reopen · o reopen · esc/q back")
	}
	return top + "\n" + body + "\n" + help
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

// specIsEmpty reports whether the configured spec entry point is missing or
// trivially empty (only blank lines and markdown headings, no real content).
// The entry point is resolved via the workspace's .ycc/config.toml (spec_path),
// falling back to <workspace>/spec.md when unconfigured.
func specIsEmpty(workspace string) bool {
	data, err := os.ReadFile(docs.NewStore(workspace).SpecPath())
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
// file matching the NNNN-*.md pattern (stray non-task .md files don't count).
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

// statusBar renders the single-row session status line as a set of colored,
// glyph-prefixed segments — an activity spinner / state dot, a mode pill, the
// interaction level, the coordinator's thinking level, the elapsed clock, a live
// token/cost readout (task 0062), and the location/id — joined by dim chevrons.
//
// It is ALWAYS exactly one physical row. Each segment carries a priority (lower =
// keep longer); when the joined bar would exceed the terminal width we drop the
// lowest-priority segments first, then apply an ANSI-aware truncation as a final
// clamp. This preserves the fixed-height frame (a wrap here corrupts Bubble Tea's
// line accounting; see TestSessionViewFitsTerminal) while degrading gracefully on
// narrow terminals.
func (m model) statusBar() string {
	type seg struct {
		text string
		prio int // lower = more important (dropped last)
	}
	var segs []seg

	// The quit guard warning rides at the very highest priority so it's never
	// dropped — the user pressed ctrl+c and needs to see why nothing quit (task 0109).
	if m.quitArmed {
		segs = append(segs, seg{errStyle.Render("⚠ " + quitGuardHint), -2})
	}
	// A transient inline error (failed RPC on an otherwise-live session) rides at
	// the highest priority so the width-greedy fitter never drops it (task 0104).
	if m.flashErr != "" {
		segs = append(segs, seg{errStyle.Render("✗ " + m.flashErr), -1})
	}
	// A transient inline notice (e.g. "copied ✓" after a yank) rides at the same
	// high priority so it's never dropped (task 0141).
	if m.flashNote != "" {
		segs = append(segs, seg{successStyle.Render(m.flashNote), -1})
	}
	// status: a state-colored dot. The header always shows the static dot; the
	// activity spinner now lives next to the input box at the bottom of the
	// session view (see inputRow / task 0076). The static dot covers
	// idle/paused/error so a stale error never animates (task 0051).
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
	glyph := dot.Render("●")
	segs = append(segs, seg{glyph + " " + typeStyle.Render(m.status), 0})

	if m.mode != "" {
		segs = append(segs, seg{dimStyle.Render("mode ") + typeStyle.Render(m.mode), 1})
	}
	// The backlog task currently in focus (task_focus): which task the work
	// agent is on right now. The title tags along, truncated, when present.
	if m.focusTask != "" {
		label := m.focusTask
		if m.focusTaskTitle != "" {
			label += " " + trunc(m.focusTaskTitle, 32)
		}
		segs = append(segs, seg{dimStyle.Render("task ") + typeStyle.Render(label), 1})
	}
	// Surface that an unattended loop run is driving this session (tab on the work
	// entry); kept high-priority so the user always sees they're in a loop.
	if m.looping {
		segs = append(segs, seg{recoStyle.Render("⟳ loop"), 1})
	}
	// Spend guard (task 0137, spec §20.6): a visually distinct, high-priority
	// segment once the session crosses ~80% (warn) or the cap (err). Kept above
	// the normal Σ readout so a budget breach is unmistakable.
	if m.budgetExceeded {
		segs = append(segs, seg{errStyle.Render("⚠ budget reached"), 1})
	} else if m.budgetPct > 0 {
		segs = append(segs, seg{recoStyle.Render(fmt.Sprintf("⚠ budget %d%%", int(m.budgetPct*100))), 1})
	}
	// live token/cost readout — the headline new datum, kept at high priority.
	if tokens, cost, st := m.sessionUsage(); tokens > 0 {
		readout := dimStyle.Render("Σ ") + typeStyle.Render(fmtTokens(tokens))
		switch st {
		case "priced":
			readout += " " + successStyle.Render(fmt.Sprintf("$%.4f", cost))
		case "partial":
			readout += " " + recoStyle.Render(fmt.Sprintf("$%.4f*", cost))
		}
		segs = append(segs, seg{readout, 2})
	}
	if m.level != "" {
		segs = append(segs, seg{dimStyle.Render("lvl ") + typeStyle.Render(m.level), 3})
	}
	if lvl := m.thinkLevels["coordinator"]; lvl != "" {
		segs = append(segs, seg{pathStyle.Render("◆") + " " + dimStyle.Render(lvl), 4})
	}
	if !m.sessionStart.IsZero() {
		segs = append(segs, seg{dimStyle.Render("⏱ " + fmtElapsed(time.Since(m.sessionStart))), 5})
	}
	if loc := m.locationLabel(); loc != "" {
		segs = append(segs, seg{dimStyle.Render(loc), 6})
	}
	if m.sessionID != "" {
		segs = append(segs, seg{dimStyle.Render(short(m.sessionID)), 7})
	}

	prefix := ""
	if m.pending != "" {
		prefix = askStyle.Render(" ? answer below ")
	}
	sep := dimStyle.Render(" › ")
	// render joins the chosen segments (in their original visual order) into the bar.
	render := func(chosen []seg) string {
		parts := make([]string, len(chosen))
		for i, s := range chosen {
			parts[i] = s.text
		}
		return prefix + " " + strings.Join(parts, sep) + " "
	}

	// Greedily include segments by priority while the rendered bar fits the width,
	// then emit the kept segments in visual order. A zero width (before the first
	// WindowSizeMsg) keeps everything.
	if m.w > 0 {
		order := make([]int, len(segs))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return segs[order[a]].prio < segs[order[b]].prio })
		keep := make([]bool, len(segs))
		for _, idx := range order {
			keep[idx] = true
			chosen := chosenSegs(segs, keep)
			if lipgloss.Width(render(chosen)) > m.w {
				keep[idx] = false // this segment doesn't fit; skip it (lower-priority ones may still fit)
			}
		}
		bar := render(chosenSegs(segs, keep))
		return ansi.Truncate(bar, m.w, dimStyle.Render("…"))
	}
	all := make([]seg, len(segs))
	copy(all, segs)
	return render(all)
}

// chosenSegs returns the segments flagged keep[i], preserving visual order. A tiny
// helper kept out of statusBar so the drop loop reads cleanly.
func chosenSegs[T any](segs []T, keep []bool) []T {
	out := make([]T, 0, len(segs))
	for i, s := range segs {
		if keep[i] {
			out = append(out, s)
		}
	}
	return out
}

// fitSeg is one width-fit segment: pre-styled text plus a drop priority (lower =
// kept first, dropped last). Used by the home-menu context header (task 0139).
type fitSeg struct {
	text string
	prio int
}

// fitSegmentStrip greedily fits priority-ordered segments into width w, joining
// the kept ones (in original visual order) with sep. It mirrors the status bar's
// priority-fit approach (task 0139): a zero/negative width keeps everything, and
// the result is ANSI-truncated to w as a final clamp so the strip never spills
// past one physical row on a narrow terminal.
func fitSegmentStrip(segs []fitSeg, sep string, w int) string {
	render := func(chosen []fitSeg) string {
		parts := make([]string, len(chosen))
		for i, s := range chosen {
			parts[i] = s.text
		}
		return strings.Join(parts, sep)
	}
	if w <= 0 {
		return render(segs)
	}
	order := make([]int, len(segs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return segs[order[a]].prio < segs[order[b]].prio })
	keep := make([]bool, len(segs))
	for _, idx := range order {
		keep[idx] = true
		if lipgloss.Width(render(chosenSegs(segs, keep))) > w {
			keep[idx] = false // doesn't fit; lower-priority segments may still fit
		}
	}
	return ansi.Truncate(render(chosenSegs(segs, keep)), w, dimStyle.Render("…"))
}

// menuReadyCount reports how many backlog tasks a work session could pick up
// right now: ready and either todo or a resumable in_progress (task 0139).
func (m model) menuReadyCount() int {
	n := 0
	for _, t := range m.backlogTasks {
		if t.Ready && (t.Status == "todo" || t.Status == "in_progress") {
			n++
		}
	}
	return n
}

// menuHeader builds the one-line project-context header for the home menu (task
// 0139): project · git branch (+dirty marker) · N ready / M blocked · $ today.
// Each segment drops out when its data is unavailable (non-git workspace, empty
// backlog, no priced usage), and the strip is width-fit to exactly one physical
// row like the session status bar so it never corrupts the frame.
func (m model) menuHeader() string {
	var segs []fitSeg
	// project name — highest priority, always present.
	name := filepath.Base(m.workspace)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = m.workspace
	}
	segs = append(segs, fitSeg{typeStyle.Render(name), 0})
	// git branch + dirty marker (dropped when the workspace isn't a git repo).
	if m.gitBranch != "" {
		git := dimStyle.Render("⎇ ") + typeStyle.Render(m.gitBranch)
		if m.gitDirty {
			git += recoStyle.Render("*")
		}
		segs = append(segs, fitSeg{git, 2})
	}
	// backlog readiness (dropped when there are no backlog tasks at all).
	if len(m.backlogTasks) > 0 {
		ready := m.menuReadyCount()
		blocked := m.blockedTaskCount()
		line := typeStyle.Render(fmt.Sprintf("%d", ready)) + dimStyle.Render(" ready")
		if blocked > 0 {
			line += dimStyle.Render(" / ") + warnStyle.Render(fmt.Sprintf("%d blocked", blocked))
		}
		segs = append(segs, fitSeg{line, 1})
	}
	// today's spend (dropped until a priced fetch reports a positive cost).
	if m.todaySpendLoaded && m.todaySpend > 0 {
		var spend string
		if m.todaySpendStatus == "partial" {
			spend = recoStyle.Render(fmt.Sprintf("$%.2f* today", m.todaySpend))
		} else {
			spend = successStyle.Render(fmt.Sprintf("$%.2f today", m.todaySpend))
		}
		segs = append(segs, fitSeg{spend, 3})
	}
	return "  " + fitSegmentStrip(segs, dimStyle.Render(" · "), m.w-2)
}

// fmtTokens renders a token count compactly: "842", "12.3k", "1.2M".
func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return strconv.Itoa(n)
	}
}

// fmtElapsed renders a session/turn duration as mm:ss, or h:mm:ss past an hour.
func fmtElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	h := total / 3600
	mn := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, mn, s)
	}
	return fmt.Sprintf("%02d:%02d", mn, s)
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
		body = m.overlaySelection(m.vp.View())
	}
	// While `/` search is being typed, a single-row search bar replaces the whole
	// footer stack (input/picker/wizard) and help line (task 0116). footerStackHeight
	// returns 0 so relayout leaves exactly one row for it below the viewport.
	if m.searching {
		return top + "\n" + body + "\n" + m.searchBar()
	}
	// A confirmed (non-entry) query leads the help line with a live match counter
	// and the n/N · esc-clear hint.
	searchHint := ""
	if m.searchQuery != "" {
		total, cur := m.searchCount()
		searchHint = fmt.Sprintf(" ⌕ %q %d/%d · n/N next/prev · esc clear ·", m.searchQuery, cur, total)
	}
	if m.wizActive {
		overview := m.wizardView()
		if m.picking {
			help := m.footer(" ↑↓/1–9 choose · enter select · ‹other…› to type · pgup/pgdn scroll · ctrl+b backlog · esc settings")
			return top + "\n" + body + "\n" + overview + "\n" + m.pickerView() + "\n" + help
		}
		help := m.footer(" type your answer + enter · esc settings")
		return top + "\n" + body + "\n" + overview + "\n" + m.inputRow() + "\n" + help
	}
	if m.picking {
		help := m.footer(" ↑↓/1–9 choose · enter select · pgup/pgdn scroll · ctrl+b backlog · esc settings")
		return top + "\n" + body + "\n" + m.pickerView() + "\n" + help
	}
	if m.paused {
		help := m.footer(" ⏸ paused — type a correction + enter to steer · enter to resume · esc settings")
		return top + "\n" + body + "\n" + m.inputRow() + "\n" + help
	}
	help := m.footer(searchHint + " ? help · enter send/expand · shift+enter newline · ↑↓ select · click expand · drag copy · pgup/pgdn scroll · " + m.interruptKeyHint() + " interrupt · esc settings · ctrl+b backlog · ctrl+o browse · ctrl+n new task")
	if m.mode == "work" {
		// Surface the loop toggle on work sessions: shift+tab halts a running loop
		// gracefully (current task finishes) or rolls a single session into a loop.
		if m.looping {
			help = m.footer(searchHint + " ? help · shift+tab halt loop · enter send/expand · ↑↓ select · pgup/pgdn scroll · " + m.interruptKeyHint() + " interrupt · esc settings")
		} else {
			help = m.footer(searchHint + " ? help · shift+tab loop · enter send/expand · ↑↓ select · pgup/pgdn scroll · " + m.interruptKeyHint() + " interrupt · esc settings")
		}
	}
	if m.sessionFinished() {
		// A finished (idle / stream-closed), non-looping session leads the footer with
		// a clean way back to the menu (task 0127). This takes precedence over the
		// work-mode loop-toggle hints above.
		help = m.footer(searchHint + " ✔ session finished — q return to menu · ? help · enter expand · ↑↓ select · pgup/pgdn scroll · esc settings")
	}
	return top + "\n" + body + "\n" + m.inputRow() + "\n" + help
}

// interruptKeyHint returns the interrupt chord to advertise in the footer. ctrl+i
// is byte-identical to Tab (0x09), so it only reaches the runtime on terminals
// that report kitty keyboard-protocol disambiguation; everywhere else we show
// ctrl+x, the universal fallback that is always delivered as a distinct byte.
func (m model) interruptKeyHint() string {
	if m.keyEnhanced {
		return "ctrl+i"
	}
	return "ctrl+x"
}

// footer renders a single-row help/status line, clamped to the terminal width so
// it can never wrap to a second physical row. Without this clamp a long help line
// wraps, overflowing the H-row frame and corrupting Bubble Tea's line accounting —
// which visually shows up as the input box overlapping the agent's last output
// line. A zero width (before the first WindowSizeMsg) is a no-op.
//
// It is the session view's footer; it delegates to footerBar so the clamp is
// byte-identical to the one shared by every other screen.
func (m model) footer(text string) string {
	return m.footerBar(text)
}

// searchBar renders the one-row transcript search-entry line shown in place of
// the input/footer while `/` search is being typed (task 0116). It is width-
// clamped like the footer so it can never wrap to a second physical row.
func (m model) searchBar() string {
	total, cur := m.searchCount()
	counter := dimStyle.Render("no matches")
	if total > 0 {
		counter = fmt.Sprintf("%d/%d", cur, total)
	}
	return m.footerBar(" ⌕ " + m.searchQuery + "▌ · " + counter + dimStyle.Render(" · enter keep · esc cancel"))
}

// histModalSearchBar renders the modal session-browser transcript's search-entry
// line while `/` search is being typed (task 0119) — the line-based analogue of
// searchBar, counting matching content lines rather than events.
func (m model) histModalSearchBar() string {
	total, cur := m.histSearchCount()
	counter := dimStyle.Render("no matches")
	if total > 0 {
		counter = fmt.Sprintf("%d/%d", cur, total)
	}
	return m.footerBar(" ⌕ " + m.histModalQuery + "▌ · " + counter + dimStyle.Render(" · enter keep · esc cancel"))
}

// questionPrompt renders the shared interactive-question badge used by the main
// agents (the askStyle " ? " badge followed by the prompt), word-wrapped to
// width w. Continuation lines are hanging-indented to align under the first
// line's text (the badge occupies 5 visible columns). Used by the capture
// overlay modal and the session picker footer.
func questionPrompt(prompt string, w int) string {
	const badgeW = 5 // " " + " ? " + " "
	badge := " " + askStyle.Render(" ? ") + " "
	if w < badgeW+1 {
		w = badgeW + 15
	}
	lines := strings.Split(wrapTo(prompt, w-badgeW), "\n")
	indent := strings.Repeat(" ", badgeW)
	for i := range lines {
		if i == 0 {
			lines[i] = badge + lines[i]
		} else {
			lines[i] = indent + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// pickerView renders the navigable list of suggested answers plus an "other…"
// escape into the free-text textarea.
func (m model) pickerView() string {
	var b strings.Builder
	if m.pending != "" {
		b.WriteString(questionPrompt(m.pending, m.w) + "\n")
	}
	rows := append(append([]string(nil), m.pickerOpts...), "other… (type your own)")
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
		// Word-wrap the question so a prompt longer than the terminal width folds
		// onto multiple lines; continuation lines hang-indent to align under the
		// prompt text (after the "  ▸ N. " prefix).
		prompt := q.prompt
		promptLines := []string{prompt}
		if m.w > 0 {
			promptLines = strings.Split(wrapTo(prompt, m.w-len(num)-4), "\n")
		}
		indent := strings.Repeat(" ", len(num)+4)
		for j, pl := range promptLines {
			if j == 0 {
				line := num + pl
				if i == m.wizIdx {
					line = selStyle.Render(line)
				}
				b.WriteString("  " + marker + line + "\n")
			} else {
				if i == m.wizIdx {
					pl = selStyle.Render(pl)
				}
				b.WriteString(indent + pl + "\n")
			}
		}
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
	exp := m.eventExpanded(int(ev.Seq), ev.Type)
	header := m.renderHeader(i, ev, i == m.selected, exp && hasBody, hasBody, first)
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
	for j >= 0 && m.hiddenRow(j) {
		j--
	}
	if j < 0 {
		return true
	}
	return m.evs[j].Actor != m.evs[i].Actor
}

// lastOfSubRun reports whether event i is the last row of a contiguous run of
// sub-agent (implementer/reviewer) rows: true when the next *rendered* row
// (skipping tool_results folded into their call, mirroring firstOfRun) is not a
// sub-agent actor, or there is none. Drives the └─ vs ├─ tree connector.
func (m *model) lastOfSubRun(i int) bool {
	j := i + 1
	for j < len(m.evs) && m.hiddenRow(j) {
		j++
	}
	if j >= len(m.evs) {
		return true
	}
	return !isSub(m.evs[j].Actor)
}

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

func (m *model) renderHeader(i int, ev *v1.Event, selected, expanded, hasBody, first bool) string {
	detail := m.detailLineFor(ev)
	if expanded && hasBody {
		// The body box already shows the full content, so the header's one-line
		// snippet would be a redundant echo — drop it for prose rows (keeping only
		// non-body metadata like a turn's elapsed time).
		detail = expandedDetailLine(ev)
	}
	return m.renderHeaderDetail(i, ev, selected, expanded, hasBody, detail, first)
}

// expandedDetailLine is the header detail used when a row is expanded. For prose
// rows whose full text is rendered in the body box, the collapsed snippet is
// redundant, so it's suppressed (a model_turn keeps just its elapsed-time suffix,
// which isn't echoed in the body). Non-prose rows keep their normal summary.
func expandedDetailLine(ev *v1.Event) string {
	switch ev.Type {
	case "model_turn":
		if ms := durationMSField(ev); ms > 0 {
			return dimStyle.Render(fmtDurMS(ms))
		}
		return ""
	case "user_input", "session_idle", "question_asked", "question_answered", "thinking":
		return ""
	}
	return detailLine(ev)
}

func (m *model) renderHeaderDetail(i int, ev *v1.Event, selected, expanded, hasBody bool, detail string, first bool) string {
	bar := "  "
	if selected {
		bar = selBarStyle.Render("▌ ")
	}
	// Sub-agent (implementer/reviewer) rows nest under the coordinator via a tree
	// connector (└─ on the last row of the run, ├─ otherwise) instead of a bare
	// two-space indent, so the nesting reads at a glance.
	indent := ""
	if isSub(ev.Actor) {
		indent = subConnector(m.lastOfSubRun(i))
	}
	tri := "  "
	if hasBody {
		if expanded {
			tri = "▼ "
		} else {
			tri = "▸ "
		}
	}
	// Per-type leading glyph: a fixed 2-cell colored column (glyph + space) placed
	// after the actor column, for fast scanning. Subtract its width from avail so
	// detail truncation stays correct.
	glyph := typeGlyph(ev.Type)
	glyphCol := ""
	if glyph != "" {
		gs := typeGlyphStyle(ev.Type)
		if ev.Type == "review_submitted" {
			gs = verdictStyle(dataField(ev, "verdict"))
		}
		glyphCol = gs.Render(glyph) + " "
	}
	avail := m.w - lipgloss.Width(indent) - 21 - lipgloss.Width(glyphCol)
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
	return fmt.Sprintf("%s%s%s%s %s%s",
		bar, indent, dimStyle.Render(tri),
		m.actorColumn(ev.Actor, first),
		glyphCol,
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

// precedingTurnText returns the text of the last coordinator model_turn before
// the event at seq (the model's final assistant output), or "" if none. Used to
// detect when a session_idle report merely echoes that final turn.
func (m *model) precedingTurnText(seq int64) string {
	last := ""
	for _, ev := range m.evs {
		if ev.Seq >= seq {
			break
		}
		if ev.Type == "model_turn" {
			if t := firstField(ev, "text"); strings.TrimSpace(t) != "" {
				last = t
			}
		}
	}
	return last
}

// dropDuplicatePrefix removes a leading occurrence of prev from s (comparing
// trimmed), returning the trimmed remainder; if s doesn't begin with prev it is
// returned unchanged. Lets an idle report show only what it adds beyond the
// already-rendered final assistant turn.
func dropDuplicatePrefix(s, prev string) string {
	ts, tp := strings.TrimSpace(s), strings.TrimSpace(prev)
	if tp == "" {
		return s
	}
	if ts == tp {
		return ""
	}
	if strings.HasPrefix(ts, tp) {
		return strings.TrimSpace(ts[len(tp):])
	}
	return s
}

// questionBody renders a question_asked event as the canonical block for the
// whole ask_user exchange (the tool plumbing rows and the question_answered
// row are hidden — see isAskUserPlumbing / isFoldedAnswer). While the footer
// picker/wizard is collecting the answer it collapses to a pointer (the footer
// already shows the prompt); once the paired question_answered event exists the
// answer folds in beneath the question; and an autonomous auto-answer compacts
// to one dim line instead of the canned "no human available" paragraph.
func (m *model) questionBody(ev *v1.Event) string {
	ansEv := m.answerEventFor(ev)
	if qs := dataQuestions(ev); len(qs) > 0 {
		return m.batchQuestionBody(ev, qs, ansEv)
	}
	txt := firstField(ev, "question")
	if txt == "" {
		return ""
	}
	// While the single-question picker below echoes this prompt, point at it
	// instead of repeating it (mirrors the wizard's condensed form).
	if ansEv == nil && m.picking && ev.Seq == m.pendingSeq {
		return indentLines(dimStyle.Render("answer below ↓"), "  ")
	}
	body := strings.TrimRight(m.markdown(txt), "\n")
	if ansEv != nil {
		// The extra two-space indent aligns the answer under the question text,
		// which carries glamour's own left margin.
		if dataField(ansEv, "auto") == "true" {
			body += "\n" + autoAnswerLine("  ")
		} else {
			body += "\n" + answerLines(dataField(ansEv, "answer"), m.bodyWrapWidth(), "  ")
		}
	}
	return indentLines(body, "  ")
}

// batchQuestionBody renders a multi-question ask_user batch: each prompt with
// its suggested options while unanswered, or with its folded answer once the
// paired question_answered event exists. While the wizard is actively
// collecting this batch it collapses to a pointer at the wizard below.
func (m *model) batchQuestionBody(ev *v1.Event, qs []wizQuestion, ansEv *v1.Event) string {
	if m.wizActive && ev.Seq == m.wizSeq {
		noun := "questions"
		if len(qs) == 1 {
			noun = "question"
		}
		return indentLines(dimStyle.Render(fmt.Sprintf("%d %s — answer below ↓", len(qs), noun)), "  ")
	}
	auto := ansEv != nil && dataField(ansEv, "auto") == "true"
	var answers []string
	if ansEv != nil && !auto {
		answers = dataList(ansEv, "answers")
	}
	w := m.bodyWrapWidth()
	var b strings.Builder
	for i, q := range qs {
		b.WriteString(wrapTo(fmt.Sprintf("%d. %s", i+1, q.prompt), w) + "\n")
		switch {
		case ansEv == nil:
			// Unanswered: keep the suggested options visible. Once answered only
			// the chosen answer matters, so the options drop away.
			for _, o := range q.options {
				b.WriteString(indentLines(dimStyle.Render(wrapTo("- "+o, w-3)), "   ") + "\n")
			}
		case !auto:
			a := ""
			if i < len(answers) {
				a = answers[i]
			}
			b.WriteString(answerLines(a, w-3, "   ") + "\n")
		}
	}
	if auto {
		b.WriteString(autoAnswerLine(""))
	}
	// Four-space indent matches the left margin glamour gives markdown-rendered
	// bodies (2 from indentLines + 2 of its own), so batch and single-question
	// blocks line up.
	return indentLines(strings.TrimRight(b.String(), "\n"), "    ")
}

// bodyWrapWidth is the wrap width for hand-assembled (non-markdown) body text,
// accounting for the two-space body indent with a little slack for prefixes.
func (m *model) bodyWrapWidth() int {
	w := m.w - 8
	if w < 20 {
		w = 20
	}
	return w
}

// wrapTo wraps s to width w: word-aware first, then hard-wrapped so an unbroken
// token can never overflow the line (the same pairing the thinking body uses).
func wrapTo(s string, w int) string {
	if w < 1 {
		return s
	}
	return wrap.String(wordwrap.String(s, w), w)
}

// answerLines renders a user answer folded beneath its question: a dim "→ "
// arrow followed by the wrapped answer text, continuation lines aligned under
// the text. indent is prepended to every line.
func answerLines(a string, w int, indent string) string {
	a = strings.TrimSpace(a)
	if a == "" {
		return indent + dimStyle.Render("→ (no answer)")
	}
	lines := strings.Split(wrapTo(a, w-2), "\n")
	for i, ln := range lines {
		if i == 0 {
			lines[i] = indent + dimStyle.Render("→ ") + ln
		} else {
			lines[i] = indent + "  " + ln
		}
	}
	return strings.Join(lines, "\n")
}

// autoAnswerLine is the compact rendering of an autonomous-mode auto-answer:
// the canned "no human is available…" paragraph the agent receives adds
// nothing for the human reading the log, so one dim line carries the fact.
func autoAnswerLine(indent string) string {
	return indent + dimStyle.Render("→ auto-answered (autonomous mode): agent proceeds on its own judgement")
}

func (m *model) renderBody(ev *v1.Event) string {
	switch ev.Type {
	case "question_asked":
		return m.questionBody(ev)
	case "question_answered":
		// Normally folded into its question_asked row (isFoldedAnswer) and never
		// rendered standalone; this remains only for an orphaned answer whose
		// question isn't in the log.
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
	case "model_turn", "user_input":
		txt := firstField(ev, "text", "report", "question", "answer")
		if txt == "" {
			return ""
		}
		return indentLines(m.markdown(txt), "  ")
	case "session_idle":
		txt := firstField(ev, "report")
		if strings.TrimSpace(txt) == "" {
			return ""
		}
		// The model's final text is already shown as its own model_turn row; the
		// idle report repeats it (when the model yields plainly its report IS that
		// text, sometimes with autonomous-mode assumptions appended). Strip the
		// duplicated portion so the final output isn't printed twice — keep only
		// what the report adds, or a control-tool report that genuinely differs.
		if prev := m.precedingTurnText(ev.Seq); prev != "" {
			txt = dropDuplicatePrefix(txt, prev)
		}
		if strings.TrimSpace(txt) == "" {
			return ""
		}
		return indentLines(m.markdown(txt), "  ")
	case "thinking":
		// Render the reasoning summary dimmed so it reads as the model's
		// "inner voice", distinct from its actual response (spec §18).
		txt := dataField(ev, "text")
		if strings.TrimSpace(txt) == "" {
			return ""
		}
		if w := m.w - lipgloss.Width(bodyBar); w > 0 {
			txt = wrap.String(wordwrap.String(txt, w), w)
		}
		return indentLines(styleLines(txt, thinkStyle), bodyBar)
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
		// The verdict is colorized in a plain header line (model — VERDICT); passing
		// it through glamour would strip the ANSI, so only the summary is markdown-
		// rendered, indented below the header. Both share the "  " body indent.
		verdict := dataField(ev, "verdict")
		head := dataField(ev, "model") + " — " + verdictStyle(verdict).Render(strings.ToUpper(verdict))
		summary := strings.TrimSpace(dataField(ev, "summary"))
		body := head
		if summary != "" {
			body += "\n" + m.markdown(summary)
		}
		return indentLines(body, "  ")
	case "session_error":
		msg := dataField(ev, "msg")
		// Error messages (e.g. a backend 400 invalid_request_error with a long
		// JSON body) are often a single very long line. Wrap to the body width so
		// the text doesn't run off the right edge — wordwrap on spaces, then hard
		// wrap to break any unbroken token (URLs, JSON, etc.).
		if w := m.w - lipgloss.Width(bodyBar); w > 0 {
			msg = wrap.String(wordwrap.String(msg, w), w)
		}
		body := errStyle.Render(msg)
		// Structured classification (engine loop failures, spec §7.2): lead with
		// a compact "kind (status) · N attempts — hint" line so the user sees
		// what class of failure it was without reading the provider body.
		if head := sessionErrorHead(ev); head != "" {
			body = errStyle.Bold(true).Render(head) + "\n" + body
		}
		return indentLines(body, bodyBar)
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

// unifiedDiff computes a git-style unified diff between oldStr and newStr at the
// given amount of context lines. It performs a line-level LCS diff and groups
// changes into hunks. Original line text (including indentation/whitespace) is
// preserved verbatim after the +/-/space prefix. The output is bounded so it
// degrades gracefully on very large or pathological inputs.
func unifiedDiff(oldStr, newStr string, context int) string {
	if context < 0 {
		context = 0
	}
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	const maxLines = 400    // cap total emitted diff lines
	const lcsLineCap = 2000 // bound O(n*m) LCS cost
	var b strings.Builder
	emitted := 0
	truncated := false
	writeLine := func(s string) bool {
		if emitted >= maxLines {
			truncated = true
			return false
		}
		b.WriteString(s)
		b.WriteByte('\n')
		emitted++
		return true
	}

	// Fall back to a wholesale remove/add diff for very large inputs to avoid the
	// quadratic LCS cost.
	if len(oldLines) > lcsLineCap || len(newLines) > lcsLineCap {
		writeLine(fmt.Sprintf("@@ -1,%d +1,%d @@", len(oldLines), len(newLines)))
		for _, ln := range oldLines {
			if !writeLine("-" + ln) {
				break
			}
		}
		for _, ln := range newLines {
			if !writeLine("+" + ln) {
				break
			}
		}
		if truncated {
			b.WriteString("… (diff truncated)\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}

	ops := diffOps(oldLines, newLines)

	// Group ops into hunks separated by more than 2*context unchanged lines.
	type hunk struct{ start, end int } // indices into ops (half-open)
	var hunks []hunk
	for i := 0; i < len(ops); {
		if ops[i].kind == diffEqual {
			i++
			continue
		}
		// Found a change; extend the hunk to include neighbouring changes that are
		// within 2*context unchanged lines of each other.
		start := i
		end := i + 1
		gap := 0
		for j := i + 1; j < len(ops); j++ {
			if ops[j].kind == diffEqual {
				gap++
				if gap > 2*context {
					break
				}
			} else {
				gap = 0
				end = j + 1
			}
		}
		// Expand by context on both sides.
		s := start - context
		if s < 0 {
			s = 0
		}
		e := end + context
		if e > len(ops) {
			e = len(ops)
		}
		// Merge with previous hunk if they overlap.
		if n := len(hunks); n > 0 && s <= hunks[n-1].end {
			hunks[n-1].end = e
		} else {
			hunks = append(hunks, hunk{s, e})
		}
		i = end
	}

	for _, h := range hunks {
		var oldStart, newStart, oldCount, newCount int
		// Compute 1-based start line numbers and counts for the hunk header.
		oldLine, newLine := 1, 1
		for i := 0; i < h.start; i++ {
			switch ops[i].kind {
			case diffEqual:
				oldLine++
				newLine++
			case diffDelete:
				oldLine++
			case diffInsert:
				newLine++
			}
		}
		oldStart, newStart = oldLine, newLine
		for i := h.start; i < h.end; i++ {
			switch ops[i].kind {
			case diffEqual:
				oldCount++
				newCount++
			case diffDelete:
				oldCount++
			case diffInsert:
				newCount++
			}
		}
		if !writeLine(fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount)) {
			break
		}
		stop := false
		for i := h.start; i < h.end; i++ {
			op := ops[i]
			var prefix string
			switch op.kind {
			case diffEqual:
				prefix = " "
			case diffDelete:
				prefix = "-"
			case diffInsert:
				prefix = "+"
			}
			if !writeLine(prefix + op.text) {
				stop = true
				break
			}
		}
		if stop {
			break
		}
	}
	if truncated {
		b.WriteString("… (diff truncated)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

type diffKind int

const (
	diffEqual diffKind = iota
	diffDelete
	diffInsert
)

type diffOp struct {
	kind diffKind
	text string
}

// diffOps produces an edit script (equal/delete/insert) transforming a into b
// using a classic LCS dynamic-programming table.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// lcs[i][j] = length of LCS of a[i:] and b[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{diffEqual, a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{diffDelete, a[i]})
			i++
		default:
			ops = append(ops, diffOp{diffInsert, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{diffDelete, a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{diffInsert, b[j]})
	}
	return ops
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

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// styleLines applies a style to each line independently so lipgloss does not
// pad the block to the longest line's width (which would push lines past the
// terminal edge and cause spurious wraps).
func styleLines(s string, st lipgloss.Style) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = st.Render(ln)
	}
	return strings.Join(lines, "\n")
}

// --- styles ---

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
	case "commit_made", "question_answered":
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

// detailLineFor is detailLine plus session-model context: a queued mid-run
// user_input echo (queued:true) that has not yet been delivered gets a dim
// "(queued)" suffix, so the transcript never claims the message was delivered
// before its checkpoint. Once the matching user_input_delivered event arrives,
// rebuild() re-renders and the suffix disappears (spec §18.7).
func (m *model) detailLineFor(ev *v1.Event) string {
	d := detailLine(ev)
	if ev.Type == "user_input" && dataField(ev, "queued") == "true" && !m.deliveredSeqs[ev.Seq] {
		d += " " + dimStyle.Render("(queued)")
	}
	return d
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
	case "job_started":
		return strings.TrimSpace(dataField(ev, "id") + " " + oneLine(dataField(ev, "label"), 100) + " · running")
	case "job_finished":
		return strings.TrimSpace(dataField(ev, "id") + " " + oneLine(dataField(ev, "label"), 80) + " · " + dataField(ev, "status"))
	case "job_notified":
		return oneLine(dataField(ev, "text"), 120)
	case "review_submitted":
		verdict := dataField(ev, "verdict")
		return fmt.Sprintf("%s: %s — %s", dataField(ev, "model"), verdictStyle(verdict).Render(verdict), oneLine(dataField(ev, "summary"), 80))
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
	case "budget_warning":
		return "⚠ budget warning — " + budgetSummary(ev)
	case "budget_exceeded":
		action := dataField(ev, "action")
		suffix := ""
		switch action {
		case "halt":
			suffix = " — halting (wrap up current task)"
		case "continue":
			suffix = " — continuing past cap (confirmed)"
		}
		return "⚠ budget reached — " + budgetSummary(ev) + suffix
	}
	return ""
}

// budgetSummary renders the spent/cap datum carried on a budget_warning /
// budget_exceeded event for the transcript row (task 0137).
func budgetSummary(ev *v1.Event) string {
	tokens := int64(floatField(ev, "tokens"))
	tokenCap := int64(floatField(ev, "token_cap"))
	cost := floatField(ev, "cost")
	costCap := floatField(ev, "cost_cap")
	var parts []string
	if tokenCap > 0 {
		parts = append(parts, fmt.Sprintf("%s/%s tok", fmtTokens(int(tokens)), fmtTokens(int(tokenCap))))
	}
	if costCap > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f/$%.2f", cost, costCap))
	}
	return strings.Join(parts, ", ")
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

// eventUsage extracts the per-turn token usage and the logical model name from a
// model_turn event's data JSON (task 0062). It parses the proto DataJson directly
// (the live stream carries proto Events, not event.Event) and reads the nested
// "usage" object plus "model_name". Numbers decode as float64; a missing or
// unparsable usage block yields the zero Usage so accumulation degrades gracefully.
func eventUsage(ev *v1.Event) (event.Usage, string) {
	if ev == nil || ev.DataJson == "" {
		return event.Usage{}, ""
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return event.Usage{}, ""
	}
	name, _ := mp["model_name"].(string)
	u, _ := mp["usage"].(map[string]any)
	if u == nil {
		return event.Usage{}, name
	}
	num := func(k string) int {
		if f, ok := u[k].(float64); ok {
			return int(f)
		}
		return 0
	}
	return event.Usage{
		Input:      num("input"),
		Output:     num("output"),
		CacheRead:  num("cache_read"),
		CacheWrite: num("cache_write"),
		Total:      num("total"),
	}, name
}

// sessionUsage sums the running per-model usage and prices it (task 0062, spec
// §20). tokens is the total token count across every model. cost is the dollar
// sum over models that have configured pricing; unpriced models contribute tokens
// but never an invented cost. status reports the pricing coverage:
//   - "priced":   every model that spent tokens is priced
//   - "partial":  some but not all spending models are priced
//   - "unpriced": no spending model is priced (or there is no usage)
func (m model) sessionUsage() (tokens int, cost float64, status string) {
	var priced, unpriced int
	for name, u := range m.usageByModel {
		t := u.Total
		if t == 0 {
			t = u.Input + u.Output + u.CacheRead + u.CacheWrite
		}
		if t == 0 {
			continue // a model that recorded no tokens doesn't affect pricing status
		}
		tokens += t
		if p, ok := m.pricing[name]; ok {
			if c, ok := p.Cost(u); ok {
				cost += c
				priced++
				continue
			}
		}
		unpriced++
	}
	switch {
	case priced > 0 && unpriced == 0:
		status = "priced"
	case priced > 0:
		status = "partial"
	default:
		status = "unpriced"
	}
	return tokens, cost, status
}

// spinnerCmd arms the activity spinner's tick loop when there is activity to
// indicate (the session is running or a quick-capture RPC is in flight) and it is
// not already ticking. It returns nil otherwise so we never stack duplicate tick
// commands. The pointer receiver lets it record that a tick is in flight.
func (m *model) spinnerCmd() tea.Cmd {
	if (m.status == "running" || m.captureBusy) && !m.spinning {
		m.spinning = true
		return m.spin.Tick
	}
	return nil
}

// deliveredSeq extracts the queued-echo seq a user_input_delivered event refers
// to (spec §18.7). Returns false for other event types or malformed data.
func deliveredSeq(ev *v1.Event) (int64, bool) {
	if ev.Type != "user_input_delivered" || ev.DataJson == "" {
		return 0, false
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return 0, false
	}
	if f, ok := mp["seq"].(float64); ok {
		return int64(f), true
	}
	return 0, false
}

// deliveredSeqSet builds the set of delivered queued-echo seqs from an event
// slice, used when the transcript view loads a whole log at once.
func deliveredSeqSet(evs []*v1.Event) map[int64]bool {
	set := map[int64]bool{}
	for _, ev := range evs {
		if seq, ok := deliveredSeq(ev); ok {
			set[seq] = true
		}
	}
	return set
}

// notifyOut is where terminal notifications (BEL / OSC 9) are written. It is a
// package-level var so tests can capture the emitted bytes; in production it is
// the real stdout the TUI already renders to.
var notifyOut io.Writer = os.Stdout

// notifyTrigger reports whether a live event type warrants a bell / desktop
// notification when the user may be looking elsewhere (task 0108).
func notifyTrigger(t string) bool {
	switch t {
	case "question_asked", "session_idle", "session_error", "interrupted":
		return true
	}
	return false
}

// maybeNotify emits a terminal bell and/or OSC 9 desktop notification for a
// genuinely-new live event, gated by client prefs. It is called only from the
// live subscription path (evMsg) — never from transcript/replay loads — and it
// suppresses events whose timestamp predates the subscribe instant so the
// daemon's full-log replay on reopen stays silent (task 0108).
func (m *model) maybeNotify(ev *v1.Event) {
	if ev == nil || (!m.prefs.NotifyBell && !m.prefs.NotifyDesktop) {
		return
	}
	if !notifyTrigger(ev.Type) {
		return
	}
	// While auto-looping, session_idle just means the current task finished and
	// the loop will advance itself — a bell per task would be noise. Keep the
	// attention-worthy events (question/error/interrupt).
	if m.looping && ev.Type == "session_idle" {
		return
	}
	// Auto-answered questions (autonomous mode) never need the user, so a bell
	// would be a false alarm.
	if ev.Type == "question_asked" && dataField(ev, "auto") == "true" {
		return
	}
	// Only notify for events newer than the subscribe instant; earlier ones are
	// the daemon replaying the persisted log on reopen.
	ts, err := time.Parse(time.RFC3339, ev.Ts)
	if err != nil {
		return
	}
	if !m.notifyAfter.IsZero() && ts.Before(m.notifyAfter) {
		return
	}

	var b []byte
	if m.prefs.NotifyBell {
		b = append(b, '\a')
	}
	if m.prefs.NotifyDesktop {
		b = append(b, notifyOSC9(ev)...)
	}
	if len(b) > 0 {
		// Single Write so the escape bytes can't interleave mid-frame with the
		// renderer's output to the same file.
		_, _ = notifyOut.Write(b)
	}
}

// notifyOSC9 builds an OSC 9 desktop-notification escape sequence for an event:
// ESC ] 9 ; <text> BEL.
func notifyOSC9(ev *v1.Event) []byte {
	return []byte("\x1b]9;" + notifyText(ev) + "\x07")
}

// notifyText picks the desktop-notification body for an event: the question
// text for question_asked (truncated, control chars stripped), else a short
// labelled status line.
func notifyText(ev *v1.Event) string {
	switch ev.Type {
	case "question_asked":
		q := sanitizeNotify(dataField(ev, "question"))
		if q == "" {
			// Batch (multi-question) asks carry their prompts under "questions"
			// rather than "question"; surface the first one.
			if qs := dataQuestions(ev); len(qs) > 0 {
				q = sanitizeNotify(qs[0].prompt)
			}
		}
		if q == "" {
			return "ycc: question waiting"
		}
		return "ycc: " + q
	case "session_idle":
		return "ycc: session idle"
	case "session_error":
		return "ycc: session error"
	case "interrupted":
		return "ycc: interrupted"
	}
	return "ycc"
}

// sanitizeNotify strips control characters (which would corrupt the escape
// sequence) and truncates to a sane length for a notification body.
func sanitizeNotify(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	const max = 120
	// Truncate on a rune boundary so a multibyte rune can't be split.
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
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

// floatField pulls a numeric field from an event's data JSON as a float64,
// returning 0 when absent or non-numeric (task 0137).
func floatField(ev *v1.Event, key string) float64 {
	if ev.DataJson == "" {
		return 0
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return 0
	}
	if v, ok := mp[key].(float64); ok {
		return v
	}
	return 0
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
