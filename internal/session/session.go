// Package session manages the lifecycle of a daemon session: it binds an event
// log, an emitter, and an agent loop, runs the agent in a goroutine, and accepts
// follow-up input ("prods") and question answers between/within turns.
//
// The session's mode selects the agent: "work" runs the orchestrator coordinator
// (which delegates to subagents); anything else runs a single worker agent.
package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/jobshim"
	"github.com/whyrusleeping/ycc/internal/notify"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/publish"
	"github.com/whyrusleeping/ycc/internal/tools"
	"github.com/whyrusleeping/ycc/internal/usage"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// Origins identify explicit launch sites; an empty persisted origin is unknown.
const (
	OriginUser        = "user"
	OriginWorkLoop    = "work_loop"
	OriginMemoryGroom = "memory_groom"
	OriginAutomation  = "automation"
)

// Config parameterizes a new session.
type Config struct {
	Workspace  string
	Mode       string
	Unattended bool
	Prompt     string
	// Origin records the launch source, not the mode or opening-prompt preset.
	// Empty defaults to user, or automation for an unattended launch.
	Origin string
	// loopContinuation is daemon-generated context, seeded separately from the
	// opening user input so a prior model report is never echoed as user intent.
	loopContinuation string
	// Project, when set, names a registered project whose workspace is used,
	// overriding Workspace.
	Project string
	// CoordinatorModel, when set, overrides the coordinator's logical model FOR
	// THIS SESSION ONLY: the persisted per-role defaults are
	// untouched and implementer/reviewers keep them. An unknown name is an error.
	CoordinatorModel string
	// CoordinatorThinking, when set, is a session-only thinking level for the
	// coordinator's model, clamped to that model's supported levels. The saved
	// model config is untouched. An unknown level is ErrUnknownThinkingLevel.
	CoordinatorThinking string
	// Preset identifies the client-side opening-prompt preset. A configured
	// roles.presets binding selects this session's initial coordinator without
	// changing the persisted role defaults. An unbound preset behaves normally.
	Preset string
	// Images optionally attaches validated pictures to the opening prompt. The
	// initial user_input event records only metadata plus opaque references;
	// payloads are retained separately for transcript display.
	Images []engine.Image
	// PlanOnly starts a chat session with its plan-only guardrail on (see
	// SetPlanOnly). Other modes reject it with ErrPlanOnlyMode.
	PlanOnly bool
	// SubagentModels restricts generic spawn_agent choices for this session.
	// nil allows all enabled models; non-nil is an explicit allowlist (including empty).
	SubagentModels []string
}

// Session is one running agent conversation backed by a persistent event log.
type Session struct {
	ID        string
	Workspace string
	Mode      string

	unattended bool
	origin     string // immutable launch origin; empty for reopened legacy logs
	// Compact incremental reduction of durable engagement for live history overlays.
	participationMu     sync.Mutex
	participationCursor int
	participation       sessionParticipation

	log                 *event.Log
	emitter             *event.Emitter
	loop                *engine.Loop
	inter               *interaction
	deps                *orchestrator.Deps
	reg                 *config.Registry
	prompt              string
	preset              string
	coordinatorExplicit bool     // StartSession coordinator_model won over any preset binding
	coordinatorThinking string   // start-time session-only coordinator level, recorded in session_started
	subagentModels      []string // nil allows all enabled models; non-nil is a session-local allowlist
	// startupNotices are emitted as visible, non-fatal events after the session
	// lifecycle marker (a stale preset binding falling back safely, untracked
	// files too large to snapshot).
	startupNotices   []string
	loopContinuation string // startup-only synthetic context, recorded for replay
	startupPreload   orchestrator.ExplicitTaskPreload
	buildLoop        func(mode, prompt string) (*engine.Loop, error)

	// promptImages are pictures attached to the OPENING prompt. The bytes seed the
	// first loop's history exactly once and are retained separately for transcript
	// display; a later mode transition re-seeds text only.
	promptImages []engine.Image
	// promptImagesUsed marks those bytes as already seeded into a loop.
	promptImagesUsed bool

	// resumed marks a session re-instantiated on an EXISTING log via Reopen
	// ("resume = replay"): run() then skips the SessionStarted /
	// initial UserInput / seed, emits a SessionReopened marker, and waits idle for
	// the first new input before continuing on the reconstructed history.
	resumed bool

	// resumeQuestion, set by Reopen, is a still-unanswered ask_user question the
	// persisted log ended on; run() waits for its answer before anything else.
	resumeQuestion *resumableQuestion
	// resumeWait, set by Reopen, is the wait the persisted log ended inside;
	// run() finishes it (resume_wait.go) before anything else.
	resumeWait *resumableWait

	// forkAwaitsInput, set by Reopen for a fork no input has reached yet, makes
	// run() wait for that input even when the copied conversation ends on tool
	// results: a fork cut at a steer resumes with the edited steer, as the
	// parent's model received the original.
	forkAwaitsInput bool

	// reopenSurviving, set by Reopen, lists the coordinator's supervised jobs
	// still running after reconciliation; reopenAwaitingJobs marks a reopen
	// straight into wake-eligible idle (reopenAwaitingJobs), already armed
	// before publication. run() records both on session_reopened.
	reopenSurviving    []string
	reopenAwaitingJobs bool
	// reopenParked marks a reopen that waits idle for input: its last turn
	// finished, its history owes no response, or it is a fork awaiting its
	// first input.
	reopenParked bool
	// noDetach makes a restart teardown kill and join supervised shells
	// instead of leaving them running: the workstream they run in is being
	// merged or discarded, so its worktree must be settled.
	noDetach atomic.Bool
	// recovery is the reopen's supervised-job reconciliation; its survivors'
	// outcomes wait until session_reopened is recorded or teardown begins.
	recovery *tools.SupervisedReopen

	// messageCh is the single ordered queue of idle user input (text and any
	// native attachments). One queue is what keeps serially accepted inputs in
	// their accepted order when the run owner drains them.
	messageCh chan engine.UserMessage
	// sendMu serializes idle senders across the capacity-check, durable echo, and
	// channel delivery. The run goroutine only drains this channel, so once a
	// slot is observed the post-record send is guaranteed not to block or fail.
	sendMu sync.Mutex
	// retryCh nudges the idle-after-error run loop to re-run the failed turn on
	// the existing history (no new user message). Unbuffered so a Retry while the
	// loop is running/paused is a harmless no-op (nothing is receiving).
	retryCh chan struct{}
	// rolloverCh serializes explicit context-view changes through the run owner.
	// The same channel is consumed by an in-flight loop checkpoint and by the idle
	// run wait, so no RPC goroutine can replace history concurrently with input.
	rolloverCh chan *rolloverRequest
	ctx        context.Context
	cancel     context.CancelFunc
	// cancelCause cancels ctx with a cause; a graceful daemon restart uses
	// jobs.ErrRestartSuspend so blocked tools exit without results.
	cancelCause context.CancelCauseFunc
	// runDone is closed when the run owner goroutine returns, so restart
	// teardown can join it before closing the event log. Under launchMu,
	// launched records that the run owner was started and retired that restart
	// teardown claimed the session, so a late launch never starts it.
	runDone  chan struct{}
	launchMu sync.Mutex
	launched bool
	retired  bool

	// stopOnce guards Stop so a hard terminate runs at most once even if both a
	// StopSession RPC and a manager teardown race to call it.
	stopOnce sync.Once
	// stopErr is the honest outcome of Stop's job join: an *jobs.UnjoinedError
	// when supervision of a background job was lost, so its process may still
	// be running. Written inside stopOnce.
	stopErr error

	mu          sync.Mutex
	status      event.Status
	coordinator string   // logical model name driving the coordinator
	implementer string   // logical model name for the implementer role
	reviewers   []string // logical model names for the reviewer role
	// jobWakeArmed marks an idle session whose plain final response background
	// work may resume by itself; jobWakePosted marks such a continuation already
	// claimed and about to run. Both live under mu with status so a terminal-state
	// observer (work loop, GC reaper) never sees a finished-looking session that
	// is in fact waiting for, or resuming on, delegated work.
	jobWakeArmed  bool
	jobWakePosted bool
	// wakeGate and wakeCancel, set by the work loop under mu, admit automatic
	// job continuations into the loop's working slot. A denied wake leaves the
	// completion queued; the loop re-arms the wake once the slot frees. An
	// admitted wake that delivers nothing gives the slot back with wakeCancel.
	wakeGate   func() bool
	wakeCancel func()
	// thinkLevels holds per-model reasoning overrides, keyed
	// by logical model name. An empty/missing entry means "use the model config";
	// any of off/low/medium/high/xhigh/max forces that level for the model until
	// changed. Overrides are session-only and rebuilt from the log on reopen.
	thinkLevels map[string]string
	// Spend guard, guarded by s.mu. budgetWarned is set
	// once the session crosses ~80% of a configured cap (so the warning fires at
	// most once); budgetBreached is set once a cap is crossed and handled (the
	// attended Confirm is asked / the unattended wrap-up is injected at most once).
	// Both are seeded from the replayed log on Reopen so a reopened session that
	// already crossed the line does not re-fire or re-ask.
	budgetWarned   bool
	budgetBreached bool
	// budgetMu guards the incremental durable-event cursor and compact per-model
	// usage totals used by budget checks.
	budgetMu      sync.Mutex
	budgetCursor  int
	budgetByModel map[string]usage.Tokens
	// refused marks a session parked after a provider-side safety refusal
	// (engine Result.Refused): the coordinator's last turn came back
	// with stop_reason "refusal" and was kept out of history. Refusals are
	// sticky (per provider docs, continuing the conversation keeps being
	// refused), so while set, SendInput is rejected with guidance; recovery is
	// Resume() (explicit retry of the pending turn as-is) or a coordinator model
	// change via SetRoleConfig (which clears the gate and retries
	// automatically). Cleared at the start of every run-loop iteration.
	refused bool
	// switchRetry latches the automatic retry owed after a coordinator switch
	// cleared a refusal or context model-switch gate, and switchRetryCh (buffer
	// 1, coalescing) wakes the parked owner to consume it. Unlike the unbuffered
	// retryCh nudge, the token survives a switch that lands before the owner
	// reaches its wait. The latch is cleared at the start of every run-loop
	// iteration, so a stale token can never cause an extra turn.
	switchRetry   bool
	switchRetryCh chan struct{}

	// Interrupt & steer state, guarded by a dedicated mutex so it
	// never contends with the s.mu hot paths. pauseReq is set by Interrupt and
	// consumed at the next checkpoint; paused is true while a checkpoint blocks;
	// resumeReq wakes it (Resume or a steered correction); corrections buffers
	// steered-in user messages to inject at the next checkpoint; resumeCh is closed
	// to wake. running is true while the agent loop's Run is executing, so a
	// mid-run SendInput is queued as a correction (steer-by-default) and delivered
	// at the next safe checkpoint rather than waiting for the run to finish.
	steerMu         sync.Mutex
	pauseReq        bool
	paused          bool
	running         bool
	resumeReq       bool
	corrections     []correction
	resumeCh        chan struct{}
	inputWakeCh     chan struct{} // closed (then cleared) when a correction is queued; see inputWake
	rolloverPending bool          // explicit request accepted; idle input is queued until its durable view boundary

	// Plan-only guardrail (chat only), guarded by planMu. planOnly is the
	// user's current choice (plan_only_changed); planOnlyTold is what the
	// coordinator's conversation was last told (plan_only_delivered). A
	// checkpoint delivers a note whenever they differ.
	planMu       sync.Mutex
	planOnly     bool
	planOnlyTold bool

	// Experimental session coordination (coordination.go). coordination is
	// fixed when the session is built; coordinationOn reads the live project
	// setting. Overlap notices from other sessions wait in notices (memory
	// only) for the next checkpoint.
	coordination   bool
	coordinationOn func() bool
	noticeMu       sync.Mutex
	notices        []overlapNotice
	// Notice board (noticeboard.go): board is set when coordination is.
	// boardSeen holds the active notices this session has been told of;
	// intentSeen holds each other live session's last delivered intent text;
	// boardOpened is set once its first digest (the current board) is out.
	// The intent line lives only as long as the live session. All under
	// noticeMu.
	board       *sessionBoard
	boardOpened bool
	boardSeen   map[string]bool
	intentSeen  map[string]string
	intentText  string
	intentAt    time.Time
	// leases backs the coordinator's lease tools (leases.go); the session's
	// leases are released when it ends.
	leases *sessionLeaseTools

	// contextSummary is a test seam; nil uses the deterministic durable-evidence
	// summary builder.
	contextSummary contextSummaryBuilder
}

// correction is a steered-in user message buffered until the next checkpoint (or
// explicit Resume when paused). seq is the sequence of the queued user_input echo
// emitted when it was accepted, so the later user_input_delivered event can refer
// back to it.
type correction struct {
	message engine.UserMessage
	seq     int
	// recordedImages is the picture metadata of a queued message recovered
	// from the log on reopen (see resume_wait.go); its bytes are not reloaded,
	// so the delivered text carries the replay attachment note instead.
	recordedImages any
}

// Role name constants used to resolve settings requests to assigned models.
const (
	roleCoordinator = config.RoleCoordinator
	roleImplementer = config.RoleImplementer
	roleReviewers   = config.RoleReviewers
)

// Log exposes the session's event log for subscription.
func (s *Session) Log() *event.Log { return s.log }

// Status returns the current lifecycle status.
func (s *Session) Status() event.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Session) setStatus(st event.Status) {
	s.mu.Lock()
	s.status = st
	// Any transition out of idle ends the automatic-continuation window, and the
	// two are set together so no observer can pair a stale status with a later
	// wake decision.
	s.jobWakeArmed = false
	s.jobWakePosted = false
	s.mu.Unlock()
}

// setIdle records the idle transition together with whether background-job
// completion may resume this session by itself (see jobWakeArmed).
func (s *Session) setIdle(jobWake bool) {
	s.mu.Lock()
	s.status = event.StatusIdle
	s.jobWakeArmed = jobWake
	s.jobWakePosted = false
	s.mu.Unlock()
}

// setJobWakePosted marks an automatic continuation as decided but not yet
// running. The claim it covers has already left the registry, so this is what
// keeps the window between claiming a report and starting the turn visible to
// terminal-state observers.
func (s *Session) setJobWakePosted(v bool) {
	s.mu.Lock()
	s.jobWakePosted = v
	s.mu.Unlock()
}

// StatusWithJobContinuation reports the lifecycle status together with whether
// an automatic background-job continuation may still resume the session. A
// caller deciding that a session is finished must use this rather than Status
// alone: an idle coordinator with live jobs, an unclaimed final report, or a
// claim already in flight has not finished. Blocked, errored, refused, paused,
// stopped, and just-reopened sessions are never awaiting (nothing will wake
// them), so they stay terminal for their observers.
func (s *Session) StatusWithJobContinuation() (event.Status, bool) {
	s.mu.Lock()
	st, armed, posted := s.status, s.jobWakeArmed, s.jobWakePosted
	s.mu.Unlock()
	if st != event.StatusIdle || !armed {
		return st, false
	}
	if posted {
		return st, true
	}
	pending := false
	if s.deps != nil && s.deps.Jobs != nil {
		pending = s.deps.Jobs.PendingContinuation("coordinator")
	}
	if pending {
		return st, true
	}
	// Nothing looked pending, but the run owner may have claimed a report
	// concurrently and be about to start the turn. Any change during this check
	// is reported as still awaiting; the caller re-reads a stable state on its
	// next poll instead of reclaiming a session that is resuming.
	s.mu.Lock()
	stable := s.status == st && !s.jobWakePosted
	s.mu.Unlock()
	return st, !stable
}

// AwaitingJobs reports whether this session is idle but will still be resumed
// by delegated work (see StatusWithJobContinuation). Clients present such a
// session as active rather than finished.
func (s *Session) AwaitingJobs() bool {
	_, awaiting := s.StatusWithJobContinuation()
	return awaiting
}

// awaitingShellJobsOnly reports whether the session is idle and only its own
// background shell jobs can still resume it: automatic delivery is outstanding
// from at least one coordinator job and from no subagent run, and no wake is
// already on its way. The work loop counts such a session as waiting rather
// than working.
func (s *Session) awaitingShellJobsOnly() bool {
	s.mu.Lock()
	st, armed, posted := s.status, s.jobWakeArmed, s.jobWakePosted
	s.mu.Unlock()
	if st != event.StatusIdle || !armed || posted || s.deps == nil || s.deps.Jobs == nil {
		return false
	}
	pending, agent := s.deps.Jobs.PendingContinuationKinds("coordinator")
	return pending && !agent
}

// setWakeGate installs the work loop's admission gate for automatic job
// continuations (see wakeGate).
func (s *Session) setWakeGate(gate func() bool, cancel func()) {
	s.mu.Lock()
	s.wakeGate, s.wakeCancel = gate, cancel
	s.mu.Unlock()
}

// loopWorking reports whether a loop session that the work loop let wait has
// woken and works again: it is mid-turn, paused, or idle awaiting a subagent
// run. A session idle on shell jobs only, or finished, is not working.
func (s *Session) loopWorking() bool {
	switch st, awaiting := s.StatusWithJobContinuation(); st {
	case event.StatusIdle, event.StatusError, event.StatusStopped:
		return awaiting && !s.awaitingShellJobsOnly()
	}
	return true
}

func (s *Session) logFailure() error {
	if s.log != nil {
		if err := s.log.Err(); err != nil {
			return err
		}
	}
	if s.emitter != nil {
		return s.emitter.Err()
	}
	return nil
}

// BudgetBreached reports whether this session crossed a configured spend cap and
// handled it. The daemon work loop reads it after a loop
// session finishes to decide whether to halt the loop at a safe point.
func (s *Session) BudgetBreached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budgetBreached
}

// Refused reports whether the session is parked after a provider safety
// refusal (see the refused field): user input is gated until a retry.
func (s *Session) Refused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused
}

func (s *Session) setRefused(v bool) {
	s.mu.Lock()
	s.refused = v
	s.mu.Unlock()
}

func (s *Session) currentLoop() *engine.Loop {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loop
}

func (s *Session) setLoop(l *engine.Loop) {
	s.mu.Lock()
	s.loop = l
	s.mu.Unlock()
}

// SendInput delivers user text. If the agent is currently blocked on a question,
// the text answers it — including a pending batch (multi-question) ask_user, where
// the free-form text becomes the answer to the first question and the rest point
// back to it (see interaction.Answer), so a scripted reply is never silently
// buffered and lost. If a run is in flight (or the loop is paused/pausing at a
// steer checkpoint) the text is queued as a correction and delivered at the next
// safe checkpoint (steer-by-default) — its echo carries queued:true
// so the transcript never claims delivery before it happens. Otherwise (idle) it
// is enqueued as a follow-up prod for when the agent next picks it up.
func (s *Session) SendInput(text string) error {
	return s.SendInputMessage(engine.UserMessage{Text: text})
}

// imageMetadata renders picture attachments as the safe, byte-free shape
// recorded on user_input events. attachment_id is an opaque reference to a
// separately retained payload; event JSON never contains the image bytes.
func imageMetadata(images []engine.Image) []map[string]any {
	if len(images) == 0 {
		return nil
	}
	meta := make([]map[string]any, len(images))
	for i, img := range images {
		item := map[string]any{"media_type": img.MediaType, "filename": img.Filename}
		if img.AttachmentID != "" {
			item["attachment_id"] = img.AttachmentID
		}
		meta[i] = item
	}
	return meta
}

// retainImages writes validated image payloads beside the event log, then adds
// opaque ids to the in-memory model images. Files inherit the session's retention
// lifecycle while remaining outside append-only events.jsonl.
func (s *Session) retainImages(images []engine.Image) error {
	if len(images) == 0 {
		return nil
	}
	dir := filepath.Join(s.Workspace, ".ycc", "sessions", s.ID, "attachments")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session attachment directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure session attachment directory: %w", err)
	}
	for i := range images {
		if images[i].AttachmentID != "" {
			continue
		}
		id, err := newAttachmentID()
		if err != nil {
			return fmt.Errorf("create attachment id: %w", err)
		}
		data, err := base64.StdEncoding.DecodeString(images[i].Base64)
		if err != nil {
			return fmt.Errorf("decode picture attachment: %w", err)
		}
		path := filepath.Join(dir, id)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("retain picture attachment: %w", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("secure picture attachment: %w", err)
		}
		images[i].AttachmentID = id
	}
	return nil
}

// takeSeedImages returns the opening-prompt attachments exactly once. A later
// mode transition rebuilds the loop with the same code path and must NOT re-post
// the pictures into the fresh history.
func (s *Session) takeSeedImages() []engine.Image {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.promptImagesUsed {
		return nil
	}
	s.promptImagesUsed = true
	return s.promptImages
}

// SendInputMessage delivers user text plus optional native image attachments.
// Image bytes enter live model history and a separate session attachment store;
// user_input events remain byte-free so events.jsonl is not a binary payload store.
func (s *Session) SendInputMessage(input engine.UserMessage) error {
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	// A refused session (provider safety refusal) rejects new input:
	// per documented provider semantics the conversation would just keep being
	// refused, silently eating every message. Recovery is a model switch (which
	// retries automatically) or an explicit retry — not another message.
	if s.Refused() {
		return fmt.Errorf("the model's provider refused the last turn (safety classifier); sending another message would be refused too — switch the coordinator to a different model (retries automatically) or retry the turn")
	}
	if s.contextModelSwitchRequired() {
		return fmt.Errorf("the compact coordinator context still exceeds this model's window; switch the coordinator to a model with a larger context window before sending more input")
	}
	if len(input.Images) > 0 && s.inter.pending() {
		return fmt.Errorf("answer the pending question before sending pictures")
	}
	if len(input.Images) == 0 && s.inter.Answer(input.Text) {
		return nil
	}
	eventData := func(queued bool) map[string]any {
		data := map[string]any{"text": input.Text}
		if queued {
			data["queued"] = true
		}
		if meta := imageMetadata(input.Images); meta != nil {
			data["images"] = meta
		}
		return data
	}
	// Serialize classification with explicit rollover acceptance. Taking sendMu
	// before steerMu means a sender is wholly before the selected-view request or
	// observes rolloverPending and receives a durable delivered event after it;
	// there is no non-queued user_input stranded immediately before the reset.
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	// Mid-run, paused, or while an idle rollover is being built: buffer as a
	// correction and echo it as queued. A run drains corrections at its next safe
	// checkpoint; an idle rollover records their delivery only after its durable
	// selected-view transition. Multiple sends remain FIFO under steerMu. It does
	// NOT auto-resume a paused loop.
	s.steerMu.Lock()
	if s.paused || s.pauseReq || s.running || s.rolloverPending {
		if err := s.retainImages(input.Images); err != nil {
			s.steerMu.Unlock()
			return err
		}
		ev := s.emitter.EmitAs("user", event.UserInput, eventData(true))
		if err := s.logFailure(); err != nil {
			s.steerMu.Unlock()
			return fmt.Errorf("session event log failed: %w", err)
		}
		s.corrections = append(s.corrections, correction{message: input, seq: ev.Seq})
		if s.inputWakeCh != nil {
			close(s.inputWakeCh)
			s.inputWakeCh = nil
		}
		s.steerMu.Unlock()
		return nil
	}
	s.steerMu.Unlock()

	// Idle delivery is transactional with respect to the durable user_input echo:
	// reject a full queue before recording, then record before making the input
	// visible to the run goroutine. sendMu excludes competing producers, while the
	// sole consumer can only create more capacity, so the final send cannot block.
	// Text and pictures share one queue, so serially accepted inputs keep their
	// accepted order in live history exactly as the durable log replays them.
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	if len(s.messageCh) >= cap(s.messageCh) {
		return fmt.Errorf("session %s input buffer full", s.ID)
	}
	if err := s.retainImages(input.Images); err != nil {
		return err
	}
	// Emit metadata before delivery; image bytes stay in messageCh/model history
	// and the separate retained attachment file, never in events.jsonl.
	s.emitter.EmitAs("user", event.UserInput, eventData(false))
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	s.messageCh <- input
	return nil
}

// inputWake implements tools.Workspace.InputWake for the coordinator: the
// returned channel is closed when a user message is queued as a correction (or
// is already closed if one is pending), so a blocking wait returns and the
// message is delivered at the checkpoint after that tool result.
func (s *Session) inputWake() <-chan struct{} {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if len(s.corrections) > 0 {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	if s.inputWakeCh == nil {
		s.inputWakeCh = make(chan struct{})
	}
	return s.inputWakeCh
}

// Answer responds to a pending question. Errors if none is pending.
func (s *Session) Answer(text string) error {
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	if !s.inter.Answer(text) {
		return fmt.Errorf("session %s has no pending question", s.ID)
	}
	return nil
}

// AnswerOption responds to a pending question with a chosen option (resolved by
// index when idx >= 0 and in range) or free text. Errors if none is pending.
func (s *Session) AnswerOption(idx int, text string) error {
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	if !s.inter.AnswerOption(idx, text) {
		return fmt.Errorf("session %s has no pending question", s.ID)
	}
	return nil
}

// AnswerBatch responds to a pending batch (multi-question) ask_user call. idxs
// and texts are positional and parallel: the i-th answer resolves to option
// idxs[i] of the i-th question when in range, otherwise to free text texts[i].
// Errors if no batch question is pending.
func (s *Session) AnswerBatch(idxs []int, texts []string) error {
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	n := len(idxs)
	if len(texts) > n {
		n = len(texts)
	}
	ans := make([]answer, n)
	for i := 0; i < n; i++ {
		a := answer{idx: -1}
		if i < len(idxs) {
			a.idx = idxs[i]
		}
		if i < len(texts) {
			a.text = texts[i]
		}
		ans[i] = a
	}
	if !s.inter.AnswerAll(ans) {
		return fmt.Errorf("session %s has no pending question", s.ID)
	}
	return nil
}

// Stop terminates the session's live process: it records a session_stopped
// event (informational — the session stays reopenable via log replay),
// cancels the agent loop (unblocking any ask_user / checkpoint waiting on the
// ctx), and closes the event log. It is idempotent (runs at most once).
func (s *Session) Stop() {
	if s.logFailure() != nil {
		s.cancel()
		s.killJobs()
		s.releaseLeases()
		return
	}
	s.stopOnce.Do(func() {
		s.setStatus(event.StatusStopped)
		s.emitter.Emit(event.SessionStopped, map[string]any{})
		s.cancel()
		// Job cancellation has a separate execution-join boundary: keep the log and
		// mutation ownership alive until every tracked process/agent has stopped.
		s.stopErr = s.killJobs()
		s.releaseLeases()
		s.log.Close()
	})
}

// reap releases a live but idle session's in-memory resources for the GC
// reaper WITHOUT recording a session_stopped marker: it cancels the
// loop and closes the event log, leaving the durable events.jsonl exactly as
// the session left it. Both reap and Stop keep a session reopenable (resume =
// log replay); reap simply avoids writing a spurious "terminated" marker into a
// log the reaper is only reclaiming for memory. It shares stopOnce with Stop so
// at most one of them ever runs.
func (s *Session) reap() {
	s.stopOnce.Do(func() {
		s.cancel()
		s.killJobs()
		s.releaseLeases()
		s.log.Close()
	})
}

// reapable reports whether the session is safe for the idle reaper to stop: it
// is idle (between turns — a turn blocked on ask_user stays StatusRunning), not
// paused/pausing for steer, and has no pending question. This deliberately
// excludes sessions legitimately waiting for user input.
func (s *Session) reapable() bool {
	status, awaiting := s.StatusWithJobContinuation()
	if status != event.StatusIdle {
		return false
	}
	// Delegated work still running, an unclaimed final report, or a claim already
	// in flight all mean the session is waiting rather than gone quiet.
	if awaiting || s.hasLiveJobs() {
		return false
	}
	s.steerMu.Lock()
	paused := s.paused || s.pauseReq
	s.steerMu.Unlock()
	if paused {
		return false
	}
	return !s.inter.pending()
}

// activelyWorking reports whether the session is mid-turn, paused, waiting on
// its own background work, or waiting on a question — i.e. still working on its
// focused tasks. A session idle between turns, errored, or stopped is not.
func (s *Session) activelyWorking() bool {
	switch s.Status() {
	case event.StatusError, event.StatusStopped:
		return false
	}
	return !s.reapable()
}

// PendingQuestion reports whether the session is currently blocked on an
// unanswered ask_user question (single or batch) — the same gate the idle
// reaper uses. Exposed so ListSessionHistory can surface sessions that are
// waiting on the user in the home menu. Nil-safe for minimally-constructed
// sessions (no interaction wired) so it can be called on any live row.
func (s *Session) PendingQuestion() bool {
	if s.inter == nil {
		return false
	}
	return s.inter.pending()
}

// Checkpoint implements engine.Steer. At a safe checkpoint the
// loop calls it. If no pause is pending it drains any mid-run corrections queued
// since the last checkpoint (steer-by-default) and returns their texts to append
// before the next turn — with no pause ceremony; if none are pending it returns
// immediately (cheap no-op). If a pause IS pending it marks the session paused,
// emits interrupted, and blocks until a Resume or a steered SendInput wakes it
// (or ctx is cancelled, returned as a normal stop). Either way it emits a
// user_input_delivered event for each drained correction, marking the point at
// which the queued input actually enters the conversation.
func (s *Session) Checkpoint(ctx context.Context) ([]string, error) {
	messages, err := s.CheckpointMessages(ctx)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, nil
	}
	texts := make([]string, len(messages))
	for i, message := range messages {
		texts[i] = message.Text
	}
	return texts, nil
}

// CheckpointMessages is engine.MessageSteer's multimodal checkpoint path.
func (s *Session) CheckpointMessages(ctx context.Context) ([]engine.UserMessage, error) {
	if err := s.logFailure(); err != nil {
		return nil, fmt.Errorf("session event log failed: %w", err)
	}
	s.steerMu.Lock()
	if !s.pauseReq {
		// Steer-by-default fast path: deliver any queued corrections and any
		// finished-job notifications now without a pause, or a cheap no-op when
		// there are none of either.
		corr := s.corrections
		s.corrections = nil
		s.steerMu.Unlock()
		msgs, err := s.deliverCorrections(corr)
		if err != nil {
			return nil, err
		}
		notes, err := s.drainJobNotes()
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, textMessages(notes)...)
		notices, err := s.drainCoordinationNotices()
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, textMessages(notices)...)
		digest, err := s.drainBoardDigest(time.Now())
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, textMessages(digest)...)
		plan, err := s.planOnlyNote()
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, textMessages(plan)...)
		msgs = append(msgs, textMessages(s.checkBudget(ctx))...)
		if err := s.logFailure(); err != nil {
			return nil, fmt.Errorf("session event log failed: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(msgs) == 0 {
			return nil, nil
		}
		return msgs, nil
	}
	s.pauseReq = false
	s.paused = true
	s.steerMu.Unlock()

	s.setStatus(event.StatusPaused)
	s.emitter.Emit(event.Interrupted, map[string]any{})
	if err := s.logFailure(); err != nil {
		return nil, fmt.Errorf("session event log failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.steerMu.Lock()
	for !s.resumeReq {
		s.resumeCh = make(chan struct{})
		ch := s.resumeCh
		s.steerMu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			s.steerMu.Lock()
			s.paused = false
			s.resumeCh = nil
			s.steerMu.Unlock()
			return nil, ctx.Err()
		}
		s.steerMu.Lock()
	}
	corr := s.corrections
	s.corrections = nil
	s.resumeReq = false
	s.paused = false
	s.resumeCh = nil
	s.steerMu.Unlock()

	s.setStatus(event.StatusRunning)
	s.emitter.Emit(event.Resumed, map[string]any{})
	if err := s.logFailure(); err != nil {
		return nil, fmt.Errorf("session event log failed: %w", err)
	}
	msgs, err := s.deliverCorrections(corr)
	if err != nil {
		return nil, err
	}
	notes, err := s.drainJobNotes()
	if err != nil {
		return nil, err
	}
	msgs = append(msgs, textMessages(notes)...)
	notices, err := s.drainCoordinationNotices()
	if err != nil {
		return nil, err
	}
	msgs = append(msgs, textMessages(notices)...)
	digest, err := s.drainBoardDigest(time.Now())
	if err != nil {
		return nil, err
	}
	msgs = append(msgs, textMessages(digest)...)
	plan, err := s.planOnlyNote()
	if err != nil {
		return nil, err
	}
	msgs = append(msgs, textMessages(plan)...)
	msgs = append(msgs, textMessages(s.checkBudget(ctx))...)
	if err := s.logFailure(); err != nil {
		return nil, fmt.Errorf("session event log failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

func textMessages(texts []string) []engine.UserMessage {
	out := make([]engine.UserMessage, len(texts))
	for i, text := range texts {
		out[i] = engine.UserMessage{Text: text}
	}
	return out
}

// drainJobNotes claims and delivers pending automatic final notifications for
// finished coordinator jobs as user-role messages. Each
// is recorded as a user-actor job_notified event — the same rule as a steer
// correction — so reopen replays the identical history. Returns the texts for
// the engine to Post before the next turn. Nil-safe: no registry ⇒ no notes.
func (s *Session) drainJobNotes() ([]string, error) {
	if s.deps == nil || s.deps.Jobs == nil {
		return nil, nil
	}
	reports := s.deps.Jobs.DrainFinished("coordinator")
	if len(reports) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(reports))
	for _, r := range reports {
		text := tools.FormatJobReport(r)
		s.emitter.EmitAs("user", event.JobNotified, map[string]any{
			"id": r.ID, "kind": r.Kind, "label": r.Label,
			"status": string(r.Status), "result": r.Result, "text": text,
		})
		if err := s.logFailure(); err != nil {
			return nil, fmt.Errorf("session event log failed: %w", err)
		}
		out = append(out, text)
	}
	return out, nil
}

// killJobs terminates every background job for this session (session end). It is
// nil-safe so minimally-constructed sessions (tests) can call it. It returns an
// *jobs.UnjoinedError, also logged, when a job's supervision was lost so its
// process could not be confirmed stopped.
func (s *Session) killJobs() error {
	// Survivors held back for the reopen marker must be able to finish before
	// teardown joins them.
	s.recovery.ReleaseMarker()
	if s.deps == nil || s.deps.Jobs == nil {
		return nil
	}
	var err error
	if s.ctx != nil && jobs.RestartSuspended(s.ctx) && s.noDetach.Load() {
		err = s.deps.Jobs.KillAllCause(jobs.ErrRestartSuspend)
	} else if s.ctx != nil && jobs.RestartSuspended(s.ctx) {
		// Restart suspension: subagent loops see the restart cause too, and
		// the coordinator's supervised shells are left running for the next
		// daemon to re-attach on reopen (docs/design/daemon-restart.md §1).
		err = s.deps.Jobs.SuspendForRestart("coordinator")
	} else {
		err = s.deps.Jobs.KillAllCause(nil)
	}
	if err != nil {
		log.Printf("session %s: %v", s.ID, err)
	}
	return err
}

// jobCompletions is the registry's coalescing completion signal, or nil (a
// forever-blocking select case) when this session has no job registry.
func (s *Session) jobCompletions() <-chan struct{} {
	if s.deps == nil || s.deps.Jobs == nil {
		return nil
	}
	return s.deps.Jobs.Completions()
}

// hasLiveJobs reports whether any background job is still running. An idle
// coordinator with live jobs is waiting for them, not quiescent, so the GC
// reaper must not reclaim it out from under the completion wake.
func (s *Session) hasLiveJobs() bool {
	if s.deps == nil || s.deps.Jobs == nil {
		return false
	}
	for _, info := range s.deps.Jobs.List("", true) {
		if info.Status == jobs.Running {
			return true
		}
	}
	return false
}

// resumeForFinishedJobs delivers finished background-job reports to an idle
// coordinator and reports whether the loop should run another turn. It runs on
// the session run owner, so a wake can never invoke the model concurrently with
// a turn or with input delivery. Reports are coalesced (every claimable one
// enters history before the turn starts) and enter as synthetic job-completion
// context, never as user input. An explicit pause, a pending rollover, or an
// unanswered question keeps its boundary: nothing is claimed then, and the
// report falls back to ordinary checkpoint delivery on the next turn.
//
// sendMu is held across the input sweep, the claim, and the idle→running
// handoff so an idle sender is wholly before this boundary (its message is
// absorbed into the same turn, ahead of the notification it durably precedes)
// or wholly after it (queued as a correction the starting run delivers at its
// first checkpoint). Otherwise live history could order a note before an input
// that the durable log — and therefore reopen — orders first.
func (s *Session) resumeForFinishedJobs() (bool, error) {
	if s.deps == nil || s.deps.Jobs == nil || s.ctx.Err() != nil {
		return false, nil
	}
	s.sendMu.Lock()
	s.steerMu.Lock()
	held := s.paused || s.pauseReq || s.rolloverPending
	s.steerMu.Unlock()
	if held || (s.inter != nil && s.inter.pending()) {
		s.sendMu.Unlock()
		return false, nil
	}
	// Mark the continuation in flight BEFORE claiming anything: between the claim
	// and the turn actually starting, the registry reports nothing pending, and a
	// terminal-state observer must not read that gap as a finished session.
	// It is also marked before asking the work loop's gate, so the loop never
	// sees an admitted wake as a session still only waiting on its jobs.
	s.setJobWakePosted(true)
	// The gate is asked only when a completion is ready to deliver, so a wake
	// with nothing to deliver never takes the slot.
	s.mu.Lock()
	gate, cancel := s.wakeGate, s.wakeCancel
	s.mu.Unlock()
	admitted := false
	if gate != nil && s.deps.Jobs.CompletionReady("coordinator") {
		if !gate() {
			s.setJobWakePosted(false)
			s.sendMu.Unlock()
			return false, nil
		}
		admitted = true
	}
	absorbed := s.absorbBufferedIdleInputs()
	// A gated session claims reports only once admitted: a completion that
	// raced the readiness check stays queued, and its own signal wakes the
	// session to ask the gate.
	var notes []string
	var err error
	if gate == nil || admitted {
		notes, err = s.drainJobNotes()
	}
	if err != nil || (!absorbed && len(notes) == 0) {
		// Nothing to continue on: stay idle with senders still excluded, so no
		// sender was ever classified against a run that is not going to start.
		s.setJobWakePosted(false)
		s.sendMu.Unlock()
		if admitted && cancel != nil {
			cancel()
		}
		return false, err
	}
	// Hand idle off to running while senders are still excluded. A pause request
	// that lands now is honoured at the run's first checkpoint, as for any turn.
	s.steerMu.Lock()
	s.running = true
	s.steerMu.Unlock()
	s.sendMu.Unlock()
	// The claim and its durable job_notified event already happened, so a
	// cancellation racing this post still replays identically on reopen.
	for _, note := range notes {
		s.currentLoop().Post(note)
	}
	return true, nil
}

// rearmJobWake re-arms the completion check for an idle session that may still
// be woken. An explicit boundary (a pause taken while idle) consumes the
// coalescing completion edge without delivering anything, so clearing that
// boundary must restore the check. It never retries errors, refusals, or blocked
// outcomes: those are not armed for a wake in the first place.
func (s *Session) rearmJobWake() {
	if s.deps == nil || s.deps.Jobs == nil {
		return
	}
	s.mu.Lock()
	armed := s.status == event.StatusIdle && s.jobWakeArmed
	s.mu.Unlock()
	if armed {
		s.deps.Jobs.SignalCompletion()
	}
}

// deliverCorrections emits a user_input_delivered event for each queued
// correction — marking the checkpoint at which its (queued) echo actually enters
// the conversation — and returns their texts, in order, for the engine to Post
// before the next turn. The delivered event references the queued
// echo by seq so replay and the TUI can pair them.
func (s *Session) deliverCorrections(corr []correction) ([]engine.UserMessage, error) {
	if len(corr) == 0 {
		return nil, nil
	}
	messages := make([]engine.UserMessage, 0, len(corr))
	for _, c := range corr {
		data := map[string]any{"seq": c.seq, "text": c.message.Text}
		if images := imageMetadata(c.message.Images); images != nil {
			data["images"] = images
		} else if c.recordedImages != nil {
			data["images"] = c.recordedImages
		}
		s.emitter.EmitAs("user", event.UserInputDelivered, data)
		if err := s.logFailure(); err != nil {
			return nil, fmt.Errorf("session event log failed: %w", err)
		}
		message := c.message
		if c.recordedImages != nil {
			message.Text = engine.ReplayUserText(data)
		}
		messages = append(messages, message)
	}
	return messages, nil
}

// signalResumeLocked wakes a blocked Checkpoint. Call only while holding steerMu.
func (s *Session) signalResumeLocked() {
	s.resumeReq = true
	if s.resumeCh != nil {
		close(s.resumeCh)
		s.resumeCh = nil
	}
}

// Interrupt requests a graceful pause: the running loop stops at its next safe
// checkpoint (between turns / after a tool result) without aborting a tool
// mid-run. Resume or a steered SendInput continues it.
func (s *Session) Interrupt() error {
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	s.steerMu.Lock()
	if s.rolloverPending {
		s.steerMu.Unlock()
		return fmt.Errorf("coordinator context rollover is pending; wait for it to finish before pausing")
	}
	if s.paused || s.pauseReq {
		s.steerMu.Unlock()
		return nil
	}
	s.pauseReq = true
	s.emitter.EmitAs("user", event.PauseRequested, map[string]any{})
	err := s.logFailure()
	s.steerMu.Unlock()
	if err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	return nil
}

// Resume continues a paused loop with no correction. It cancels a
// not-yet-effective pause request. When the session is idle after a session
// error (retries exhausted), it instead nudges the run loop to re-run the failed
// turn on the existing history — a true retry with no injected user message
// (that is what "retry by sending another message" was standing in for). It is a
// no-op if nothing is paused and the session is not errored.
func (s *Session) Resume() error {
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	if s.contextModelSwitchRequired() {
		return fmt.Errorf("the compact coordinator context still exceeds this model's window; switch the coordinator to a model with a larger context window instead of retrying the unchanged request")
	}
	s.steerMu.Lock()
	if s.paused {
		s.signalResumeLocked()
		s.steerMu.Unlock()
		return nil
	}
	if s.pauseReq {
		s.pauseReq = false
		s.emitter.EmitAs("user", event.PauseCancelled, map[string]any{})
		if err := s.logFailure(); err != nil {
			s.steerMu.Unlock()
			return fmt.Errorf("session event log failed: %w", err)
		}
	}
	running := s.running
	s.steerMu.Unlock()
	// Errored + not running ⇒ the run loop is blocked waiting for input. Signal a
	// retry so it re-runs without a bogus user turn. Non-blocking: if the loop is
	// mid-run (or not on the retry-aware wait) the send falls through as a no-op.
	if !running && s.Status() == event.StatusError {
		select {
		case s.retryCh <- struct{}{}:
		default:
		}
	}
	// An idle pause holds back delivery of finished background work and consumes
	// its completion signal; clearing the pause must restore that check.
	if !running {
		s.rearmJobWake()
	}
	return nil
}

// SetRoleConfig reassigns per-role logical models for this session and rebuilds
// the relevant gollama clients so the next coordinator turn / next spawned
// subagent uses the new assignment. Empty coordinator/implementer leaves that
// role unchanged; an empty reviewers slice leaves reviewers unchanged. Global
// defaults are deliberately unaffected.
func (s *Session) SetRoleConfig(coordinator, implementer string, reviewers []string) error {
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	s.mu.Lock()
	oldCoord, oldImpl := s.coordinator, s.implementer
	oldRevs := append([]string(nil), s.reviewers...)
	s.mu.Unlock()
	newCoord, newImpl := oldCoord, oldImpl
	newRevs := append([]string(nil), oldRevs...)

	validateAssignable := func(name string, alreadyAssigned bool) error {
		if !s.reg.Has(name) {
			return fmt.Errorf("unknown model %q", name)
		}
		if !s.reg.Enabled(name) && !alreadyAssigned {
			return fmt.Errorf("%w %q", ErrDisabledModel, name)
		}
		return nil
	}
	if coordinator != "" {
		if err := validateAssignable(coordinator, coordinator == oldCoord); err != nil {
			return err
		}
		newCoord = coordinator
	}
	if implementer != "" {
		if err := validateAssignable(implementer, implementer == oldImpl); err != nil {
			return err
		}
		newImpl = implementer
	}
	if len(reviewers) > 0 {
		oldCounts := make(map[string]int, len(oldRevs))
		newCounts := make(map[string]int, len(reviewers))
		for _, name := range oldRevs {
			oldCounts[name]++
		}
		for _, name := range reviewers {
			newCounts[name]++
		}
		for _, name := range reviewers {
			if err := validateAssignable(name, newCounts[name] <= oldCounts[name]); err != nil {
				return err
			}
		}
		newRevs = append([]string(nil), reviewers...)
	}
	coordChanged := newCoord != oldCoord
	implChanged := newImpl != oldImpl
	reviewersChanged := !slices.Equal(newRevs, oldRevs)

	// Rebuild only changed role backends. Unchanged disabled assignments remain
	// represented while the user migrates roles one at a time; disabled reviewers
	// are retained in config but omitted from the next executable fan-out.
	if implChanged {
		implSpec, err := s.agentSpec(newImpl)
		if err != nil {
			return err
		}
		s.deps.SetImplementer(implSpec)
	}
	if reviewersChanged {
		var revSpecs []orchestrator.AgentSpec
		for _, name := range newRevs {
			if !s.reg.Enabled(name) {
				continue
			}
			rs, err := s.agentSpec(name)
			if err != nil {
				return err
			}
			revSpecs = append(revSpecs, rs)
		}
		s.deps.SetReviewers(revSpecs)
	}

	// Sample the durable context gate before the role change clears it: a
	// coordinator switch is its documented recovery and should retry by itself.
	wasContextGated := coordChanged && s.contextModelSwitchRequired()
	// Swap the live coordinator loop only when that assignment changed, preserving
	// an already-constructed client until the disabled role is explicitly replaced.
	if coordChanged {
		client, model, err := s.reg.Build(newCoord)
		if err != nil {
			return fmt.Errorf("build coordinator backend: %w", err)
		}
		contextWindow, _ := s.reg.ContextBudget(newCoord)
		s.currentLoop().SetBackendWithContextWindow(client, model, newCoord, s.reg.BackendFor(newCoord), contextWindow, s.thinkingFor(newCoord))
	}

	s.mu.Lock()
	s.coordinator, s.implementer, s.reviewers = newCoord, newImpl, newRevs
	// A coordinator model change is the documented way out of a provider safety
	// refusal: retrying the refused turn on a DIFFERENT model is
	// the provider-recommended recovery, so clear the input gate and nudge the
	// parked run loop to re-run the pending turn on the new backend.
	wasRefused := s.refused && coordChanged
	if wasRefused {
		s.refused = false
	}
	if wasRefused || wasContextGated {
		s.switchRetry = true
	}
	s.mu.Unlock()

	s.emitter.Emit(event.RoleConfigChanged, map[string]any{
		"coordinator": newCoord,
		"implementer": newImpl,
		"reviewers":   newRevs,
	})
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	if wasRefused || wasContextGated {
		// Coalescing token for the latched retry: it is kept until the parked
		// owner consumes it, even if that owner has not reached its wait yet.
		select {
		case s.switchRetryCh <- struct{}{}:
		default:
		}
	}
	return nil
}

// RoleConfigSnapshot returns one synchronized view of this session's live role
// assignments and the thinking level resolved for each role's model (the session
// override, else the saved model level). The first reviewer model represents the
// reviewer row, matching global settings.
func (s *Session) RoleConfigSnapshot() (coordinator, implementer string, reviewers []string, coordinatorThinking, implementerThinking, reviewersThinking string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	coordinator, implementer = s.coordinator, s.implementer
	reviewers = append([]string(nil), s.reviewers...)
	reviewer := ""
	if len(reviewers) > 0 {
		reviewer = reviewers[0]
	}
	level := func(name string) string {
		if l := s.thinkLevels[name]; l != "" {
			return l
		}
		return s.reg.ModelThinkingLevel(name)
	}
	return coordinator, implementer, reviewers, level(coordinator), level(implementer), level(reviewer)
}

// ReferencesModel reports whether the session's current (possibly mid-session
// overridden via SetRoleConfig) role assignments reference the named logical
// model. Used by Manager.RemoveModel so a running session can never be left
// pointing at a removed backend.
func (s *Session) ReferencesModel(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.coordinator == name || s.implementer == name {
		return true
	}
	for _, r := range s.reviewers {
		if r == name {
			return true
		}
	}
	return false
}

// SetThinking applies a session-only reasoning level to the model(s) assigned to
// role. An empty role targets all assigned models; reviewers targets the
// whole reviewer fan-out. Models are deduplicated, so shared role assignments
// receive one override. Each model gets the level clamped to its supported
// levels. Every role backed by a targeted model is refreshed for its next
// turn/spawn. The saved model config is untouched; reopen replays the
// thinking_level_changed event.
func (s *Session) SetThinking(role, level string) error {
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	if _, ok := thinkingForLevel(level); !ok {
		return fmt.Errorf("unknown thinking level %q", level)
	}
	switch role {
	case "", roleCoordinator, roleImplementer, roleReviewers:
	default:
		return fmt.Errorf("unknown thinking role %q", role)
	}

	s.mu.Lock()
	coord, impl := s.coordinator, s.implementer
	revs := append([]string(nil), s.reviewers...)
	byRole := map[string][]string{
		roleCoordinator: {coord},
		roleImplementer: {impl},
		roleReviewers:   revs,
	}
	targets := map[string]bool{}
	if role == "" {
		for _, names := range byRole {
			for _, name := range names {
				targets[name] = true
			}
		}
	} else {
		for _, name := range byRole[role] {
			targets[name] = true
		}
	}
	models := make([]string, 0, len(targets))
	for name := range targets {
		models = append(models, name)
	}
	sort.Strings(models)
	perModel := make(map[string]string, len(models))
	for _, name := range models {
		perModel[name] = s.reg.ClampThinkingLevelFor(name, level)
	}
	fromModel := coord // empty/all preserves the old coordinator-based "from" value
	if role != "" && len(byRole[role]) > 0 {
		fromModel = byRole[role][0]
	}

	// A shared model means a request naming one role can affect other roles too.
	affected := map[string]bool{}
	for r, names := range byRole {
		for _, name := range names {
			if targets[name] {
				affected[r] = true
				break
			}
		}
	}
	if s.thinkLevels == nil {
		s.thinkLevels = map[string]string{}
	}
	from := s.thinkLevels[fromModel]
	for _, name := range models {
		s.thinkLevels[name] = perModel[name]
	}
	s.mu.Unlock()
	if from == "" && fromModel != "" {
		from = s.reg.ModelThinkingLevel(fromModel)
	}

	if affected[roleImplementer] {
		implSpec, err := s.agentSpec(impl)
		if err != nil {
			return err
		}
		s.deps.SetImplementer(implSpec)
	}
	if affected[roleReviewers] {
		var revSpecs []orchestrator.AgentSpec
		for _, name := range revs {
			rs, err := s.agentSpec(name)
			if err != nil {
				return err
			}
			revSpecs = append(revSpecs, rs)
		}
		s.deps.SetReviewers(revSpecs)
	}
	if affected[roleCoordinator] {
		// Update the live coordinator loop's reasoning settings for its next turn
		// (its conversation history is preserved).
		s.currentLoop().SetThinking(s.thinkingFor(coord))
	}

	emittedRole := role
	if emittedRole == "" {
		emittedRole = "all"
	}
	// "to" is the requested level; "levels" holds each model's clamped level.
	s.emitter.Emit(event.ThinkingLevelChanged, map[string]any{
		"role": emittedRole, "models": models, "from": from, "to": level, "levels": perModel,
	})
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	return nil
}

// replaySubagentModels restores the optional generic-spawn allowlist from the
// initial session marker. Missing field means legacy/all enabled; [] means none.
func replaySubagentModels(events []event.Event) []string {
	for _, ev := range events {
		if ev.Type != event.SessionStarted {
			continue
		}
		value, ok := ev.Data["subagent_models"]
		if !ok || value == nil {
			return nil
		}
		return append([]string{}, strList(value)...)
	}
	return nil
}

// replayThinkLevels rebuilds a session's thinking overrides from its log: the
// start-time coordinator level, then each thinking_level_changed in order.
func replayThinkLevels(events []event.Event) map[string]string {
	levels := map[string]string{}
	set := func(name, level string) {
		if _, ok := thinkingForLevel(level); ok && name != "" {
			levels[name] = level
		}
	}
	for _, ev := range events {
		switch ev.Type {
		case event.SessionStarted:
			coord, _ := ev.Data["coordinator"].(string)
			level, _ := ev.Data["coordinator_thinking"].(string)
			set(coord, level)
		case event.ThinkingLevelChanged:
			to, _ := ev.Data["to"].(string)
			for _, name := range strList(ev.Data["models"]) {
				level := to
				switch perModel := ev.Data["levels"].(type) {
				case map[string]string:
					if l := perModel[name]; l != "" {
						level = l
					}
				case map[string]any:
					if l, _ := perModel[name].(string); l != "" {
						level = l
					}
				}
				set(name, level)
			}
		}
	}
	return levels
}

// seedThinkLevels installs thinking overrides before the session runs and
// applies them to the startup implementer and reviewer specs.
func (s *Session) seedThinkLevels(levels map[string]string) {
	if len(levels) == 0 {
		return
	}
	s.mu.Lock()
	if s.thinkLevels == nil {
		s.thinkLevels = map[string]string{}
	}
	for name, level := range levels {
		s.thinkLevels[name] = level
	}
	s.mu.Unlock()
	apply := func(spec orchestrator.AgentSpec) orchestrator.AgentSpec {
		if _, ok := levels[spec.Name]; ok {
			th := s.thinkingFor(spec.Name)
			spec.Thinking, spec.Effort, spec.ThinkingDisplay = th.Thinking, th.Effort, th.ThinkingDisplay
		}
		return spec
	}
	s.deps.SetImplementer(apply(s.deps.Implementer))
	reviewers := make([]orchestrator.AgentSpec, len(s.deps.Reviewers))
	for i, spec := range s.deps.Reviewers {
		reviewers[i] = apply(spec)
	}
	s.deps.SetReviewers(reviewers)
}

// thinkingForLevel maps a session thinking level to engine.Thinking. "off"
// disables reasoning entirely; any effort level enables adaptive thinking at that
// effort with summarized display. The bool reports whether the level is valid.
func thinkingForLevel(level string) (engine.Thinking, bool) {
	switch level {
	case "off":
		return engine.Thinking{}, true
	case "low", "medium", "high", "xhigh", "max":
		return engine.Thinking{Thinking: "adaptive", Effort: level, ThinkingDisplay: "summarized"}, true
	default:
		return engine.Thinking{}, false
	}
}

// thinkingFor resolves reasoning for a logical model using the documented
// precedence: per-model session override → per-model config →
// package defaults.
func (s *Session) thinkingFor(name string) engine.Thinking {
	s.mu.Lock()
	level := s.thinkLevels[name]
	s.mu.Unlock()
	if level != "" {
		th, _ := thinkingForLevel(level)
		return th
	}
	th := s.reg.ThinkingFor(name)
	return engine.Thinking{Thinking: th.Thinking, Effort: th.Effort, ThinkingDisplay: th.ThinkingDisplay}
}

// Agent specs defer backend/credential construction until use. Their factory
// has no error return, so failedTurner reports a construction failure on the
// first turn rather than returning a nil backend.
type failedTurner struct{ err error }

func (t failedTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return nil, t.err
}

func (s *Session) allowsSubagentModel(name string) bool {
	return s.subagentModels == nil || slices.Contains(s.subagentModels, name)
}

// agentModels snapshots eligible models for spawn_agent's tool schema.
// The resolved agent spec is still checked against the live registry at spawn.
func (s *Session) agentModels() []orchestrator.ModelCatalogEntry {
	infos := s.reg.Models()
	entries := make([]orchestrator.ModelCatalogEntry, 0, len(infos))
	for _, info := range infos {
		if info.Disabled || !s.allowsSubagentModel(info.Name) {
			continue
		}
		th := s.thinkingFor(info.Name)
		entries = append(entries, orchestrator.ModelCatalogEntry{
			Name: info.Name, Backend: info.Backend, Model: info.Model, Auth: info.Auth,
			Notes: info.Notes, Modalities: info.Modalities, ContextWindow: info.ContextWindow,
			Thinking: th.Thinking, Effort: th.Effort, Priced: info.Pricing.Configured,
			PriceInput: info.Pricing.Input, PriceOutput: info.Pricing.Output,
			PriceInputKnown: info.PriceInputKnown, PriceOutputKnown: info.PriceOutputKnown,
		})
	}
	return entries
}

// resolveSubagentSpec enforces the session-local generic-spawn allowlist before
// resolving a live model spec. Role-based implementer/reviewer paths do not use it.
func (s *Session) resolveSubagentSpec(name string) (orchestrator.AgentSpec, error) {
	if !s.allowsSubagentModel(name) {
		return orchestrator.AgentSpec{}, fmt.Errorf("model %q is not allowed for generic subagents in this session", name)
	}
	return s.agentSpec(name)
}

// agentSpec resolves metadata without constructing a backend or resolving credentials.
// Availability is checked now and again by the factory when the role is used.
func (s *Session) agentSpec(name string) (orchestrator.AgentSpec, error) {
	model, ok := s.reg.GetModel(name)
	if !ok {
		return orchestrator.AgentSpec{}, fmt.Errorf("unknown model %q", name)
	}
	if model.Disabled {
		return orchestrator.AgentSpec{}, fmt.Errorf("%w %q", config.ErrModelDisabled, name)
	}
	th := s.thinkingFor(name)
	contextWindow, contextSafeFraction := s.reg.ContextBudget(name)
	return orchestrator.AgentSpec{
		Name:                name,
		Model:               model.Model,
		Backend:             model.Backend,
		ContextWindow:       contextWindow,
		ContextSafeFraction: contextSafeFraction,
		NewClient: func() engine.Turner {
			c, _, err := s.reg.Build(name)
			if err != nil {
				return failedTurner{err: fmt.Errorf("build backend %q: %w", name, err)}
			}
			return c
		},
		Thinking:        th.Thinking,
		Effort:          th.Effort,
		ThinkingDisplay: th.ThinkingDisplay,
	}, nil
}

// resolveReviewTier turns a requested tier name into a concrete ReviewPlan,
// resolving the configured tier to reviewer agent specs. Each
// reviewer carries its tier label and its extra focus prompt, so one tier can
// task several models (or the same model twice) with different review lenses.
// Unknown/disabled models are skipped; an empty tier falls back to the session's
// reviewer assignment. If no independent reviewer is available, fail explicitly
// rather than silently converting the request into author self-review.
func (s *Session) resolveReviewTier(requested string) orchestrator.ReviewPlan {
	td := s.reg.ReviewTier(requested)
	plan := orchestrator.ReviewPlan{Tier: td.Name, Requested: requested, Fallback: td.Fallback}
	if td.SelfReview {
		plan.SelfReview = true
		return plan
	}
	reviewers := td.Reviewers
	if len(reviewers) == 0 {
		reviewers = s.currentReviewers()
	}
	for _, rv := range reviewers {
		spec, err := s.reviewerSpec(rv)
		if err != nil {
			continue // skip unbuildable/unknown model — degrade gracefully
		}
		plan.Specs = append(plan.Specs, spec)
	}
	if len(plan.Specs) == 0 {
		for _, rv := range s.currentReviewers() {
			if spec, err := s.reviewerSpec(rv); err == nil {
				plan.Specs = append(plan.Specs, spec)
			}
		}
		if len(plan.Specs) == 0 {
			plan.Err = fmt.Errorf("no enabled independent reviewer is available for tier %q; enable/configure a reviewer or report that the required review is blocked", plan.Tier)
		}
	}
	return plan
}

// currentReviewers is the session's live reviewer role assignment expressed as
// plain (unfocused) reviewer slots — the fallback whenever a tier names no
// reviewers of its own.
func (s *Session) currentReviewers() []config.ResolvedReviewer {
	s.mu.Lock()
	names := append([]string(nil), s.reviewers...)
	s.mu.Unlock()
	out := make([]config.ResolvedReviewer, 0, len(names))
	for _, n := range names {
		out = append(out, config.ResolvedReviewer{Label: n, Model: n})
	}
	return out
}

// reviewerSpec builds the agent spec for one resolved reviewer slot: its logical
// model's backend, its tier label, its focus prompt, and its reasoning level
// (a per-reviewer `thinking` overrides the model-level resolution).
func (s *Session) reviewerSpec(rv config.ResolvedReviewer) (orchestrator.AgentSpec, error) {
	spec, err := s.agentSpec(rv.Model)
	if err != nil {
		return orchestrator.AgentSpec{}, err
	}
	spec.Label = rv.Label
	spec.Focus = rv.Prompt
	if rv.Thinking != "" {
		if th, ok := thinkingForLevel(rv.Thinking); ok {
			spec.Thinking, spec.Effort, spec.ThinkingDisplay = th.Thinking, th.Effort, th.ThinkingDisplay
		}
	}
	return spec, nil
}

// reviewTiers exposes the project's effective review tiers to the coordinator's
// spawn_reviewers tool description so custom tiers are discoverable by the model.
func (s *Session) reviewTiers() []orchestrator.ReviewTierInfo {
	tiers := s.reg.ReviewTiers()
	out := make([]orchestrator.ReviewTierInfo, 0, len(tiers))
	for _, t := range tiers {
		info := orchestrator.ReviewTierInfo{
			Name:        t.Name,
			Description: t.Description,
			Default:     t.Default,
			SelfReview:  t.SelfReview,
		}
		for _, rv := range t.Reviewers {
			if rv.Label != rv.Model {
				info.Reviewers = append(info.Reviewers, rv.Label+" ("+rv.Model+")")
				continue
			}
			info.Reviewers = append(info.Reviewers, rv.Model)
		}
		out = append(out, info)
	}
	return out
}

func (s *Session) run() {
	// Deferred first so it runs last: teardown that joins the run owner also
	// observes the job kill below as finished.
	if s.runDone != nil {
		defer close(s.runDone)
	}
	// Kill every background job when the coordinator loop's lifetime ends, so a
	// session leaves no orphan processes. Also release an RPC waiting on a rollover
	// request that cancellation prevented this owner from reaching.
	defer s.killJobs()
	defer s.rejectPendingRollover(fmt.Errorf("session %s ended before context rollover", s.ID))
	workImplementation := (config.Work{}).ResolvedImplementation()
	if s.deps != nil {
		workImplementation = (config.Work{Implementation: s.deps.WorkImplementation}).ResolvedImplementation()
	}
	if s.resumed {
		// Reopened session ("resume = replay"): the loop already
		// carries a history reconstructed from the existing log, so do NOT emit a
		// fresh SessionStarted / initial UserInput nor seed. Mark the reopen in the
		// continuous log.
		reopened := map[string]any{"work_implementation": workImplementation}
		if s.resumeQuestion != nil {
			// Tell clients the question replayed just before this marker is still
			// live (restored), not a stale gate to drop.
			reopened["question_restored"] = true
		}
		if s.resumeWait != nil {
			reopened["wait_restored"] = true
		}
		// Recovery metadata (docs/design/daemon-restart.md §3); omitted fields
		// keep the legacy meaning (nothing survived the reopen).
		if len(s.reopenSurviving) > 0 {
			reopened["surviving_jobs"] = s.reopenSurviving
		}
		awaiting := s.reopenAwaitingJobs
		if awaiting {
			reopened["awaiting_jobs"] = true
		}
		// Every message on the correction queue precedes the marker, whether the
		// reopen put it back or it arrived since publication: list them all so
		// a later reopen keeps them if the log is cut before their delivery.
		// steerMu orders the marker against a correction being accepted.
		s.steerMu.Lock()
		var requeued []int
		for _, c := range s.corrections {
			requeued = append(requeued, c.seq)
		}
		if len(requeued) > 0 {
			reopened["requeued_inputs"] = requeued
		}
		s.emitter.Emit(event.SessionReopened, reopened)
		s.steerMu.Unlock()
		s.recovery.ReleaseMarker()
		for _, notice := range s.startupNotices {
			s.emitter.Emit(event.SessionNotice, map[string]any{"msg": notice, "level": "warning"})
		}
		if s.ctx.Err() != nil {
			return
		}
		// The log ended while ask_user waited for the user: wait for that answer
		// first. It replaces the replay placeholder, so the history then owes a
		// model response and falls through to the run loop below.
		if rq := s.resumeQuestion; rq != nil {
			s.resumeQuestion = nil
			if !s.awaitRestoredQuestion(rq) {
				return
			}
		}
		// The log ended inside wait: finish it on its recorded targets and
		// deadline. Its result is the model's pending input, so the history then
		// owes a model response and falls through to the run loop below, whose
		// first checkpoint delivers any messages queued while it waited.
		if rw := s.resumeWait; rw != nil {
			s.resumeWait = nil
			if !s.awaitRestoredWait(rw) {
				return
			}
		}
		// If the reconstructed history ends mid-turn — the model still owes a
		// response to a user message or to tool results (e.g. the session was
		// reopened after tools ran but before the model replied, or a dangling
		// tool call was repaired with a synthetic result) — fall through to the
		// run loop so the model responds FIRST and the conversation reaches a clean
		// idle state. Waiting for new input here and Post-ing it would place two
		// non-assistant turns back to back: Anthropic renders tool results as
		// user-role messages, so a tool result (or bare user turn) immediately
		// followed by a fresh user message is two consecutive user turns, which
		// backends reject with a 400 invalid_request_error.
		//
		// Exception: a durable model-switch gate (the compact view also
		// overflowed this coordinator) means re-running would blindly resend the
		// same oversized request. Park in error until an actual coordinator
		// switch (which nudges the retry) or a successful explicit rollover.
		if s.contextModelSwitchRequired() {
			s.setStatus(event.StatusError)
			if !s.awaitNextRun(false) {
				return
			}
		} else if awaiting {
			// The session was idle waiting on its background jobs: their
			// completion (a downtime finish at once) resumes it as before.
			s.setIdle(true)
			if !s.awaitNextRun(true) {
				return
			}
		} else if s.reopenParked || !s.currentLoop().PendingResponse() || s.forkAwaitsInput {
			s.setStatus(event.StatusIdle)
			if !s.awaitNextRun(false) {
				return
			}
		}
	} else {
		s.mu.Lock()
		coord := s.coordinator
		s.mu.Unlock()
		started := map[string]any{
			"workspace":            s.Workspace,
			"mode":                 s.Mode,
			"origin":               s.origin,
			"preset":               s.preset,
			"coordinator_explicit": s.coordinatorExplicit,
			"work_implementation":  workImplementation,
			// The coordinator model is recorded so a resume replays the session on
			// the model it was started with, including a per-session override
			// picked at StartSession.
			"coordinator": coord,
		}
		if s.subagentModels != nil {
			started["subagent_models"] = s.subagentModels
		}
		if s.coordinatorThinking != "" {
			started["coordinator_thinking"] = s.coordinatorThinking
		}
		s.emitter.Emit(event.SessionStarted, started)
		s.planMu.Lock()
		if s.planOnly {
			s.emitter.EmitAs("user", event.PlanOnlyChanged, map[string]any{"plan_only": true})
		}
		s.planMu.Unlock()
		for _, notice := range s.startupNotices {
			s.emitter.Emit(event.SessionNotice, map[string]any{"msg": notice, "level": "warning"})
		}
		if s.ctx.Err() != nil {
			return
		}
		// Mark the launch echo so automatic prompts cannot count as human input,
		// even if a real input races ahead of startup. Pictures are recorded as
		// metadata plus retained-payload references; model replay still gets the
		// explicit unavailable-in-history note rather than reinjected pixels.
		initial := map[string]any{"text": s.prompt, "opening": true}
		if meta := imageMetadata(s.promptImages); meta != nil {
			initial["images"] = meta
		}
		s.emitter.EmitAs("user", event.UserInput, initial)
		if s.loopContinuation != "" {
			s.emitter.Emit(event.LoopContinuation, map[string]any{"text": s.loopContinuation})
		}
		// When the opening prompt named one existing task, Start already executed
		// the routine backlog reads and appended their synthetic exchange to model
		// history. Record that exact exchange immediately after the real user input
		// so event replay reconstructs the same ordering.
		s.startupPreload.Emit(s.emitter, s.loop.ModelName, s.loop.Backend, s.loop.Model)
		if s.logFailure() != nil || s.ctx.Err() != nil {
			return
		}
	}

	contextRecoveryUsed := false
	for {
		if s.ctx.Err() != nil {
			return
		}
		s.setStatus(event.StatusRunning)
		// A starting run clears the refusal gate: this iteration IS the retry
		// (via Resume, a model switch, or reopen re-running the pending turn).
		// If the provider refuses again the gate is simply re-established below.
		s.setRefused(false)
		s.takeSwitchRetry()
		s.steerMu.Lock()
		s.running = true
		s.steerMu.Unlock()
		res, err := s.currentLoop().Run(s.ctx)
		rolloverInputReady := false
		// jobWake gates completion-driven continuation for THIS iteration's
		// outcome; only a plain finished turn (set below) enables it.
		jobWake := false
		// Clear running BEFORE checking ctx/handling the result so a SendInput
		// racing the end of the run either landed as a correction (drained just
		// below) or, seeing running=false, takes the idle input-queue path.
		s.steerMu.Lock()
		s.running = false
		s.steerMu.Unlock()
		if s.ctx.Err() != nil {
			return
		}
		if err == nil {
			contextRecoveryUsed = false
		}
		if err != nil && engine.IsContextLengthError(err) {
			// The rejected request made no model/tool mutation. Recover at this full-
			// batch boundary exactly once, and only continue after the smaller selected
			// view has been durably recorded. An explicit request already queued for this
			// boundary owns the same single attempt; never apply it and then auto-roll a
			// second time.
			if !contextRecoveryUsed {
				contextRecoveryUsed = true
				var rolloverErr error
				select {
				case req := <-s.rolloverCh:
					rolloverErr = s.performRolloverRequest(req, "explicit")
					rolloverInputReady, rolloverErr = s.releaseRolloverInputs(rolloverErr, false)
					req.complete(rolloverErr)
				default:
					rolloverInputReady, rolloverErr = s.automaticOverflowRollover()
				}
				// Cancellation (Stop/reap) is not a recovery failure: never leave a
				// durable model-switch gate behind for reopen, which retries instead.
				if s.ctx.Err() != nil {
					return
				}
				if rolloverErr == nil {
					continue
				}
				err = fmt.Errorf("%v; automatic context recovery failed: %w", err, rolloverErr)
			}
			s.setStatus(event.StatusError)
			s.rejectPendingRollover(fmt.Errorf("the compact coordinator context still exceeds this model's window; switch the coordinator to a model with a larger context window or start a new session with narrower authorized input"))
			s.emitter.Emit(event.SessionError, map[string]any{
				"msg":  err.Error() + "; context cannot be safely reduced further — switch the coordinator to a model with a larger context window or start a new session with narrower authorized input",
				"kind": string(engine.KindContextLength), "retryable": false,
				"action": "switch_model",
			})
			// Park behind the durable gate. Input released into the view above is
			// retained (durably delivered) but must not trigger another request on
			// the unchanged oversized view; an idle-path sender must also see
			// running=false so a later coordinator switch can wake this owner.
			rolloverInputReady = false
			s.steerMu.Lock()
			s.running = false
			s.steerMu.Unlock()
		} else if err != nil {
			s.setStatus(event.StatusError)
			// A model-turn failure was already recorded by the engine loop as a
			// structured session_error (engine.TurnError marks that); emitting it
			// again here would double-log every API failure. Other Run errors
			// (max turns, truncation give-up, ...) are recorded here.
			var te *engine.TurnError
			if !errors.As(err, &te) {
				s.emitter.Emit(event.SessionError, map[string]any{"msg": err.Error()})
			}
		} else if res.Refused {
			// Provider-side safety refusal (engine Result.Refused):
			// the refused turn was kept out of history, so the conversation
			// still owes a response and the next Run re-runs the pending turn.
			// Park in StatusError with an explanatory session_error (kind
			// "refusal") and gate SendInput until a retry: Resume() re-runs
			// as-is; a coordinator model change via SetRoleConfig retries
			// automatically (the provider-documented recovery — same-model
			// retries usually refuse again).
			s.setRefused(true)
			s.setStatus(event.StatusError)
			s.emitter.Emit(event.SessionError, map[string]any{
				"msg":  "the model's provider refused to respond (safety classifier, stop_reason \"refusal\"). Continuing this conversation as-is will keep being refused — switch the coordinator to a different model (retries the turn automatically) or retry.",
				"kind": "refusal",
			})
		} else if res.NextMode != "" && res.NextMode != s.Mode {
			// A control tool requested a mode transition within this session. A
			// carried prompt (e.g. the pm → work hand-off) seeds the new loop
			// verbatim so it carries the target task + planning context; otherwise
			// fall back to a generic per-mode transition prompt.
			seed := res.NextPrompt
			if seed == "" {
				seed = modeTransitionPrompt(res.NextMode)
			}
			if next, berr := s.buildLoop(res.NextMode, seed); berr == nil {
				s.emitter.Emit(event.ModeChanged, map[string]any{"from": s.Mode, "to": res.NextMode})
				if s.logFailure() != nil || s.ctx.Err() != nil {
					return
				}
				s.Mode = res.NextMode
				s.setLoop(next)
				// The completed control result is authoritative. A rollover that
				// raced it must not replace its handoff or manufacture a mutation turn.
				s.rejectPendingRollover(fmt.Errorf("coordinator completed a mode handoff before the rollover checkpoint; context was not changed"))
				continue // run the new mode's loop immediately (its first checkpoint drains any pending corrections)
			} else {
				s.emitter.Emit(event.SessionError, map[string]any{"msg": "mode switch failed: " + berr.Error()})
			}
		} else {
			// A plain final response is the only outcome that background work may
			// resume by itself. Blocked reports, errors, refusals, explicit pauses,
			// and a just-reopened session all keep their user boundary. Status and
			// this decision are recorded together so an observer that sees idle also
			// sees whether the session is still awaiting delegated work.
			jobWake = !res.Blocked
			s.setIdle(jobWake)
			data := map[string]any{"report": s.withAssumptions(res.Report)}
			if _, awaiting := s.StatusWithJobContinuation(); awaiting {
				data["awaiting_jobs"] = true
			}
			if res.Blocked {
				data["blocked"] = true
			}
			s.emitter.Emit(event.SessionIdle, data)
			if s.logFailure() != nil || s.ctx.Err() != nil {
				return
			}
			// Usage attribution remains authoritative in the session event log.
		}
		if s.logFailure() != nil || s.ctx.Err() != nil {
			return
		}
		// A request that missed Run's final full-turn checkpoint cannot take
		// precedence over the result just recorded above (finish/block/refusal/error).
		// Reject it; a later request made while truly idle is handled below.
		s.rejectPendingRollover(fmt.Errorf("coordinator completed the turn before the rollover checkpoint; context was not changed"))
		if rolloverInputReady {
			continue
		}

		// Steer-by-default race: input that arrived after the run's final
		// checkpoint but before running was cleared is buffered as corrections.
		// Deliver it now (Post + user_input_delivered) and continue the loop so
		// the model sees it, rather than dropping into the idle wait below.
		s.steerMu.Lock()
		corr := s.corrections
		s.corrections = nil
		s.steerMu.Unlock()
		if len(corr) > 0 {
			messages, err := s.deliverCorrections(corr)
			if err != nil {
				return
			}
			for _, input := range messages {
				s.currentLoop().PostMessage(input)
			}
			// Retained in the view, but a durable model-switch gate forbids
			// resending the unchanged oversized request until a switch.
			if !s.contextModelSwitchRequired() {
				continue
			}
		}

		if !s.awaitNextRun(jobWake) {
			return
		}
	}
}

// awaitNextRun parks the run owner between turns until something warrants
// another model turn, reporting false when the session ended. While the durable
// context model-switch gate is active nothing may resend the unchanged
// oversized view: input that raced the gate is retained in history (its durable
// non-queued echo replays identically), and only an actual coordinator switch
// (SetRoleConfig latches a retry and signals switchRetryCh) or a successful
// explicit rollover proceeds.
func (s *Session) awaitNextRun(jobWake bool) bool {
	for {
		// Background work that finished after the last checkpoint (including
		// while this session was becoming idle) continues the conversation
		// instead of stranding its report until the user types. Checked before
		// blocking so a completion that raced the transition is never missed.
		if jobWake {
			resumed, err := s.resumeForFinishedJobs()
			if err != nil {
				return false
			}
			if resumed {
				return true
			}
		}
		var completed <-chan struct{}
		if jobWake {
			completed = s.jobCompletions()
		}
		select {
		case input := <-s.messageCh:
			if s.ctx.Err() != nil {
				return false
			}
			s.currentLoop().PostMessage(input)
			if s.contextModelSwitchRequired() {
				continue
			}
			return true
		case <-completed:
			if s.ctx.Err() != nil {
				return false
			}
			// Re-check at the top: the completion may already have been claimed
			// by a wait, or delivery may currently be held by a pause/rollover.
		case <-s.retryCh:
			if s.ctx.Err() != nil {
				return false
			}
			if s.contextModelSwitchRequired() {
				continue
			}
			// Retry after a session error: mark the retry as resumed only when the
			// parked run loop actually consumes it, then re-run the failed turn on
			// existing history without injecting a user message.
			s.emitter.Emit(event.Resumed, map[string]any{})
			return true
		case <-s.switchRetryCh:
			if s.ctx.Err() != nil {
				return false
			}
			// A stale token (its latch already consumed by a started run) is ignored.
			if s.contextModelSwitchRequired() || !s.takeSwitchRetry() {
				continue
			}
			s.emitter.Emit(event.Resumed, map[string]any{})
			return true
		case req := <-s.rolloverCh:
			if s.ctx.Err() != nil {
				s.steerMu.Lock()
				s.rolloverPending = false
				s.steerMu.Unlock()
				req.complete(fmt.Errorf("session %s was cancelled before context rollover", s.ID))
				return false
			}
			absorbed := s.absorbBufferedIdleInputs()
			rErr := s.performRolloverRequest(req, "explicit")
			var released bool
			released, rErr = s.releaseRolloverInputs(rErr, absorbed)
			req.complete(rErr)
			if rErr == nil {
				s.emitter.Emit(event.Resumed, map[string]any{"reason": "context_rollover"})
				return true
			}
			// On failure the selected view stays unchanged, but independently
			// accepted input must still run rather than remain a queued orphan —
			// unless the model-switch gate forbids resending this view.
			if (absorbed || released) && !s.contextModelSwitchRequired() {
				return true
			}
			if !s.contextModelSwitchRequired() {
				continue
			}
			// Gated: input stays retained in the view; keep idle senders on the
			// idle path so they (or a coordinator switch) can still wake this
			// owner. A sender that saw the brief running window was queued: retain
			// it durably too.
			s.steerMu.Lock()
			s.running = false
			corr := s.corrections
			s.corrections = nil
			s.steerMu.Unlock()
			messages, err := s.deliverCorrections(corr)
			if err != nil {
				return false
			}
			for _, input := range messages {
				s.currentLoop().PostMessage(input)
			}
		case <-s.ctx.Done():
			return false
		}
	}
}

// takeSwitchRetry consumes the latched post-switch retry, reporting whether one
// was owed.
func (s *Session) takeSwitchRetry() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	owed := s.switchRetry
	s.switchRetry = false
	return owed
}

// defaultPrompt is the starting instruction for a mode when the user gives none.
// presetPrompt returns a known preset's opening prompt when it applies to mode,
// so a client can start a preset session (e.g. a one-tap "groom memory") by
// name without carrying the prompt text. Unknown presets yield "".
func presetPrompt(preset, mode string) string {
	for _, p := range orchestrator.Presets() {
		if p.Name == preset && p.Mode == mode {
			return p.Prompt
		}
	}
	return ""
}

func defaultPrompt(mode string) string {
	switch mode {
	case "chat":
		return "Briefly introduce yourself as the ycc assistant and ask what I'd like to work on."
	case "pm":
		return "Review the project's design docs (start from the spec entry point) and the existing backlog against the current codebase, and ask me what I'd like to plan, document, or groom."
	default: // work
		return "Work on the backlog: choose the next ready task (one whose dependencies are all done) and complete it."
	}
}

func modeTransitionPrompt(mode string) string {
	if mode == "work" {
		return "You are now in work mode. Begin working the backlog: pick the next ready task (one whose dependencies are all done) and drive it to completion."
	}
	return "You are now in " + mode + " mode."
}

// withAssumptions appends any assumptions recorded by unattended ask_user
// calls to the final report.
func (s *Session) withAssumptions(report string) string {
	as := s.inter.Assumptions()
	if len(as) == 0 {
		return report
	}
	var b []byte
	b = append(b, report...)
	b = append(b, "\n\nAssumptions made without consulting the user (unattended execution):\n"...)
	for _, a := range as {
		b = append(b, "- "+a+"\n"...)
	}
	return string(b)
}

// Manager owns the set of live sessions and the backend registry used to build
// their agent loops.
type Manager struct {
	mu                sync.Mutex
	sessions          map[string]*Session
	followUpMu        sync.Mutex
	readMarkMu        sync.Mutex
	quickReplies      quickReplyStore
	publishMu         sync.Mutex     // guards publications
	publishOpMu       sync.Mutex     // serializes PublishFile's check-then-upload
	publications      *publish.Store // nil until first use: memory-only
	historyCacheMu    sync.Mutex
	historyCache      map[string]sessionSummaryCacheEntry
	usageScans        *usage.Scanner // per-log usage reductions for UsageReport
	reg               *config.Registry
	projects          *project.Registry
	workstreams       *workstream.Registry
	worktreesRoot     string
	ownership         *workspacelease.Service
	idAlloc           *docs.IDAllocator
	workstreamWatches map[string]workstreamWatch
	integrators       map[string]*workstreamIntegrator
	integrationWG     sync.WaitGroup
	integrationCtx    context.Context
	integrationCancel context.CancelFunc
	integrationStop   bool
	// integrateAgent is the injectable recovery seam. The default starts a real
	// unattended integrate-mode session in the workstream's linked worktree.
	integrateAgent func(workstream.Workstream, int, integrationOutcome) integrateAgentResult
	// notifier pushes best-effort daemon-side notifications when an agent needs
	// the user. Nil when unconfigured; all uses are nil-safe.
	notifier *notify.Notifier
	// jobLauncher, when set, runs sessions' background Bash through detached
	// supervisors (internal/jobshim). Nil (the default, and every production
	// daemon until restart reconciliation lands) keeps direct child processes.
	jobLauncher *jobshim.Launcher
	// passiveJobs holds the supervised jobs of sessions that stayed closed
	// after a daemon restart (SupervisePassiveJobs) until each exits or its
	// session's reopen takes it over.
	passiveJobs *tools.PassiveJobs
	// ending counts, per session id, instances that left sessions but are
	// still being torn down (endingLocked). Guarded by mu.
	ending map[string]int
	// fencedWorktrees counts, per absolute worktree path, workstream merges,
	// integrations and discards settling it (settleWorkstreamExecution): no
	// session rooted there may be started or reopened meanwhile. Guarded by mu.
	fencedWorktrees map[string]int
	// workstreamSpawnMu makes the max_parallel check and subsequent registration
	// atomic with respect to other spawns.
	workstreamSpawnMu sync.Mutex
	// mergeMu serializes MergeWorkstream across all workstreams so integrations
	// happen one at a time and each trial-merges against the latest base HEAD
	// (sequential reconciliation).
	mergeMu sync.Mutex

	// claimMu serializes task claims (claimTask) so checking for another
	// holder and recording this session's focus are one step.
	claimMu sync.Mutex

	// workLoops holds the daemon-side work loops keyed by resolved absolute
	// workspace. Guarded by loopMu, a dedicated mutex so loop
	// bookkeeping never contends with the session-map mu that Start/reclaim take.
	loopMu     sync.Mutex
	workLoops  map[string]*workLoop
	loopCtx    context.Context
	loopCancel context.CancelFunc
	loopWG     sync.WaitGroup
	loopStop   bool
	// newRunSession, when non-nil, overrides the loop's session runner (the test
	// seam that drives control logic without a live model). Nil => the real runner.
	newRunSession func(*workLoop) func(context.Context) (loopSessRec, bool, error)
	// newLoopWait, when non-nil, overrides provider retry waiting. Tests use it to
	// exercise multi-hour patience schedules without sleeping.
	newLoopWait func(*workLoop) func(time.Duration) bool

	// gitSync owns the periodic, networked fetch loop and its small per-workspace
	// cache. Local status itself is computed on demand and never performs network
	// I/O.
	gitSyncMu       sync.RWMutex
	gitSyncCache    map[string]gitFetchState
	gitSyncInterval time.Duration
	gitSyncWake     chan struct{}
	gitSyncCtx      context.Context
	gitSyncCancel   context.CancelFunc
	gitSyncWG       sync.WaitGroup

	// recency orders project pickers most-recently-used first
	// (projectrecency.go).
	recency projectRecency

	// overlapSeen records each (worktree, path, session pair) already told of
	// a same-file overlap (coordination.go), so a pair hears of a path once.
	overlapMu   sync.Mutex
	overlapSeen map[string]bool
	// boards caches each project's notice board (noticeboard.go), keyed by
	// canonical primary tree.
	boardMu sync.Mutex
	boards  map[string]boardState
	// leases holds each project's exclusive leases (leases.go), keyed by
	// canonical primary tree and lease name. Job-bound ones are also saved in
	// leasesFile (leases.json; leasesSaved is its last written content) to
	// survive a daemon restart or crash. leasesClosed stops lease activity at
	// manager shutdown; leaseWG joins closed holders' release records.
	leaseMu      sync.Mutex
	leases       map[string]map[string]*leaseEntry
	leasesFile   string
	leasesSaved  []byte
	leasesClosed bool
	leaseWG      sync.WaitGroup

	// Automatic memory grooming (memorygroom.go). grooms maps a primary tree to
	// its live automatic groom session id. groomMu is independent of mu so the
	// scheduler can start a session (which takes mu) while holding it.
	groomMu     sync.Mutex
	grooms      map[string]string
	groomWG     sync.WaitGroup
	groomCtx    context.Context
	groomCancel context.CancelFunc
	groomStop   bool
	// startMemoryGroom, when non-nil, replaces the real groom session launcher
	// (test seam).
	startMemoryGroom func(primary string, status docs.MemoryStatus) (*Session, error)
	// backlogGrooms maps a primary tree to its live automatic backlog groom
	// (backloggroom.go); it shares groomMu, groomWG and groomCtx with memory
	// grooms. startBacklogGroom is its launcher test seam.
	backlogGrooms     map[string]string
	startBacklogGroom func(primary, prompt string) (*Session, error)

	// Watches (watch.go). watchMu serializes all watch file I/O and guards the
	// in-memory run state; it may be held while a run session starts.
	watchMu       sync.Mutex
	watchBusy     map[string]bool   // watch key → probe or first pass active
	watchActive   map[string]string // watch key → session id of the run in its first pass
	watchFollowed map[string]bool   // run session id → follower attached
	watchWG       sync.WaitGroup
	watchCtx      context.Context
	watchCancel   context.CancelFunc
	watchWake     chan struct{}
	watchStop     bool
	watchStarted  bool
	watchRunning  atomic.Bool
	// Test seams: run launcher, clock, and follower poll interval.
	startWatchSession func(primary string, def WatchDefinition, runCtx string) (*Session, error)
	watchNow          func() time.Time
	watchPoll         time.Duration

	// Graceful daemon shutdown and phased startup (shutdown.go). shuttingDown
	// refuses new session registration and routes teardown through restart
	// suspension; execHeld (under mu) defers run owners prepared during startup
	// into heldRuns until ReleaseExecution.
	shuttingDown atomic.Bool
	execHeld     bool
	heldRuns     []*Session
	// idLocks serializes Reopen per session id (under mu).
	idLocks map[string]*idLock
	// The GC reaper (reaper.go) runs under reaperCtx and joins reaperWG.
	reaperCtx    context.Context
	reaperCancel context.CancelFunc
	reaperWG     sync.WaitGroup
}

// NewManager creates a session manager backed by the given model registry. It
// starts with an in-memory project registry; call SetProjects to back it with a
// persistent one. The workstream registry likewise defaults to an
// in-memory one; SetWorkstreams backs it with durable state.
func NewManager(reg *config.Registry, initialWorkspace string) *Manager {
	projects := project.NewMemory()
	if initialWorkspace != "" {
		// Every workspace is a normal named project. This also gives a one-shot
		// daemon exactly one listable project instead of a synthetic "Default".
		_, _ = projects.EnsureWorkspace(initialWorkspace)
	}
	integrationCtx, integrationCancel := context.WithCancel(context.Background())
	gitSyncCtx, gitSyncCancel := context.WithCancel(context.Background())
	loopCtx, loopCancel := context.WithCancel(context.Background())
	groomCtx, groomCancel := context.WithCancel(context.Background())
	watchCtx, watchCancel := context.WithCancel(context.Background())
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	m := &Manager{
		reaperCtx:         reaperCtx,
		reaperCancel:      reaperCancel,
		watchBusy:         map[string]bool{},
		watchActive:       map[string]string{},
		watchFollowed:     map[string]bool{},
		watchCtx:          watchCtx,
		watchCancel:       watchCancel,
		watchWake:         make(chan struct{}, 1),
		sessions:          map[string]*Session{},
		historyCache:      map[string]sessionSummaryCacheEntry{},
		usageScans:        usage.NewScanner(),
		reg:               reg,
		projects:          projects,
		workstreams:       workstream.NewMemory(),
		worktreesRoot:     workstream.DefaultWorktreesRoot(),
		ownership:         persistentOwnership(),
		passiveJobs:       tools.NewPassiveJobs(),
		idAlloc:           docs.AllocatorFor(""),
		workstreamWatches: map[string]workstreamWatch{},
		integrators:       map[string]*workstreamIntegrator{},
		integrationCtx:    integrationCtx,
		integrationCancel: integrationCancel,
		workLoops:         map[string]*workLoop{},
		loopCtx:           loopCtx,
		loopCancel:        loopCancel,
		gitSyncCache:      map[string]gitFetchState{},
		gitSyncInterval:   defaultGitSyncInterval,
		gitSyncWake:       make(chan struct{}, 1),
		gitSyncCtx:        gitSyncCtx,
		gitSyncCancel:     gitSyncCancel,
		grooms:            map[string]string{},
		backlogGrooms:     map[string]string{},
		groomCtx:          groomCtx,
		groomCancel:       groomCancel,
	}
	m.integrateAgent = m.runIntegrateSession
	m.ownership.SetOverlapNotifier(m.claimOverlap)
	m.ownership.SetSharedHeadPeers(m.sharedHeadPeers)
	// Bookkeeping lines of sessions that are no longer live ride along with
	// the next commit of that file rather than staying stranded.
	m.ownership.SetLiveScopes(func(scope string) bool {
		_, live := m.Get(scope)
		return live
	})
	m.gitSyncWG.Add(1)
	go m.runGitSyncPoller()
	return m
}

// SetProjects replaces the manager's project registry. Daemon construction is
// responsible for registering its startup workspace in the replacement registry.
func (m *Manager) SetProjects(p *project.Registry) {
	// NewManager starts the git poller before daemon construction replaces the
	// default registry, so synchronize the pointer handoff with that reader.
	m.gitSyncMu.Lock()
	m.projects = p
	m.gitSyncMu.Unlock()
}

// SetNotifier installs the daemon-side push notifier. A nil notifier
// (the default / unconfigured case) disables notifications; every session watcher
// and the Notify RPC are nil-safe.
func (m *Manager) SetNotifier(n *notify.Notifier) { m.notifier = n }

// SetJobLauncher opts sessions created afterwards into supervised background
// Bash jobs (docs/design/daemon-restart.md §2). Production Serve() does not
// call it yet: detached jobs need reopen reconciliation and crash-safe
// job-bound leases first. Nil restores direct child processes.
func (m *Manager) SetJobLauncher(l *jobshim.Launcher) { m.jobLauncher = l }

// Notify pushes a best-effort notification for the given kind via the configured
// notifier, returning whether it was delivered (false when no notifier is
// configured or the kind is muted). It backs the client-driven Notify RPC used for
// work-loop digests. Delivery is async and never blocks.
func (m *Manager) Notify(kind, project, sessionID, line string) bool {
	if !m.notifier.Enabled(kind) {
		return false
	}
	m.notifier.Send(kind, project, sessionID, line)
	return true
}

// projectLabel resolves a human project label for a session workspace: the
// registered project name when the workspace matches a known project path,
// otherwise the workspace's base name.
func (m *Manager) projectLabel(absWS string) string {
	for _, p := range m.projects.List() {
		if p.Path == absWS {
			return p.Name
		}
	}
	return filepath.Base(absWS)
}

// SetWorkstreams backs the manager with a (persistent) workstream registry and
// sets the root directory under which linked worktrees are created. An empty
// root keeps the current default.
func (m *Manager) SetWorkstreams(w *workstream.Registry, root string) {
	m.workstreams = w
	if root != "" {
		m.worktreesRoot = root
	}
}

// SetIDStateFile selects the shared backlog id allocator for this daemon. An
// empty path uses the process-wide in-memory allocator (the one-shot path).
func (m *Manager) SetIDStateFile(path string) { m.idAlloc = docs.AllocatorFor(path) }

// backlogStore constructs a Store whose id reservations are keyed by the
// registered project's primary backlog, even when absWS is a linked worktree.
func (m *Manager) backlogStore(absWS string) *docs.Store {
	if abs, err := filepath.Abs(absWS); err == nil {
		absWS = abs
	}
	absWS = filepath.Clean(absWS)
	primary := m.primaryTreeFor(absWS)
	store := docs.NewStore(absWS)
	store.SetIDSource(func() (string, error) {
		return m.idAlloc.NextID(filepath.Join(primary, "backlog"))
	})
	store.SetRepairLease(func() (func(), error) {
		token := m.ownership.NewToken("backlog duplicate repair for " + absWS)
		lease, err := m.ownership.Acquire(absWS, token)
		if err != nil {
			return nil, err
		}
		return lease.Release, nil
	})
	return store
}

// worktreeConfigFor resolves a project's bootstrap configuration. A non-empty
// [worktree] table in the primary tree wins as one complete unit; otherwise the
// daemon registry configuration is the fallback.
func (m *Manager) worktreeConfigFor(primary string) config.Worktree {
	if cfg, ok := config.LoadWorktree(primary); ok {
		return cfg
	}
	return m.reg.WorktreeConfig()
}

func sortedWorktreeEnv(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

// primaryTreeFor resolves a session workspace to its daemon-registered primary
// project tree. The worktrees-root fallback covers newly spawned workstreams,
// whose session starts immediately before their registry entry is added.
func (m *Manager) primaryTreeFor(absWS string) string {
	absWS = filepath.Clean(absWS)
	projects := m.projects.List()
	for _, p := range projects {
		if filepath.Clean(p.Path) == absWS {
			return filepath.Clean(p.Path)
		}
	}
	for _, ws := range m.workstreams.List() {
		if filepath.Clean(ws.WorktreePath) != absWS {
			continue
		}
		if primary, ok := m.projects.Resolve(ws.Project); ok {
			return filepath.Clean(primary)
		}
	}

	root := m.worktreesRoot
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	root = filepath.Clean(root)
	if rel, err := filepath.Rel(root, absWS); err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		projectDir := strings.Split(rel, string(filepath.Separator))[0]
		for _, p := range projects {
			if workstream.SafeProjectDir(p.Name) == projectDir {
				return filepath.Clean(p.Path)
			}
		}
	}
	return absWS
}

// Projects returns the registered projects (name + path) for ListProjects.
func (m *Manager) Projects() []project.Project { return m.projects.List() }

// resolveProjectWorkspace resolves a named project. An omitted name is accepted
// only when the daemon has exactly one project, as an unambiguous convenience;
// there is no hidden/default workspace alongside the registry.
func (m *Manager) resolveProjectWorkspace(name string) (string, error) {
	if name != "" {
		ws, ok := m.projects.Resolve(name)
		if !ok {
			return "", fmt.Errorf("%w %q", ErrUnknownProject, name)
		}
		return ws, nil
	}
	projects := m.projects.List()
	if len(projects) == 1 {
		return projects[0].Path, nil
	}
	if len(projects) == 0 {
		return "", fmt.Errorf("%w: no projects are registered", ErrUnknownProject)
	}
	return "", fmt.Errorf("%w: project is required when %d projects are registered", ErrUnknownProject, len(projects))
}

// ProjectWorkspace resolves a project (see resolveProjectWorkspace) to its
// absolute root workspace.
func (m *Manager) ProjectWorkspace(name string) (string, error) {
	ws, err := m.resolveProjectWorkspace(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(ws)
}

// AddProject registers a workspace under an optional name.
func (m *Manager) AddProject(path, name string) (project.Project, error) {
	return m.projects.Add(path, name)
}

// RemoveProject deregisters a project by name.
func (m *Manager) RemoveProject(name string) error { return m.projects.Remove(name) }

// RenameProject renames a registered project, carrying its workstreams to the
// new name. Live sessions and daemon work loops are keyed by the workspace's
// absolute path — name resolution happens per request — so they follow the
// rename with no further bookkeeping.
func (m *Manager) RenameProject(oldName, newName string) (project.Project, error) {
	p, err := m.projects.Rename(oldName, newName)
	if err != nil {
		return project.Project{}, err
	}
	if err := m.publicationStore().RenameProject(oldName, newName, time.Now()); err != nil {
		return p, fmt.Errorf("project renamed, but relabeling published files failed: %w", err)
	}
	if m.workstreams != nil {
		if err := m.workstreams.RenameProject(oldName, newName); err != nil {
			// The project rename itself committed; surface the label failure
			// rather than half-undoing (a retry of the rename is a no-op).
			return p, fmt.Errorf("project renamed, but relabeling workstreams failed: %w", err)
		}
	}
	return p, nil
}

// Start creates, persists, and launches a new session.
func (m *Manager) Start(cfg Config) (*Session, error) {
	s, err := m.start(cfg, true)
	if err == nil {
		// A project whose memory is already over budget is groomed even if no
		// agent writes to it; the scheduler's cooldown bounds this.
		m.memoryPressure(s.Workspace, s.ID)
		// Backlog churn since the last groom is likewise checked when work
		// starts, so a project nobody uses is never groomed.
		m.maybeGroomBacklog(s.Workspace)
	}
	return s, err
}

// validateSubagentModels rejects explicit generic-spawn allowlist entries that
// cannot be selected by a newly started session.
func (m *Manager) validateSubagentModels(models []string) error {
	for _, name := range models {
		if !m.reg.Has(name) {
			return fmt.Errorf("%w %q", ErrUnknownModel, name)
		}
		if !m.reg.Enabled(name) {
			return fmt.Errorf("%w %q", ErrDisabledModel, name)
		}
	}
	return nil
}

// initialCoordinator resolves the session-only coordinator selection. An
// explicit API override wins; otherwise a preset binding is applied. A stale
// preset binding safely falls back to the configured coordinator with a warning.
func (m *Manager) initialCoordinator(preset, explicit string) (coordinator, warning string, err error) {
	if explicit != "" {
		if !m.reg.Has(explicit) {
			return "", "", fmt.Errorf("%w %q", ErrUnknownModel, explicit)
		}
		if !m.reg.Enabled(explicit) {
			return "", "", fmt.Errorf("%w %q", ErrDisabledModel, explicit)
		}
		return explicit, "", nil
	}
	model, bound := m.reg.PresetModel(preset)
	if !bound {
		// newSession resolves the configured default, but classify a disabled
		// default here so StartSession reports a useful failed-precondition error.
		model = m.reg.CoordinatorName()
		if m.reg.Has(model) && !m.reg.Enabled(model) {
			return "", "", fmt.Errorf("%w %q", ErrDisabledModel, model)
		}
		return "", "", nil
	}
	if m.reg.Enabled(model) {
		return model, "", nil
	}
	fallback := m.reg.CoordinatorName()
	reason := "unknown"
	if m.reg.Has(model) {
		reason = "disabled"
	}
	if m.reg.Has(fallback) && !m.reg.Enabled(fallback) {
		return "", "", fmt.Errorf("%w %q (fallback for preset %q)", ErrDisabledModel, fallback, preset)
	}
	return "", fmt.Sprintf("preset %q is bound to %s model %q; using default coordinator %q", preset, reason, model, fallback), nil
}

// start is the shared session-launch body. When autoRegisterProject is true a
// not-yet-known workspace is auto-registered as a first-class project.
// Workstream sessions pass false so an ephemeral worktree path never
// pollutes the user-facing project picker.
func (m *Manager) start(cfg Config, autoRegisterProject bool) (*Session, error) {
	if m.shuttingDown.Load() {
		return nil, ErrShuttingDown
	}
	ws := cfg.Workspace
	// A named project resolves to its registered workspace, overriding ws. With
	// neither field, permit the sole registered project but never guess among
	// multiple projects.
	if cfg.Project != "" || ws == "" {
		var err error
		ws, err = m.resolveProjectWorkspace(cfg.Project)
		if err != nil {
			return nil, err
		}
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	wsKey := worktreeKey(absWS)
	if m.insideWorktree(wsKey) {
		return nil, fmt.Errorf("workspace %s is inside a workstream worktree: start the session at the worktree's root", absWS)
	}
	if err := m.validateSubagentModels(cfg.SubagentModels); err != nil {
		return nil, err
	}
	// Auto-register a not-yet-known workspace so it becomes a first-class,
	// listable project — unless this is a workstream worktree.
	if autoRegisterProject {
		if _, err := m.projects.EnsureWorkspace(absWS); err != nil {
			return nil, fmt.Errorf("register project: %w", err)
		}
	}
	mode := cfg.Mode
	if mode == "" {
		mode = "work"
	}
	if cfg.PlanOnly && mode != "chat" {
		return nil, ErrPlanOnlyMode
	}
	// The prompt is optional: an empty one (e.g. "work" mode with no suggested
	// task) gets a sensible per-mode default so the agent has a starting point.
	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		prompt = presetPrompt(cfg.Preset, mode)
	}
	if prompt == "" {
		prompt = defaultPrompt(mode)
	}

	// Resolve an explicit or preset coordinator before creating the log. Explicit
	// bad input remains an error; stale config bindings safely produce a warning.
	coord, startupNotice, err := m.initialCoordinator(cfg.Preset, cfg.CoordinatorModel)
	if err != nil {
		return nil, err
	}
	if err := validStartThinking(cfg.CoordinatorThinking); err != nil {
		return nil, err
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(absWS, ".ycc", "sessions", id, "events.jsonl")
	log, err := event.OpenLog(logPath)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}

	s, err := m.newSession(absWS, id, mode, cfg.Unattended, prompt, log, false, coord)
	if err != nil {
		log.Close()
		return nil, err
	}
	s.subagentModels = slices.Clone(cfg.SubagentModels)
	s.deps.DisableGenericAgents = s.subagentModels != nil && len(s.subagentModels) == 0
	s.origin = cfg.Origin
	if s.origin == "" {
		s.origin = OriginUser
		if cfg.Unattended {
			s.origin = OriginAutomation
		}
	}
	s.participation.origin = s.origin
	s.participation.knownOrigin = knownSessionOrigin(s.origin)
	s.preset = cfg.Preset
	s.coordinatorExplicit = cfg.CoordinatorModel != ""
	if cfg.CoordinatorThinking != "" {
		s.coordinatorThinking = m.reg.ClampThinkingLevelFor(s.coordinator, cfg.CoordinatorThinking)
		s.seedThinkLevels(map[string]string{s.coordinator: s.coordinatorThinking})
	}
	if startupNotice != "" {
		s.startupNotices = append([]string{startupNotice}, s.startupNotices...)
	}
	if cfg.PlanOnly {
		// run records plan_only_changed after session_started; the first
		// checkpoint then tells the coordinator.
		s.planOnly = true
		s.deps.SetPlanOnly(true)
	}
	// Opening-prompt pictures must be retained and attached BEFORE the first loop
	// is built: buildLoop consumes them so the seed message is multimodal.
	s.promptImages = cfg.Images
	if err := s.retainImages(s.promptImages); err != nil {
		log.Close()
		return nil, err
	}
	loop, err := s.buildLoop(mode, prompt)
	if err != nil {
		return nil, err
	}
	if cfg.loopContinuation != "" {
		s.loopContinuation = cfg.loopContinuation
		loop.Seed(cfg.loopContinuation)
	}
	// Seed a named task's get_task result after the opening user message, without
	// loading the full backlog or guessing when the id is ambiguous or stale.
	if mode == "work" {
		s.startupPreload = orchestrator.BuildExplicitTaskPreload(s.ctx, prompt, s.deps, loop.Tools)
		if s.startupPreload.TaskID != "" {
			history := loop.History()
			history = append(history, s.startupPreload.History...)
			loop.SetHistory(history)
		}
	}
	s.loop = loop

	m.mu.Lock()
	if m.shuttingDown.Load() {
		m.mu.Unlock()
		s.cancel()
		log.Close()
		return nil, ErrShuttingDown
	}
	if m.worktreeFencedLocked(wsKey) {
		m.mu.Unlock()
		s.cancel()
		log.Close()
		return nil, fmt.Errorf("%w: %s", ErrWorktreeSettling, absWS)
	}
	m.sessions[id] = s
	m.mu.Unlock()
	if !cfg.Unattended && s.origin != OriginWatch {
		// A user-started session floats its project to the top of pickers now;
		// background activity is picked up by the periodic log-mtime scan.
		m.TouchProjectWorkspace(absWS)
	}

	m.launch(s)
	return s, nil
}

// SpawnWorkstreamConfig parameterizes SpawnWorkstream.
type SpawnWorkstreamConfig struct {
	// Project is the parent project name (required). Its primary tree supplies
	// the base commit and object store.
	Project string
	// BaseRef is the ref/commit the worktree branch is created from; defaults to
	// HEAD.
	BaseRef string
	// TaskID optionally records the backlog task this workstream targets and is
	// appended to the branch name.
	TaskID string
	// Prompt seeds the session (optional).
	Prompt string
	// Mode is the session mode; empty means "work".
	Mode string
	// CoordinatorModel, CoordinatorThinking, Preset, and Images apply to the
	// workstream's session exactly as the same Config fields do for StartSession.
	CoordinatorModel    string
	CoordinatorThinking string
	Preset              string
	Images              []engine.Image
	// SubagentModels carries the session's generic-spawn allowlist; nil allows
	// all enabled models, while a non-nil empty slice allows none.
	SubagentModels []string
}

// SpawnWorkstream creates a linked git worktree + branch (ycc/ws/<id>) off the
// parent project's primary tree and starts a session (work mode by default)
// scoped to that worktree, recording the pair in the workstream registry. It
// preserves the single-writer invariant: at most one active workstream per
// worktree path, and no second live session for the same path. On any failure
// after the worktree exists it best-effort tears it down (remove + delete
// branch + prune).
func (m *Manager) SpawnWorkstream(cfg SpawnWorkstreamConfig) (workstream.Workstream, *Session, error) {
	m.workstreamSpawnMu.Lock()
	defer m.workstreamSpawnMu.Unlock()

	if cfg.Project == "" {
		return workstream.Workstream{}, nil, fmt.Errorf("project required")
	}
	primary, ok := m.projects.Resolve(cfg.Project)
	if !ok {
		return workstream.Workstream{}, nil, fmt.Errorf("unknown project %q", cfg.Project)
	}
	// Reject a bad model choice before any git work; start resolves it again.
	if _, _, err := m.initialCoordinator(cfg.Preset, cfg.CoordinatorModel); err != nil {
		return workstream.Workstream{}, nil, err
	}
	if err := validStartThinking(cfg.CoordinatorThinking); err != nil {
		return workstream.Workstream{}, nil, err
	}
	if err := m.validateSubagentModels(cfg.SubagentModels); err != nil {
		return workstream.Workstream{}, nil, err
	}
	mode := cfg.Mode
	if mode == "" {
		mode = "work"
	}
	spawnToken := m.ownership.NewToken(fmt.Sprintf("workstream spawn for project %s", cfg.Project))
	spawnLease, err := m.acquireSection(primary, spawnToken)
	if err != nil {
		return workstream.Workstream{}, nil, fmt.Errorf("spawn workstream: %w", err)
	}
	defer spawnLease.Release()
	if limit := m.reg.IntegrationConfig().MaxParallel; limit > 0 {
		active := 0
		for _, ws := range m.workstreams.ListByProject(cfg.Project) {
			if ws.Status == workstream.StatusActive {
				active++
			}
		}
		if active >= limit {
			return workstream.Workstream{}, nil, fmt.Errorf("project %q already has %d active workstreams (integration.max_parallel = %d)", cfg.Project, active, limit)
		}
	}
	repo, err := git.Open(primary)
	if err != nil {
		return workstream.Workstream{}, nil, fmt.Errorf("open project repo: %w", err)
	}
	baseRef := cfg.BaseRef
	if baseRef == "" {
		baseRef = "HEAD"
	}
	baseCommit, err := repo.RevParse(baseRef)
	if err != nil {
		return workstream.Workstream{}, nil, fmt.Errorf("resolve base ref %q: %w", baseRef, err)
	}

	// The immutable spawn commit and mutable integration branch are separate: an
	// explicit local branch names both; an arbitrary commit-ish still spawns from
	// that commit but integrates into the configured/default branch.
	var baseBranch string
	if cfg.BaseRef != "" {
		name, local, lerr := repo.LocalBranch(cfg.BaseRef)
		if lerr != nil {
			return workstream.Workstream{}, nil, fmt.Errorf("resolve base branch from %q: %w", cfg.BaseRef, lerr)
		}
		if local {
			baseBranch = name
		}
	}
	if baseBranch == "" {
		baseBranch = strings.TrimSpace(m.reg.IntegrationBase())
		if baseBranch != "" {
			name, local, lerr := repo.LocalBranch(baseBranch)
			if lerr != nil {
				return workstream.Workstream{}, nil, fmt.Errorf("resolve configured integration base %q: %w", baseBranch, lerr)
			}
			if !local {
				return workstream.Workstream{}, nil, fmt.Errorf("configured integration base %q is not a local branch", baseBranch)
			}
			baseBranch = name
		}
	}
	if baseBranch == "" {
		baseBranch, err = repo.DefaultBranch()
		if err != nil {
			return workstream.Workstream{}, nil, fmt.Errorf("resolve workstream base branch: %w", err)
		}
		name, local, lerr := repo.LocalBranch(baseBranch)
		if lerr != nil {
			return workstream.Workstream{}, nil, fmt.Errorf("resolve default base branch %q: %w", baseBranch, lerr)
		}
		if !local {
			return workstream.Workstream{}, nil, fmt.Errorf("default base branch %q is not a local branch", baseBranch)
		}
		baseBranch = name
	}

	id, err := newWorkstreamID()
	if err != nil {
		return workstream.Workstream{}, nil, err
	}
	branch := "ycc/ws/" + id
	if cfg.TaskID != "" {
		branch += "-" + cfg.TaskID
	}
	projectDir := workstream.SafeProjectDir(cfg.Project)
	dir, err := workstream.ContainedPath(m.worktreesRoot, projectDir, id)
	if err != nil {
		return workstream.Workstream{}, nil, fmt.Errorf("resolve worktree path: %w", err)
	}

	// Single-writer guard: reject if a live session already writes to this path.
	// (The registry's Add enforces the persistent side of the invariant.)
	m.mu.Lock()
	for _, s := range m.sessions {
		if s.Workspace == dir {
			m.mu.Unlock()
			return workstream.Workstream{}, nil, fmt.Errorf("%w: %s", workstream.ErrWorktreeInUse, dir)
		}
	}
	m.mu.Unlock()

	// Pre-check the registry so we don't create a worktree we'll have to reap.
	for _, ex := range m.workstreams.ListByProject(cfg.Project) {
		if ex.WorktreePath == dir && !ex.Status.Terminal() {
			return workstream.Workstream{}, nil, fmt.Errorf("%w: %s", workstream.ErrWorktreeInUse, dir)
		}
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return workstream.Workstream{}, nil, fmt.Errorf("create worktrees root: %w", err)
	}
	if err := repo.AddWorktree(dir, branch, baseCommit); err != nil {
		return workstream.Workstream{}, nil, fmt.Errorf("add worktree: %w", err)
	}
	cleanup := func() {
		repo.RemoveWorktree(dir)
		repo.DeleteBranch(branch, true)
		repo.PruneWorktrees()
	}

	wtCfg := m.worktreeConfigFor(primary)
	if err := workstream.Bootstrap(primary, dir, wtCfg); err != nil {
		cleanup()
		return workstream.Workstream{}, nil, fmt.Errorf("bootstrap worktree: %w", err)
	}

	s, err := m.start(Config{
		Workspace:           dir,
		Mode:                mode,
		Prompt:              cfg.Prompt,
		CoordinatorModel:    cfg.CoordinatorModel,
		CoordinatorThinking: cfg.CoordinatorThinking,
		Preset:              cfg.Preset,
		Images:              cfg.Images,
		SubagentModels:      cfg.SubagentModels,
	}, false)
	if err != nil {
		cleanup()
		return workstream.Workstream{}, nil, fmt.Errorf("start workstream session: %w", err)
	}

	ws := workstream.Workstream{
		ID:           id,
		Project:      cfg.Project,
		BaseCommit:   baseCommit,
		BaseBranch:   baseBranch,
		Branch:       branch,
		WorktreePath: dir,
		SessionID:    s.ID,
		TaskID:       cfg.TaskID,
		Status:       workstream.StatusActive,
		CreatedAt:    time.Now(),
	}
	if err := m.workstreams.Add(ws); err != nil {
		m.Stop(s.ID)
		cleanup()
		return workstream.Workstream{}, nil, fmt.Errorf("register workstream: %w", err)
	}
	// Record the workstream's creation on its own session stream so the merge
	// flow is auditable and projectable.
	m.emitWorkstreamEvent(ws, event.WorkstreamCreated, map[string]any{
		"workstream":  ws.ID,
		"branch":      ws.Branch,
		"base":        ws.BaseCommit,
		"base_branch": ws.BaseBranch,
		"worktree":    ws.WorktreePath,
		"project":     ws.Project,
		"task":        ws.TaskID,
	})
	// Registration happens after the session starts, so attach explicitly here;
	// newSession cannot discover a fresh workstream yet. The immediate status
	// check closes the small race where the initial turn finished before attach.
	m.startWorkstreamWatcher(ws, s.log)
	if status := s.Status(); status == event.StatusIdle || status == event.StatusError || status == event.StatusStopped {
		m.evaluateWorkstreamReadiness(ws.ID, status, workstreamRunBlocked(s.log.Snapshot()))
	}
	return ws, s, nil
}

// Workstreams returns the workstreams for a project (empty name => all).
func (m *Manager) Workstreams(project string) []workstream.Workstream {
	return m.workstreams.ListByProject(project)
}

// ReconcileWorkstreams reconciles the workstream registry against git and the
// durable session log on startup: ValidateWorkstreamTrees followed by
// DeriveWorkstreamReadiness. A persistent daemon runs the two in separate startup
// phases (docs/design/daemon-restart.md §1).
func (m *Manager) ReconcileWorkstreams() error {
	if err := m.ValidateWorkstreamTrees(); err != nil {
		return err
	}
	m.DeriveWorkstreamReadiness()
	return nil
}

// ValidateWorkstreamTrees is the static half of startup reconciliation. In-root
// in-flight states retain a live worktree; missing trees become stale, while
// out-of-root legacy entries become needs-attention with a manual migration
// reason. It derives no readiness and enqueues no integration.
func (m *Manager) ValidateWorkstreamTrees() error {
	all := m.workstreams.List()
	// Group all worktree-owning states by project. Persisted paths are untrusted:
	// pre-upgrade unsafe project names may have created a still-live worktree beyond
	// the daemon root. Surface those entries for manual migration, but never pass
	// their paths to git or traverse their session logs.
	byProject := map[string][]workstream.Workstream{}
	for _, w := range all {
		if !w.Status.InFlight() {
			continue
		}
		if err := workstream.VerifyUnderRoot(m.worktreesRoot, w.WorktreePath); err != nil {
			reason := fmt.Sprintf("worktree path %q is outside the daemon worktrees root; merge its branch if wanted, then remove it manually with `git worktree remove`", w.WorktreePath)
			if _, err := m.workstreams.Transition(w.ID, workstream.StatusNeedsAttention, reason,
				workstream.StatusActive, workstream.StatusReady, workstream.StatusNeedsAttention); err != nil {
				return fmt.Errorf("mark out-of-root workstream %q needs attention: %w", w.ID, err)
			}
			continue
		}
		byProject[w.Project] = append(byProject[w.Project], w)
	}
	for proj, list := range byProject {
		primary, ok := m.projects.Resolve(proj)
		if !ok {
			// Parent project is gone: its worktrees are unrecoverable.
			for _, w := range list {
				m.workstreams.SetStatus(w.ID, workstream.StatusStale)
			}
			continue
		}
		reconcileLease, err := m.ownership.Acquire(primary, m.ownership.NewToken("workstream startup reconciliation"))
		if err != nil {
			continue
		}
		repo, err := git.Open(primary)
		if err != nil {
			reconcileLease.Release()
			// Cannot inspect the repo; leave entries as-is for the next attempt.
			continue
		}
		repo.PruneWorktrees()
		trees, err := repo.ListWorktrees()
		reconcileLease.Release()
		if err != nil {
			continue
		}
		known := map[string]bool{}
		for _, t := range trees {
			abs, err := filepath.Abs(t.Path)
			if err != nil {
				abs = t.Path
			}
			known[filepath.Clean(abs)] = true
		}
		for _, w := range list {
			path := filepath.Clean(w.WorktreePath)
			_, statErr := os.Stat(path)
			if os.IsNotExist(statErr) || !known[path] {
				m.workstreams.SetStatus(w.ID, workstream.StatusStale)
			}
		}
	}
	return nil
}

// DeriveWorkstreamReadiness re-derives completion from the worktree-local logs
// of non-live sessions, so daemon restarts cannot lose it, then re-enqueues every
// ready stream for auto-integration. A session idle while awaiting background
// jobs is still waiting, not finished, and is skipped, as is any live session.
func (m *Manager) DeriveWorkstreamReadiness() {
	for _, w := range m.workstreams.List() {
		if !w.Status.InFlight() || w.SessionID == "" || m.workstreamSessionBusy(w) {
			continue
		}
		events := m.workstreamSessionLog(w)
		if len(events) == 0 {
			continue
		}
		status := workstreamTerminalStatus(events)
		if status == event.StatusIdle || status == event.StatusError || status == event.StatusStopped {
			m.evaluateWorkstreamReadiness(w.ID, status, workstreamRunBlocked(events))
		}
	}

	// A ready state is durable while the queue is in-memory. Re-enqueue every
	// eligible stream after reconciliation so daemon restarts cannot strand it,
	// with the same exclusions: integration must not take over a tree whose
	// session is live or still waiting on its jobs.
	var ready []workstream.Workstream
	for _, w := range m.workstreams.List() {
		if w.Status == workstream.StatusReady && !m.workstreamSessionBusy(w) {
			ready = append(ready, w)
		}
	}
	if len(ready) > 0 && m.effectiveIntegrationMode() == "auto" {
		for _, w := range ready {
			m.enqueueWorkstreamIntegration(w)
		}
	}
}

// workstreamSessionLog reads a workstream session's worktree-local log, or nil
// when it has no session, its persisted path is untrusted, or there is no log.
func (m *Manager) workstreamSessionLog(w workstream.Workstream) []event.Event {
	if w.SessionID == "" {
		return nil
	}
	if err := workstream.VerifyUnderRoot(m.worktreesRoot, w.WorktreePath); err != nil {
		return nil
	}
	return readEventsTolerant(filepath.Join(w.WorktreePath, ".ycc", "sessions", w.SessionID, "events.jsonl"))
}

// workstreamSessionBusy reports whether a workstream's session is live or its
// log last went idle while still awaiting background jobs, or a job of one of
// its sessions is held passively after a daemon restart, so startup must not
// derive readiness from it or hand its tree to integration.
func (m *Manager) workstreamSessionBusy(w workstream.Workstream) bool {
	if w.SessionID == "" {
		return false
	}
	if _, live := m.Get(w.SessionID); live {
		return true
	}
	if workstream.VerifyUnderRoot(m.worktreesRoot, w.WorktreePath) == nil {
		for _, id := range m.workstreamSessionIDs(w) {
			if m.passiveUnsettled(w.WorktreePath, id) != "" {
				return true
			}
		}
	}
	return idleAwaitingJobs(m.workstreamSessionLog(w))
}

// newSession assembles a Session (emitter, interaction, deps, role specs, the
// buildLoop closure, and review-tier wiring) on a given event log, WITHOUT
// creating/seeding its loop or registering it — callers do that, since Start
// seeds the loop while Reopen installs a reconstructed history. resumed marks a
// session re-instantiated on an existing log.
// newSession builds a Session on an open log. coordOverride, when non-empty,
// names the logical model this session's coordinator uses INSTEAD of the
// configured default — a per-session choice that never touches
// the persisted role defaults; implementer/reviewers always follow the config.
func (m *Manager) newSession(absWS, id, mode string, unattended bool, prompt string, log *event.Log, resumed bool, coordOverride string) (*Session, error) {
	emitter := event.NewEmitter(log, "coordinator")
	inter := newInteraction(unattended, emitter)
	// Session startup deliberately takes no worktree lease: git.Open only
	// captures a baseline snapshot (and initializes a missing repository), and
	// refusing a new session because another scope is mid-write is far more
	// disruptive than the rare race it would prevent.
	var repo *git.Repo
	var err error
	if resumed {
		repo, err = git.OpenExisting(absWS)
	} else {
		repo, err = git.Open(absWS)
	}
	if err != nil {
		return nil, fmt.Errorf("prepare git workspace: %w", err)
	}
	var baseline *git.Baseline
	var baselineErr error
	if resumed {
		baseline, baselineErr = repo.LoadBaseline(id)
		if baselineErr != nil {
			baselineErr = fmt.Errorf("persisted git baseline for session %s is unavailable: %w; start a new session before reviewing or committing because change ownership cannot be reconstructed safely", id, baselineErr)
		}
	} else {
		baseline = repo.OpenBaseline()
		if baseline == nil {
			cause := repo.OpenBaselineError()
			if cause == nil {
				cause = fmt.Errorf("initial baseline was not captured")
			}
			baselineErr = fmt.Errorf("git baseline for session %s is unavailable: %w; review and commit are disabled because change ownership cannot be reconstructed safely (create an initial commit without absorbing unrelated files, then start a new session)", id, cause)
		} else if err := repo.PersistBaseline(id, baseline); err != nil {
			return nil, fmt.Errorf("persist session git baseline: %w", err)
		}
	}
	var startupNotices []string
	if oversized := baseline.Oversized(); len(oversized) > 0 {
		startupNotices = append(startupNotices, oversizedUntrackedNotice(oversized))
	}
	coordName := m.reg.CoordinatorName()
	if coordOverride != "" {
		if !m.reg.Has(coordOverride) {
			return nil, fmt.Errorf("%w %q", ErrUnknownModel, coordOverride)
		}
		if !m.reg.Enabled(coordOverride) {
			return nil, fmt.Errorf("%w %q", ErrDisabledModel, coordOverride)
		}
		coordName = coordOverride
	}
	implName := m.reg.ImplementerName()
	reviewerNames := append([]string(nil), m.reg.ReviewerNames()...)
	implSpec, err := m.agentSpec(implName)
	if err != nil {
		return nil, err
	}
	var reviewers []orchestrator.AgentSpec
	for _, name := range reviewerNames {
		rs, err := m.agentSpec(name)
		if err != nil {
			return nil, err
		}
		reviewers = append(reviewers, rs)
	}
	var worktreeEnv []string
	if primary := m.primaryTreeFor(absWS); filepath.Clean(primary) != filepath.Clean(absWS) {
		worktreeEnv = sortedWorktreeEnv(m.worktreeConfigFor(primary).Env)
	}
	workImplementation := m.reg.WorkImplementation()
	if resumed {
		workImplementation = replayWorkImplementation(log.Snapshot(), workImplementation)
	}
	deps := &orchestrator.Deps{
		Workspace:          absWS,
		Env:                worktreeEnv,
		Docs:               m.backlogStore(absWS),
		Repo:               repo,
		Baseline:           baseline,
		BaselineErr:        baselineErr,
		Emitter:            emitter,
		Implementer:        implSpec,
		Reviewers:          reviewers,
		Asker:              inter,
		MaxTok:             m.reg.MaxTokens(),
		MaxTurns:           m.reg.MaxTurns(),
		Retry:              m.reg.RetryPolicy(),
		WriteRoots:         m.reg.WriteRoots(),
		WorkImplementation: workImplementation,
		Jobs:               jobs.NewRegistry(),
		Ownership:          m.ownership,
		PromptCacheScope:   id,
	}
	if m.jobLauncher != nil {
		deps.JobSupervisor = m.jobLauncher.ForSession(filepath.Join(absWS, ".ycc", "sessions", id))
	}
	// The session id is the attribution scope shared by the coordinator and every
	// agent it delegates to (see workspacelease claims).
	deps.CoordinatorToken = m.ownership.NewScopedToken(id, fmt.Sprintf("session %s coordinator", id))
	deps.Docs.SetWriteObserver(deps.RecordBookkeeping)
	deps.MemorySource = func(kind docs.MemoryKind) docs.MemoryProvenance {
		return memorySourceFromEvents(id, log.Snapshot(), kind)
	}
	deps.MemoryPressure = func(int) string { return m.memoryPressure(absWS, id) }
	deps.HeldTasks = func() map[string]string { return m.heldTasks(absWS, id) }
	deps.ClaimTask = func(taskID string, record func()) string { return m.claimTask(absWS, id, taskID, record) }
	deps.OtherSessions = func() string { return m.otherSessionsNote(absWS, id, time.Now()) }
	coordination := m.coordinationEnabled(absWS)
	// list_sessions is offered without coordination too: it then lists only
	// exclusive lease holders.
	deps.ListSessions = func(path string) (string, error) { return m.listSessionsFor(absWS, id, path, time.Now()) }
	if mode == watchMode {
		deps.WatchReport = func(r orchestrator.WatchReport) (string, error) { return m.recordWatchReport(absWS, id, r) }
	} else if !unattended && m.watchSchedulerRunning() {
		m.configureChatWatches(absWS, deps)
	}

	ctx, cancelCause := context.WithCancelCause(context.Background())
	s := &Session{
		ID:             id,
		Workspace:      absWS,
		Mode:           mode,
		log:            log,
		emitter:        emitter,
		inter:          inter,
		deps:           deps,
		reg:            m.reg,
		prompt:         prompt,
		resumed:        resumed,
		messageCh:      make(chan engine.UserMessage, 64),
		retryCh:        make(chan struct{}),
		switchRetryCh:  make(chan struct{}, 1),
		rolloverCh:     make(chan *rolloverRequest, 1),
		ctx:            ctx,
		cancel:         func() { cancelCause(nil) },
		cancelCause:    cancelCause,
		runDone:        make(chan struct{}),
		status:         event.StatusRunning,
		coordinator:    coordName,
		startupNotices: startupNotices,
		implementer:    implName,
		reviewers:      reviewerNames,
		thinkLevels:    map[string]string{},
		coordination:   coordination,
		coordinationOn: func() bool { return m.coordinationEnabled(absWS) },
	}
	if coordination {
		s.board = &sessionBoard{m: m, s: s, primary: m.primaryTreeFor(absWS)}
		deps.Board = s.board
	}
	// Leases are not part of experimental coordination: loop sessions must
	// hold one to deploy or restart shared services (spec §9.1).
	s.leases = &sessionLeaseTools{m: m, s: s, primary: m.primaryTreeFor(absWS)}
	deps.Leases = s.leases
	// Durability loss is terminal for a live session: continuing would mutate
	// model/tool state from history that cannot be replayed after restart. Log
	// itself sends live subscribers a transient session_error, so do not attempt
	// to record another error through the already-broken log here.
	log.OnFailure(func(error) {
		s.setStatus(event.StatusError)
		s.cancel()
	})
	// Generic chat subagents resolve at spawn time so live per-model overrides
	// and refreshed credentials are honored. Tool metadata is a bounded snapshot
	// of enabled models at loop construction; spawn validation remains live.
	deps.ResolveAgent = s.resolveSubagentSpec
	deps.AgentModels = s.agentModels
	deps.InputWake = s.inputWake

	// buildLoop assembles the agent loop for a mode; reused on mode transitions.
	// It reads the session's current coordinator assignment so a mid-session
	// role-config change drives the next coordinator loop.
	s.buildLoop = func(mode, prompt string) (*engine.Loop, error) {
		// Loop sessions declare intents even without session coordination, so
		// parallel loop siblings see each other's areas and resources.
		if s.board == nil && s.origin == OriginWorkLoop {
			deps.SetIntent = s.declareIntent
		}
		// A loop session blocked in a long wait on its shell jobs holds a
		// working slot; a capped wait reminds it to end its turn (spec §9.1).
		if s.origin == OriginWorkLoop {
			deps.ShellWaitCap = loopShellWaitCap
		}
		reg, sys := orchestrator.BuildMode(mode, deps, unattended)
		s.mu.Lock()
		coord := s.coordinator
		s.mu.Unlock()
		client, model, err := m.reg.Build(coord)
		if err != nil {
			return nil, fmt.Errorf("build coordinator backend: %w", err)
		}
		th := s.thinkingFor(coord)
		contextWindow, _ := m.reg.ContextBudget(coord)
		loop := &engine.Loop{
			Client: client, Model: model, ModelName: coord, Backend: m.reg.BackendFor(coord),
			System: sys, Tools: reg, Emitter: emitter, ContextWindow: contextWindow,
			MaxTok: m.reg.MaxTokens(), MaxTurns: m.reg.MaxTurns(), Retry: m.reg.RetryPolicy(),
			Thinking: th.Thinking, Effort: th.Effort, ThinkingDisplay: th.ThinkingDisplay,
			ContextLengthHandled: true,
			PromptCacheKey:       id + "/coordinator",
		}
		loop.Steer = s
		// Mode transitions and Start always pass a non-empty seed; reopen passes
		// "" because it installs a reconstructed history instead of seeding.
		if prompt != "" {
			if images := s.takeSeedImages(); len(images) > 0 {
				loop.PostMessage(engine.UserMessage{Text: prompt, Images: images})
			} else {
				loop.Seed(prompt)
			}
		}
		return loop, nil
	}

	// Wire the review-tier resolver so spawn_reviewers can pick a tier per change
	// It resolves the configured tier to concrete reviewer specs,
	// and ReviewTiers lets the tool description enumerate the available tiers.
	deps.ReviewTier = func(name string) orchestrator.ReviewPlan {
		return s.resolveReviewTier(name)
	}
	deps.ReviewTiers = func() []orchestrator.ReviewTierInfo {
		return s.reviewTiers()
	}

	// Attach the daemon-side notification watcher when a notifier is configured
	// It subscribes from LastSeq at attach time so a reopened
	// session's replayed history never re-fires; the goroutine exits when the log
	// is closed (its channel closes).
	if m.notifier != nil {
		m.startNotifyWatcher(absWS, id, mode, log)
	}

	return s, nil
}

// startNotifyWatcher spawns a goroutine that maps session events to best-effort
// push notifications. It attaches at the log's current LastSeq so only
// events emitted after attach fire — a reopened session's replayed history never
// re-notifies. The goroutine exits when the log closes (the subscription channel
// closes).
func (m *Manager) startNotifyWatcher(absWS, id, mode string, log *event.Log) {
	project := m.projectLabel(absWS)
	ch, cancel := log.Subscribe(log.LastSeq())
	go func() {
		defer cancel()
		for ev := range ch {
			if ev.Transient {
				continue
			}
			switch ev.Type {
			case event.QuestionAsked:
				if boolVal(ev.Data, "auto") {
					continue // auto-answered unattended ask — nobody is waiting
				}
				m.notifier.Send(notify.KindQuestion, project, id, questionLine(ev.Data))
			case event.SessionIdle:
				if mode == watchMode {
					// Watch runs notify through their report status instead.
					continue
				}
				if boolVal(ev.Data, "awaiting_jobs") {
					// A progress report while delegated work still runs: the
					// session resumes by itself, and its later idle report is the
					// one worth a "finished" push.
					continue
				}
				m.notifier.Send(notify.KindIdle, project, id, firstLine(str(ev.Data, "report")))
			case event.SessionError:
				m.notifier.Send(notify.KindError, project, id, str(ev.Data, "msg"))
			case event.SubagentFinished:
				if boolVal(ev.Data, "blocked") {
					role := str(ev.Data, "role")
					if role == "" {
						role = "implementer"
					}
					m.notifier.Send(notify.KindBlocked, project, id, role+" blocked — decision needed")
				}
			}
		}
	}()
}

// questionLine extracts a short human line from a question_asked payload, handling
// both the single-question ("question") and batch ("questions") shapes. For a batch
// it uses the first prompt and appends "(+N more)".
func questionLine(data map[string]any) string {
	if q := str(data, "question"); q != "" {
		return firstLine(q)
	}
	qs, ok := data["questions"].([]any)
	if !ok {
		// Live (non-replayed) payload carries the concrete slice type.
		if typed, ok2 := data["questions"].([]map[string]any); ok2 {
			qs = make([]any, len(typed))
			for i, q := range typed {
				qs[i] = q
			}
		}
	}
	if len(qs) == 0 {
		return "a question was asked"
	}
	first := ""
	if qm, ok := qs[0].(map[string]any); ok {
		first = firstLine(str(qm, "question"))
	}
	if len(qs) > 1 {
		return fmt.Sprintf("%s (+%d more)", first, len(qs)-1)
	}
	return first
}

// boolVal reports whether data[key] is the boolean true.
func boolVal(data map[string]any, key string) bool {
	b, _ := data[key].(bool)
	return b
}

// str returns data[key] as a string, or "" when absent/not a string.
func str(data map[string]any, key string) string {
	s, _ := data[key].(string)
	return s
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// Reopen re-instantiates a persisted session on its EXISTING event log ("resume
// = replay"): it re-opens the log, restores mode + interaction
// level from the projection, reconstructs the coordinator loop's conversation
// history from the log, and registers it as a live session whose new activity
// appends to the same continuous events.jsonl. It is idempotent: reopening an
// already-live session returns the live one. A previously Stop-terminated
// session is reopenable too — resume is pure log replay, so session_stopped is
// informational and does not block it. The project must be named unless it is
// the daemon's sole project; an unknown/ambiguous project is ErrUnknownProject;
// a session with no persisted log is ErrUnknownSession.
func (m *Manager) Reopen(project, id string) (*Session, error) {
	// One preparation per session id at a time, so concurrent callers never open
	// duplicate writers on the same log or attach duplicate watchers.
	unlock := m.lockSessionID(id)
	defer unlock()
	if s, ok := m.Get(id); ok {
		if s.logFailure() == nil {
			return s, nil // already live — no-op
		}
		// The live instance fail-stopped because its event log could not be
		// appended (e.g. the disk filled). It can never run again, so release it
		// and replay a fresh instance from the durable log instead of requiring a
		// daemon restart. If the disk is still full the new instance simply
		// fail-stops again on its first append.
		m.reclaimIfCurrent(s)
	}
	if m.shuttingDown.Load() {
		return nil, ErrShuttingDown
	}
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	// Resolve before opening: OpenLog creates a missing file, and an empty log
	// left in the primary would shadow a workstream session's worktree log.
	if id == "" || id == "." || filepath.Base(id) != id {
		return nil, fmt.Errorf("%w %q", ErrUnknownSession, id)
	}
	absWS, logPath, err := m.locateSessionLog(absWS, id)
	if err != nil {
		return nil, err
	}
	log, err := event.OpenLog(logPath)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}
	events := log.Snapshot()
	if len(events) == 0 {
		log.Close()
		return nil, fmt.Errorf("%w %q", ErrUnknownSession, id)
	}

	proj := event.Reduce(events)
	// A previously Stop-terminated session is still reopenable: resume is pure
	// log replay, so a session_stopped marker is informational
	// (it records that the live process was terminated) and does NOT block
	// reconstructing the conversation from the durable log.
	mode := proj.Mode
	if mode == "" {
		mode = "work"
	}
	// A preset session re-resolves its binding so it retains the configured
	// cross-model coordinator (and gets the same graceful stale-binding fallback),
	// unless an explicit start override or later role change superseded it. Those
	// sessions replay on the model they last ran on from the projection.
	coord := proj.Coordinator
	startupNotice := ""
	if proj.Preset != "" && !proj.CoordinatorExplicit && !proj.CoordinatorChanged {
		coord, startupNotice, err = m.initialCoordinator(proj.Preset, "")
		if err != nil { // preset resolution is currently non-fatal; keep defensive
			log.Close()
			return nil, err
		}
	} else if coord != "" && !m.reg.Has(coord) {
		coord = ""
	}
	s, err := m.newSession(absWS, id, mode, false, "", log, true, coord)
	if err != nil {
		log.Close()
		return nil, err
	}
	s.subagentModels = replaySubagentModels(events)
	s.deps.DisableGenericAgents = s.subagentModels != nil && len(s.subagentModels) == 0
	s.participation.consume(events)
	s.participationCursor = len(events)
	s.origin = s.participation.origin
	s.preset = proj.Preset
	if startupNotice != "" {
		s.startupNotices = append([]string{startupNotice}, s.startupNotices...)
	}
	// Supervised background jobs may have outlived the daemon that started
	// them: reconcile each one before the registry and history are rebuilt
	// (docs/design/daemon-restart.md §3). Jobs held passively since startup
	// are taken over through the handles already following them.
	entries := engine.RestoreJobEntries(events)
	var adopted map[string]*jobshim.Handle
	endPassive := func() {}
	if s.deps.JobSupervisor != nil {
		adopted, endPassive = m.passiveJobs.Take(s.deps.JobSupervisor.Dir())
	}
	recovery := tools.ReconcileSupervisedJobs(s.deps.JobSupervisor, entries, "coordinator", time.Now(), adopted)
	s.deps.Jobs = engine.FinalizeRestoredJobs(entries, events)
	s.deps.CoordinatorArtifacts = tools.NewArtifactStore()
	s.seedThinkLevels(replayThinkLevels(events))
	loop, err := s.buildLoop(mode, "")
	if err != nil {
		recovery.Abandon()
		endPassive()
		// The session stays closed: hold its jobs passively again.
		m.supervisePassively(id, s.deps.JobSupervisor, absWS, events)
		log.Close()
		return nil, err
	}
	loop.SetHistory(engine.ReplayHistoryReconciled(events, recovery.Reconciled()))
	s.loop = loop
	// A log that ended while the coordinator waited in ask_user restores that
	// question as a live gate (armed before registration, so an answer RPC can
	// never race the run goroutine's start).
	s.restoreQuestion(findResumableQuestion(events), loop.History())
	// Likewise a log that ended inside wait resumes that wait, with input
	// routing armed before registration so a message sent meanwhile ends it
	// early, as it would a live wait.
	s.restoreWait(findResumableWait(events), loop.History())
	s.forkAwaitsInput = forkAwaitingInput(events)
	// Seed the spend-guard flags from the replayed log so a session that already
	// crossed the warning/breach line does not re-fire or re-ask on reopen.
	s.seedBudgetFromLog(events)
	if mode == "chat" {
		s.restorePlanOnly(events)
	}

	// Record downtime finishes and losses (ahead of the run's session_reopened)
	// and re-attach surviving jobs before the session is published, so a
	// concurrent teardown (a restart, StopSession) only ever sees jobs whose
	// runners can be stopped or detached. Reopen holds this id's preparation
	// lock, so no other caller records or attaches for it meanwhile.
	recovery.Activate(s.recoveryWorkspace())
	// The reopen now registers its re-attached shells as running; the passive
	// registrations they replace can go.
	endPassive()
	s.recovery = recovery
	s.reopenSurviving = recovery.Surviving()
	s.prepareReopenDisposition(events)

	wsKey := worktreeKey(absWS)
	// Register atomically: re-check under the lock so a concurrent Reopen/Start
	// that won the race keeps its live session. The loser closes its freshly
	// opened log and abandons its (never-started) session — no goroutine or log
	// leak.
	m.mu.Lock()
	if existing, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		recovery.Abandon()
		s.cancel()
		log.Close()
		return existing, nil
	}
	if m.shuttingDown.Load() {
		m.mu.Unlock()
		recovery.Abandon()
		s.cancel()
		log.Close()
		return nil, ErrShuttingDown
	}
	if m.worktreeFencedLocked(wsKey) {
		m.mu.Unlock()
		recovery.Abandon()
		s.cancel()
		log.Close()
		// The session stays closed: hold its jobs passively again.
		m.supervisePassively(id, s.deps.JobSupervisor, absWS, events)
		return nil, fmt.Errorf("%w: %s", ErrWorktreeSettling, absWS)
	}
	m.sessions[id] = s
	m.mu.Unlock()
	// Leases a closed instance of this session still holds (a job-bound one
	// held passively across a restart, say) belong to this instance now.
	m.adoptLeases(s)

	if linked, ok := m.workstreams.BySession(id); ok && linked.Status.InFlight() {
		m.startWorkstreamWatcher(linked, log)
	}
	m.launch(s)
	return s, nil
}

// recoveryWorkspace is the coordinator's tool workspace over the reopened
// registry, which reopen reconciliation uses to record and follow supervised
// jobs. It shares the coordinator loop's artifact store.
func (s *Session) recoveryWorkspace() *tools.Workspace {
	d := s.deps
	return &tools.Workspace{Root: d.Workspace, Env: append([]string(nil), d.Env...), Jobs: d.Jobs,
		JobSupervisor: d.JobSupervisor, Emitter: d.Emitter, Ownership: d.Ownership,
		MutationToken: d.CoordinatorToken, Artifacts: d.CoordinatorArtifacts}
}

// reopenAwaitingJobs reports whether the log's latest lifecycle boundary
// leaves the coordinator idle waiting on its background jobs (reopenBoundary).
// The caller also requires a job continuation still pending in the restored
// registry.
func reopenAwaitingJobs(events []event.Event) bool {
	_, awaiting := reopenBoundary(events)
	return awaiting
}

// reopenBoundary reports whether the log's latest conversation boundary is a
// finished turn's session_idle (idle), and whether background-job completion
// may still wake it (awaiting): that report was awaiting jobs, and no stop,
// error, or pending pause has disarmed it since. A later turn, input, job
// notification, fork, question, or context switch means the conversation
// moved on (neither). Reopen and lifecycle markers change nothing in the
// conversation, so they are looked through. A session whose log ends at such
// a boundary reopens idle even when its history ends on a tool result (a
// finish or report_blocked call): its turn is complete.
func reopenBoundary(events []event.Event) (idle, awaiting bool) {
	// A pause request the user cancelled again (pause_cancelled) set no
	// boundary; only one still pending does.
	cancelledPauses := 0
	disarmed := false
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Transient {
			continue
		}
		switch ev.Type {
		case event.SessionIdle:
			return true, boolVal(ev.Data, "awaiting_jobs") && !disarmed
		case event.PauseCancelled:
			cancelledPauses++
		case event.PauseRequested:
			if cancelledPauses > 0 {
				cancelledPauses--
				continue
			}
			disarmed = true
		case event.SessionStopped, event.SessionError, event.Interrupted, event.Resumed:
			disarmed = true
		case event.UserInput:
			if boolVal(ev.Data, "queued") {
				continue
			}
			return false, false
		case event.ModelTurn, event.ToolCall, event.ToolResult:
			if ev.Actor != "coordinator" {
				continue
			}
			return false, false
		case event.UserInputDelivered, event.JobNotified, event.SessionForked, event.QuestionAsked,
			event.ContextViewChanged, event.SessionStarted:
			return false, false
		}
	}
	return false, false
}

// SessionLogPath resolves a session to its authoritative JSONL log and workspace.
// It is used by rebuildable read indexes; callers must still treat the log as the
// source of truth. Session ids are constrained to one path component.
func (m *Manager) SessionLogPath(project, id string) (workspace, logPath string, err error) {
	if id == "" || id == "." || filepath.Base(id) != id {
		return "", "", fmt.Errorf("%w %q", ErrUnknownSession, id)
	}
	if s, ok := m.Get(id); ok {
		absWS, err := filepath.Abs(s.Workspace)
		if err != nil {
			return "", "", fmt.Errorf("resolve workspace: %w", err)
		}
		logPath = filepath.Join(absWS, ".ycc", "sessions", id, "events.jsonl")
		if _, err := os.Stat(logPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", "", fmt.Errorf("%w %q", ErrUnknownSession, id)
			}
			return "", "", err
		}
		return absWS, logPath, nil
	}
	workspace, err = m.resolveProjectWorkspace(project)
	if err != nil {
		return "", "", err
	}
	absWS, err := filepath.Abs(workspace)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace: %w", err)
	}
	return m.locateSessionLog(absWS, id)
}

// locateSessionLog resolves a persisted session of the project at absWS to its
// log and workspace: the project's own log, else — for a finished workstream or
// integrate-agent session — the log kept in its in-flight workstream's worktree
// until merge/discard preserves it. A zero-length log counts as missing: older
// daemons left one in the primary when reopening a worktree session, and it
// must not shadow the real log. id must already be one path component.
func (m *Manager) locateSessionLog(absWS, id string) (workspace, logPath string, err error) {
	logPath = filepath.Join(absWS, ".ycc", "sessions", id, "events.jsonl")
	if err := nonEmptyLog(logPath); err == nil {
		return absWS, logPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if wt, ok := m.workstreamSessionWorktree(absWS, id); ok {
		wtLog := filepath.Join(wt, ".ycc", "sessions", id, "events.jsonl")
		if nonEmptyLog(wtLog) == nil {
			return wt, wtLog, nil
		}
	}
	return "", "", fmt.Errorf("%w %q", ErrUnknownSession, id)
}

// nonEmptyLog reports a missing or zero-length file as os.ErrNotExist.
func nonEmptyLog(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return os.ErrNotExist
	}
	return nil
}

// workstreamSessionWorktree returns the worktree of the in-flight workstream of
// the project at absWS whose implementation or integrate session is id.
// Persisted worktree paths are untrusted: only paths under the daemon-owned
// worktrees root qualify.
func (m *Manager) workstreamSessionWorktree(absWS, id string) (string, bool) {
	if m.workstreams == nil {
		return "", false
	}
	for _, ws := range m.workstreams.List() {
		if ws.SessionID != id && ws.IntegrateSessionID != id {
			continue
		}
		if !ws.Status.InFlight() {
			return "", false
		}
		primary, ok := m.projects.Resolve(ws.Project)
		if !ok {
			return "", false
		}
		if p, err := filepath.Abs(primary); err != nil || p != absWS {
			return "", false
		}
		if err := workstream.VerifyUnderRoot(m.worktreesRoot, ws.WorktreePath); err != nil {
			return "", false
		}
		return ws.WorktreePath, true
	}
	// A session whose log is in an in-flight worktree although the workstream
	// never recorded it (an integration session a crash left before its id was
	// stored) is that worktree's too.
	for _, ws := range m.workstreams.List() {
		if !ws.Status.InFlight() || workstream.VerifyUnderRoot(m.worktreesRoot, ws.WorktreePath) != nil {
			continue
		}
		if nonEmptyLog(filepath.Join(ws.WorktreePath, ".ycc", "sessions", id, "events.jsonl")) != nil {
			continue
		}
		primary, ok := m.projects.Resolve(ws.Project)
		if !ok {
			continue
		}
		if p, err := filepath.Abs(primary); err == nil && p == absWS {
			return ws.WorktreePath, true
		}
	}
	return "", false
}

// SessionViewLogPath resolves the authoritative log together with an explicit
// indexing upper bound. A live log's sequence and byte offset are read through
// Log's durability lock and exclude a write that has not completed fsync. A -1
// boundary means a closed persisted log may be indexed through its complete newline-terminated
// file snapshot. A named project confines resolution to that project
// (ProjectSessionLog).
func (m *Manager) SessionViewLogPath(project, id string) (workspace, logPath string, durableSeq, durableOffset int64, err error) {
	if project != "" {
		root, err := m.ProjectWorkspace(project)
		if err != nil {
			return "", "", 0, 0, err
		}
		return m.ProjectSessionLog(root, id)
	}
	workspace, logPath, err = m.SessionLogPath(project, id)
	if err != nil {
		return "", "", 0, 0, err
	}
	if live, ok := m.Get(id); ok {
		seq, offset := live.Log().DurableBoundary()
		return workspace, logPath, int64(seq), offset, nil
	}
	return workspace, logPath, -1, -1, nil
}

// ProjectSessionLog is SessionViewLogPath confined to the project at the
// absolute root workspace absWS. A live session with this id counts only when
// it runs in absWS or one of the project's in-flight worktrees; otherwise the
// project's own persisted log is used, never another project's.
func (m *Manager) ProjectSessionLog(absWS, id string) (workspace, logPath string, durableSeq, durableOffset int64, err error) {
	if id == "" || id == "." || filepath.Base(id) != id {
		return "", "", 0, 0, fmt.Errorf("%w %q", ErrUnknownSession, id)
	}
	if s, ok := m.Get(id); ok {
		live, err := filepath.Abs(s.Workspace)
		if err != nil {
			return "", "", 0, 0, fmt.Errorf("resolve workspace: %w", err)
		}
		inProject := live == absWS
		if !inProject {
			_, worktrees := m.workstreamSessionOwners(absWS)
			_, inProject = worktrees[live]
		}
		if inProject {
			logPath = filepath.Join(live, ".ycc", "sessions", id, "events.jsonl")
			if _, err := os.Stat(logPath); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return "", "", 0, 0, fmt.Errorf("%w %q", ErrUnknownSession, id)
				}
				return "", "", 0, 0, err
			}
			seq, offset := s.Log().DurableBoundary()
			return live, logPath, int64(seq), offset, nil
		}
	}
	workspace, logPath, err = m.locateSessionLog(absWS, id)
	if err != nil {
		return "", "", 0, 0, err
	}
	return workspace, logPath, -1, -1, nil
}

// SessionTranscript returns the full event log for a session — live or persisted
// on disk — for the read-only transcript view. A live session
// returns its in-memory snapshot; otherwise the persisted
// <workspace>/.ycc/sessions/<id>/events.jsonl is read. The project must be
// named unless it is the daemon's sole project; an unknown/ambiguous project is
// ErrUnknownProject; a session with no live state and no persisted log is
// ErrUnknownSession.
func (m *Manager) SessionTranscript(project, id string) ([]event.Event, error) {
	if s, ok := m.Get(id); ok {
		return s.Log().Snapshot(), nil
	}
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	if id == "" || id == "." || filepath.Base(id) != id {
		return nil, fmt.Errorf("%w %q", ErrUnknownSession, id)
	}
	_, logPath, err := m.locateSessionLog(absWS, id)
	if err != nil {
		return nil, err
	}
	events, err := event.ReadLog(logPath)
	if err != nil {
		return nil, fmt.Errorf("read event log: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("%w %q", ErrUnknownSession, id)
	}
	return events, nil
}

// SessionAttachment returns a retained picture referenced by a user_input event.
// The event-log reference check makes the opaque id session-scoped, while strict
// id validation prevents the request from becoming an arbitrary file read.
func (m *Manager) SessionAttachment(project, id, attachmentID string) ([]byte, string, error) {
	if !validAttachmentID(attachmentID) {
		return nil, "", fmt.Errorf("%w attachment %q", ErrUnknownSession, attachmentID)
	}
	events, err := m.SessionTranscript(project, id)
	if err != nil {
		return nil, "", err
	}
	mediaType := attachmentMediaType(events, attachmentID)
	if mediaType == "" {
		return nil, "", fmt.Errorf("%w attachment %q", ErrUnknownSession, attachmentID)
	}
	var workspace string
	if s, ok := m.Get(id); ok {
		workspace = s.Workspace
	} else {
		workspace, err = m.resolveProjectWorkspace(project)
		if err != nil {
			return nil, "", err
		}
	}
	path := filepath.Join(workspace, ".ycc", "sessions", id, "attachments", attachmentID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("%w attachment %q", ErrUnknownSession, attachmentID)
		}
		return nil, "", fmt.Errorf("read session attachment: %w", err)
	}
	if len(data) == 0 || len(data) > 5<<20 {
		return nil, "", fmt.Errorf("invalid retained attachment size")
	}
	return data, mediaType, nil
}

func validAttachmentID(id string) bool {
	if len(id) != len("a_")+32 || !strings.HasPrefix(id, "a_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "a_"))
	return err == nil
}

func attachmentMediaType(events []event.Event, id string) string {
	for _, ev := range events {
		if ev.Type != event.UserInput {
			continue
		}
		switch images := ev.Data["images"].(type) {
		case []map[string]any:
			for _, image := range images {
				if image["attachment_id"] == id {
					mediaType, _ := image["media_type"].(string)
					return mediaType
				}
			}
		case []any:
			for _, raw := range images {
				image, _ := raw.(map[string]any)
				if image["attachment_id"] == id {
					mediaType, _ := image["media_type"].(string)
					return mediaType
				}
			}
		}
	}
	return ""
}

// CommitDiff returns the `git show` output (stat + patch) for a commit in a
// project's workspace, for the transcript commit-diff drill-in.
// The project must be named unless it is the daemon's sole project. Linked-
// worktree commits are visible from the primary repo since they share the object
// database. A bad/unknown sha surfaces as the underlying git error.
func (m *Manager) CommitDiff(project, sha string) (string, error) {
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return "", err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	repo, err := git.OpenExisting(absWS)
	if err != nil {
		return "", fmt.Errorf("open repo: %w", err)
	}
	return repo.Show(sha)
}

// agentSpec snapshots role metadata at session startup. Unused roles do not
// construct clients or resolve credentials; their factories report errors on use.
// Live reasoning overrides are layered on later via Session.SetThinking.
func (m *Manager) agentSpec(name string) (orchestrator.AgentSpec, error) {
	model, ok := m.reg.GetModel(name)
	if !ok {
		return orchestrator.AgentSpec{}, fmt.Errorf("unknown model %q", name)
	}
	th := m.reg.ThinkingFor(name)
	contextWindow, contextSafeFraction := m.reg.ContextBudget(name)
	return orchestrator.AgentSpec{
		Name:                name,
		Model:               model.Model,
		Backend:             model.Backend,
		ContextWindow:       contextWindow,
		ContextSafeFraction: contextSafeFraction,
		NewClient: func() engine.Turner {
			c, _, err := m.reg.Build(name)
			if err != nil {
				return failedTurner{err: fmt.Errorf("build backend %q: %w", name, err)}
			}
			return c
		},
		Thinking:        th.Thinking,
		Effort:          th.Effort,
		ThinkingDisplay: th.ThinkingDisplay,
	}, nil
}

// Get returns a session by id.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Stop hard-terminates a live session: it cancels the agent loop, closes the
// event log, and removes the session from the manager so its goroutine and
// resources are reclaimed (no leak). Unknown id => ErrUnknownSession.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w %q", ErrUnknownSession, id)
	}
	// Decided under mu, where BeginShutdown flips the phase: once graceful
	// shutdown began, a stop suspends for restart instead of hard-stopping, but
	// still joins the session before returning, so a caller may then destroy its
	// state (a discarded worktree). It stays registered until suspended.
	if m.shuttingDown.Load() {
		m.mu.Unlock()
		m.suspendSession(s)
		m.mu.Lock()
		if m.sessions[id] == s {
			delete(m.sessions, id)
		}
		m.mu.Unlock()
		return nil
	}
	delete(m.sessions, id)
	ended := m.endingLocked(id)
	m.mu.Unlock()
	defer ended()
	s.Stop()
	// Write claims deliberately outlive the session: its uncommitted edits stay
	// attributed to it (other sessions' changesets exclude and list them) until
	// they are committed or reverted, and reopening the session resumes them.
	// Only its ambiguous claims go (endClaims).
	m.endClaims(s)
	m.waitWorkstreamWatcher(id)
	if s.stopErr != nil {
		// Stopped, but not cleanly joined: say so rather than report success.
		return fmt.Errorf("session %s stopped, but %w", id, s.stopErr)
	}
	return nil
}

// endClaims releases an ended session's ambiguous claims (spec §7.3): its shell
// only possibly wrote those paths, and no coordinator is left to release them.
// Call it after the session's jobs have exited. A replacement that reopened
// the session meanwhile has its own token and keeps its claims.
func (m *Manager) endClaims(s *Session) {
	if s.deps != nil {
		m.ownership.EndScope(s.deps.CoordinatorToken, s.Workspace)
	}
}

// waitWorkstreamWatcher joins a watcher after its session log has been closed so
// callers may safely tear down/reopen the worktree. Never hold m.mu while
// waiting: readiness emission briefly needs that mutex.
func (m *Manager) waitWorkstreamWatcher(id string) {
	m.mu.Lock()
	watch, ok := m.workstreamWatches[id]
	m.mu.Unlock()
	if ok {
		<-watch.done
	}
}

// stopWorkstreamWatcher ends a session's workstream watcher without closing
// its log (the watcher finishes the event in hand, then stops) and joins it.
func (m *Manager) stopWorkstreamWatcher(id string) {
	m.mu.Lock()
	watch, ok := m.workstreamWatches[id]
	m.mu.Unlock()
	if ok {
		watch.stop()
		<-watch.done
	}
}

// reclaimIfCurrent reclaims s only while it is still the exact live instance
// registered under its id. It cancels the loop and closes the log via
// Session.reap without emitting a terminal session_stopped marker. A
// stopped/reclaimed session can be reopened with the same id, so callers doing
// delayed cleanup must never act on id alone.
func (m *Manager) reclaimIfCurrent(s *Session) bool {
	m.mu.Lock()
	current, ok := m.sessions[s.ID]
	if !ok || current != s {
		m.mu.Unlock()
		return false
	}
	// Decided under mu, where BeginShutdown flips the phase: once shutdown
	// began, ShutdownForRestart owns teardown, so leave the session live for the
	// drain and the final suspension.
	if m.shuttingDown.Load() {
		m.mu.Unlock()
		return true
	}
	delete(m.sessions, s.ID)
	ended := m.endingLocked(s.ID)
	m.mu.Unlock()
	defer ended()
	s.reap()
	m.endClaims(s)
	m.waitWorkstreamWatcher(s.ID)
	return true
}

// endingLocked records, until the returned func runs, that an instance of id
// has left the live map but its teardown (joining its jobs) has not finished,
// so workstream cleanup does not mistake it for a closed session (Ending).
// The caller holds mu.
func (m *Manager) endingLocked(id string) func() {
	if m.ending == nil {
		m.ending = map[string]int{}
	}
	m.ending[id]++
	return func() {
		m.mu.Lock()
		if m.ending[id]--; m.ending[id] <= 0 {
			delete(m.ending, id)
		}
		m.mu.Unlock()
	}
}

// sessionEnding reports whether an instance of id is still being torn down.
func (m *Manager) sessionEnding(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ending[id] > 0
}

// ReclaimAll releases every live session without writing session_stopped markers.
// It is used during daemon teardown: cancellation stops in-flight turns, while
// leaving each durable log exactly as it was keeps the session reopenable. The
// live map is drained atomically before cancellation, so repeated calls are safe.
// After BeginShutdown it suspends sessions for restart instead (teardown), so a
// late or deferred call can never fall back to the ordinary cancel path.
func (m *Manager) ReclaimAll() {
	if !m.shuttingDown.Load() {
		m.gitSyncCancel()
		m.gitSyncWG.Wait()
		m.stopBackground()
	}
	// Decide the teardown kind atomically with retiring the map: BeginShutdown
	// sets shuttingDown under mu, so an ordinary reclaim that raced it either
	// emptied the map first or hands over to restart suspension here.
	m.mu.Lock()
	if m.shuttingDown.Load() {
		m.mu.Unlock()
		m.cancelBackground()
		m.gitSyncCancel()
		m.suspendAll()
		// Workers were cancelled at BeginShutdown; join them only now, so a
		// non-interruptible worker step (a rebase, a fetch) cannot hold sessions
		// running past the drain deadline.
		m.joinBackground()
		m.gitSyncWG.Wait()
		m.closeLeases()
		return
	}
	sessions := m.sessions
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()

	// Cancel every session before joining any watcher so independent in-flight
	// turns are torn down together rather than serially.
	for _, s := range sessions {
		s.reap()
	}
	for id := range sessions {
		m.waitWorkstreamWatcher(id)
	}
	m.closeLeases()
}

// stopBackground stops and joins, in order, the manager-owned background
// workers other than git sync: work loops, the integration queue, grooms, the
// watch scheduler, and the GC reaper. (Ordinary ReclaimAll stops and joins git
// sync first, so no background git races the session or worktree teardown.)
func (m *Manager) stopBackground() {
	// Cancel and join daemon-owned work loops before draining the session map. The
	// real runner uses this context to reclaim its current session, clear the
	// persisted current-session id, and publish a terminal loop outcome.
	m.cancelLoops()
	m.loopWG.Wait()
	m.cancelIntegration()
	m.integrationWG.Wait()
	// Stop scheduling automatic memory grooms and join their watchers, which
	// reclaim any groom still running.
	m.cancelGrooms()
	m.groomWG.Wait()
	// Stop the watch scheduler and run followers; run sessions are torn down by
	// the caller.
	m.stopWatches()
	m.reaperCancel()
	m.reaperWG.Wait()
}

// cancelBackground cancels every worker stopBackground stops, without joining
// and without taking the workers' own locks: a worker may hold its lock across
// a slow, non-interruptible step (a watch or groom starting a session runs git),
// and shutdown must not wait behind it. New work is refused meanwhile by
// shuttingDown; joinBackground later sets the workers' stop flags.
func (m *Manager) cancelBackground() {
	m.loopCancel()
	m.integrationCancel()
	m.groomCancel()
	m.watchRunning.Store(false)
	m.watchCancel()
	m.reaperCancel()
}

// joinBackground stops (now under their locks) and joins the workers
// cancelBackground cancelled.
func (m *Manager) joinBackground() {
	m.cancelLoops()
	m.cancelIntegration()
	m.cancelGrooms()
	m.cancelWatches()
	m.loopWG.Wait()
	m.integrationWG.Wait()
	m.groomWG.Wait()
	m.watchWG.Wait()
	m.reaperWG.Wait()
}

func (m *Manager) cancelLoops() {
	m.loopMu.Lock()
	defer m.loopMu.Unlock()
	if !m.loopStop {
		m.loopStop = true
		m.loopCancel()
	}
}

func (m *Manager) cancelIntegration() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.integrationStop {
		m.integrationStop = true
		m.integrationCancel()
	}
}

func (m *Manager) cancelGrooms() {
	m.groomMu.Lock()
	defer m.groomMu.Unlock()
	if !m.groomStop {
		m.groomStop = true
		m.groomCancel()
	}
}

// List returns all live sessions.
func (m *Manager) List() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// ListByProject returns live sessions whose workspace is the named project's
// workspace. An empty name returns all sessions; an unknown name returns none.
func (m *Manager) ListByProject(name string) []*Session {
	if name == "" {
		return m.List()
	}
	ws, ok := m.projects.Resolve(name)
	if !ok {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.Workspace == ws {
			out = append(out, s)
		}
	}
	return out
}

// Models returns the configured logical models for the settings overlay pickers.
func (m *Manager) Models() []config.ModelInfo { return m.reg.Models() }

// OAuthModels groups configured subscription-authenticated logical model names
// by provider. A provider login is account-scoped, so models in each group share
// one subscription allowance report.
func (m *Manager) OAuthModels() map[string][]string {
	out := map[string][]string{}
	for _, model := range m.reg.Models() {
		if model.Auth == "oauth" && (model.Backend == "anthropic" || model.Backend == "openai" || model.Backend == "gemini") {
			out[model.Backend] = append(out[model.Backend], model.Name)
		}
	}
	return out
}

// Budget returns the configured spend caps so the RPC
// layer can serve the loop caps to the TUI work-loop driver.
func (m *Manager) Budget() config.Budget { return m.reg.Budget() }

// GetModel returns a copy of a logical model's record for editing in the
// settings overlay.
func (m *Manager) GetModel(name string) (config.Model, bool) { return m.reg.GetModel(name) }

// AdoptAnthropicLogin points a not-yet-set-up daemon at a just-stored Claude
// subscription login (see config.Registry.AdoptAnthropicLogin).
func (m *Manager) AdoptAnthropicLogin() (bool, error) { return m.reg.AdoptAnthropicLogin() }

// AdoptOpenAILogin points an untouched built-in fallback at the ChatGPT subscription.
func (m *Manager) AdoptOpenAILogin() (bool, error) { return m.reg.AdoptOpenAILogin() }

// AdoptGoogleLogin points an untouched built-in fallback at the Google subscription.
func (m *Manager) AdoptGoogleLogin() (bool, error) { return m.reg.AdoptGoogleLogin() }

// DiscoverModels lists the model ids available from a backend connection for the
// connection form. key_env is resolved locally; the secret
// value never leaves the daemon.
func (m *Manager) DiscoverModels(ctx context.Context, backend, baseURL, keyEnv string) ([]string, error) {
	return m.reg.DiscoverConnModels(ctx, backend, baseURL, keyEnv)
}

// UpsertModel adds or replaces a logical model backend at runtime; persist also
// writes ycc.toml.
func (m *Manager) UpsertModel(name string, mdl config.Model, persist bool) error {
	return m.reg.UpsertModel(name, mdl, persist)
}

// ReviewTierConfigs lists the effective review tiers in configuration form plus
// the effective default tier name so clients can render and
// edit them.
func (m *Manager) ReviewTierConfigs() ([]config.ReviewTierListing, string) {
	return m.reg.ReviewTierConfigs()
}

// UpsertReviewTier adds or replaces a configured review tier and persists it to
// ycc.toml. Takes effect on the next spawn_reviewers.
func (m *Manager) UpsertReviewTier(name string, t config.ReviewTier) error {
	return m.reg.UpsertReviewTier(name, t)
}

// RemoveReviewTier deletes a configured review tier (a built-in name reverts to
// its built-in behaviour) and persists.
func (m *Manager) RemoveReviewTier(name string) error {
	return m.reg.RemoveReviewTier(name)
}

// SetReviewDefault sets reviews.default (empty clears it) and persists.
func (m *Manager) SetReviewDefault(name string) error {
	return m.reg.SetReviewDefault(name)
}

// WorkImplementation returns the effective work-mode implementation strategy
// used when the next session is built.
func (m *Manager) WorkImplementation() string { return m.reg.WorkImplementation() }

// SetWorkImplementation updates and persists the work-mode implementation
// strategy. Existing sessions retain the toolset and prompt they started with.
func (m *Manager) SetWorkImplementation(impl string) error {
	return m.reg.SetWorkImplementation(impl)
}

// SetRoles updates and persists the default per-role model assignment. It is
// used only for global settings changes made without a session id; live-session
// changes go through Session.SetRoleConfig and remain local to that session.
func (m *Manager) SetRoles(coordinator, implementer string, reviewers []string) error {
	return m.reg.SetRoles(coordinator, implementer, reviewers)
}

// SetThinking resolves role to its currently assigned model(s) and persists the
// level, clamped to each model's supported levels, in each model's config. An empty role targets all
// assigned models; shared assignments are deduplicated. Used when there is no
// live session to update.
func (m *Manager) SetThinking(role, level string) error {
	if _, ok := thinkingForLevel(level); !ok {
		return fmt.Errorf("unknown thinking level %q", level)
	}
	var names []string
	switch role {
	case "":
		names = append(names, m.reg.CoordinatorName(), m.reg.ImplementerName())
		names = append(names, m.reg.ReviewerNames()...)
	case roleCoordinator:
		names = []string{m.reg.CoordinatorName()}
	case roleImplementer:
		names = []string{m.reg.ImplementerName()}
	case roleReviewers:
		names = m.reg.ReviewerNames()
	default:
		return fmt.Errorf("unknown thinking role %q", role)
	}
	seen := make(map[string]bool, len(names))
	models := make([]string, 0, len(names))
	for _, name := range names {
		if name != "" && !seen[name] {
			seen[name] = true
			models = append(models, name)
		}
	}
	sort.Strings(models)
	for _, name := range models {
		if err := m.reg.SetModelThinking(name, level); err != nil {
			return fmt.Errorf("persist thinking level for model %q: %w", name, err)
		}
	}
	return nil
}

// Roles returns the current default per-role assignment (config.Roles) so the
// settings overlay can seed its pickers with the real current selection.
func (m *Manager) Roles() (coordinator, implementer string, reviewers []string) {
	return m.reg.CoordinatorName(), m.reg.ImplementerName(), m.reg.ReviewerNames()
}

// ThinkingLevels returns each role's current model-level setting so the overlay
// can seed its role-oriented controls. Reviewers use the first assigned model.
func (m *Manager) ThinkingLevels() (coordinator, implementer, reviewers string) {
	coord, impl, revs := m.Roles()
	reviewer := ""
	if len(revs) > 0 {
		reviewer = revs[0]
	}
	return m.reg.ModelThinkingLevel(coord), m.reg.ModelThinkingLevel(impl), m.reg.ModelThinkingLevel(reviewer)
}

// RemoveModel deletes a logical model backend; persist also writes ycc.toml
// The removal is rejected if a static role (cfg.Roles) references
// the model, or if any running session's live role config (set via
// SetRoleConfig, stored on the Session rather than cfg.Roles) still references
// it — otherwise that session's next spawn would point at a missing backend.
func (m *Manager) RemoveModel(name string, persist bool) error {
	m.mu.Lock()
	var refID string
	for id, s := range m.sessions {
		if s.ReferencesModel(name) {
			refID = id
			break
		}
	}
	m.mu.Unlock()
	if refID != "" {
		return fmt.Errorf("cannot remove model %q: still referenced by running session %s", name, refID)
	}
	return m.reg.RemoveModel(name, persist)
}

// Backlog returns a docs.Store for the named project (or the sole project when
// omitted). Used by the read-only backlog RPCs.
func (m *Manager) Backlog(project string) (*docs.Store, error) {
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	return m.backlogStore(absWS), nil
}

// Ownership exposes the daemon-wide worktree execution-lease service (used by
// tests and diagnostics to observe or simulate a busy worktree). Structured
// backlog/memory writes intentionally do not acquire it: they are atomic,
// serialized by the docs.Store directory lock, and must stay available to the
// user while a session or agent is working.
func (m *Manager) Ownership() *workspacelease.Service { return m.ownership }

// persistentOwnership returns the daemon's ownership service with write claims
// persisted per worktree, so change attribution survives a daemon restart.
func persistentOwnership() *workspacelease.Service {
	s := workspacelease.NewService()
	s.PersistClaims()
	return s
}

// sectionWait bounds how long a user-initiated whole-tree operation (workstream
// spawn, merge, discard) waits for an in-flight write or commit in the same
// tree. Worktree sections are operation-scoped, so this is normally instant.
const sectionWait = 30 * time.Second

// acquireSection takes the short exclusive worktree section for a whole-tree
// operation, waiting out a concurrent write or commit rather than refusing.
func (m *Manager) acquireSection(root string, token *workspacelease.Token) (*workspacelease.Lease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sectionWait)
	defer cancel()
	return m.ownership.AcquireWait(ctx, root, token)
}

// CaptureBacklogItem runs the lightweight, off-stream "quick-add backlog item"
// capture agent for a project: it turns a natural-language
// description into a structured backlog task without disturbing any running
// session. The agent may ask ONE clarifying question (returned via Question);
// the client re-invokes with priorQuestion/priorAnswer so it creates the task.
// The project may be omitted only when the daemon has one project; backlog writes
// are serialized in docs.Store so a concurrent work session can't corrupt the
// index. emit, when non-nil, receives the capture agent's action-log events live.
func (m *Manager) CaptureBacklogItem(ctx context.Context, project, description, priorQuestion, priorAnswer string, emit func(event.Event)) (orchestrator.CaptureResult, error) {
	if strings.TrimSpace(description) == "" {
		return orchestrator.CaptureResult{}, fmt.Errorf("description is required")
	}
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return orchestrator.CaptureResult{}, err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return orchestrator.CaptureResult{}, fmt.Errorf("resolve workspace: %w", err)
	}
	store := m.backlogStore(absWS)
	coord := m.reg.CoordinatorName()
	client, model, err := m.reg.Build(coord)
	if err != nil {
		return orchestrator.CaptureResult{}, fmt.Errorf("build capture backend: %w", err)
	}
	contextWindow, _ := m.reg.ContextBudget(coord)
	cd := orchestrator.CaptureDeps{
		Workspace:        absWS,
		Docs:             store,
		Client:           client,
		Model:            model,
		ModelName:        coord,
		Backend:          m.reg.BackendFor(coord),
		Thinking:         engine.Thinking{}, // reasoning OFF for a fast capture
		MaxTok:           m.reg.MaxTokens(),
		ContextWindow:    contextWindow,
		Retry:            m.reg.RetryPolicy(),
		Ownership:        m.ownership,
		CoordinatorToken: m.ownership.NewToken("backlog capture for " + absWS),
	}
	var rec event.Recorder
	if emit != nil {
		rec = event.NewFuncRecorder(emit)
	}
	ctx, cancel := context.WithTimeout(ctx, orchestrator.CaptureTimeout)
	defer cancel()
	res, err := orchestrator.RunCapture(ctx, cd, rec, description, priorQuestion, priorAnswer)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return res, fmt.Errorf("capture timed out after %s", orchestrator.CaptureTimeout)
	}
	return res, err
}

// ErrUnknownProject indicates a project name did not resolve to a registered
// workspace. It is a client-input error (distinct from IO/scan failures) so RPC
// handlers can map it to an invalid-argument code rather than internal.
var ErrUnknownProject = errors.New("unknown project")

// ErrUnknownSession indicates a session id is not (or no longer) live in the
// manager, so RPC handlers can map it to a not-found code.
var ErrUnknownSession = errors.New("unknown session")

// ErrUnknownModel indicates a requested logical model name is not configured
// (e.g. a per-session coordinator override at StartSession), so RPC handlers can
// map it to an invalid-argument code.
var ErrUnknownModel = errors.New("unknown model")

// ErrUnknownThinkingLevel indicates a requested thinking level is not one of
// off|low|medium|high|xhigh|max.
var ErrUnknownThinkingLevel = errors.New("unknown thinking level")

// validStartThinking checks an optional start-time thinking level.
func validStartThinking(level string) error {
	if level == "" {
		return nil
	}
	if _, ok := thinkingForLevel(level); !ok {
		return fmt.Errorf("%w %q", ErrUnknownThinkingLevel, level)
	}
	return nil
}

// ErrDisabledModel indicates a configured logical model was explicitly selected
// for new work while temporarily disabled.
var ErrDisabledModel = config.ErrModelDisabled

// ErrLoopRunning indicates a work loop is already running/waiting/stopping for a
// workspace, so StartWorkLoop handlers can map it to a failed-precondition code.
var ErrLoopRunning = errors.New("work loop already running")

// UsageReport scans the named project's workspace, or all registered project
// workspaces when omitted, and returns the aggregated, priced usage breakdown
// Pricing comes from the daemon's model registry.
func (m *Manager) UsageReport(project string, opts usage.Options) (*usage.Result, error) {
	projects := m.projects.List()
	if project == "" && len(projects) > 1 {
		seen := make(map[string]struct{}, len(projects))
		var entries []usage.Entry
		for _, p := range projects {
			absWS, err := filepath.Abs(p.Path)
			if err != nil {
				return nil, fmt.Errorf("resolve workspace for project %q: %w", p.Name, err)
			}
			if _, ok := seen[absWS]; ok {
				continue
			}
			seen[absWS] = struct{}{}
			projectEntries, err := m.usageScans.Scan(absWS)
			if err != nil {
				return nil, err
			}
			entries = append(entries, projectEntries...)
		}
		res := usage.Aggregate(entries, m.reg, opts)
		return &res, nil
	}

	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	entries, err := m.usageScans.Scan(absWS)
	if err != nil {
		return nil, err
	}
	res := usage.Aggregate(entries, m.reg, opts)
	res.Workspace = absWS
	return &res, nil
}

// memorySourceFromEvents chooses the closest durable candidate evidence
// available for a remember call. This structural match is not proof that the
// event semantically supports the note. User guidance prefers the latest actual
// user-authored event; measurements prefer tool output. Inferences and proposals
// point at the latest event (normally the remember call containing the claim).
func memorySourceFromEvents(sessionID string, events []event.Event, kind docs.MemoryKind) docs.MemoryProvenance {
	matches := func(ev event.Event) bool {
		switch kind {
		case docs.MemoryUserGuidance:
			if ev.Type == event.QuestionAnswered {
				auto, _ := ev.Data["auto"].(bool)
				return !auto
			}
			return ev.Actor == "user" && (ev.Type == event.UserInput || ev.Type == event.UserInputDelivered)
		case docs.MemoryObservation:
			return ev.Type == event.ToolResult
		default:
			return true
		}
	}
	var source *event.Event
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Transient || !matches(events[i]) {
			continue
		}
		source = &events[i]
		break
	}
	if source == nil && kind != docs.MemoryUserGuidance {
		for i := len(events) - 1; i >= 0; i-- {
			if !events[i].Transient {
				source = &events[i]
				break
			}
		}
	}
	// Preserve the session reference even when no event of the preferred kind is
	// available. AppendMemoryEntry will conservatively downgrade unsupported
	// user-guidance claims instead of inventing user authority.
	provenance := docs.MemoryProvenance{SessionID: sessionID, Scope: "workspace"}
	if source != nil {
		provenance.EventSeq = source.Seq
		provenance.EventTime = source.TS
		provenance.Actor = source.Actor
		if source.Type == event.QuestionAnswered {
			provenance.Actor = "user"
		}
	}
	return provenance
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "s_" + hex.EncodeToString(b), nil
}

func newAttachmentID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "a_" + hex.EncodeToString(b), nil
}

// newWorkstreamID mints a stable short workstream id (ws_<8-hex>), mirroring
// newID.
func newWorkstreamID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ws_" + hex.EncodeToString(b), nil
}

// oversizedUntrackedNotice tells the user which untracked files the session's
// git snapshots leave out because of their size.
func oversizedUntrackedNotice(paths []string) string {
	const shown = 5
	list := strings.Join(paths[:min(len(paths), shown)], ", ")
	if len(paths) > shown {
		list += fmt.Sprintf(" and %d more", len(paths)-shown)
	}
	return fmt.Sprintf("Untracked files over %d MiB are left out of this session's git snapshots, so they are never reviewed or committed: %s. Add them to .gitignore to silence this.", git.MaxUntrackedSnapshotMiB(), list)
}
