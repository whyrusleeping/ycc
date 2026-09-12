package session

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// A finished background job the model never waited on gets its final report
// injected at the next Checkpoint as a user-role message AND recorded as a
// user-actor job_notified event, so reopen replays the identical history
// (docs/design/async-jobs.md §3.3).
func TestCheckpointInjectsFinishedJob(t *testing.T) {
	s, rec := newSteerSession()
	jr := jobs.NewRegistry()
	defer jr.KillAll()
	s.deps = &orchestrator.Deps{Jobs: jr, Emitter: s.emitter}

	j := jr.Start("bash", "go test ./...", "coordinator")
	j.Finish(jobs.Done, "exit 0\nok")

	msgs, err := s.Checkpoint(context.Background())
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], "job_1") || !strings.Contains(msgs[0], "exit 0") {
		t.Fatalf("checkpoint msgs = %v, want one job note with exit 0", msgs)
	}

	notif := lastEvent(rec, event.JobNotified)
	if notif == nil {
		t.Fatal("no job_notified event recorded")
	}
	if notif.Actor != "user" {
		t.Fatalf("job_notified actor = %q, want user", notif.Actor)
	}
	if notif.Data["id"] != "job_1" || notif.Data["kind"] != "bash" ||
		notif.Data["label"] != "go test ./..." || notif.Data["status"] != "done" ||
		notif.Data["result"] != "exit 0\nok" {
		t.Fatalf("job_notified data = %+v", notif.Data)
	}

	// Exactly-once: a second checkpoint injects nothing (already consumed).
	if got, err := s.Checkpoint(context.Background()); err != nil || got != nil {
		t.Fatalf("second Checkpoint = (%v, %v), want (nil, nil)", got, err)
	}
}

// A still-running job is NOT injected at a checkpoint.
func TestCheckpointSkipsRunningJob(t *testing.T) {
	s, _ := newSteerSession()
	jr := jobs.NewRegistry()
	defer jr.KillAll()
	s.deps = &orchestrator.Deps{Jobs: jr, Emitter: s.emitter}
	jr.Start("bash", "watch", "coordinator")

	if got, err := s.Checkpoint(context.Background()); err != nil || got != nil {
		t.Fatalf("Checkpoint with running job = (%v, %v), want (nil, nil)", got, err)
	}
}

// scriptedTurner returns one canned assistant text per call and optionally runs
// a hook DURING a turn (used to land a job completion in the window where the
// parent is transitioning to idle). It is safe for the test goroutine to read.
type scriptedTurner struct {
	mu    sync.Mutex
	calls int
	texts []string
	fail  bool
	hook  func(turn int)
}

func (s *scriptedTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	text := "done"
	if n <= len(s.texts) {
		text = s.texts[n-1]
	}
	fail, hook := s.fail, s.hook
	s.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	if fail {
		return nil, errors.New("boom: connection reset by peer")
	}
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{
		Message: gollama.Message{Role: "assistant", Content: text},
	}}}, nil
}

func (s *scriptedTurner) turns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// newJobWakeSession builds a running session whose coordinator loop is driven by
// turner, with a live job registry attached.
func newJobWakeSession(t *testing.T, turner engine.Turner) (*Session, *jobs.Registry) {
	t.Helper()
	s := newStopSession(t)
	s.inter = newInteraction(true, s.emitter)
	s.Mode = "chat"
	s.prompt = "start"
	s.retryCh = make(chan struct{})
	s.loop = &engine.Loop{
		Client: turner, Model: "test", Tools: tools.New(), Emitter: s.emitter,
		Steer: s, Retry: engine.RetryPolicy{MaxAttempts: 1},
	}
	jr := jobs.NewRegistry()
	s.deps = &orchestrator.Deps{Jobs: jr, Emitter: s.emitter}
	t.Cleanup(func() { s.Stop() })
	return s, jr
}

// An idle coordinator that already sent its final response is woken by the
// completion of its own background job: the retained report enters history as
// synthetic job_notified context (never as user input) and the model runs again
// without any user message.
func TestIdleCoordinatorWokenByJobCompletion(t *testing.T) {
	turner := &scriptedTurner{texts: []string{"spawned the job, nothing else to do", "job landed"}}
	s, jr := newJobWakeSession(t, turner)
	go s.run()
	waitStatus(t, s, event.StatusIdle)

	j := jr.Start("bash", "go test ./...", "coordinator")
	j.Finish(jobs.Done, "exit 0\nok")

	waitFor(t, func() bool { return turner.turns() >= 2 })
	waitStatus(t, s, event.StatusIdle)

	var note string
	for _, msg := range s.currentLoop().History() {
		if msg.Role == "user" && strings.Contains(msg.Content, "job_1") {
			note = msg.Content
		}
	}
	if note == "" || !strings.Contains(note, "exit 0") {
		t.Fatalf("job report did not enter history: %q", note)
	}
	events := s.log.Snapshot()
	notified := 0
	for _, ev := range events {
		if ev.Type == event.JobNotified {
			notified++
			if ev.Actor != "user" {
				t.Fatalf("job_notified actor = %q, want user", ev.Actor)
			}
		}
	}
	if notified != 1 {
		t.Fatalf("job_notified events = %d, want 1", notified)
	}
	// The wake is synthetic context, not user intent: no extra user_input beyond
	// the session's own opening prompt echo.
	if n := countType(events, event.UserInput); n > 1 {
		t.Fatalf("user_input events = %d, want at most the opening prompt", n)
	}
	// Exactly-once: the claim was consumed by the wake.
	if reports := jr.DrainFinished("coordinator"); len(reports) != 0 {
		t.Fatalf("report still claimable after wake: %+v", reports)
	}
}

// A job that finishes while the parent is transitioning to idle (after the last
// checkpoint, before the idle wait) must not be missed: the buffered completion
// signal still wakes the session.
func TestJobCompletionRacingIdleTransitionWakes(t *testing.T) {
	var jr *jobs.Registry
	turner := &scriptedTurner{
		texts: []string{"kicked it off", "job landed"},
		hook: func(turn int) {
			if turn == 1 {
				j := jr.Start("bash", "sleep 1", "coordinator")
				j.Finish(jobs.Done, "exit 0")
			}
		},
	}
	var s *Session
	s, jr = newJobWakeSession(t, turner)
	go s.run()

	waitFor(t, func() bool { return turner.turns() >= 2 })
	waitStatus(t, s, event.StatusIdle)
	waitFor(t, func() bool { return countType(s.log.Snapshot(), event.SessionIdle) == 2 })
	idle := 0
	for _, ev := range s.log.Snapshot() {
		if ev.Type == event.SessionIdle {
			idle++
			if got := boolVal(ev.Data, "awaiting_jobs"); got != (idle == 1) {
				t.Fatalf("idle report %d awaiting_jobs = %v", idle, got)
			}
		}
	}
	if idle != 2 {
		t.Fatalf("idle reports = %d, want progress and final", idle)
	}
	if reports := jr.DrainFinished("coordinator"); len(reports) != 0 {
		t.Fatalf("report still claimable after wake: %+v", reports)
	}
}

// An errored (parked) session is NOT resumed by a job completion: recovery stays
// explicit, and the unclaimed report waits for the next checkpoint.
func TestErroredSessionNotWokenByJobCompletion(t *testing.T) {
	turner := &scriptedTurner{fail: true}
	s, jr := newJobWakeSession(t, turner)
	go s.run()
	waitStatus(t, s, event.StatusError)

	j := jr.Start("bash", "go test ./...", "coordinator")
	j.Finish(jobs.Done, "exit 0")

	time.Sleep(50 * time.Millisecond)
	if got := turner.turns(); got != 1 {
		t.Fatalf("model turns after completion = %d, want 1 (no automatic resume)", got)
	}
	if s.Status() != event.StatusError {
		t.Fatalf("status = %q, want error", s.Status())
	}
	if reports := jr.DrainFinished("coordinator"); len(reports) != 1 {
		t.Fatalf("reports claimable = %d, want 1 (nothing consumed while parked)", len(reports))
	}
}

// A pause requested while the session is idle keeps its boundary: the completion
// wake neither claims the report nor starts a turn.
func TestPausedIdleSessionNotWokenByJobCompletion(t *testing.T) {
	turner := &scriptedTurner{texts: []string{"idle"}}
	s, jr := newJobWakeSession(t, turner)
	go s.run()
	waitStatus(t, s, event.StatusIdle)
	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	j := jr.Start("bash", "go test ./...", "coordinator")
	j.Finish(jobs.Done, "exit 0")

	time.Sleep(50 * time.Millisecond)
	if got := turner.turns(); got != 1 {
		t.Fatalf("model turns after completion = %d, want 1 (pause not auto-resumed)", got)
	}
	if reports := jr.DrainFinished("coordinator"); len(reports) != 1 {
		t.Fatalf("reports claimable = %d, want 1 (nothing consumed while pausing)", len(reports))
	}
}

// Several completions available at the wake are coalesced into ONE continuation,
// and a report already claimed by an explicit wait is not delivered again.
func TestResumeForFinishedJobsCoalescesAndRespectsWaitClaim(t *testing.T) {
	turner := &scriptedTurner{}
	s, jr := newJobWakeSession(t, turner)

	claimed := jr.Start("bash", "already awaited", "coordinator")
	claimed.Finish(jobs.Done, "exit 0")
	if reports, _ := jr.Wait(context.Background(), []string{claimed.ID()}, "all", time.Second); len(reports) != 1 {
		t.Fatalf("wait reports = %d, want 1", len(reports))
	}
	first := jr.Start("bash", "one", "coordinator")
	first.Finish(jobs.Done, "exit 0\nfirst")
	second := jr.Start("agent", "two", "coordinator")
	second.Finish(jobs.Done, "REPORT: second")

	resumed, err := s.resumeForFinishedJobs()
	if err != nil {
		t.Fatalf("resumeForFinishedJobs: %v", err)
	}
	if !resumed {
		t.Fatal("resumeForFinishedJobs = false, want a continuation")
	}
	notes := 0
	for _, msg := range s.currentLoop().History() {
		if msg.Role != "user" {
			continue
		}
		if strings.Contains(msg.Content, "already awaited") {
			t.Fatalf("wait-claimed report was delivered again: %q", msg.Content)
		}
		if strings.Contains(msg.Content, "job_") {
			notes++
		}
	}
	if notes != 2 {
		t.Fatalf("posted job notes = %d, want 2 coalesced into one continuation", notes)
	}
	if countType(s.log.Snapshot(), event.JobNotified) != 2 {
		t.Fatalf("job_notified events = %d, want 2", countType(s.log.Snapshot(), event.JobNotified))
	}

	// Nothing left to deliver ⇒ no further continuation.
	if resumed, err := s.resumeForFinishedJobs(); err != nil || resumed {
		t.Fatalf("second resumeForFinishedJobs = (%v, %v), want (false, nil)", resumed, err)
	}
}

// An idle input accepted just before the wake is absorbed into the SAME
// continuation and stays ahead of the job note, matching the durable event
// order that reopen replays.
func TestJobWakeAbsorbsIdleInputBeforeNote(t *testing.T) {
	turner := &scriptedTurner{}
	s, jr := newJobWakeSession(t, turner)

	if err := s.SendInput("also check the lint output"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	j := jr.Start("bash", "go test ./...", "coordinator")
	j.Finish(jobs.Done, "exit 0")

	resumed, err := s.resumeForFinishedJobs()
	if err != nil || !resumed {
		t.Fatalf("resumeForFinishedJobs = (%v, %v), want (true, nil)", resumed, err)
	}
	var order []string
	for _, msg := range s.currentLoop().History() {
		if msg.Role != "user" {
			continue
		}
		switch {
		case strings.Contains(msg.Content, "lint output"):
			order = append(order, "input")
		case strings.Contains(msg.Content, "job_1"):
			order = append(order, "note")
		}
	}
	if len(order) != 2 || order[0] != "input" || order[1] != "note" {
		t.Fatalf("history order = %v, want [input note]", order)
	}
	events := s.log.Snapshot()
	inputSeq, noteSeq := 0, 0
	for _, ev := range events {
		switch ev.Type {
		case event.UserInput:
			inputSeq = ev.Seq
		case event.JobNotified:
			noteSeq = ev.Seq
		}
	}
	if inputSeq == 0 || noteSeq == 0 || inputSeq > noteSeq {
		t.Fatalf("durable order: user_input #%d, job_notified #%d", inputSeq, noteSeq)
	}
}

// Input accepted after the wake has claimed a continuation must be classified
// against the run that is about to start: the handoff from idle to running
// happens while senders are excluded, so the text is a queued correction the run
// delivers at its first checkpoint (durably after the note) rather than an idle
// prod the starting Run would ignore.
func TestJobWakeHandsIdleOffToRunning(t *testing.T) {
	turner := &scriptedTurner{}
	s, jr := newJobWakeSession(t, turner)
	j := jr.Start("bash", "go test ./...", "coordinator")
	j.Finish(jobs.Done, "exit 0")

	resumed, err := s.resumeForFinishedJobs()
	if err != nil || !resumed {
		t.Fatalf("resumeForFinishedJobs = (%v, %v), want (true, nil)", resumed, err)
	}
	if err := s.SendInput("one more thing"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if n := len(s.messageCh); n != 0 {
		t.Fatalf("idle queue holds %d inputs; the starting run would never see them", n)
	}
	s.steerMu.Lock()
	queued := len(s.corrections)
	s.steerMu.Unlock()
	if queued != 1 {
		t.Fatalf("queued corrections = %d, want the input queued for the starting run", queued)
	}
	events := s.log.Snapshot()
	var echo *event.Event
	for i := range events {
		if events[i].Type == event.UserInput {
			echo = &events[i]
		}
	}
	if echo == nil || echo.Data["queued"] != true {
		t.Fatalf("user_input echo = %+v, want queued:true", echo)
	}
	// Live order matches replay order: the note is already in history and the
	// correction follows at the run's first checkpoint.
	msgs, err := s.Checkpoint(context.Background())
	if err != nil || len(msgs) != 1 || msgs[0] != "one more thing" {
		t.Fatalf("Checkpoint = (%v, %v), want the queued correction", msgs, err)
	}
	var order []string
	for _, msg := range s.currentLoop().History() {
		if strings.Contains(msg.Content, "job_1") {
			order = append(order, "note")
		}
	}
	if len(order) != 1 {
		t.Fatalf("history notes = %v, want exactly one job note before the correction", order)
	}
}

// A wake with nothing to deliver must leave the session idle: it may not strand
// a sender's message as a correction for a run that never starts.
func TestJobWakeWithoutWorkLeavesSessionIdle(t *testing.T) {
	turner := &scriptedTurner{}
	s, jr := newJobWakeSession(t, turner)
	jr.Start("bash", "still running", "coordinator")

	if resumed, err := s.resumeForFinishedJobs(); err != nil || resumed {
		t.Fatalf("resumeForFinishedJobs = (%v, %v), want (false, nil)", resumed, err)
	}
	if err := s.SendInput("hello"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if n := len(s.messageCh); n != 1 {
		t.Fatalf("idle queue holds %d inputs, want the input accepted on the idle path", n)
	}
	s.steerMu.Lock()
	running, queued := s.running, len(s.corrections)
	s.steerMu.Unlock()
	if running || queued != 0 {
		t.Fatalf("after a no-op wake: running=%v corrections=%d, want idle with none queued", running, queued)
	}
}

// Text and picture input accepted serially while idle enter history in the
// accepted order when the wake absorbs them — the same order the durable log
// replays.
func TestIdleInputsKeepAcceptedOrderAcrossWake(t *testing.T) {
	turner := &scriptedTurner{}
	s, jr := newJobWakeSession(t, turner)
	s.Workspace = t.TempDir()

	if err := s.SendInput("first, the text"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	picture := engine.Image{
		Base64:    base64.StdEncoding.EncodeToString([]byte("not-a-real-png")),
		MediaType: "image/png", Filename: "shot.png",
	}
	if err := s.SendInputMessage(engine.UserMessage{Text: "second, a picture", Images: []engine.Image{picture}}); err != nil {
		t.Fatalf("SendInputMessage: %v", err)
	}
	if err := s.SendInput("third, more text"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	j := jr.Start("bash", "go test ./...", "coordinator")
	j.Finish(jobs.Done, "exit 0")

	if resumed, err := s.resumeForFinishedJobs(); err != nil || !resumed {
		t.Fatalf("resumeForFinishedJobs = (%v, %v), want (true, nil)", resumed, err)
	}
	var order []string
	for _, msg := range s.currentLoop().History() {
		text := msg.Content
		for _, block := range msg.MultiContent {
			text += block.Text
		}
		switch {
		case strings.Contains(text, "first,"):
			order = append(order, "first")
		case strings.Contains(text, "second,"):
			order = append(order, "second")
		case strings.Contains(text, "third,"):
			order = append(order, "third")
		case strings.Contains(text, "job_1"):
			order = append(order, "note")
		}
	}
	want := []string{"first", "second", "third", "note"}
	if !slices.Equal(order, want) {
		t.Fatalf("history order = %v, want %v", order, want)
	}
}

// A pause taken while idle consumes the completion signal without delivering
// anything. Resume must restore that check, so the retained report still
// produces exactly one continuation.
func TestResumeAfterIdlePauseDeliversRetainedReport(t *testing.T) {
	turner := &scriptedTurner{texts: []string{"idle", "job landed"}}
	s, jr := newJobWakeSession(t, turner)
	go s.run()
	waitStatus(t, s, event.StatusIdle)
	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	j := jr.Start("bash", "go test ./...", "coordinator")
	j.Finish(jobs.Done, "exit 0")
	// The paused wake consumes the completion edge and delivers nothing; settle so
	// the run owner is parked again with no further edge to observe.
	waitFor(t, func() bool { return len(jr.Completions()) == 0 })
	time.Sleep(50 * time.Millisecond)
	if got := turner.turns(); got != 1 {
		t.Fatalf("model turns while paused = %d, want 1", got)
	}

	if err := s.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitFor(t, func() bool { return turner.turns() >= 2 })
	waitStatus(t, s, event.StatusIdle)
	if got := turner.turns(); got != 2 {
		t.Fatalf("model turns after Resume = %d, want exactly one continuation", got)
	}
	if n := countType(s.log.Snapshot(), event.JobNotified); n != 1 {
		t.Fatalf("job_notified events = %d, want 1", n)
	}
}

// A tracked job's report is not deliverable until its execution has actually
// stopped: Finish alone (the report is terminal while the runner still holds its
// lease) must not start a continuation.
func TestTrackedJobWakeWaitsForExecutionRelease(t *testing.T) {
	turner := &scriptedTurner{texts: []string{"spawned the subagent", "subagent landed"}}
	s, jr := newJobWakeSession(t, turner)
	go s.run()
	waitStatus(t, s, event.StatusIdle)

	job, ok := jr.TryStartMutatingTracked("agent", "implementer 0379", "coordinator")
	if !ok {
		t.Fatal("tracked start refused")
	}
	// Session shutdown joins tracked execution, so release it even if an
	// assertion below fails first.
	t.Cleanup(job.ExecutionComplete)
	job.Finish(jobs.Done, "REPORT: implemented")
	time.Sleep(50 * time.Millisecond)
	if got := turner.turns(); got != 1 {
		t.Fatalf("model turns before execution release = %d, want 1", got)
	}
	if st, awaiting := s.StatusWithJobContinuation(); st != event.StatusIdle || !awaiting {
		t.Fatalf("status/awaiting = (%q, %v), want an idle session still awaiting delivery", st, awaiting)
	}

	job.ExecutionComplete()
	waitFor(t, func() bool { return turner.turns() >= 2 })
	waitStatus(t, s, event.StatusIdle)
	if got := turner.turns(); got != 2 {
		t.Fatalf("model turns after execution release = %d, want exactly one continuation", got)
	}
	if n := countType(s.log.Snapshot(), event.JobNotified); n != 1 {
		t.Fatalf("job_notified events = %d, want 1", n)
	}
}

// An idle session with live background jobs is not reclaimed by the GC reaper:
// it is waiting for their completion wake, not gone quiet.
func TestReapableFalseWithLiveJobs(t *testing.T) {

	s, _ := newSteerSession()
	s.status = event.StatusIdle
	jr := jobs.NewRegistry()
	defer jr.KillAll()
	s.deps = &orchestrator.Deps{Jobs: jr, Emitter: s.emitter}
	if !s.reapable() {
		t.Fatal("idle session without jobs should be reapable")
	}
	j := jr.Start("bash", "watch", "coordinator")
	if s.reapable() {
		t.Fatal("idle session with a live job must not be reapable")
	}
	j.Finish(jobs.Done, "exit 0")
	if !s.reapable() {
		t.Fatal("session should be reapable again once jobs are terminal")
	}
}

// The gap between a job finishing and its wake delivering the report must not
// look quiescent either: a completed, unclaimed report keeps the session out of
// the reaper until the continuation has run. A final report that cannot be woken
// (blocked) is not held.
func TestReapableWaitsForPendingJobContinuation(t *testing.T) {
	s, _ := newSteerSession()
	jr := jobs.NewRegistry()
	defer jr.KillAll()
	s.deps = &orchestrator.Deps{Jobs: jr, Emitter: s.emitter}
	j := jr.Start("agent", "reviewers", "coordinator")
	j.Finish(jobs.Done, "REVIEW: accepted")
	s.setIdle(true)

	if s.reapable() {
		t.Fatal("session with an unclaimed final report must not be reapable")
	}
	// A wake in flight (claim taken, turn not started yet) is the same window.
	s.setJobWakePosted(true)
	if reports := jr.DrainFinished("coordinator"); len(reports) != 1 {
		t.Fatalf("claimable reports = %d, want 1", len(reports))
	}
	if s.reapable() {
		t.Fatal("session must not be reapable while a claimed continuation is in flight")
	}
	s.setJobWakePosted(false)
	if !s.reapable() {
		t.Fatal("session should be reapable once the continuation is accounted")
	}

	// A blocked final report is never woken, so it must not pin the session.
	blocked := jr.Start("bash", "watcher report", "coordinator")
	blocked.Finish(jobs.Done, "exit 0")
	s.setIdle(false)
	if !s.reapable() {
		t.Fatal("blocked idle session must not be held by an unwakeable report")
	}
}
