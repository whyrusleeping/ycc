// Package jobs implements the session-scoped registry of background jobs
// (docs/design/async-jobs.md). A job is a unit of background work owned by the
// session — in this first phase, a background shell command started via
// Bash(run_in_background: true). Jobs are addressed by a monotonic "job_<n>" id
// and expose incremental output (job_output), a blocking retrieval (wait), and a
// kill.
//
// Delivery of a job's FINAL report is exactly-once (design §3.3): it is consumed
// either by a wait() that covers it OR by checkpoint injection (DrainFinished),
// whichever fires first, guarded by one per-job consumed flag. job_output (Read)
// never consumes the final report. This is the Claude Code deadlock lesson (§2):
// one authoritative delivery path, not two competing queues.
package jobs

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Status is a job's lifecycle state.
type Status string

const (
	Running Status = "running"
	Done    Status = "done"   // process exited 0
	Failed  Status = "failed" // process exited non-zero or failed to run
	Killed  Status = "killed" // terminated by kill_job or session end
)

// maxJobBuf caps the retained output buffer of a single job. When exceeded the
// oldest bytes are dropped (the read cursor is adjusted) so a chatty watcher
// cannot grow memory without bound; the tail — what the final report needs — is
// always preserved.
const maxJobBuf = 256 * 1024

// Report is the final (or current) summary of a job, returned by wait /
// DrainFinished and used to build the notification/tool-result text.
type Report struct {
	ID     string
	Kind   string
	Label  string
	Status Status
	Result string // exit code + output tail (bash), or the agent report
}

// Job is one unit of background work.
type Job struct {
	id    string
	kind  string
	label string
	owner string // actor that started it (checkpoint drain filters on this)

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	mu       sync.Mutex
	status   Status
	buf      []byte
	cursor   int    // read cursor for incremental job_output
	result   string // final report
	consumed bool   // exactly-once final-report delivery flag
}

// ID returns the job's id ("job_<n>").
func (j *Job) ID() string { return j.id }

// Kind returns the job kind (e.g. "bash").
func (j *Job) Kind() string { return j.kind }

// Label returns the job label (e.g. the command line).
func (j *Job) Label() string { return j.label }

// Owner returns the actor that started the job.
func (j *Job) Owner() string { return j.owner }

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
		j.buf = append([]byte(nil), j.buf[drop:]...)
		j.cursor -= drop
		if j.cursor < 0 {
			j.cursor = 0
		}
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

// Read returns the output produced since the last Read and the current status,
// advancing the read cursor. It NEVER consumes the final report (design §3.3):
// job_output is not part of the exactly-once rule and can be re-read any time.
func (j *Job) Read() (string, Status) {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := string(j.buf[j.cursor:])
	j.cursor = len(j.buf)
	return out, j.status
}

// Tail returns the last n lines of the buffered output.
func (j *Job) Tail(n int) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return lastLines(string(j.buf), n)
}

// Finish transitions a running job to a terminal state exactly once, recording
// its final report. It returns true if THIS call effected the transition (so the
// caller should emit job_finished) and false if the job was already terminal
// (e.g. it was killed first) — this is what makes job_finished fire once.
func (j *Job) Finish(status Status, result string) bool {
	return j.finalize(status, result)
}

// Kill terminates the job: it finalizes the job as Killed (recording a killed
// report with the current tail) and cancels its context so the process tree is
// signalled. It returns true if THIS call effected the transition.
func (j *Job) Kill() bool {
	tail := j.Tail(20)
	result := "killed"
	if strings.TrimSpace(tail) != "" {
		result += "\n" + tail
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

// consume returns the job's report and marks it consumed, exactly once, if the
// job is terminal and not already consumed. Shared by wait and DrainFinished so
// the final report is delivered exactly once (design §3.3).
func (j *Job) consume() (Report, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.consumed || j.status == Running {
		return Report{}, false
	}
	j.consumed = true
	return j.reportLocked(), true
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
	mu     sync.Mutex
	seq    int
	jobs   map[string]*Job
	order  []string
	ctx    context.Context
	cancel context.CancelFunc
}

// NewRegistry returns an empty registry with a fresh root context.
func NewRegistry() *Registry {
	ctx, cancel := context.WithCancel(context.Background())
	return &Registry{jobs: map[string]*Job{}, ctx: ctx, cancel: cancel}
}

// Start allocates a new job id, registers a running job whose context derives
// from the registry root, and returns it.
func (r *Registry) Start(kind, label, owner string) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	id := fmt.Sprintf("job_%d", r.seq)
	ctx, cancel := context.WithCancel(r.ctx)
	j := &Job{
		id: id, kind: kind, label: label, owner: owner,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
		status: Running,
	}
	r.jobs[id] = j
	r.order = append(r.order, id)
	return j
}

// Get returns the job with id, or ok=false.
func (r *Registry) Get(id string) (*Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	return j, ok
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
// returns the final reports of the finished-and-unconsumed jobs it covers and
// the ids of any that are still running (on timeout / ctx cancellation).
//
// mode "any" returns as soon as one target finishes; anything else ("all",
// default) waits for all. Empty ids ⇒ all live jobs. timeout <= 0 ⇒ no timeout.
// It never holds the registry mutex while blocking (lock-ordering discipline
// from the design's §2 deadlock lesson).
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
		if rep, ok := j.consume(); ok {
			reports = append(reports, rep)
		} else if j.Status() == Running {
			running = append(running, j.id)
		}
	}
	return reports, running
}

// DrainFinished returns the final reports of all finished, unconsumed jobs owned
// by owner and marks them consumed. Non-blocking; used for checkpoint injection.
func (r *Registry) DrainFinished(owner string) []Report {
	r.mu.Lock()
	js := make([]*Job, 0, len(r.order))
	for _, id := range r.order {
		js = append(js, r.jobs[id])
	}
	r.mu.Unlock()
	var out []Report
	for _, j := range js {
		if j.owner != owner {
			continue
		}
		if rep, ok := j.consume(); ok {
			out = append(out, rep)
		}
	}
	return out
}

// KillAll cancels the root context (killing every running job's process tree)
// and finalizes any still-running jobs as Killed. Called on session end so no
// orphan processes survive.
func (r *Registry) KillAll() {
	r.mu.Lock()
	r.cancel()
	js := make([]*Job, 0, len(r.jobs))
	for _, id := range r.order {
		js = append(js, r.jobs[id])
	}
	r.mu.Unlock()
	for _, j := range js {
		j.finalize(Killed, "killed: session ended")
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
