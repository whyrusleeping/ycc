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
	Lost    Status = "lost"   // was running when the daemon restarted
)

// maxJobBuf caps the retained output buffer of a single job. When exceeded the
// oldest bytes are dropped and their absolute offset retained, so a chatty
// watcher cannot grow memory without bound; the tail remains available.
const (
	maxJobBuf         = 256 * 1024
	maxJobReportBytes = 64 * 1024
)

// Report is the retained final (or current) summary of a job, returned by wait,
// job_result, and DrainFinished without being destroyed by retrieval.
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

// Job is one unit of background work.
type Job struct {
	id       string
	kind     string
	label    string
	owner    string // actor responsible for completion delivery
	purpose  string // explicit parent handoff purpose
	delivery string // explicit parent handoff delivery contract
	mutates  bool   // writes to the worktree (single-writer guard)

	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{} // terminal report is available
	once          sync.Once
	executionDone chan struct{} // tracked process/agent has actually stopped
	executionOnce sync.Once
	tracked       bool

	mu              sync.Mutex
	status          Status
	buf             []byte
	result          string // retained final report
	terminationHint string // retrieval/readiness detail included if killed
	notified        bool   // exactly-once automatic notification claim
	dropped         int64  // absolute offset of the retained output's first byte
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

// Mutates reports whether the job may write to the worktree. The single-writer
// guard refuses a background implementer while any mutating job is
// live in the same tree; read-only jobs (reviewers) never set this.
func (j *Job) Mutates() bool { return j.mutates }

// Context returns the job's context: cancelled when the job is killed or the
// session ends. Background processes should run under it.
func (j *Job) Context() context.Context { return j.ctx }

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
	j.mu.Lock()
	defer j.mu.Unlock()
	retainedStart := j.dropped
	retainedEnd := retainedStart + int64(len(j.buf))
	start := offset
	var gapStart, gapEnd int64
	if tailLines > 0 {
		idx := tailLineStart(j.buf, tailLines)
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
	data := append([]byte(nil), j.buf[start-retainedStart:end-retainedStart]...)
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
		Started: j.started, Finished: j.finished, Activity: j.activity}
}

// Tail returns the last n lines of the buffered output.
func (j *Job) Tail(n int) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	tail := strings.ToValidUTF8(lastLines(string(j.buf), n), "�")
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
// makes its report terminal immediately, but lifecycle cleanup and shutdown wait
// for this separate boundary before releasing execution ownership.
func (j *Job) ExecutionComplete() {
	if j.tracked {
		j.executionOnce.Do(func() { close(j.executionDone) })
	}
}

// WaitExecution waits until a tracked process/agent has actually stopped. Jobs
// created through Start/StartMutating have no separately tracked execution and
// return immediately.
func (j *Job) WaitExecution() {
	if j.tracked {
		<-j.executionDone
	}
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
	defer j.mu.Unlock()
	if j.status != Running {
		return Handoff{}, false
	}
	j.owner = parent
	j.purpose = purpose
	j.delivery = ParentCheckpointDelivery
	return Handoff{ID: j.id, Purpose: purpose, Owner: parent, Delivery: j.delivery, Mutates: j.mutates}, true
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
func (j *Job) Kill() bool {
	tail := j.Tail(20)
	j.mu.Lock()
	hint := j.terminationHint
	j.mu.Unlock()
	result := "killed"
	if strings.TrimSpace(tail) != "" {
		result += "\n" + tail
	}
	if hint != "" {
		result += "\n" + hint
	}
	fired := j.finalize(Killed, result)
	j.cancel()
	return fired
}

func (j *Job) finalize(status Status, result string) bool {
	fired := false
	j.once.Do(func() {
		j.mu.Lock()
		j.status = status
		j.result = result
		j.finished = time.Now()
		j.activity.CurrentTool = ""
		j.mu.Unlock()
		close(j.done)
		fired = true
	})
	return fired
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
	defer j.mu.Unlock()
	if j.notified || j.status == Running {
		return false
	}
	j.notified = true
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
	cancel  context.CancelFunc
	closing bool // registration barrier: KillAll has begun snapshotting runners
}

// NewRegistry returns an empty registry with a fresh root context.
func NewRegistry() *Registry {
	ctx, cancel := context.WithCancel(context.Background())
	return &Registry{jobs: map[string]*Job{}, ctx: ctx, cancel: cancel}
}

// NewRestored rebuilds durable job metadata, notification state, and terminal
// results. Evidence remains available through list_jobs/job_result. Any later
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
			started: started, finished: finished}
		if e.Kind == "agent" {
			j.activity.Last = finished
		}
		if j.status == "" || j.status == Running {
			j.status = Lost
			j.notified = true // ReplayHistory injects the lost-on-restart note.
			if j.result == "" {
				j.result = "job lost: daemon restarted"
			}
		}
		j.once.Do(func() { close(j.done) })
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
// runner must call ExecutionComplete after all lifetime leases have been
// released. A successful concurrent registration is guaranteed to be included
// in the shutdown snapshot, cancelled, and joined.
func (r *Registry) TryStartTracked(kind, label, owner string) (*Job, bool) {
	job := r.start(kind, label, owner, false, true)
	return job, job != nil
}

// StartMutating is like Start but marks the job as writing to the worktree, so
// the single-writer guard can refuse a second mutating job in the
// same tree. Used by synthetic/restored work without a separately joined runner.
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
	if r.closing {
		return nil
	}
	r.seq++
	id := fmt.Sprintf("job_%d", r.seq)
	ctx, cancel := context.WithCancel(r.ctx)
	started := time.Now()
	j := &Job{
		id: id, kind: kind, label: label, owner: owner, mutates: mutates,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
		executionDone: make(chan struct{}), tracked: tracked,
		status: Running, started: started,
	}
	if kind == "agent" {
		j.activity.Last = started
	}
	r.jobs[id] = j
	r.order = append(r.order, id)
	return j
}

// LiveMutating returns a currently-running mutating job, or nil if none. Used by
// the single-writer guard to refuse a second mutating job in the same tree.
// When several are somehow live it returns the earliest-started.
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
	targets := r.targets(ids)
	if len(targets) == 0 {
		return nil, nil
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
				return r.collect(targets)
			case <-timer:
				return r.collect(targets)
			}
		}
	}
	return r.collect(targets)
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

// DrainFinished claims and returns final reports not yet delivered or suppressed,
// scoped to owner. Non-blocking; used only for automatic checkpoint injection.
func (r *Registry) DrainFinished(owner string) []Report {
	r.mu.Lock()
	js := make([]*Job, 0, len(r.order))
	for _, id := range r.order {
		js = append(js, r.jobs[id])
	}
	r.mu.Unlock()
	var out []Report
	for _, j := range js {
		if j.Owner() != owner {
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
func (r *Registry) ResolveOwner(owner, parent string, retain []RetainRequest) (reports []Report, handoffs []Handoff, rejected []string) {
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
		j.WaitExecution()
		if j.claimNotification() {
			reports = append(reports, j.Report())
		}
	}
	return reports, handoffs, rejected
}

// KillAll cancels the root context, finalizes every running report as Killed,
// and joins tracked processes/agents. It does not return while mutation lifetime
// ownership can still be held by a terminating job.
func (r *Registry) KillAll() {
	r.mu.Lock()
	// Linearize shutdown before taking the snapshot. A tracked start either
	// registered before this point and is included below, or observes closing and
	// declines without launching a runner.
	r.closing = true
	r.cancel()
	js := make([]*Job, 0, len(r.jobs))
	for _, id := range r.order {
		js = append(js, r.jobs[id])
	}
	r.mu.Unlock()
	for _, j := range js {
		j.finalize(Killed, "killed: session ended")
	}
	for _, j := range js {
		j.WaitExecution()
	}
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
