package orchestrator

import (
	"context"
	"fmt"
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
	"github.com/whyrusleeping/ycc/internal/sandbox"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
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

func (b *blockingTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
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
func TestGenericAgentBackgroundModelAndFollowup(t *testing.T) {
	rec := &syncRec{}
	d, _ := bgDeps(t, rec, nil, nil)
	turner := &scripted{resp: []*gollama.ResponseMessageGenerate{text("first answer"), text("follow-up answer")}}
	var resolved string
	d.ResolveAgent = func(name string) (AgentSpec, error) {
		resolved = name
		if name != "chosen" {
			return AgentSpec{}, fmt.Errorf("unknown model %q", name)
		}
		return AgentSpec{Name: name, Model: "provider-model", Backend: "test", NewClient: func() engine.Turner { return turner }}, nil
	}
	d.AgentModels = func() []string { return []string{"chosen", "other"} }

	res, err := spawnAgent(d).Call(context.Background(), map[string]any{"model": "chosen", "prompt": "inspect alpha"})
	if err != nil || res.IsError || !strings.Contains(res.Content, "agent_1") || !strings.Contains(res.Content, "job_1") {
		t.Fatalf("spawn_agent = %+v, %v", res, err)
	}
	if resolved != "chosen" {
		t.Fatalf("resolved model = %q, want chosen", resolved)
	}
	job1, _ := d.Jobs.Get("job_1")
	if job1.Mutates() != !genericReadOnlyEnforced() {
		t.Fatalf("generic job mutates = %v, sandbox = %s", job1.Mutates(), sandbox.Available())
	}
	waitJobDone(t, job1)
	if rep := job1.Report(); rep.Status != jobs.Done || rep.Result != "first answer" {
		t.Fatalf("first report = %+v", rep)
	}

	res, err = sendToAgent(d).Call(context.Background(), map[string]any{"agent_id": "agent_1", "prompt": "now inspect beta"})
	if err != nil || res.IsError || !strings.Contains(res.Content, "job_2") {
		t.Fatalf("send_to_agent = %+v, %v", res, err)
	}
	job2, _ := d.Jobs.Get("job_2")
	waitJobDone(t, job2)
	if rep := job2.Report(); rep.Status != jobs.Done || rep.Result != "follow-up answer" {
		t.Fatalf("follow-up report = %+v", rep)
	}
	if turner.i != 2 {
		t.Fatalf("model turns = %d, want 2", turner.i)
	}
	var sawFirst, sawFollowup bool
	for _, msg := range turner.messages {
		if msg.Role == "assistant" && msg.Content == "first answer" {
			sawFirst = true
		}
		if msg.Role == "user" && msg.Content == "now inspect beta" {
			sawFollowup = true
		}
	}
	if !sawFirst || !sawFollowup {
		t.Fatalf("follow-up did not retain history: %+v", turner.messages)
	}

	var spawns int
	for _, ev := range rec.snapshot() {
		if ev.Type == event.SubagentSpawned && ev.Data["role"] == "generic" {
			spawns++
			if ev.Data["agent_id"] != "agent_1" || ev.Data["logical_model"] != "chosen" {
				t.Fatalf("generic spawn metadata = %+v", ev.Data)
			}
		}
	}
	if spawns != 2 {
		t.Fatalf("generic spawn events = %d, want 2", spawns)
	}
}

func TestGenericMutatingAgentUsesSingleWriterGuard(t *testing.T) {
	d, _ := bgDeps(t, &syncRec{}, nil, nil)
	turner := &scripted{resp: []*gollama.ResponseMessageGenerate{text("coded")}}
	d.ResolveAgent = func(name string) (AgentSpec, error) {
		return AgentSpec{Name: name, Model: "m", NewClient: func() engine.Turner { return turner }}, nil
	}

	blocker := d.Jobs.StartMutating("bash", "existing writer", d.Emitter.Actor())
	res, _ := spawnAgent(d).Call(context.Background(), map[string]any{"model": "coder", "prompt": "edit it", "mutating": true})
	if !res.IsError || !strings.Contains(res.Content, "another mutating job") {
		t.Fatalf("mutating spawn beside writer = %+v", res)
	}
	blocker.Finish(jobs.Done, "done")

	res, _ = spawnAgent(d).Call(context.Background(), map[string]any{"model": "coder", "prompt": "edit it", "mutating": true})
	if res.IsError || !strings.Contains(res.Content, "mutating subagent") {
		t.Fatalf("mutating spawn = %+v", res)
	}
	job, _ := d.Jobs.Get("job_2")
	if !job.Mutates() {
		t.Fatal("explicitly mutating generic agent was not registered as mutating")
	}
	waitJobDone(t, job)
}

func TestGenericAgentErrorsAndRunningFollowup(t *testing.T) {
	d, _ := bgDeps(t, &syncRec{}, nil, nil)
	ownership := workspacelease.NewService()
	d.Ownership = ownership
	d.CoordinatorToken = ownership.NewToken("session one coordinator")
	if res, _ := spawnAgent(d).Call(context.Background(), map[string]any{"model": "x", "prompt": "p"}); !res.IsError || !strings.Contains(res.Content, "model resolution") {
		t.Fatalf("spawn without resolver = %+v", res)
	}

	blocker := newBlockingTurner(text("done"))
	d.ResolveAgent = func(name string) (AgentSpec, error) {
		if name != "known" {
			return AgentSpec{}, fmt.Errorf("unknown model %q", name)
		}
		return AgentSpec{Name: name, Model: "m", NewClient: func() engine.Turner { return blocker }}, nil
	}
	if res, _ := spawnAgent(d).Call(context.Background(), map[string]any{"model": "missing", "prompt": "p"}); !res.IsError || !strings.Contains(res.Content, "unknown model") {
		t.Fatalf("unknown model = %+v", res)
	}
	if res, _ := spawnAgent(d).Call(context.Background(), map[string]any{"model": "known", "prompt": "p", "mutating": true}); res.IsError {
		t.Fatalf("spawn known = %+v", res)
	}
	<-blocker.started
	if res, _ := sendToAgent(d).Call(context.Background(), map[string]any{"agent_id": "agent_1", "prompt": "too soon"}); !res.IsError || !strings.Contains(res.Content, "still running") {
		t.Fatalf("running follow-up = %+v", res)
	}
	if res, _ := sendToAgent(d).Call(context.Background(), map[string]any{"agent_id": "agent_999", "prompt": "p"}); !res.IsError || !strings.Contains(res.Content, "no such agent") {
		t.Fatalf("unknown agent = %+v", res)
	}
	job, _ := d.Jobs.Get("job_1")
	job.Kill()
	if res, _ := sendToAgent(d).Call(context.Background(), map[string]any{"agent_id": "agent_1", "prompt": "still too soon"}); !res.IsError || !strings.Contains(res.Content, "unwinding") {
		t.Fatalf("killed-but-unwinding follow-up = %+v", res)
	}
	close(blocker.release)
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !d.genericAgent["agent_1"].running
	})
}

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

	// The final report matches the synchronous path (report + bounded manifest/excerpt).
	reports := d.Jobs.DrainFinished("coordinator")
	if len(reports) != 1 {
		t.Fatalf("DrainFinished delivered %d reports, want 1", len(reports))
	}
	if !strings.Contains(reports[0].Result, "IMPLEMENTER REPORT") || !strings.Contains(reports[0].Result, "CHANGE MANIFEST") {
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
	res, _ = sendToImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "instructions": "tweak", "context_mode": "fresh"})
	if !res.IsError || !strings.Contains(res.Content, "still running") {
		t.Fatalf("fresh send_to_implementer should report the job still running, got: %q", res.Content)
	}
	// kill_job makes Job.Status terminal synchronously, but the model turn is
	// deliberately still blocked. A new top-level turn must remain refused until
	// Loop.Run actually returns.
	job.Kill()
	res, _ = sendToImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "instructions": "too early"})
	if !res.IsError || !strings.Contains(res.Content, "unwinding") {
		t.Fatalf("killed-but-unwinding implementer admitted a revision: %q", res.Content)
	}
}

func TestSpawnImplementerLeaseRejectsCrossSessionStart(t *testing.T) {
	blocker := newBlockingTurner(call("finish", `{"report":"eventually"}`))
	first, _ := bgDeps(t, &syncRec{}, nil, nil)
	first.Implementer = AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return blocker }}
	ownership := workspacelease.NewService()
	first.Ownership = ownership
	first.CoordinatorToken = ownership.NewToken("session one coordinator")

	second := &Deps{
		Workspace: first.Workspace, Docs: first.Docs, Repo: first.Repo,
		Emitter: event.NewEmitter(&syncRec{}, "coordinator"), Jobs: jobs.NewRegistry(),
		Ownership: ownership, CoordinatorToken: ownership.NewToken("session two coordinator"),
		Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner {
			return &scripted{resp: []*gollama.ResponseMessageGenerate{call("finish", `{"report":"wrong"}`)}}
		}},
	}
	defer second.Jobs.KillAll()
	if res, _ := spawnImplementer(first).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "go", "background": true}); res.IsError {
		t.Fatalf("first session start: %s", res.Content)
	}
	<-blocker.started
	res, _ := spawnImplementer(second).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "race", "background": true})
	if !res.IsError || !strings.Contains(res.Content, "session one coordinator") || !strings.Contains(res.Content, "workstream") {
		t.Fatalf("cross-session start refusal = %+v", res)
	}
	// A kill request makes the session job terminal immediately, but ownership
	// stays with the agent until its Run actually unwinds.
	first.Jobs.KillAll()
	if res, _ = spawnImplementer(second).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "too soon", "background": true}); !res.IsError {
		t.Fatalf("killed-but-running agent released lease early: %+v", res)
	}
	close(blocker.release)
	deadline := time.Now().Add(3 * time.Second)
	for {
		res, _ = spawnImplementer(second).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "after exit", "background": true})
		if !res.IsError {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent lease was not released after termination: %s", res.Content)
		}
		time.Sleep(2 * time.Millisecond)
	}
	job, _ := second.Jobs.Get("job_1")
	waitJobDone(t, job)
}

func TestImplementerAsyncChildBlocksKilledJobRevisionUntilExit(t *testing.T) {
	impl := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Bash", `{"command":"setsid sh -c 'echo detached; sleep 1' & sleep 30","run_in_background":true}`),
		call("finish", `{"report":"started a child"}`),
	}}
	d, _ := bgDeps(t, &syncRec{}, impl, nil)
	ownership := workspacelease.NewService()
	d.Ownership = ownership
	d.CoordinatorToken = ownership.NewToken("session one coordinator")
	defer d.Jobs.KillAll()

	res, _ := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "start child"})
	if !res.IsError || !strings.Contains(res.Content, "asynchronous mutation") {
		t.Fatalf("implementer finalized over live child: %+v", res)
	}
	job, ok := d.Jobs.Get("job_1")
	if !ok {
		t.Fatal("implementer background Bash was not registered")
	}
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(job.Tail(20), "detached") {
		if time.Now().After(deadline) {
			t.Fatal("detached process did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	job.Kill()
	res, _ = sendToImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "instructions": "too early"})
	if !res.IsError || !strings.Contains(res.Content, "background Bash") {
		t.Fatalf("killed delegated child admitted a new implementer turn: %+v", res)
	}

	other := ownership.NewToken("session two coordinator")
	deadline = time.Now().Add(3 * time.Second)
	for {
		lease, err := ownership.Acquire(d.Workspace, other)
		if err == nil {
			lease.Release()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child ownership not released after process exit: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
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
	res, _ := reReview(d).Call(context.Background(), map[string]any{"task_id": "0001", "context_mode": "fresh", "handoff": "verify the revision"})
	if !res.IsError || !strings.Contains(res.Content, "still running") {
		t.Fatalf("fresh re_review should be refused while the reviewer job is live, got: %q", res.Content)
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
