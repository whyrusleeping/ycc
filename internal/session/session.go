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
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/notify"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/tools"
	"github.com/whyrusleeping/ycc/internal/usage"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// Config parameterizes a new session.
type Config struct {
	Workspace  string
	Mode       string
	Unattended bool
	Prompt     string
	// Project, when set, names a registered project whose workspace is used,
	// overriding Workspace.
	Project string
	// CoordinatorModel, when set, overrides the coordinator's logical model FOR
	// THIS SESSION ONLY: the persisted per-role defaults are
	// untouched and implementer/reviewers keep them. An unknown name is an error.
	CoordinatorModel string
	// Preset identifies the client-side opening-prompt preset. A configured
	// roles.presets binding selects this session's initial coordinator without
	// changing the persisted role defaults. An unbound preset behaves normally.
	Preset string
	// Images optionally attaches validated pictures to the opening prompt. The
	// initial user_input event records only metadata plus opaque references;
	// payloads are retained separately for transcript display.
	Images []engine.Image
}

// Session is one running agent conversation backed by a persistent event log.
type Session struct {
	ID        string
	Workspace string
	Mode      string

	unattended bool

	log                 *event.Log
	emitter             *event.Emitter
	loop                *engine.Loop
	inter               *interaction
	deps                *orchestrator.Deps
	reg                 *config.Registry
	prompt              string
	preset              string
	coordinatorExplicit bool // StartSession coordinator_model won over any preset binding
	// startupNotice is emitted as a visible, non-fatal event after the session
	// lifecycle marker (used when a stale preset binding falls back safely).
	startupNotice  string
	startupPreload orchestrator.ExplicitTaskPreload
	buildLoop      func(mode, prompt string) (*engine.Loop, error)

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

	inputCh   chan string
	messageCh chan engine.UserMessage
	// sendMu serializes idle senders across the capacity-check, durable echo, and
	// channel delivery. The run goroutine only drains these channels, so once a
	// slot is observed the post-record send is guaranteed not to block or fail.
	sendMu sync.Mutex
	// retryCh nudges the idle-after-error run loop to re-run the failed turn on
	// the existing history (no new user message). Unbuffered so a Retry while the
	// loop is running/paused is a harmless no-op (nothing is receiving).
	retryCh chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc

	// stopOnce guards Stop so a hard terminate runs at most once even if both a
	// StopSession RPC and a manager teardown race to call it.
	stopOnce sync.Once

	mu          sync.Mutex
	status      event.Status
	coordinator string   // logical model name driving the coordinator
	implementer string   // logical model name for the implementer role
	reviewers   []string // logical model names for the reviewer role
	// thinkLevels holds per-model reasoning overrides, keyed
	// by logical model name. An empty/missing entry means "use the model config";
	// any of off/low/medium/high/xhigh/max forces that level for the model until
	// changed.
	thinkLevels map[string]string
	// Spend guard, guarded by s.mu. budgetWarned is set
	// once the session crosses ~80% of a configured cap (so the warning fires at
	// most once); budgetBreached is set once a cap is crossed and handled (the
	// attended Confirm is asked / the unattended wrap-up is injected at most once).
	// Both are seeded from the replayed log on Reopen so a reopened session that
	// already crossed the line does not re-fire or re-ask.
	budgetWarned   bool
	budgetBreached bool
	// refused marks a session parked after a provider-side safety refusal
	// (engine Result.Refused): the coordinator's last turn came back
	// with stop_reason "refusal" and was kept out of history. Refusals are
	// sticky (per provider docs, continuing the conversation keeps being
	// refused), so while set, SendInput is rejected with guidance; recovery is
	// Resume() (explicit retry of the pending turn as-is) or a coordinator model
	// change via SetRoleConfig (which clears the gate and retries
	// automatically). Cleared at the start of every run-loop iteration.
	refused bool

	// Interrupt & steer state, guarded by a dedicated mutex so it
	// never contends with the s.mu hot paths. pauseReq is set by Interrupt and
	// consumed at the next checkpoint; paused is true while a checkpoint blocks;
	// resumeReq wakes it (Resume or a steered correction); corrections buffers
	// steered-in user messages to inject at the next checkpoint; resumeCh is closed
	// to wake. running is true while the agent loop's Run is executing, so a
	// mid-run SendInput is queued as a correction (steer-by-default) and delivered
	// at the next safe checkpoint rather than waiting for the run to finish.
	steerMu     sync.Mutex
	pauseReq    bool
	paused      bool
	running     bool
	resumeReq   bool
	corrections []correction
	resumeCh    chan struct{}
}

// correction is a steered-in user message buffered until the next checkpoint (or
// explicit Resume when paused). seq is the sequence of the queued user_input echo
// emitted when it was accepted, so the later user_input_delivered event can refer
// back to it.
type correction struct {
	message engine.UserMessage
	seq     int
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
	s.mu.Unlock()
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
	// Mid-run or paused: buffer as a correction and echo it as queued. A run in
	// flight drains corrections at its next safe checkpoint; a paused loop drains
	// them only on an explicit Resume. Either way multiple sends land in FIFO
	// order because the queued echo (which stamps the seq) and the append happen
	// together under steerMu. It does NOT auto-resume a paused loop.
	s.steerMu.Lock()
	if s.paused || s.pauseReq || s.running {
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
		s.steerMu.Unlock()
		return nil
	}
	s.steerMu.Unlock()

	// Idle delivery is transactional with respect to the durable user_input echo:
	// reject a full queue before recording, then record before making the input
	// visible to the run goroutine. sendMu excludes competing producers, while the
	// sole consumer can only create more capacity, so the final send cannot block.
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	if len(input.Images) == 0 {
		if len(s.inputCh) >= cap(s.inputCh) {
			return fmt.Errorf("session %s input buffer full", s.ID)
		}
		s.emitter.EmitAs("user", event.UserInput, eventData(false))
		if err := s.logFailure(); err != nil {
			return fmt.Errorf("session event log failed: %w", err)
		}
		s.inputCh <- input.Text
		return nil
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
		return
	}
	s.stopOnce.Do(func() {
		s.setStatus(event.StatusStopped)
		s.emitter.Emit(event.SessionStopped, map[string]any{})
		s.cancel()
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
		s.log.Close()
	})
}

// reapable reports whether the session is safe for the idle reaper to stop: it
// is idle (between turns — a turn blocked on ask_user stays StatusRunning), not
// paused/pausing for steer, and has no pending question. This deliberately
// excludes sessions legitimately waiting for user input.
func (s *Session) reapable() bool {
	if s.Status() != event.StatusIdle {
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

// drainJobNotes delivers the final reports of finished, unconsumed coordinator
// jobs as user-role notification messages. Each
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
			"status": string(r.Status), "text": text,
		})
		if err := s.logFailure(); err != nil {
			return nil, fmt.Errorf("session event log failed: %w", err)
		}
		out = append(out, text)
	}
	return out, nil
}

// killJobs terminates every background job for this session (session end). It is
// nil-safe so minimally-constructed sessions (tests) can call it.
func (s *Session) killJobs() {
	if s.deps != nil && s.deps.Jobs != nil {
		s.deps.Jobs.KillAll()
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
		if len(c.message.Images) > 0 {
			images := make([]map[string]any, len(c.message.Images))
			for j, img := range c.message.Images {
				images[j] = map[string]any{"media_type": img.MediaType, "filename": img.Filename}
			}
			data["images"] = images
		}
		s.emitter.EmitAs("user", event.UserInputDelivered, data)
		if err := s.logFailure(); err != nil {
			return nil, fmt.Errorf("session event log failed: %w", err)
		}
		messages = append(messages, c.message)
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
	s.pauseReq = true
	s.steerMu.Unlock()
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
	s.steerMu.Lock()
	if s.paused {
		s.signalResumeLocked()
		s.steerMu.Unlock()
		return nil
	}
	s.pauseReq = false
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
	return nil
}

// SetRoleConfig reassigns per-role logical models mid-session and rebuilds the
// relevant gollama clients so the next coordinator turn / next spawned subagent
// uses the new assignment. Empty coordinator/implementer leaves
// that role unchanged; an empty reviewers slice leaves reviewers unchanged. The
// new assignment is also persisted as the default (roles in ycc.toml) so it
// survives a restart and applies to future sessions.
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

	// Swap the live coordinator loop only when that assignment changed, preserving
	// an already-constructed client until the disabled role is explicitly replaced.
	if coordChanged {
		client, model, err := s.reg.Build(newCoord)
		if err != nil {
			return fmt.Errorf("build coordinator backend: %w", err)
		}
		s.currentLoop().SetBackend(client, model, newCoord, s.reg.BackendFor(newCoord), s.thinkingFor(newCoord))
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
	s.mu.Unlock()

	s.emitter.Emit(event.RoleConfigChanged, map[string]any{
		"coordinator": newCoord,
		"implementer": newImpl,
		"reviewers":   newRevs,
	})
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	// Persist defaults only after the session mutation is replayable. Full-state
	// clients submit unchanged roles too, but a per-session override must not become
	// the global default merely because another role changed; persist deltas only.
	persistCoord, persistImpl := "", ""
	var persistReviewers []string
	if coordChanged {
		persistCoord = newCoord
	}
	if implChanged {
		persistImpl = newImpl
	}
	if reviewersChanged {
		persistReviewers = newRevs
	}
	if err := s.reg.SetRoles(persistCoord, persistImpl, persistReviewers); err != nil {
		return fmt.Errorf("persist role config: %w", err)
	}
	if wasRefused {
		// Non-blocking, like Resume: if the loop is mid-run (not parked on the
		// retry-aware wait) the nudge falls through as a harmless no-op.
		select {
		case s.retryCh <- struct{}{}:
		default:
		}
	}
	return nil
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

// SetThinking applies a reasoning level to the model(s) assigned to role. An
// empty role targets all assigned models; reviewers targets the
// whole reviewer fan-out. Models are deduplicated, so shared role assignments
// receive one override and one persisted update. Every role backed by a targeted
// model is refreshed for its next turn/spawn.
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
		s.thinkLevels[name] = level
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
	s.emitter.Emit(event.ThinkingLevelChanged, map[string]any{
		"role": emittedRole, "models": models, "from": from, "to": level,
	})
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	// Persist only after the live mutation is replayable. Each target model keeps
	// its level when role assignments later change.
	for _, name := range models {
		if err := s.reg.SetModelThinking(name, level); err != nil {
			return fmt.Errorf("persist thinking level for model %q: %w", name, err)
		}
	}
	return nil
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

// failedTurner lets a model that became unavailable after an AgentSpec was
// assembled fail its next turn cleanly instead of returning a nil backend from
// the factory. AgentSpec's factory predates error returns, so the error is
// surfaced through the TurnCtx capability it already requires.
type failedTurner struct{ err error }

func (t failedTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return nil, t.err
}

// agentSpec builds an orchestrator.AgentSpec for a logical model name.
func (s *Session) agentSpec(name string) (orchestrator.AgentSpec, error) {
	_, model, err := s.reg.Build(name)
	if err != nil {
		return orchestrator.AgentSpec{}, fmt.Errorf("build backend %q: %w", name, err)
	}
	th := s.thinkingFor(name)
	contextWindow, contextSafeFraction := s.reg.ContextBudget(name)
	return orchestrator.AgentSpec{
		Name:                name,
		Model:               model,
		Backend:             s.reg.BackendFor(name),
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
// Unknown models are skipped (graceful degradation); a tier that resolves to no
// agents falls back to the session's current reviewer assignment, and finally to
// coordinator self-review if even that is empty.
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
			plan.SelfReview = true
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
	// Kill every background job when the coordinator loop's lifetime ends, so a
	// session leaves no orphan processes.
	defer s.killJobs()
	if s.resumed {
		// Reopened session ("resume = replay"): the loop already
		// carries a history reconstructed from the existing log, so do NOT emit a
		// fresh SessionStarted / initial UserInput nor seed. Mark the reopen in the
		// continuous log.
		s.emitter.Emit(event.SessionReopened, map[string]any{})
		if s.startupNotice != "" {
			s.emitter.Emit(event.SessionNotice, map[string]any{"msg": s.startupNotice, "level": "warning"})
		}
		if s.ctx.Err() != nil {
			return
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
		if !s.currentLoop().PendingResponse() {
			s.setStatus(event.StatusIdle)
			select {
			case text := <-s.inputCh:
				if s.ctx.Err() != nil {
					return
				}
				s.currentLoop().Post(text)
			case input := <-s.messageCh:
				if s.ctx.Err() != nil {
					return
				}
				s.currentLoop().PostMessage(input)
			case <-s.ctx.Done():
				return
			}
		}
	} else {
		s.mu.Lock()
		coord := s.coordinator
		s.mu.Unlock()
		s.emitter.Emit(event.SessionStarted, map[string]any{
			"workspace":            s.Workspace,
			"mode":                 s.Mode,
			"preset":               s.preset,
			"coordinator_explicit": s.coordinatorExplicit,
			// The coordinator model is recorded so a resume replays the session on
			// the model it was started with, including a per-session override
			// picked at StartSession.
			"coordinator": coord,
		})
		if s.startupNotice != "" {
			s.emitter.Emit(event.SessionNotice, map[string]any{"msg": s.startupNotice, "level": "warning"})
		}
		if s.ctx.Err() != nil {
			return
		}
		// The opening prompt echoes like any other user input; pictures are recorded
		// as metadata plus retained-payload references. Model replay still gets the
		// explicit unavailable-in-history note rather than reinjected pixels.
		initial := map[string]any{"text": s.prompt}
		if meta := imageMetadata(s.promptImages); meta != nil {
			initial["images"] = meta
		}
		s.emitter.EmitAs("user", event.UserInput, initial)
		// When the opening prompt named one existing task, Start already executed
		// the routine backlog reads and appended their synthetic exchange to model
		// history. Record that exact exchange immediately after the real user input
		// so event replay reconstructs the same ordering.
		s.startupPreload.Emit(s.emitter, s.loop.ModelName, s.loop.Backend, s.loop.Model)
		if s.logFailure() != nil || s.ctx.Err() != nil {
			return
		}
	}

	for {
		if s.ctx.Err() != nil {
			return
		}
		s.setStatus(event.StatusRunning)
		// A starting run clears the refusal gate: this iteration IS the retry
		// (via Resume, a model switch, or reopen re-running the pending turn).
		// If the provider refuses again the gate is simply re-established below.
		s.setRefused(false)
		s.steerMu.Lock()
		s.running = true
		s.steerMu.Unlock()
		res, err := s.currentLoop().Run(s.ctx)
		// Clear running BEFORE checking ctx/handling the result so a SendInput
		// racing the end of the run either landed as a correction (drained just
		// below) or, seeing running=false, takes the idle inputCh path.
		s.steerMu.Lock()
		s.running = false
		s.steerMu.Unlock()
		if s.ctx.Err() != nil {
			return
		}
		if err != nil {
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
				continue // run the new mode's loop immediately (its first checkpoint drains any pending corrections)
			} else {
				s.emitter.Emit(event.SessionError, map[string]any{"msg": "mode switch failed: " + berr.Error()})
			}
		} else {
			s.setStatus(event.StatusIdle)
			data := map[string]any{"report": s.withAssumptions(res.Report)}
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
			continue
		}

		select {
		case text := <-s.inputCh:
			if s.ctx.Err() != nil {
				return
			}
			s.currentLoop().Post(text)
		case input := <-s.messageCh:
			if s.ctx.Err() != nil {
				return
			}
			s.currentLoop().PostMessage(input)
		case <-s.retryCh:
			if s.ctx.Err() != nil {
				return
			}
			// Retry after a session error: mark the retry as resumed only when the
			// parked run loop actually consumes it, then re-run the failed turn on
			// existing history without injecting a user message.
			s.emitter.Emit(event.Resumed, map[string]any{})
		case <-s.ctx.Done():
			return
		}
	}
}

// defaultPrompt is the starting instruction for a mode when the user gives none.
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
	reg               *config.Registry
	projects          *project.Registry
	workstreams       *workstream.Registry
	worktreesRoot     string
	ownership         *workspacelease.Service
	idAlloc           *docs.IDAllocator
	workstreamWatches map[string]chan struct{}
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
	// workstreamSpawnMu makes the max_parallel check and subsequent registration
	// atomic with respect to other spawns.
	workstreamSpawnMu sync.Mutex
	// mergeMu serializes MergeWorkstream across all workstreams so integrations
	// happen one at a time and each trial-merges against the latest base HEAD
	// (sequential reconciliation).
	mergeMu sync.Mutex

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
	m := &Manager{
		sessions:          map[string]*Session{},
		reg:               reg,
		projects:          projects,
		workstreams:       workstream.NewMemory(),
		worktreesRoot:     workstream.DefaultWorktreesRoot(),
		ownership:         workspacelease.NewService(),
		idAlloc:           docs.AllocatorFor(""),
		workstreamWatches: map[string]chan struct{}{},
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
	}
	m.integrateAgent = m.runIntegrateSession
	m.gitSyncWG.Add(1)
	go m.runGitSyncPoller()
	return m
}

// SetProjects replaces the manager's project registry. Daemon construction is
// responsible for registering its startup workspace in the replacement registry.
func (m *Manager) SetProjects(p *project.Registry) { m.projects = p }

// SetNotifier installs the daemon-side push notifier. A nil notifier
// (the default / unconfigured case) disables notifications; every session watcher
// and the Notify RPC are nil-safe.
func (m *Manager) SetNotifier(n *notify.Notifier) { m.notifier = n }

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
	return m.start(cfg, true)
}

// initialCoordinator resolves the session-only coordinator selection. An
// explicit API override wins; otherwise a preset binding is applied. Unlike an
// explicit unknown override, a stale preset binding is non-fatal: it falls back
// to the configured coordinator and returns a warning for the session log/UI.
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
	// The prompt is optional: an empty one (e.g. "work" mode with no suggested
	// task) gets a sensible per-mode default so the agent has a starting point.
	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		prompt = defaultPrompt(mode)
	}

	// Resolve an explicit or preset coordinator before creating the log. Explicit
	// bad input remains an error; stale config bindings safely produce a warning.
	coord, startupNotice, err := m.initialCoordinator(cfg.Preset, cfg.CoordinatorModel)
	if err != nil {
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
	s.preset = cfg.Preset
	s.coordinatorExplicit = cfg.CoordinatorModel != ""
	s.startupNotice = startupNotice
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
	// A specific task id in a work-session prompt makes the coordinator's first
	// list_backlog/get_task calls deterministic. Execute and seed them now, after
	// the real opening user message, to save model round trips without guessing
	// when the prompt is absent, ambiguous, or stale.
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
	m.sessions[id] = s
	m.mu.Unlock()

	go s.run()
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
	// Prompt seeds the work session (optional).
	Prompt string
}

// SpawnWorkstream creates a linked git worktree + branch (ycc/ws/<id>) off the
// parent project's primary tree and starts a `work` session scoped to that
// worktree, recording the pair in the workstream registry. It
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
	spawnToken := m.ownership.NewToken(fmt.Sprintf("workstream spawn for project %s", cfg.Project))
	spawnLease, err := m.ownership.Acquire(primary, spawnToken)
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
		Workspace: dir,
		Mode:      "work",
		Prompt:    cfg.Prompt,
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
// durable session log on startup. In-root in-flight states retain
// a live worktree; missing trees become stale, while out-of-root legacy entries
// become needs-attention with a manual migration reason. Non-live terminal
// session logs have readiness re-derived so daemon restarts cannot lose completion.
func (m *Manager) ReconcileWorkstreams() error {
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

	// Reduce worktree-local logs after stale detection. This re-derives completion
	// before any session is reopened following a daemon restart.
	for _, w := range m.workstreams.List() {
		if !w.Status.InFlight() || w.SessionID == "" {
			continue
		}
		if err := workstream.VerifyUnderRoot(m.worktreesRoot, w.WorktreePath); err != nil {
			continue
		}
		m.mu.Lock()
		_, live := m.sessions[w.SessionID]
		m.mu.Unlock()
		if live {
			continue
		}
		logPath := filepath.Join(w.WorktreePath, ".ycc", "sessions", w.SessionID, "events.jsonl")
		events := readEventsTolerant(logPath)
		if len(events) == 0 {
			continue
		}
		status := workstreamTerminalStatus(events)
		if status == event.StatusIdle || status == event.StatusError || status == event.StatusStopped {
			m.evaluateWorkstreamReadiness(w.ID, status, workstreamRunBlocked(events))
		}
	}

	// A ready state is durable while the queue is in-memory. Re-enqueue every
	// eligible stream after reconciliation so daemon restarts cannot strand it.
	var ready []workstream.Workstream
	for _, w := range m.workstreams.List() {
		if w.Status == workstream.StatusReady {
			ready = append(ready, w)
		}
	}
	if len(ready) > 0 && m.effectiveIntegrationMode() == "auto" {
		for _, w := range ready {
			m.enqueueWorkstreamIntegration(w)
		}
	}
	return nil
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
	var startupLease *workspacelease.Lease
	if !resumed {
		var acquireErr error
		startupLease, acquireErr = m.ownership.Acquire(absWS, m.ownership.NewToken(fmt.Sprintf("session %s startup", id)))
		if acquireErr != nil {
			return nil, fmt.Errorf("prepare workspace: %w", acquireErr)
		}
		defer startupLease.Release()
	}
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
		WorkImplementation: m.reg.WorkImplementation(),
		Jobs:               jobs.NewRegistry(),
		Ownership:          m.ownership,
	}
	deps.CoordinatorToken = m.ownership.NewToken(fmt.Sprintf("session %s coordinator", id))

	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:          id,
		Workspace:   absWS,
		Mode:        mode,
		log:         log,
		emitter:     emitter,
		inter:       inter,
		deps:        deps,
		reg:         m.reg,
		prompt:      prompt,
		resumed:     resumed,
		inputCh:     make(chan string, 64),
		messageCh:   make(chan engine.UserMessage, 64),
		retryCh:     make(chan struct{}),
		ctx:         ctx,
		cancel:      cancel,
		status:      event.StatusRunning,
		coordinator: coordName,
		implementer: implName,
		reviewers:   reviewerNames,
		thinkLevels: map[string]string{},
	}
	// Durability loss is terminal for a live session: continuing would mutate
	// model/tool state from history that cannot be replayed after restart. Log
	// itself sends live subscribers a transient session_error, so do not attempt
	// to record another error through the already-broken log here.
	log.OnFailure(func(error) {
		s.setStatus(event.StatusError)
		s.cancel()
	})
	// Generic chat subagents may select any configured logical model. Resolve at
	// spawn time so live per-model thinking overrides and refreshed credentials are
	// honored, and expose the current sorted names in the tool schema.
	deps.ResolveAgent = s.agentSpec
	deps.AgentModels = func() []string {
		infos := m.reg.Models()
		names := make([]string, 0, len(infos))
		for _, info := range infos {
			if !info.Disabled {
				names = append(names, info.Name)
			}
		}
		return names
	}

	// buildLoop assembles the agent loop for a mode; reused on mode transitions.
	// It reads the session's current coordinator assignment so a mid-session
	// role-config change drives the next coordinator loop.
	s.buildLoop = func(mode, prompt string) (*engine.Loop, error) {
		reg, sys := orchestrator.BuildMode(mode, deps, unattended)
		s.mu.Lock()
		coord := s.coordinator
		s.mu.Unlock()
		client, model, err := m.reg.Build(coord)
		if err != nil {
			return nil, fmt.Errorf("build coordinator backend: %w", err)
		}
		th := s.thinkingFor(coord)
		loop := &engine.Loop{
			Client: client, Model: model, ModelName: coord, Backend: m.reg.BackendFor(coord),
			System: sys, Tools: reg, Emitter: emitter,
			MaxTok: m.reg.MaxTokens(), MaxTurns: m.reg.MaxTurns(), Retry: m.reg.RetryPolicy(),
			Thinking: th.Thinking, Effort: th.Effort, ThinkingDisplay: th.ThinkingDisplay,
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
		m.startNotifyWatcher(absWS, id, log)
	}

	return s, nil
}

// startNotifyWatcher spawns a goroutine that maps session events to best-effort
// push notifications. It attaches at the log's current LastSeq so only
// events emitted after attach fire — a reopened session's replayed history never
// re-notifies. The goroutine exits when the log closes (the subscription channel
// closes).
func (m *Manager) startNotifyWatcher(absWS, id string, log *event.Log) {
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
	if s, ok := m.Get(id); ok {
		return s, nil // already live — no-op
	}
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	logPath := filepath.Join(absWS, ".ycc", "sessions", id, "events.jsonl")
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
	s.preset = proj.Preset
	s.startupNotice = startupNotice
	loop, err := s.buildLoop(mode, "")
	if err != nil {
		log.Close()
		return nil, err
	}
	loop.SetHistory(engine.ReplayHistory(events))
	s.loop = loop
	// Seed the spend-guard flags from the replayed log so a session that already
	// crossed the warning/breach line does not re-fire or re-ask on reopen.
	s.seedBudgetFromLog(events)

	// Register atomically: re-check under the lock so a concurrent Reopen/Start
	// that won the race keeps its live session. The loser closes its freshly
	// opened log and abandons its (never-started) session — no goroutine or log
	// leak.
	m.mu.Lock()
	if existing, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		s.cancel()
		log.Close()
		return existing, nil
	}
	m.sessions[id] = s
	m.mu.Unlock()

	if linked, ok := m.workstreams.BySession(id); ok && linked.Status.InFlight() {
		m.startWorkstreamWatcher(linked, log)
	}
	go s.run()
	return s, nil
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
	logPath := filepath.Join(absWS, ".ycc", "sessions", id, "events.jsonl")
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

// agentSpec builds an orchestrator.AgentSpec for a logical model name,
// validating that it resolves now so the per-spawn closures can assume success.
// Thinking comes from the model config and package defaults; live session
// overrides are layered on later via Session.SetThinking.
func (m *Manager) agentSpec(name string) (orchestrator.AgentSpec, error) {
	_, model, err := m.reg.Build(name)
	if err != nil {
		return orchestrator.AgentSpec{}, fmt.Errorf("build backend %q: %w", name, err)
	}
	th := m.reg.ThinkingFor(name)
	contextWindow, contextSafeFraction := m.reg.ContextBudget(name)
	return orchestrator.AgentSpec{
		Name:                name,
		Model:               model,
		Backend:             m.reg.BackendFor(name),
		ContextWindow:       contextWindow,
		ContextSafeFraction: contextSafeFraction,
		NewClient: func() engine.Turner {
			c, _, _ := m.reg.Build(name)
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
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownSession, id)
	}
	s.Stop()
	m.waitWorkstreamWatcher(id)
	return nil
}

// waitWorkstreamWatcher joins a watcher after its session log has been closed so
// callers may safely tear down/reopen the worktree. Never hold m.mu while
// waiting: readiness emission briefly needs that mutex.
func (m *Manager) waitWorkstreamWatcher(id string) {
	m.mu.Lock()
	watchDone := m.workstreamWatches[id]
	m.mu.Unlock()
	if watchDone != nil {
		<-watchDone
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
	if ok && current == s {
		delete(m.sessions, s.ID)
	} else {
		ok = false
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	s.reap()
	m.waitWorkstreamWatcher(s.ID)
	return true
}

// ReclaimAll releases every live session without writing session_stopped markers.
// It is used during daemon teardown: cancellation stops in-flight turns, while
// leaving each durable log exactly as it was keeps the session reopenable. The
// live map is drained atomically before cancellation, so repeated calls are safe.
func (m *Manager) ReclaimAll() {
	// Stop and join all manager-owned git commands before tearing down sessions or
	// their worktrees. This keeps daemon shutdown free of background git races.
	m.gitSyncCancel()
	m.gitSyncWG.Wait()

	// Cancel and join daemon-owned work loops before draining the session map. The
	// real runner uses this context to reclaim its current session, clear the
	// persisted current-session id, and publish a terminal loop outcome.
	m.loopMu.Lock()
	if !m.loopStop {
		m.loopStop = true
		m.loopCancel()
	}
	m.loopMu.Unlock()
	m.loopWG.Wait()

	m.mu.Lock()
	if !m.integrationStop {
		m.integrationStop = true
		m.integrationCancel()
	}
	m.mu.Unlock()
	m.integrationWG.Wait()

	m.mu.Lock()
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
		if model.Auth == "oauth" && (model.Backend == "anthropic" || model.Backend == "openai") {
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

// SetRoles updates the default per-role model assignment (config.Roles) and
// persists it to ycc.toml. Used when a role change is made with no
// live session to apply it to (e.g. from the home-menu settings overlay); a
// change made inside a session goes through Session.SetRoleConfig, which also
// applies it live before persisting the same way.
func (m *Manager) SetRoles(coordinator, implementer string, reviewers []string) error {
	return m.reg.SetRoles(coordinator, implementer, reviewers)
}

// SetThinking resolves role to its currently assigned model(s) and persists the
// level in each model's config. An empty role targets all
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

// WithBacklogMutation runs fn while holding this daemon's mutation lease for the
// project's canonical worktree. RPC mutation entry points use it instead of
// relying only on docs.Store's process-local file lock.
func (m *Manager) WithBacklogMutation(project, owner string, fn func(*docs.Store) error) error {
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	lease, err := m.ownership.Acquire(absWS, m.ownership.NewToken(owner))
	if err != nil {
		return err
	}
	defer lease.Release()
	return fn(m.backlogStore(absWS))
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
	cd := orchestrator.CaptureDeps{
		Workspace:        absWS,
		Docs:             store,
		Client:           client,
		Model:            model,
		ModelName:        coord,
		Backend:          m.reg.BackendFor(coord),
		Thinking:         engine.Thinking{}, // reasoning OFF for a fast capture
		MaxTok:           m.reg.MaxTokens(),
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
			projectEntries, err := usage.Scan(absWS)
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
	entries, err := usage.Scan(absWS)
	if err != nil {
		return nil, err
	}
	res := usage.Aggregate(entries, m.reg, opts)
	res.Workspace = absWS
	return &res, nil
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
