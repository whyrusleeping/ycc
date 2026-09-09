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
type bashThenError struct{ calls int }

func (b *bashThenError) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	b.calls++
	if b.calls == 1 {
		return call("Bash", `{"command":"sleep 30","run_in_background":true}`), nil
	}
	return nil, fmt.Errorf("provider failed")
}

type bashThenCancel struct {
	calls   int
	waiting chan struct{}
}

type genericContextFailure struct{ calls int }

func (t *genericContextFailure) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.calls++
	if t.calls == 1 {
		return text("initial answer"), nil
	}
	return nil, fmt.Errorf("maximum context length exceeded")
}

func (b *bashThenCancel) TurnCtx(ctx context.Context, _ gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	b.calls++
	if b.calls == 1 {
		return call("Bash", `{"command":"sleep 30","run_in_background":true}`), nil
	}
	close(b.waiting)
	<-ctx.Done()
	return nil, ctx.Err()
}

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

func waitGenericIdle(t *testing.T, d *Deps, agentID string) {
	t.Helper()
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		h := d.genericAgent[agentID]
		return h != nil && !h.running
	})
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
	waitGenericIdle(t, d, "agent_1")
	if rep := job1.Report(); rep.Status != jobs.Done || !strings.Contains(rep.Result, "first answer") ||
		!strings.Contains(rep.Result, "mode=fresh round=1") {
		t.Fatalf("first report = %+v", rep)
	}

	res, err = sendToAgent(d).Call(context.Background(), map[string]any{"agent_id": "agent_1", "prompt": "now inspect beta"})
	if err != nil || res.IsError || !strings.Contains(res.Content, "job_2") {
		t.Fatalf("send_to_agent = %+v, %v", res, err)
	}
	job2, _ := d.Jobs.Get("job_2")
	waitJobDone(t, job2)
	if rep := job2.Report(); rep.Status != jobs.Done || !strings.Contains(rep.Result, "follow-up answer") ||
		!strings.Contains(rep.Result, "mode=retain round=2") || !strings.Contains(rep.Result, "prior_tokens=") {
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

func TestGenericAgentFreshFollowupReplacesHistoryAndPreservesIdentity(t *testing.T) {
	rec := &syncRec{}
	d, _ := bgDeps(t, rec, nil, nil)
	first := &scripted{resp: []*gollama.ResponseMessageGenerate{text("obsolete first answer")}}
	fresh := &scripted{resp: []*gollama.ResponseMessageGenerate{text("fresh answer")}}
	clients := []engine.Turner{first, fresh}
	d.ResolveAgent = func(name string) (AgentSpec, error) {
		return AgentSpec{Name: name, Model: "provider-model", Backend: "test", NewClient: func() engine.Turner {
			client := clients[0]
			clients = clients[1:]
			return client
		}}, nil
	}

	res, _ := spawnAgent(d).Call(context.Background(), map[string]any{
		"model": "chosen", "prompt": "obsolete initial request", "mutating": true,
	})
	if res.IsError {
		t.Fatalf("spawn = %+v", res)
	}
	job1, _ := d.Jobs.Get("job_1")
	waitJobDone(t, job1)
	waitGenericIdle(t, d, "agent_1")
	d.mu.Lock()
	oldLoop := d.genericAgent["agent_1"].loop
	d.mu.Unlock()

	handoff := "Request: implement beta. Evidence/artifacts: artifact://build-42. Unresolved questions: none. Verification: run go test ./...\nNotes: " +
		strings.Repeat("x", maxRevisionHandoffBytes)
	res, _ = sendToAgent(d).Call(context.Background(), map[string]any{
		"agent_id": "agent_1", "prompt": handoff, "context_mode": "fresh",
	})
	if res.IsError || !strings.Contains(res.Content, "context_mode=fresh") ||
		!strings.Contains(res.Content, "round=2") || !strings.Contains(res.Content, "logical model chosen") {
		t.Fatalf("fresh follow-up = %+v", res)
	}
	job2, _ := d.Jobs.Get("job_2")
	if !job2.Mutates() {
		t.Fatal("fresh follow-up lost mutating job identity")
	}
	waitJobDone(t, job2)
	waitGenericIdle(t, d, "agent_1")
	d.mu.Lock()
	h := d.genericAgent["agent_1"]
	d.mu.Unlock()
	if h.loop == oldLoop || h.spec.Name != "chosen" || h.spec.Model != "provider-model" || !h.writeAccess {
		t.Fatalf("fresh replacement changed handle identity/access: %+v", h)
	}
	if !hasTool(h.loop.Tools, "Edit") || !hasTool(h.loop.Tools, "Write") {
		t.Fatal("fresh replacement did not preserve worker tools")
	}
	if len(fresh.messages) == 0 {
		t.Fatal("fresh client received no request")
	}
	seed := fresh.messages[len(fresh.messages)-1].Content
	for _, want := range []string{"No prior conversation", "artifact://build-42", "Unresolved questions", "run go test ./..."} {
		if !strings.Contains(seed, want) {
			t.Fatalf("fresh handoff missing %q:\n%s", want, seed)
		}
	}
	if strings.Contains(seed, "obsolete initial request") || strings.Contains(seed, "obsolete first answer") {
		t.Fatalf("fresh handoff replayed obsolete history:\n%s", seed)
	}
	if len(seed) > maxRevisionHandoffBytes || !strings.Contains(seed, "handoff truncated") {
		t.Fatalf("fresh handoff was not bounded: %d bytes\n%s", len(seed), seed)
	}

	var freshSpawn map[string]any
	for _, ev := range rec.snapshot() {
		if ev.Type == event.SubagentSpawned && ev.Data["role"] == "generic" && ev.Data["round"] == 2 {
			freshSpawn = ev.Data
		}
	}
	if freshSpawn == nil || freshSpawn["context_mode"] != "fresh" || freshSpawn["rollover_reason"] != "coordinator_fresh" ||
		freshSpawn["logical_model"] != "chosen" || freshSpawn["mutating"] != true {
		t.Fatalf("fresh spawn metadata = %+v", freshSpawn)
	}
}

func TestGenericAgentFreshRecoveryAfterContextFailure(t *testing.T) {
	d, _ := bgDeps(t, &syncRec{}, nil, nil)
	retained := &genericContextFailure{}
	fresh := &scripted{resp: []*gollama.ResponseMessageGenerate{text("recovered")}}
	clients := []engine.Turner{retained, fresh}
	d.ResolveAgent = func(name string) (AgentSpec, error) {
		return AgentSpec{Name: name, Model: "m", NewClient: func() engine.Turner {
			client := clients[0]
			clients = clients[1:]
			return client
		}}, nil
	}
	if res, _ := spawnAgent(d).Call(context.Background(), map[string]any{"model": "chosen", "prompt": "initial"}); res.IsError {
		t.Fatalf("spawn = %+v", res)
	}
	job1, _ := d.Jobs.Get("job_1")
	waitJobDone(t, job1)
	waitGenericIdle(t, d, "agent_1")
	if res, _ := sendToAgent(d).Call(context.Background(), map[string]any{"agent_id": "agent_1", "prompt": "retained continuation"}); res.IsError {
		t.Fatalf("retained follow-up start = %+v", res)
	}
	job2, _ := d.Jobs.Get("job_2")
	waitJobDone(t, job2)
	waitGenericIdle(t, d, "agent_1")
	failed := job2.Report()
	if failed.Status != jobs.Failed || !strings.Contains(failed.Result, `context_mode="fresh"`) ||
		!strings.Contains(failed.Result, "prior conversation and tool logs will not be replayed") ||
		!strings.Contains(failed.Result, "mode=retain round=2") {
		t.Fatalf("context failure report = %+v", failed)
	}

	handoff := "Request: recover. Evidence/artifacts: artifact://failure. Unresolved questions: root cause. Verification: rerun test."
	res, _ := sendToAgent(d).Call(context.Background(), map[string]any{
		"agent_id": "agent_1", "prompt": handoff, "context_mode": "fresh",
	})
	if res.IsError || !strings.Contains(res.Content, "round=3") {
		t.Fatalf("fresh recovery start = %+v", res)
	}
	job3, _ := d.Jobs.Get("job_3")
	waitJobDone(t, job3)
	if rep := job3.Report(); rep.Status != jobs.Done || !strings.Contains(rep.Result, "recovered") ||
		!strings.Contains(rep.Result, "mode=fresh round=3") {
		t.Fatalf("fresh recovery = %+v", rep)
	}
	seed := fresh.messages[len(fresh.messages)-1].Content
	if strings.Contains(seed, "retained continuation") || !strings.Contains(seed, "artifact://failure") {
		t.Fatalf("fresh recovery handoff = %q", seed)
	}
	d.mu.Lock()
	recoveredLoop := d.genericAgent["agent_1"].loop
	d.mu.Unlock()
	if !hasTool(recoveredLoop.Tools, "Read") || hasTool(recoveredLoop.Tools, "Edit") {
		t.Fatal("fresh replacement did not preserve read-only tools")
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
	if res, _ := sendToAgent(d).Call(context.Background(), map[string]any{"agent_id": "agent_1", "prompt": "too soon", "context_mode": "fresh"}); !res.IsError || !strings.Contains(res.Content, "still running") {
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
	// A kill request makes the session job terminal immediately, but KillAll now
	// joins the agent and therefore must not return while Run is still unwinding.
	killed := make(chan struct{})
	go func() {
		first.Jobs.KillAll()
		close(killed)
	}()
	select {
	case <-killed:
		t.Fatal("KillAll returned before the agent stopped")
	case <-time.After(20 * time.Millisecond):
	}
	if res, _ = spawnImplementer(second).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "too soon", "background": true}); !res.IsError {
		t.Fatalf("killed-but-running agent released lease early: %+v", res)
	}
	close(blocker.release)
	select {
	case <-killed:
	case <-time.After(3 * time.Second):
		t.Fatal("KillAll did not return after the agent stopped")
	}
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

func TestImplementerFinishCleansAndJoinsAsyncChild(t *testing.T) {
	impl := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Bash", `{"command":"printf started; sleep 30","run_in_background":true}`),
		call("finish", `{"report":"started a child"}`),
		call("finish", `{"report":"follow-up ran"}`),
	}}
	rec := &syncRec{}
	d, _ := bgDeps(t, rec, impl, nil)
	ownership := workspacelease.NewService()
	d.Ownership = ownership
	d.CoordinatorToken = ownership.NewToken("session one coordinator")
	defer d.Jobs.KillAll()

	res, _ := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "start child"})
	if res.IsError || !strings.Contains(res.Content, "BACKGROUND JOBS RESOLVED") || !strings.Contains(res.Content, "job_1") {
		t.Fatalf("implementer did not account for cleaned child: %+v", res)
	}
	job, ok := d.Jobs.Get("job_1")
	if !ok || job.Status() != jobs.Killed {
		t.Fatalf("implementer background Bash was not killed: %#v", job)
	}
	if live := d.Jobs.LiveMutating(); live != nil {
		t.Fatalf("child execution still owns mutation after finish: %s", live.ID())
	}
	if ev, ok := rec.find(event.JobClaimed); !ok || ev.Data["id"] != "job_1" || ev.Data["reason"] != "subagent_exit" {
		t.Fatalf("subagent cleanup notification claim missing: %+v", ev)
	}
	other := ownership.NewToken("session two coordinator")
	lease, err := ownership.Acquire(d.Workspace, other)
	if err != nil {
		t.Fatalf("child ownership not released after joined finish: %v", err)
	}
	lease.Release()

	res, _ = sendToImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "instructions": "continue"})
	if res.IsError || !strings.Contains(res.Content, "follow-up ran") {
		t.Fatalf("retained follow-up after cleanup failed: %+v", res)
	}
}

func TestImplementerNoProgressPreservesResolvedChildEvidence(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		impl := &scripted{resp: []*gollama.ResponseMessageGenerate{
			call("Bash", `{"command":"true","run_in_background":true}`),
			text(""),
		}}
		d, _ := bgDeps(t, &syncRec{}, impl, nil)
		defer d.Jobs.KillAll()
		res, _ := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "check"})
		if !res.IsError || !strings.Contains(res.Content, "no changes") ||
			!strings.Contains(res.Content, "BACKGROUND JOBS RESOLVED") || !strings.Contains(res.Content, "job_1") {
			t.Fatalf("fresh no-progress result lost lifecycle evidence: %+v", res)
		}
	})

	t.Run("retained follow-up", func(t *testing.T) {
		impl := &scripted{resp: []*gollama.ResponseMessageGenerate{
			call("finish", `{"report":"initial done"}`),
			call("Bash", `{"command":"true","run_in_background":true}`),
			text(""),
		}}
		d, _ := bgDeps(t, &syncRec{}, impl, nil)
		defer d.Jobs.KillAll()
		if res, _ := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "initial"}); res.IsError {
			t.Fatalf("initial implementer run: %+v", res)
		}
		res, _ := sendToImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "instructions": "check again"})
		if !res.IsError || !strings.Contains(res.Content, "no changes") ||
			!strings.Contains(res.Content, "BACKGROUND JOBS RESOLVED") || !strings.Contains(res.Content, "job_1") {
			t.Fatalf("retained no-progress result lost lifecycle evidence: %+v", res)
		}
	})
}

func TestImplementerFailureCleansAndAccountsAsyncChild(t *testing.T) {
	turner := &bashThenError{}
	d, _ := bgDeps(t, &syncRec{}, nil, nil)
	d.Implementer = AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return turner }}
	ownership := workspacelease.NewService()
	d.Ownership = ownership
	d.CoordinatorToken = ownership.NewToken("session one coordinator")
	defer d.Jobs.KillAll()

	res, _ := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "start then fail"})
	if !res.IsError || !strings.Contains(res.Content, "provider failed") ||
		!strings.Contains(res.Content, "BACKGROUND JOBS RESOLVED") || !strings.Contains(res.Content, "job_1") {
		t.Fatalf("failed subagent did not account for child cleanup: %+v", res)
	}
	job, ok := d.Jobs.Get("job_1")
	if !ok || job.Status() != jobs.Killed || d.Jobs.LiveMutating() != nil {
		t.Fatalf("failed subagent left child execution live: %#v", job)
	}
}

func TestImplementerCancellationCleansAndJoinsAsyncChild(t *testing.T) {
	turner := &bashThenCancel{waiting: make(chan struct{})}
	d, _ := bgDeps(t, &syncRec{}, nil, nil)
	d.Implementer = AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return turner }}
	ownership := workspacelease.NewService()
	d.Ownership = ownership
	d.CoordinatorToken = ownership.NewToken("session one coordinator")
	defer d.Jobs.KillAll()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan *gollama.ToolResult, 1)
	go func() {
		res, _ := spawnImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "plan": "start then cancel"})
		result <- res
	}()
	<-turner.waiting
	cancel()
	res := <-result
	if !res.IsError || !strings.Contains(res.Content, "context canceled") || !strings.Contains(res.Content, "BACKGROUND JOBS RESOLVED") {
		t.Fatalf("cancelled subagent did not account for child cleanup: %+v", res)
	}
	job, ok := d.Jobs.Get("job_1")
	if !ok || job.Status() != jobs.Killed || d.Jobs.LiveMutating() != nil {
		t.Fatalf("cancelled subagent left child execution live: %#v", job)
	}
}

func TestImplementerExplicitWatcherHandoffToParent(t *testing.T) {
	impl := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Bash", `{"command":"printf watching; sleep 30","run_in_background":true}`),
		call("finish", `{"report":"watcher installed","handoff_jobs":[{"job_id":"job_1","purpose":"observe the follow-up deployment"}]}`),
		call("finish", `{"report":"follow-up after watcher"}`),
	}}
	rec := &syncRec{}
	d, _ := bgDeps(t, rec, impl, nil)
	ownership := workspacelease.NewService()
	d.Ownership = ownership
	d.CoordinatorToken = ownership.NewToken("session one coordinator")
	defer d.Jobs.KillAll()

	res, _ := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "start watcher"})
	if res.IsError || !strings.Contains(res.Content, "BACKGROUND JOB HANDOFF") ||
		!strings.Contains(res.Content, `purpose="observe the follow-up deployment"`) ||
		!strings.Contains(res.Content, `owner="coordinator"`) || !strings.Contains(res.Content, "exactly once") {
		t.Fatalf("explicit handoff metadata missing: %+v", res)
	}
	job, ok := d.Jobs.Get("job_1")
	if !ok || job.Status() != jobs.Running || job.Owner() != "coordinator" {
		t.Fatalf("watcher was not transferred alive to parent: %#v", job)
	}
	infos := d.Jobs.List("coordinator", false)
	if len(infos) != 1 || infos[0].Purpose != "observe the follow-up deployment" || infos[0].Delivery == "" {
		t.Fatalf("handoff not discoverable: %+v", infos)
	}
	if ev, ok := rec.find(event.JobHandedOff); !ok || ev.Data["id"] != "job_1" || ev.Data["owner"] != "coordinator" {
		t.Fatalf("durable handoff event missing: %+v", ev)
	}

	res, _ = sendToImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "instructions": "too early"})
	if !res.IsError || !strings.Contains(res.Content, "background Bash") {
		t.Fatalf("mutating follow-up started while handed-off watcher ran: %+v", res)
	}
	job.Kill()
	job.WaitExecution()
	res, _ = sendToImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "instructions": "continue now"})
	if res.IsError || !strings.Contains(res.Content, "follow-up after watcher") {
		t.Fatalf("follow-up after watcher stopped failed: %+v", res)
	}
	// Handoff completion belongs to the parent and remains exactly-once for
	// automatic delivery while repeatable evidence stays on the job.
	reports := d.Jobs.DrainFinished("coordinator")
	if len(reports) != 1 || reports[0].ID != "job_1" {
		t.Fatalf("parent handoff delivery = %+v", reports)
	}
	if extra := d.Jobs.DrainFinished("coordinator"); len(extra) != 0 {
		t.Fatalf("handoff delivered twice: %+v", extra)
	}
	if rep := job.Report(); rep.ID != "job_1" || rep.Status != jobs.Killed {
		t.Fatalf("retained handoff result unavailable: %+v", rep)
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
func TestGenericFollowupDeclinesAfterJobShutdownWithoutStateOrLeaseLeak(t *testing.T) {
	d, _ := bgDeps(t, &syncRec{}, nil, nil)
	turner := &scripted{resp: []*gollama.ResponseMessageGenerate{text("first")}}
	d.ResolveAgent = func(name string) (AgentSpec, error) {
		return AgentSpec{Name: name, Model: "m", NewClient: func() engine.Turner { return turner }}, nil
	}
	ownership := workspacelease.NewService()
	d.Ownership = ownership
	d.CoordinatorToken = ownership.NewToken("session one coordinator")
	res, _ := spawnAgent(d).Call(context.Background(), map[string]any{"model": "coder", "prompt": "first", "mutating": true})
	if res.IsError {
		t.Fatalf("initial generic agent: %+v", res)
	}
	job, _ := d.Jobs.Get("job_1")
	waitJobDone(t, job)
	job.WaitExecution()
	d.Jobs.KillAll()

	res, _ = sendToAgent(d).Call(context.Background(), map[string]any{"agent_id": "agent_1", "prompt": "late"})
	if !res.IsError || !strings.Contains(res.Content, "shutting down") {
		t.Fatalf("late generic follow-up = %+v", res)
	}
	d.mu.Lock()
	h := d.genericAgent["agent_1"]
	round, running, retainedJob := h.round, h.running, h.job
	d.mu.Unlock()
	if round != 1 || running || retainedJob != job {
		t.Fatalf("declined follow-up mutated retained state: round=%d running=%t job=%v", round, running, retainedJob)
	}
	other := ownership.NewToken("session two coordinator")
	lease, err := ownership.Acquire(d.Workspace, other)
	if err != nil {
		t.Fatalf("declined generic follow-up retained mutation lease: %v", err)
	}
	lease.Release()
}

func TestProductionAgentStartsDeclineAfterJobShutdown(t *testing.T) {
	impl := &scripted{resp: []*gollama.ResponseMessageGenerate{call("finish", `{"report":"must not run"}`)}}
	reviewer := AgentSpec{Name: "rev", Model: "m", NewClient: func() engine.Turner { return impl }}
	d, _ := bgDeps(t, &syncRec{}, impl, []AgentSpec{reviewer})
	ownership := workspacelease.NewService()
	d.Ownership = ownership
	d.CoordinatorToken = ownership.NewToken("session one coordinator")
	d.ResolveAgent = func(name string) (AgentSpec, error) { return d.Implementer, nil }
	d.Jobs.KillAll()

	res, _ := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "late", "background": true})
	if !res.IsError || !strings.Contains(res.Content, "shutting down") {
		t.Fatalf("late implementer start = %+v", res)
	}
	d.mu.Lock()
	implRunning := d.implRunning
	d.mu.Unlock()
	if implRunning {
		t.Fatal("declined implementer left its active-run guard set")
	}
	other := ownership.NewToken("session two coordinator")
	lease, err := ownership.Acquire(d.Workspace, other)
	if err != nil {
		t.Fatalf("declined implementer retained mutation lease: %v", err)
	}
	lease.Release()

	res, _ = spawnAgent(d).Call(context.Background(), map[string]any{"model": "impl", "prompt": "late", "mutating": true})
	if !res.IsError || !strings.Contains(res.Content, "shutting down") {
		t.Fatalf("late generic agent start = %+v", res)
	}
	lease, err = ownership.Acquire(d.Workspace, other)
	if err != nil {
		t.Fatalf("declined generic agent retained mutation lease: %v", err)
	}
	lease.Release()

	res, _ = spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": "0001", "background": true})
	if !res.IsError || !strings.Contains(res.Content, "shutting down") {
		t.Fatalf("late reviewer start = %+v", res)
	}
}

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
