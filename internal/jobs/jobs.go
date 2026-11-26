// Package jobs implements the session-scoped registry of background jobs
// (docs/design/async-jobs.md). A job is session-owned background work, such as a
// shell command started via Bash(run_in_background: true). Jobs are addressed by
// a monotonic "job_<n>" id and expose discovery, cursor-based output, repeatable
// final results, bounded wait, and cancellation.
//
// Automatic delivery of a job's final report is exactly-once: a wait suppresses
// later checkpoint injection, while DrainFinished claims the one automatic
// notification. The retained report and absolute-cursor output remain repeatable
// evidence independent of notification state.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Status is a job's lifecycle state.
type Status string

const (
	Running Status = "running"
	Done    Status = "done"   // process exited 0
	Failed  Status = "failed" // process exited non-zero or failed to run
	Killed  Status = "killed" // terminated by kill_job or session end
	Lost    Status = "lost"   // was running when the daemon restarted, or its supervision was lost
)

// maxJobBuf caps the retained output buffer of a single job. When exceeded the
// oldest bytes are dropped and their absolute offset retained, so a chatty
// watcher cannot grow memory without bound; the tail remains available.
const (
	maxJobBuf         = 256 * 1024
	maxJobReportBytes = 64 * 1024
)

// Report is the retained final (or current) summary of a job, returned by wait,
// job_output, and DrainFinished without being destroyed by retrieval.
type Report struct {
	ID                  string
	Kind                string
	Label               string
	Status              Status
	Result              string // exit code + output tail (bash), or the agent report
	ClaimedNotification bool   // this wait newly suppressed automatic delivery
}

// Usage is the bounded, non-reasoning token summary retained for an agent job.
type Usage struct {
	Input, Output, CacheRead, CacheWrite, Total int
}

// Activity is the safe progress summary retained for an agent job. It contains
// lifecycle/tool/accounting metadata only, never model text or reasoning.
type Activity struct {
	Last        time.Time
	CurrentTool string
	Turns       int
	Usage       Usage
}

// Info is a deterministic list_jobs snapshot.
type Info struct {
	ID, Kind, Label, Owner string
	Purpose, Delivery      string // set when a child explicitly hands the job to its parent
	Status                 Status
	Mutates                bool
	Started, Finished      time.Time
	Activity               Activity
	Restored               bool // reconstructed from the log when the session was reopened
	// OutputAvailable means job_output can read the job's output: its live
	// buffer, or a supervised job's retained on-disk segments (which survive a
	// reopen; a restored direct child's output does not).
	OutputAvailable bool
	// ExecutionUnknown means supervision of the job's process was lost without
	// an exit record: it may still be running and was never joined.
	ExecutionUnknown bool
}

// Output is a repeatable absolute-byte view over a job's retained output.
type Output struct {
	Data                       []byte
	Start, End                 int64
	RetainedStart, RetainedEnd int64
	GapStart, GapEnd           int64
	TailTruncated              bool
}

// Restored describes durable job state reconstructed from the event log.
type Restored struct {
	ID, Kind, Label, Owner string
	Purpose, Delivery      string
	Status                 Status
	Result                 string
	Mutates, Notified      bool
	Started, Finished      time.Time
	// Supervised marks a job started through an out-of-process supervisor
	// (job_started "supervised"); Artifact is its reserved output artifact id.
	Supervised bool
	Artifact   string
	// Recorded means the log holds the job's terminal evidence (job_finished,
	// or a claim or notification carrying the terminal report).
	Recorded bool
	// Reopen reconciliation of a supervised job (docs/design/daemon-restart.md
	// §3) sets at most one of these. Live restores a still-running job as a
	// tracked Running job whose runner is re-attached. ExecutionPending keeps
	// a terminal job's execution tracked until its supervisor confirms the
	// exit (a kill still unwinding). ExecutionUnknown restores a job whose
	// supervision was lost: execution is never reported complete.
	Live, ExecutionPending, ExecutionUnknown bool
}

// Handoff describes a running child job explicitly transferred to its parent.
// Delivery names the completion-notification contract; retained result and output
// evidence remain repeatable independently of that notification.
type Handoff struct {
	ID, Purpose, Owner, Delivery string
	Mutates                      bool
}

// RetainRequest is an explicit child request to leave a running watcher under
// parent ownership instead of applying default cleanup.
type RetainRequest struct{ ID, Purpose string }

// OutputSource supplies a job's retained output from outside its in-memory
// buffer, such as a supervised job's on-disk tail segments. Retained returns
// the retained bytes and the absolute stream offset of the first one.
type OutputSource interface {
	Retained() (data []byte, start int64)
}

// UnjoinedError reports tracked jobs whose execution could not be confirmed
// stopped because their supervision was lost.
type UnjoinedError struct{ IDs []string }

func (e *UnjoinedError) Error() string {
	return fmt.Sprintf("supervision of background job(s) %s was lost: their processes could not be confirmed stopped and may still be running",
		strings.Join(e.IDs, ", "))
}

// Job is one unit of background work.
type Job struct {
	id       string
	kind     string
	label    string
	owner    string // actor responsible for completion delivery
	purpose  string // explicit parent handoff purpose
	delivery string // explicit parent handoff delivery contract
	mutates  bool   // writes to the worktree (concurrent-writer awareness)

	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{} // terminal report is available
	once          sync.Once
	onFinish      func()        // registry-level completion signal, fired once
	executionDone chan struct{} // tracked process/agent has actually stopped
	executionOnce sync.Once
	// settled is closed once execution either stopped or became unknown, the
	// point past which teardown cannot learn more by waiting.
	settled     chan struct{}
	settledOnce sync.Once
	tracked     bool
	restored    bool

	mu              sync.Mutex
	status          Status
	buf             []byte
	result          string // retained final report
	terminationHint string // retrieval/readiness detail included if killed
	notified        bool   // exactly-once automatic notification claim
	execStopped     bool   // tracked execution has actually stopped
	execUnknown     bool   // supervision was lost: execution may still be running
	source          OutputSource
	stopper         func() error // synchronous kill request (supervised jobs)
	detacher        func() bool  // leaves the process running across a graceful restart
	detached        bool         // detacher succeeded: nothing follows the job any more
	stopPending     bool         // a stop was requested before the stopper was installed
	recorded        bool         // the terminal report is durable in the session log
	killErr         error        // the stopper's failure, if any
	onNotified      func()       // runs once the automatic notification is claimed
	signalled       bool         // registry completion signal already fired
	dropped         int64        // absolute offset of the retained output's first byte
	started         time.Time
	finished        time.Time
	activity        Activity
}

// ID returns the job's id ("job_<n>").
func (j *Job) ID() string { return j.id }

// Kind returns the job kind (e.g. "bash").
func (j *Job) Kind() string { return j.kind }

// Label returns the job label (e.g. the command line).
func (j *Job) Label() string { return j.label }

// Owner returns the actor responsible for the job's completion delivery.
func (j *Job) Owner() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.owner
}

// Mutates reports whether the job may write to the worktree. Coordinators are
// told about live mutating jobs when they start another writer; read-only jobs
// (reviewers) never set this.
func (j *Job) Mutates() bool { return j.mutates }

// Context returns the job's context: cancelled when the job is killed or the
// session ends. Background processes should run under it.
func (j *Job) Context() context.Context { return j.ctx }

// Done is closed once the job's terminal report is available. A tracked job's
// execution may still be stopping then; WaitExecution joins it.
func (j *Job) Done() <-chan struct{} { return j.done }

// Status returns the job's current status.
func (j *Job) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

// Append writes more output into the job's buffer (capped, oldest-dropped).
func (j *Job) Append(p []byte) {
	j.mu.Lock()
	j.buf = append(j.buf, p...)
	if len(j.buf) > maxJobBuf {
		drop := len(j.buf) - maxJobBuf
		j.dropped += int64(drop)
		j.buf = append([]byte(nil), j.buf[drop:]...)
	}
	j.mu.Unlock()
}

// SetOutputSource makes Output and Tail read from src instead of the job's
// in-memory buffer.
func (j *Job) SetOutputSource(src OutputSource) {
	j.mu.Lock()
	j.source = src
	j.mu.Unlock()
}

// retained returns the job's retained output and its absolute start offset.
// The caller must not hold j.mu.
func (j *Job) retained() ([]byte, int64) {
	j.mu.Lock()
	src := j.source
	if src == nil {
		defer j.mu.Unlock()
		return append([]byte(nil), j.buf...), j.dropped
	}
	j.mu.Unlock()
	return src.Retained()
}

// Writer returns an io.Writer that appends to the job's output buffer, for
// wiring a process's combined stdout/stderr.
func (j *Job) Writer() io.Writer { return jobWriter{j} }

type jobWriter struct{ j *Job }

func (w jobWriter) Write(p []byte) (int, error) {
	w.j.Append(p)
	return len(p), nil
}

// Output returns a repeatable absolute-byte range from retained output. offset
// is relative to the complete stream, not the current buffer. A request before
// RetainedStart reports the missing interval in GapStart/GapEnd and resumes at
// the first retained byte. tailLines > 0 selects the retained tail instead.
func (j *Job) Output(offset int64, limit, tailLines int) Output {
	buf, retainedStart := j.retained()
	retainedEnd := retainedStart + int64(len(buf))
	start := offset
	var gapStart, gapEnd int64
	if tailLines > 0 {
		idx := tailLineStart(buf, tailLines)
		start = retainedStart + int64(idx)
	} else if start < retainedStart {
		gapStart, gapEnd = start, retainedStart
		start = retainedStart
	}
	if start < retainedStart {
		start = retainedStart
	}
	if start > retainedEnd {
		start = retainedEnd
	}
	selectedStart := start
	if limit <= 0 {
		limit = maxJobReportBytes
	}
	end := start + int64(limit)
	if end > retainedEnd {
		end = retainedEnd
	}
	// A tail request returns the newest bytes when its selected lines exceed the
	// byte budget; forward cursor/range requests retain their natural direction.
	if tailLines > 0 && end-start == int64(limit) && end < retainedEnd {
		end = retainedEnd
		start = end - int64(limit)
	}
	data := append([]byte(nil), buf[start-retainedStart:end-retainedStart]...)
	return Output{Data: data, Start: start, End: end, RetainedStart: retainedStart,
		RetainedEnd: retainedEnd, GapStart: gapStart, GapEnd: gapEnd,
		TailTruncated: tailLines > 0 && start > selectedStart}
}

// UpdateActivity records safe agent progress. A non-nil usage marks one model
// turn complete and is accumulated across all loops represented by the job.
func (j *Job) UpdateActivity(currentTool string, usage *Usage) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.activity.Last = time.Now()
	j.activity.CurrentTool = currentTool
	if usage != nil {
		j.activity.Turns++
		j.activity.Usage.Input += usage.Input
		j.activity.Usage.Output += usage.Output
		j.activity.Usage.CacheRead += usage.CacheRead
		j.activity.Usage.CacheWrite += usage.CacheWrite
		j.activity.Usage.Total += usage.Total
	}
}

// Info returns a lifecycle/activity snapshot for discovery.
func (j *Job) Info() Info {
	j.mu.Lock()
	defer j.mu.Unlock()
	return Info{ID: j.id, Kind: j.kind, Label: j.label, Owner: j.owner,
		Purpose: j.purpose, Delivery: j.delivery, Status: j.status, Mutates: j.mutates,
		Started: j.started, Finished: j.finished, Activity: j.activity, Restored: j.restored,
		OutputAvailable: !j.restored || j.source != nil, ExecutionUnknown: j.execUnknown}
}

// Tail returns the last n lines of the buffered output.
func (j *Job) Tail(n int) string {
	buf, _ := j.retained()
	return TailText(buf, n)
}

// TailText is Job.Tail over buf: its last n lines as valid UTF-8, bounded to
// the job report size.
func TailText(buf []byte, n int) string {
	tail := strings.ToValidUTF8(lastLines(string(buf), n), "�")
	if len(tail) <= maxJobReportBytes {
		return tail
	}
	const notice = "…[job report tail truncated to 64 KiB; use the retained output artifact for ranges]\n"
	start := len(tail) - (maxJobReportBytes - len(notice))
	for start < len(tail) && !utf8.RuneStart(tail[start]) {
		start++
	}
	return notice + tail[start:]
}

// Finish transitions a running job to a terminal state exactly once, recording
// its final report. It returns true if THIS call effected the transition (so the
// caller should emit job_finished) and false if the job was already terminal
// (e.g. it was killed first) — this is what makes job_finished fire once.
func (j *Job) Finish(status Status, result string) bool {
	return j.finalize(status, result)
}

// ExecutionComplete marks a tracked process/agent as actually stopped. A kill
// makes its report terminal immediately, but lifecycle cleanup, shutdown, and
// automatic completion delivery wait for this separate boundary before releasing
// execution ownership.
func (j *Job) ExecutionComplete() {
	if !j.tracked {
		return
	}
	j.executionOnce.Do(func() { close(j.executionDone) })
	j.settle()
	// Report-then-release is the common order, so this is usually where a tracked
	// job becomes deliverable and the owner's wake fires.
	j.mu.Lock()
	j.execStopped = true
	j.execUnknown = false // an exit record confirmed what supervision lost
	signal := j.status != Running && !j.signalled
	if signal {
		j.signalled = true
	}
	j.mu.Unlock()
	if signal && j.onFinish != nil {
		j.onFinish()
	}
}

func (j *Job) settle() {
	if j.settled != nil {
		j.settledOnce.Do(func() { close(j.settled) })
	}
}

// MarkExecutionUnknown records that supervision of a tracked job's process was
// lost without an exit record. Execution is NOT complete: WaitExecution keeps
// blocking, and ExecutionRunning stays true, until a later ExecutionComplete
// confirms an exit. Teardown stops waiting (WaitExecutionSettled), and the
// job's terminal report becomes deliverable so its owner hears of the loss.
// A non-empty note is appended to a terminal report not yet delivered (e.g. a
// killed report whose kill could not be confirmed); a report already claimed
// keeps its delivery and is not notified twice.
func (j *Job) MarkExecutionUnknown(note string) {
	if !j.tracked {
		return
	}
	j.mu.Lock()
	if j.execStopped {
		j.mu.Unlock()
		return
	}
	if note != "" && j.status != Running && !j.notified && !strings.Contains(j.result, note) {
		j.result += "\n" + note
	}
	j.execUnknown = true
	signal := j.status != Running && !j.signalled
	if signal {
		j.signalled = true
	}
	j.mu.Unlock()
	j.settle()
	if signal && j.onFinish != nil {
		j.onFinish()
	}
}

// ExecutionUnknown reports whether supervision of the job's process was lost
// without confirming that it stopped.
func (j *Job) ExecutionUnknown() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.execUnknown
}

// Notified reports whether the job's single automatic notification was
// delivered or suppressed by an explicit wait.
func (j *Job) Notified() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.notified
}

// WaitExecution waits until a tracked process/agent has actually stopped. Jobs
// created through Start/StartMutating have no separately tracked execution and
// return immediately. It keeps waiting while execution is unknown.
func (j *Job) WaitExecution() {
	if j.tracked {
		<-j.executionDone
	}
}

// WaitExecutionSettled waits until a tracked job's execution has stopped or
// become unknown, and reports whether it is unknown (never joined).
func (j *Job) WaitExecutionSettled() (unknown bool) {
	if !j.tracked {
		return false
	}
	<-j.settled
	return j.ExecutionUnknown()
}

// ExecutionRunning reports whether the job's work may still be executing: a
// tracked job until ExecutionComplete (even after a kill finalized its report),
// an untracked one while its status is running.
func (j *Job) ExecutionRunning() bool { return j.executionRunning() }

// ExecutionDone returns a channel closed once the job's work has stopped
// executing (ExecutionRunning turns false): for a tracked job at
// ExecutionComplete, never while its execution is unknown; for an untracked
// one with its report (Done).
func (j *Job) ExecutionDone() <-chan struct{} {
	if j.tracked {
		return j.executionDone
	}
	return j.done
}

func (j *Job) executionRunning() bool {
	if !j.tracked {
		return j.Status() == Running
	}
	select {
	case <-j.executionDone:
		return false
	default:
		return true
	}
}

const ParentCheckpointDelivery = "parent checkpoint exactly once unless wait claims it first; retained result remains repeatable"

// Handoff transfers a still-running job's delivery ownership to parent. It is
// atomic with respect to completion: false means the terminal report must be
// accounted by the child instead.
func (j *Job) Handoff(parent, purpose string) (Handoff, bool) {
	j.mu.Lock()
	if j.status != Running {
		j.mu.Unlock()
		return Handoff{}, false
	}
	j.owner = parent
	j.purpose = purpose
	j.delivery = ParentCheckpointDelivery
	handoff := Handoff{ID: j.id, Purpose: purpose, Owner: parent, Delivery: j.delivery, Mutates: j.mutates}
	j.mu.Unlock()
	return handoff, true
}

// SetTerminationHint records bounded completion/retrieval guidance that should
// remain discoverable if a running job is killed before its worker can publish
// the final capture.
func (j *Job) SetTerminationHint(hint string) {
	j.mu.Lock()
	j.terminationHint = hint
	j.mu.Unlock()
}

// Kill terminates the job: it finalizes the job as Killed (recording a killed
// report with the current tail) and cancels its context so the process tree is
// signalled. It returns true if THIS call effected the transition.
func (j *Job) Kill() bool { return j.kill("killed") }

func (j *Job) kill(result string) bool {
	tail := j.Tail(20)
	j.mu.Lock()
	hint := j.terminationHint
	j.mu.Unlock()
	if strings.TrimSpace(tail) != "" {
		result += "\n" + tail
	}
	if hint != "" {
		result += "\n" + hint
	}
	fired := j.finalize(Killed, result)
	j.cancel()
	j.requestStop()
	return fired
}

// SetStopper installs a synchronous kill request that Kill makes after
// cancelling the job's context, so a failure to reach the process can be
// reported (KillError) instead of discarded.
func (j *Job) SetStopper(stop func() error) {
	j.mu.Lock()
	j.stopper = stop
	pending := j.stopPending && stop != nil
	j.stopPending = false
	j.mu.Unlock()
	if pending {
		// A kill or a restart teardown reached the job while it was still
		// launching: it is waiting to join this execution.
		j.runStopper(stop)
	}
}

// requestStop makes the stopper's kill request, recording its failure. A job
// whose runner has not installed its stopper yet (it is still launching)
// keeps the request, and SetStopper honours it.
func (j *Job) requestStop() {
	j.mu.Lock()
	stopper := j.stopper
	if stopper == nil {
		j.stopPending = true
	}
	j.mu.Unlock()
	if stopper == nil {
		return
	}
	j.runStopper(stopper)
}

func (j *Job) runStopper(stopper func() error) {
	if err := stopper(); err != nil {
		j.mu.Lock()
		if j.killErr == nil {
			j.killErr = err
		}
		j.mu.Unlock()
	}
}

// SetDetacher installs fn, which a graceful daemon restart calls instead of
// killing the running job (Registry.SuspendForRestart). fn stops following the
// job while leaving its process running, and reports false when it could not
// (the job finished first).
func (j *Job) SetDetacher(fn func() bool) {
	j.mu.Lock()
	j.detacher = fn
	j.mu.Unlock()
}

func (j *Job) isDetached() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.detached
}

// detachForRestart detaches a running job owned by keepOwner that has a
// detacher, reporting whether the job is (now or already) detached.
func (j *Job) detachForRestart(keepOwner string) bool {
	j.mu.Lock()
	if j.detached {
		j.mu.Unlock()
		return true
	}
	fn := j.detacher
	ok := fn != nil && j.status == Running && j.owner == keepOwner
	j.mu.Unlock()
	if !ok || !fn() {
		return false
	}
	j.mu.Lock()
	j.detached = true
	j.mu.Unlock()
	return true
}

// MarkRecorded records that the job's terminal report is durable in the
// session log. A report finalized only in memory (a teardown kill) is not,
// so evidence that depends on it (a supervised exit record) is not consumed.
func (j *Job) MarkRecorded() {
	j.mu.Lock()
	j.recorded = true
	j.mu.Unlock()
}

// Recorded reports whether the job's terminal report is durable (MarkRecorded).
func (j *Job) Recorded() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.recorded
}

// KillError returns the stopper's failure from the last Kill, if any: the kill
// request did not reach the process, so it may still be running.
func (j *Job) KillError() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.killErr
}

// SetNotifiedHook installs fn to run (outside job locks) once the job's
// automatic notification is claimed or suppressed by a wait.
func (j *Job) SetNotifiedHook(fn func()) {
	j.mu.Lock()
	j.onNotified = fn
	j.mu.Unlock()
}

func (j *Job) finalize(status Status, result string) bool {
	fired, signal := false, false
	j.once.Do(func() {
		j.mu.Lock()
		j.status = status
		j.result = result
		j.finished = time.Now()
		j.activity.CurrentTool = ""
		// A tracked runner can still be executing here (a kill finalizes the
		// report long before the process exits), so its completion
		// signal is deferred to ExecutionComplete. Otherwise a woken owner could
		// start a turn against execution that has not released ownership.
		if !j.tracked || j.execStopped || j.execUnknown {
			j.signalled, signal = true, true
		}
		j.mu.Unlock()
		close(j.done)
		fired = true
	})
	// Signal outside the job lock and after the terminal state is visible, so a
	// woken owner that immediately drains reports observes this completion.
	if fired && signal && j.onFinish != nil {
		j.onFinish()
	}
	return fired
}

// completionReady reports whether automatic notification delivery may claim this
// job's report: a tracked job stays withheld until its execution has actually
// stopped, so a wake never runs a turn while the work still holds its leases.
func (j *Job) completionReady() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	// Lost supervision is delivered on its own authority: nothing will ever
	// confirm the stop, and the owner must hear that the child may still run.
	return !j.tracked || j.execStopped || j.execUnknown
}

// isDone reports whether the job has reached a terminal state.
func (j *Job) isDone() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

// claimNotification claims the exactly-once automatic notification. Evidence
// access through Report/Output remains repeatable and independent of this flag.
func (j *Job) claimNotification() bool {
	j.mu.Lock()
	if j.notified || j.status == Running {
		j.mu.Unlock()
		return false
	}
	j.notified = true
	hook := j.onNotified
	j.mu.Unlock()
	if hook != nil {
		hook()
	}
	return true
}

// Report returns a snapshot report without consuming.
func (j *Job) Report() Report {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.reportLocked()
}

func (j *Job) reportLocked() Report {
	return Report{ID: j.id, Kind: j.kind, Label: j.label, Status: j.status, Result: j.result}
}

// Registry is the session-scoped set of jobs. All jobs derive their context from
// the registry's root context, which KillAll cancels so session end leaves no
// orphan processes.
type Registry struct {
	mu      sync.Mutex
	seq     int
	jobs    map[string]*Job
	order   []string
	ctx     context.Context
	cancel  context.CancelCauseFunc
	closing bool // registration barrier: KillAll has begun snapshotting runners
	held    int  // Quiesce holds registration off while it is above zero
	// completions coalesces terminal transitions into at most one pending wake
	// for the single session owner that consumes them. Buffered so a completion
	// that lands while the owner is running a turn is still observed when it
	// next waits, and never blocks the finishing worker.
	completions chan struct{}
}

// NewRegistry returns an empty registry with a fresh root context.
func NewRegistry() *Registry {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &Registry{jobs: map[string]*Job{}, ctx: ctx, cancel: cancel,
		completions: make(chan struct{}, 1)}
}

// Completions returns the coalescing job-completion signal. A receive means at
// least one job reached a terminal state since the last receive; the receiver
// must then check for actually deliverable reports (DrainFinished), because a
// completion may already have been claimed by a checkpoint or an explicit wait.
// It is intended for the single session owner, not for polling by tools.
func (r *Registry) Completions() <-chan struct{} { return r.completions }

// SignalCompletion re-arms the coalescing wake without changing any job state.
// The session owner uses it when an explicit boundary consumed a completion edge
// whose retained reports are still undelivered.
func (r *Registry) SignalCompletion() { r.signalCompletion() }

func (r *Registry) signalCompletion() {
	// Lock-free by construction: finalize may run while another goroutine holds
	// the registry mutex (KillAll, ResolveOwner), so this must never take it.
	select {
	case r.completions <- struct{}{}:
	default:
	}
}

// NewRestored rebuilds durable job metadata, notification state, and terminal
// results. Evidence remains available through list_jobs/job_output. Any later
// Start continues after the greatest restored job_<n> id.
func NewRestored(entries []Restored) *Registry {
	r := NewRegistry()
	for _, e := range entries {
		if e.ID == "" {
			continue
		}
		started := e.Started
		if started.IsZero() {
			started = time.Now()
		}
		finished := e.Finished
		if finished.IsZero() {
			finished = started
		}
		ctx, cancel := context.WithCancel(r.ctx)
		j := &Job{id: e.ID, kind: e.Kind, label: e.Label, owner: e.Owner,
			purpose: e.Purpose, delivery: e.Delivery, mutates: e.Mutates,
			ctx: ctx, cancel: cancel, done: make(chan struct{}),
			status: e.Status, result: e.Result, notified: e.Notified,
			started: started, finished: finished, restored: true, recorded: e.Recorded}
		if e.Kind == "agent" {
			j.activity.Last = finished
		}
		if e.Live || e.ExecutionPending || e.ExecutionUnknown {
			// Reconciled supervised execution stays tracked: its re-attached
			// runner confirms the exit (ExecutionComplete), and teardown joins it.
			j.tracked = true
			j.executionDone = make(chan struct{})
			j.settled = make(chan struct{})
			j.onFinish = r.signalCompletion
		}
		switch {
		case e.Live:
			j.status, j.result, j.notified, j.finished = Running, "", false, time.Time{}
		case j.status == "" || j.status == Running:
			j.status = Lost
			j.notified = true // ReplayHistory injects the lost-on-restart note.
			if j.result == "" {
				j.result = "job lost: daemon restarted"
			}
		}
		if e.ExecutionUnknown && !e.Live {
			j.execUnknown = true
			j.settle()
		}
		if j.status != Running {
			// A restored terminal report's completion edge is long past; a
			// later confirmed exit must not fire it again. An undelivered
			// report waiting on its execution is the exception: it becomes
			// deliverable only when the exit is confirmed or found unknown,
			// and that must wake the owner.
			j.signalled = !(e.ExecutionPending && !e.Notified)
			j.once.Do(func() { close(j.done) })
		}
		r.jobs[j.id] = j
		r.order = append(r.order, j.id)
		var n int
		if _, err := fmt.Sscanf(j.id, "job_%d", &n); err == nil && n > r.seq {
			r.seq = n
		}
	}
	return r
}

// Start allocates a new job id and registers a running untracked job whose
// context derives from the registry root. It returns nil once shutdown begins.
func (r *Registry) Start(kind, label, owner string) *Job {
	return r.start(kind, label, owner, false, false)
}

// TryStartTracked registers joined execution unless shutdown has begun. Its
// runner must call ExecutionComplete once its execution has fully stopped. A successful concurrent registration is guaranteed to be included
// in the shutdown snapshot, cancelled, and joined.
func (r *Registry) TryStartTracked(kind, label, owner string) (*Job, bool) {
	job := r.start(kind, label, owner, false, true)
	return job, job != nil
}

// StartMutating is like Start but marks the job as writing to the worktree.
// Used by synthetic/restored work without a separately joined runner.
func (r *Registry) StartMutating(kind, label, owner string) *Job {
	return r.start(kind, label, owner, true, false)
}

// TryStartMutatingTracked is the joined mutating variant of TryStartTracked.
func (r *Registry) TryStartMutatingTracked(kind, label, owner string) (*Job, bool) {
	job := r.start(kind, label, owner, true, true)
	return job, job != nil
}

func (r *Registry) start(kind, label, owner string, mutates, tracked bool) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing || r.held > 0 {
		return nil
	}
	r.seq++
	id := fmt.Sprintf("job_%d", r.seq)
	ctx, cancel := context.WithCancel(r.ctx)
	started := time.Now()
	j := &Job{
		id: id, kind: kind, label: label, owner: owner, mutates: mutates,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
		executionDone: make(chan struct{}), settled: make(chan struct{}), tracked: tracked,
		status: Running, started: started, onFinish: r.signalCompletion,
	}
	if kind == "agent" {
		j.activity.Last = started
	}
	r.jobs[id] = j
	r.order = append(r.order, id)
	return j
}

// LiveMutating returns a currently-running mutating job, or nil if none. Used to
// tell a coordinator about a concurrent writer when it starts another one.
// When several are live it returns the earliest-started.
func (r *Registry) LiveMutating() *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range r.order {
		j := r.jobs[id]
		if j.mutates && j.executionRunning() {
			return j
		}
	}
	return nil
}

// Get returns the job with id, or ok=false.
func (r *Registry) Get(id string) (*Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	return j, ok
}

// List returns jobs in stable start order. By default only jobs owned by owner
// are visible; allOwners is an explicit session-wide diagnostic view.
func (r *Registry) List(owner string, allOwners bool) []Info {
	r.mu.Lock()
	js := make([]*Job, 0, len(r.order))
	for _, id := range r.order {
		j := r.jobs[id]
		if allOwners || j.Owner() == owner {
			js = append(js, j)
		}
	}
	r.mu.Unlock()
	out := make([]Info, 0, len(js))
	for _, j := range js {
		out = append(out, j.Info())
	}
	return out
}

// LiveIDs returns the owner's running job ids in stable start order.
func (r *Registry) LiveIDs(owner string) []string {
	infos := r.List(owner, false)
	var ids []string
	for _, info := range infos {
		if info.Status == Running {
			ids = append(ids, info.ID)
		}
	}
	return ids
}

// targets resolves the wait target set: the named ids (missing ones skipped), or
// — when ids is empty — all jobs in start order.
func (r *Registry) targets(ids []string) []*Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(ids) == 0 {
		out := make([]*Job, 0, len(r.order))
		for _, id := range r.order {
			out = append(out, r.jobs[id])
		}
		return out
	}
	var out []*Job
	for _, id := range ids {
		if j, ok := r.jobs[id]; ok {
			out = append(out, j)
		}
	}
	return out
}

// Wait blocks until the completion condition over the target jobs is met, then
// returns repeatable final reports of finished jobs and the ids of any still
// running (on timeout / ctx cancellation). Returning a report suppresses its
// later automatic notification, but does not consume retained evidence.
//
// mode "any" returns as soon as one target finishes; anything else ("all",
// default) waits for all. Empty ids ⇒ all registered jobs. Tool callers scope
// omitted ids to the current actor's live jobs. timeout <= 0 ⇒ no timeout.
// It never holds the registry mutex while blocking (lock-ordering discipline
// from the design's deadlock lesson).
func (r *Registry) Wait(ctx context.Context, ids []string, mode string, timeout time.Duration) (reports []Report, running []string) {
	reports, running, _ = r.WaitSuspendable(ctx, ids, mode, timeout)
	return reports, running
}

// WaitSuspendable is Wait that also reports a graceful-restart suspension: when
// ctx carries ErrRestartSuspend at the moment the wait would collect, it claims
// nothing and returns suspended=true. That single decision is the caller's
// commit point: a wait that collected must deliver what it claimed.
func (r *Registry) WaitSuspendable(ctx context.Context, ids []string, mode string, timeout time.Duration) (reports []Report, running []string, suspended bool) {
	targets := r.targets(ids)
	if len(targets) == 0 {
		return nil, nil, false
	}
	waitAll := mode != "any"
	satisfied := func() bool {
		n := 0
		for _, j := range targets {
			if j.isDone() {
				n++
			}
		}
		if waitAll {
			return n == len(targets)
		}
		return n >= 1
	}
	if !satisfied() {
		notify := make(chan struct{}, len(targets))
		for _, j := range targets {
			go func(j *Job) {
				select {
				case <-j.done:
				case <-ctx.Done():
				}
				notify <- struct{}{}
			}(j)
		}
		var timer <-chan time.Time
		if timeout > 0 {
			t := time.NewTimer(timeout)
			defer t.Stop()
			timer = t.C
		}
		for !satisfied() {
			select {
			case <-notify:
			case <-ctx.Done():
				return r.collectUnlessSuspended(ctx, targets)
			case <-timer:
				return r.collectUnlessSuspended(ctx, targets)
			}
		}
	}
	return r.collectUnlessSuspended(ctx, targets)
}

// CollectSuspendable is a non-blocking WaitSuspendable: it collects the
// reports of the listed jobs that have finished and the ids of those still
// running, without waiting. A restored wait whose deadline already passed uses
// it, since a zero timeout means "no timeout" to WaitSuspendable. Empty ids
// collect nothing.
func (r *Registry) CollectSuspendable(ctx context.Context, ids []string) (reports []Report, running []string, suspended bool) {
	if len(ids) == 0 {
		return nil, nil, false
	}
	return r.collectUnlessSuspended(ctx, r.targets(ids))
}

// collectUnlessSuspended collects the wait's reports, except that a wait whose
// context a graceful restart cancelled claims nothing: delivery of its reports
// belongs to the reopened session.
func (r *Registry) collectUnlessSuspended(ctx context.Context, targets []*Job) (reports []Report, running []string, suspended bool) {
	if RestartSuspended(ctx) {
		return nil, nil, true
	}
	reports, running = r.collect(targets)
	return reports, running, false
}

func (r *Registry) collect(targets []*Job) (reports []Report, running []string) {
	for _, j := range targets {
		if j.Status() == Running {
			running = append(running, j.id)
			continue
		}
		claimed := j.claimNotification()
		rep := j.Report()
		rep.ClaimedNotification = claimed
		reports = append(reports, rep)
	}
	return reports, running
}

// PendingContinuation reports whether owner still has automatic completion
// delivery outstanding: a job that has not reached a terminal report, or a
// terminal report whose exactly-once notification has been neither claimed nor
// suppressed by an explicit wait. Callers use it to tell "waiting on delegated
// work" apart from "finished", including in the gap between a job finishing and
// its report being injected.
//
// Execution alone is not counted: once a job's automatic notification has been
// claimed or suppressed it produces nothing further even while its process is
// still exiting, and the single-writer guard plus the shutdown execution join
// already keep that case safe.
func (r *Registry) PendingContinuation(owner string) bool {
	pending, _ := r.PendingContinuationKinds(owner)
	return pending
}

// PendingContinuationKinds is PendingContinuation that also reports whether
// any outstanding delivery is from an "agent" job (a subagent run) rather than
// only from shell jobs.
func (r *Registry) PendingContinuationKinds(owner string) (pending, agent bool) {
	r.mu.Lock()
	js := make([]*Job, 0, len(r.order))
	for _, id := range r.order {
		js = append(js, r.jobs[id])
	}
	r.mu.Unlock()
	for _, j := range js {
		if j.Owner() != owner || !j.deliveryPending() {
			continue
		}
		pending = true
		if j.Kind() == "agent" {
			return true, true
		}
	}
	return pending, false
}

// CompletionReady reports, without claiming anything, whether owner has a
// finished job whose automatic notification DrainFinished would deliver now.
func (r *Registry) CompletionReady(owner string) bool {
	r.mu.Lock()
	js := make([]*Job, 0, len(r.order))
	for _, id := range r.order {
		js = append(js, r.jobs[id])
	}
	r.mu.Unlock()
	for _, j := range js {
		if j.Owner() != owner || !j.completionReady() {
			continue
		}
		j.mu.Lock()
		ready := !j.notified && j.status != Running
		j.mu.Unlock()
		if ready {
			return true
		}
	}
	return false
}

// deliveryPending reports whether this job can still produce an automatic
// notification.
func (j *Job) deliveryPending() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status == Running || !j.notified
}

// DrainFinished claims and returns final reports not yet delivered or suppressed,
// scoped to owner. A tracked job whose execution has not actually stopped is left
// unclaimed: its report becomes deliverable at the same boundary that releases its
// leases, and the completion signal fires there. Non-blocking; used only for
// automatic checkpoint/wake injection.
func (r *Registry) DrainFinished(owner string) []Report {
	r.mu.Lock()
	js := make([]*Job, 0, len(r.order))
	for _, id := range r.order {
		js = append(js, r.jobs[id])
	}
	r.mu.Unlock()
	var out []Report
	for _, j := range js {
		if j.Owner() != owner || !j.completionReady() {
			continue
		}
		if j.claimNotification() {
			out = append(out, j.Report())
		}
	}
	return out
}

// ResolveOwner is the lifecycle boundary for a completing child actor. Running
// jobs named in retain are transferred atomically to parent with their explicit
// purpose and delivery contract. Every other running owned job is killed and its
// tracked execution joined. Newly claimed terminal reports are returned for
// inclusion in the child's result; retained evidence remains repeatable.
//
// unjoined lists killed jobs whose supervision was lost, so their execution was
// never confirmed stopped.
func (r *Registry) ResolveOwner(owner, parent string, retain []RetainRequest) (reports []Report, handoffs []Handoff, rejected, unjoined []string) {
	r.mu.Lock()
	js := make([]*Job, 0, len(r.order))
	byID := make(map[string]*Job, len(r.order))
	for _, id := range r.order {
		j := r.jobs[id]
		js = append(js, j)
		byID[id] = j
	}
	r.mu.Unlock()

	transferred := make(map[string]bool)
	for _, request := range retain {
		j, ok := byID[request.ID]
		if !ok || j.Owner() != owner {
			rejected = append(rejected, request.ID+": not owned by completing agent")
			continue
		}
		if handoff, ok := j.Handoff(parent, request.Purpose); ok {
			handoffs = append(handoffs, handoff)
			transferred[request.ID] = true
		}
	}
	for _, j := range js {
		if transferred[j.ID()] || j.Owner() != owner {
			continue
		}
		if j.Status() == Running {
			j.Kill()
		}
		if j.WaitExecutionSettled() {
			unjoined = append(unjoined, j.ID())
		}
		if j.claimNotification() {
			reports = append(reports, j.Report())
		}
	}
	return reports, handoffs, rejected, unjoined
}

// Hold holds new registrations off until release is called (once), without
// stopping any job, so a caller can check what is running and act on it
// before anything new starts.
func (r *Registry) Hold() (release func()) {
	r.mu.Lock()
	r.held++
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.held--
			r.mu.Unlock()
		})
	}
}

// Quiesce stops every job whose execution may still run and holds new
// registrations off until release is called, leaving the registry usable
// afterwards (unlike KillAll, its root context is not cancelled). Each such
// job is killed with result "killed: <reason>" and joined. A job whose stop
// cannot be confirmed (its supervision was lost) is returned in an
// *UnjoinedError. A job detached for restart is not this registry's to stop.
// Workstream merge and discard use it to confirm, before an irreversible
// step, that nothing a live session started can still run in its worktree.
func (r *Registry) Quiesce(reason string) (release func(), err error) {
	release = r.Hold()
	r.mu.Lock()
	all := make([]*Job, 0, len(r.order))
	for _, id := range r.order {
		all = append(all, r.jobs[id])
	}
	r.mu.Unlock()
	var running []*Job
	for _, j := range all {
		if !j.isDetached() && (j.executionRunning() || j.ExecutionUnknown()) {
			running = append(running, j)
		}
	}
	for _, j := range running {
		j.kill("killed: " + reason)
	}
	var unjoined []string
	for _, j := range running {
		if j.WaitExecutionSettled() {
			unjoined = append(unjoined, j.ID())
		}
	}
	if len(unjoined) > 0 {
		return release, &UnjoinedError{IDs: unjoined}
	}
	return release, nil
}

// KillAll cancels the root context, finalizes every running report as Killed,
// and joins tracked processes/agents. It does not return while mutation lifetime
// ownership can still be held by a terminating job. A job whose supervision was
// lost cannot be joined: KillAll then returns an *UnjoinedError naming it
// rather than reporting a clean join.
func (r *Registry) KillAll() error { return r.KillAllCause(nil) }

// KillAllCause is KillAll with a cancellation cause for every job context. A
// graceful restart passes ErrRestartSuspend, so an in-process subagent blocked
// in wait exits without claiming reports, as its coordinator does. A job whose
// supervision was lost cannot be joined: KillAllCause then returns an
// *UnjoinedError naming it rather than reporting a clean join.
func (r *Registry) KillAllCause(cause error) error { return r.killAll(cause, "") }

// SuspendForRestart is the graceful-restart teardown (cause ErrRestartSuspend)
// with one difference from KillAllCause: a running job owned by keepOwner that
// can detach (SetDetacher) is detached rather than killed, leaving its process
// running for the next daemon to re-attach on reopen. A detached job records
// no terminal report and is not joined. Every other job is killed (its
// stopper asked directly, since a restart-cancelled context does not relay a
// kill) and joined as KillAllCause does.
func (r *Registry) SuspendForRestart(keepOwner string) error {
	return r.killAll(ErrRestartSuspend, keepOwner)
}

func (r *Registry) killAll(cause error, keepOwner string) error {
	r.mu.Lock()
	// Linearize shutdown before taking the snapshot. A tracked start either
	// registered before this point and is included below, or observes closing and
	// declines without launching a runner.
	r.closing = true
	r.cancel(cause)
	all := make([]*Job, 0, len(r.jobs))
	for _, id := range r.order {
		all = append(all, r.jobs[id])
	}
	r.mu.Unlock()
	restart := errors.Is(cause, ErrRestartSuspend)
	js := all[:0:0]
	for _, j := range all {
		// A detached job is no longer this registry's to kill or join.
		if j.isDetached() || (restart && keepOwner != "" && j.detachForRestart(keepOwner)) {
			continue
		}
		js = append(js, j)
	}
	for _, j := range js {
		j.finalize(Killed, "killed: session ended")
		if restart && j.executionRunning() {
			j.requestStop()
		}
	}
	var unjoined []string
	for _, j := range js {
		if j.WaitExecutionSettled() {
			unjoined = append(unjoined, j.ID())
		}
	}
	if len(unjoined) > 0 {
		return &UnjoinedError{IDs: unjoined}
	}
	return nil
}

// lastLines returns the last n non-empty-trailing lines of s.
func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" || n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func tailLineStart(buf []byte, n int) int {
	if n <= 0 || len(buf) == 0 {
		return len(buf)
	}
	end := len(buf)
	for end > 0 && buf[end-1] == '\n' {
		end--
	}
	for i, lines := end-1, 1; i >= 0; i-- {
		if buf[i] == '\n' {
			if lines == n {
				return i + 1
			}
			lines++
		}
	}
	return 0
}
