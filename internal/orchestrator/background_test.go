package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/jobs"
)

// syncRec is a thread-safe event recorder: background agent jobs emit from a
// goroutine, so tests that assert on emitted events need a recorder that is safe
// to write from the job goroutine and snapshot from the test.
type syncRec struct {
	mu     sync.Mutex
	events []event.Event
}

func (r *syncRec) Record(actor string, t event.Type, data map[string]any) event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev := event.Event{Seq: len(r.events) + 1, Actor: actor, Type: t, Data: data}
	r.events = append(r.events, ev)
	return ev
}

func (r *syncRec) snapshot() []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event.Event(nil), r.events...)
}

// find returns the first recorded event of type t, or ok=false.
func (r *syncRec) find(t event.Type) (event.Event, bool) {
	for _, ev := range r.snapshot() {
		if ev.Type == t {
			return ev, true
		}
	}
	return event.Event{}, false
}

// blockingTurner blocks in Turn until release is closed, then returns resp. It
// keeps a background agent job in the Running state so single-writer-guard and
// still-running assertions are deterministic.
type blockingTurner struct {
	release chan struct{}
	resp    *gollama.ResponseMessageGenerate
	once    sync.Once
	started chan struct{}
}

func newBlockingTurner(resp *gollama.ResponseMessageGenerate) *blockingTurner {
	return &blockingTurner{release: make(chan struct{}), resp: resp, started: make(chan struct{})}
}

func (b *blockingTurner) Turn(gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.resp, nil
}

func waitJobDone(t *testing.T, job *jobs.Job) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if job.Status() != jobs.Running {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish in time", job.ID())
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func bgDeps(t *testing.T, rec event.Recorder, impl *scripted, reviewers []AgentSpec) (*Deps, *docs.Store) {
	t.Helper()
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("a task", "## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	d := &Deps{
		Workspace: ws,
		Docs:      store,
		Repo:      repo,
		Emitter:   event.NewEmitter(rec, "coordinator"),
		Asker:     noopAsker{},
		Jobs:      jobs.NewRegistry(),
	}
	if impl != nil {
		d.Implementer = AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return impl }}
	}
	d.Reviewers = reviewers
	return d, store
}

// A background implementer returns a job id immediately; the finished job carries
// the SAME report the synchronous path produces (implementer report + staged
// diff), and both the job_* and subagent_* event pairs are emitted (the latter
// tagged with job_id). When no wait covers it, DrainFinished delivers it exactly
// once (the checkpoint-injection path).
func TestSpawnImplementerBackground(t *testing.T) {
	rec := &syncRec{}
	impl := &scripted{resp: []*gollama.ResponseMessageGenerate{call("finish", `{"report":"did the work"}`)}}
	d, _ := bgDeps(t, rec, impl, nil)
	defer d.Jobs.KillAll()

	res, err := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "go", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "started background job job_1") {
		t.Fatalf("background spawn should return a job id immediately, got: %q", res.Content)
	}
	job, ok := d.Jobs.Get("job_1")
	if !ok {
		t.Fatal("job_1 not registered")
	}
	waitJobDone(t, job)
	// Wait until the job goroutine has emitted job_finished (it does so after
	// finalizing the job), so event assertions are race-free.
	waitFor(t, func() bool { _, ok := rec.find(event.JobFinished); return ok })

	// job_started / job_finished emitted with the id.
	if ev, ok := rec.find(event.JobStarted); !ok || ev.Data["id"] != "job_1" || ev.Data["kind"] != "agent" {
		t.Fatalf("job_started missing/wrong: %+v", ev)
	}
	if ev, ok := rec.find(event.JobFinished); !ok || ev.Data["id"] != "job_1" {
		t.Fatalf("job_finished missing/wrong: %+v", ev)
	}
	// subagent_spawned / subagent_finished still emitted, tagged with job_id.
	if ev, ok := rec.find(event.SubagentSpawned); !ok || ev.Data["job_id"] != "job_1" {
		t.Fatalf("subagent_spawned missing job_id: %+v", ev)
	}
	if ev, ok := rec.find(event.SubagentFinished); !ok || ev.Data["job_id"] != "job_1" {
		t.Fatalf("subagent_finished missing job_id: %+v", ev)
	}

	// The final report matches the synchronous path (report + staged diff).
	reports := d.Jobs.DrainFinished("coordinator")
	if len(reports) != 1 {
		t.Fatalf("DrainFinished delivered %d reports, want 1", len(reports))
	}
	if !strings.Contains(reports[0].Result, "IMPLEMENTER REPORT") || !strings.Contains(reports[0].Result, "STAGED DIFF") {
		t.Fatalf("job report not the synchronous outcome text:\n%s", reports[0].Result)
	}
	if !strings.Contains(reports[0].Result, "did the work") {
		t.Fatalf("job report missing the implementer's report text:\n%s", reports[0].Result)
	}
	// Exactly once: a second drain yields nothing.
	if extra := d.Jobs.DrainFinished("coordinator"); len(extra) != 0 {
		t.Fatalf("report delivered twice: %+v", extra)
	}
}

// wait covers a background implementer and yields the same report; afterwards
// DrainFinished returns nothing (exactly-once delivery shared between the paths).
func TestSpawnImplementerBackgroundWaitConsumes(t *testing.T) {
	rec := &syncRec{}
	impl := &scripted{resp: []*gollama.ResponseMessageGenerate{call("finish", `{"report":"done via wait"}`)}}
	d, _ := bgDeps(t, rec, impl, nil)
	defer d.Jobs.KillAll()

	if _, err := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "go", "background": true}); err != nil {
		t.Fatal(err)
	}
	reports, running := d.Jobs.Wait(context.Background(), []string{"job_1"}, "all", 0)
	if len(running) != 0 || len(reports) != 1 {
		t.Fatalf("wait returned reports=%d running=%v", len(reports), running)
	}
	if !strings.Contains(reports[0].Result, "IMPLEMENTER REPORT") || !strings.Contains(reports[0].Result, "done via wait") {
		t.Fatalf("wait report wrong:\n%s", reports[0].Result)
	}
	if extra := d.Jobs.DrainFinished("coordinator"); len(extra) != 0 {
		t.Fatalf("wait-consumed report was delivered again by drain: %+v", extra)
	}
}

// The single-writer guard: while a background implementer job is live, a second
// background spawn, a foreground spawn, and send_to_implementer are all refused;
// the refusals point at workstreams. A live mutating background bash job also
// refuses a background implementer, while a non-mutating job does not.
func TestSpawnImplementerSingleWriterGuard(t *testing.T) {
	rec := &syncRec{}
	blocker := newBlockingTurner(call("finish", `{"report":"eventually"}`))
	d, _ := bgDeps(t, nil, nil, nil)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	d.Implementer = AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return blocker }}
	defer func() {
		close(blocker.release)
		job, ok := d.Jobs.Get("job_1")
		if ok {
			waitJobDone(t, job)
		}
		d.Jobs.KillAll()
	}()

	if _, err := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "go", "background": true}); err != nil {
		t.Fatal(err)
	}
	job, ok := d.Jobs.Get("job_1")
	if !ok || job.Status() != jobs.Running {
		t.Fatalf("expected job_1 running, ok=%v", ok)
	}

	// Second background spawn — refused, names the live job, points at workstreams.
	res, _ := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "again", "background": true})
	if !res.IsError || !strings.Contains(res.Content, "job_1") || !strings.Contains(res.Content, "workstream") {
		t.Fatalf("second background spawn should be refused with workstream hint, got: %q", res.Content)
	}
	// Foreground spawn — also refused (two implementers can't share a tree).
	res, _ = spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "fg"})
	if !res.IsError || !strings.Contains(res.Content, "workstream") {
		t.Fatalf("foreground spawn should be refused while a background implementer is live, got: %q", res.Content)
	}
	// send_to_implementer — still running.
	res, _ = sendToImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "instructions": "tweak"})
	if !res.IsError || !strings.Contains(res.Content, "still running") {
		t.Fatalf("send_to_implementer should report the job still running, got: %q", res.Content)
	}
}

// A live mutating background bash job (as startBackgroundBash registers) refuses a
// background implementer; a live non-mutating job does not.
func TestSpawnImplementerBackgroundRefusedByBashJob(t *testing.T) {
	rec := &syncRec{}
	impl := &scripted{resp: []*gollama.ResponseMessageGenerate{call("finish", `{"report":"ok"}`)}}
	d, _ := bgDeps(t, rec, impl, nil)
	defer d.Jobs.KillAll()

	// A live mutating bash job blocks a background implementer.
	d.Jobs.StartMutating("bash", "go test ./...", "coordinator")
	res, _ := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "go", "background": true})
	if !res.IsError || !strings.Contains(res.Content, "workstream") {
		t.Fatalf("background implementer should be refused while a mutating bash job is live, got: %q", res.Content)
	}
}

func TestSpawnImplementerBackgroundAllowedBesideNonMutatingJob(t *testing.T) {
	rec := &syncRec{}
	impl := &scripted{resp: []*gollama.ResponseMessageGenerate{call("finish", `{"report":"ok"}`)}}
	d, _ := bgDeps(t, rec, impl, nil)
	defer d.Jobs.KillAll()

	// A non-mutating job (e.g. a reviewer set) does NOT block a background implementer.
	d.Jobs.Start("agent", "reviewers 0001", "coordinator")
	res, err := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "go", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("background implementer should be allowed beside a non-mutating job, got: %q", res.Content)
	}
	// Let the implementer job finish before returning so its work-log write
	// doesn't race the t.TempDir cleanup.
	job, _ := d.Jobs.Get("job_2")
	waitJobDone(t, job)
}

// A background reviewer set returns a job id; the finished job carries the
// aggregated verdicts; re_review while the reviewer job is live errors clearly.
func TestSpawnReviewersBackground(t *testing.T) {
	rec := &syncRec{}
	revTurner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"accept","summary":"looks good"}`),
	}}
	d, _ := bgDeps(t, rec, nil, []AgentSpec{{Name: "rev", Model: "m", NewClient: func() engine.Turner { return revTurner }}})
	defer d.Jobs.KillAll()

	res, err := spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": "0001", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "started background job job_1") {
		t.Fatalf("background reviewers should return a job id, got: %q", res.Content)
	}
	job, _ := d.Jobs.Get("job_1")
	waitJobDone(t, job)
	waitFor(t, func() bool { _, ok := rec.find(event.JobFinished); return ok })

	if job.Mutates() {
		t.Fatal("reviewer job must be non-mutating")
	}
	reports := d.Jobs.DrainFinished("coordinator")
	if len(reports) != 1 || !strings.Contains(reports[0].Result, "REVIEW SUMMARY") {
		t.Fatalf("reviewer job report missing aggregated verdicts: %+v", reports)
	}
}

// re_review is refused while the reviewer job is still running.
func TestReReviewRefusedWhileReviewJobLive(t *testing.T) {
	rec := &syncRec{}
	blocker := newBlockingTurner(call("submit_review", `{"verdict":"accept","summary":"ok"}`))
	d, _ := bgDeps(t, rec, nil, []AgentSpec{{Name: "rev", Model: "m", NewClient: func() engine.Turner { return blocker }}})
	defer d.Jobs.KillAll()

	if _, err := spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": "0001", "background": true}); err != nil {
		t.Fatal(err)
	}
	<-blocker.started // reviewer loop is actually running
	res, _ := reReview(d).Call(context.Background(), map[string]any{"task_id": "0001"})
	if !res.IsError || !strings.Contains(res.Content, "still running") {
		t.Fatalf("re_review should be refused while the reviewer job is live, got: %q", res.Content)
	}
	// Let the reviewer job finish before returning so its work-log write doesn't
	// race the t.TempDir cleanup.
	close(blocker.release)
	job, _ := d.Jobs.Get("job_1")
	waitJobDone(t, job)
	waitFor(t, func() bool { _, ok := rec.find(event.JobFinished); return ok })
}

// Background requested but no registry in this session → a clear error.
func TestSpawnImplementerBackgroundUnavailable(t *testing.T) {
	rec := &captureRec{}
	impl := &scripted{resp: []*gollama.ResponseMessageGenerate{call("finish", `{"report":"x"}`)}}
	d, _ := bgDeps(t, rec, impl, nil)
	d.Jobs = nil // no registry
	res, _ := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "go", "background": true})
	if !res.IsError || !strings.Contains(res.Content, "not available") {
		t.Fatalf("expected background-unavailable error, got: %q", res.Content)
	}
}
