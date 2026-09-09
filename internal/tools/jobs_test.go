package tools

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// captureRec collects emitted events for assertions.
type captureRec struct {
	mu     sync.Mutex
	events []event.Event
	seq    int
}

func (c *captureRec) Record(actor string, t event.Type, data map[string]any) event.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	ev := event.Event{Seq: c.seq, Actor: actor, Type: t, Data: data}
	c.events = append(c.events, ev)
	return ev
}

func (c *captureRec) find(t event.Type) *event.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.events) - 1; i >= 0; i-- {
		if c.events[i].Type == t {
			ev := c.events[i]
			return &ev
		}
	}
	return nil
}

func jobsReg(t *testing.T) (*Registry, *jobs.Registry, *captureRec) {
	t.Helper()
	rec := &captureRec{}
	jr := jobs.NewRegistry()
	t.Cleanup(jr.KillAll)
	ws := &Workspace{Root: t.TempDir(), Jobs: jr, Emitter: event.NewEmitter(rec, "coordinator")}
	reg := New()
	reg.Add(Editing(ws)...)
	return reg, jr, rec
}

// A backgrounded command returns a job_id immediately (well under its runtime),
// wait returns exit 0 + output, and job_started/job_finished are emitted.
func TestBackgroundBashWaitReturnsExitAndOutput(t *testing.T) {
	reg, _, rec := jobsReg(t)

	start := time.Now()
	res := dispatch(t, reg, "Bash", `{"command":"sleep 0.4 && echo done","run_in_background":true}`)
	if res.IsError {
		t.Fatalf("Bash bg: %s", res.Content)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("run_in_background did not return immediately (took %s)", time.Since(start))
	}
	if !strings.Contains(res.Content, "job_1") {
		t.Fatalf("expected job id in result, got %q", res.Content)
	}
	if rec.find(event.JobStarted) == nil {
		t.Fatal("no job_started event emitted")
	}

	res = dispatch(t, reg, "wait", `{"job_ids":["job_1"]}`)
	if res.IsError {
		t.Fatalf("wait: %s", res.Content)
	}
	if !strings.Contains(res.Content, "done") || !strings.Contains(res.Content, "exit 0") {
		t.Fatalf("wait result missing output/exit: %q", res.Content)
	}
	if fin := rec.find(event.JobFinished); fin == nil {
		t.Fatal("no job_finished event emitted")
	} else if fin.Data["status"] != "done" {
		t.Fatalf("job_finished status = %v, want done", fin.Data["status"])
	}
}

// job_output mid-run returns partial output + running status, and a second call
// returns only new output.
func TestJobOutputIncremental(t *testing.T) {
	reg, _, _ := jobsReg(t)
	res := dispatch(t, reg, "Bash", `{"command":"echo first; sleep 0.5; echo second","run_in_background":true}`)
	if res.IsError {
		t.Fatalf("Bash bg: %s", res.Content)
	}
	// Give the first echo time to land while the job is still running.
	time.Sleep(150 * time.Millisecond)
	out := dispatch(t, reg, "job_output", `{"job_id":"job_1"}`)
	if !strings.Contains(out.Content, "first") || !strings.Contains(out.Content, "running") {
		t.Fatalf("first job_output = %q, want 'first' + running", out.Content)
	}
	if strings.Contains(out.Content, "second") {
		t.Fatalf("first job_output already has 'second': %q", out.Content)
	}
	// Wait for completion, then a second job_output returns only the new tail.
	dispatch(t, reg, "wait", `{"job_ids":["job_1"]}`)
	out = dispatch(t, reg, "job_output", `{"job_id":"job_1"}`)
	if strings.Contains(out.Content, "first") {
		t.Fatalf("second job_output repeated old output: %q", out.Content)
	}
	if !strings.Contains(out.Content, "second") {
		t.Fatalf("second job_output missing new output: %q", out.Content)
	}
}

// kill_job terminates the process and sets status killed.
func TestKillJobTool(t *testing.T) {
	reg, jr, rec := jobsReg(t)
	res := dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)
	if res.IsError {
		t.Fatalf("Bash bg: %s", res.Content)
	}
	res = dispatch(t, reg, "kill_job", `{"job_id":"job_1"}`)
	if res.IsError || !strings.Contains(res.Content, "killed") {
		t.Fatalf("kill_job = %q (err=%v)", res.Content, res.IsError)
	}
	j, ok := jr.Get("job_1")
	if !ok || j.Status() != jobs.Killed {
		t.Fatalf("job status = %v ok=%v, want killed", j.Status(), ok)
	}
	if fin := rec.find(event.JobFinished); fin == nil || fin.Data["status"] != "killed" {
		t.Fatalf("job_finished not emitted as killed: %+v", fin)
	}
	// The process context is cancelled.
	select {
	case <-j.Context().Done():
	default:
		t.Fatal("job context not cancelled after kill")
	}
}

func TestBackgroundShellLeaseSurvivesKillUntilProcessExit(t *testing.T) {
	root := t.TempDir()
	ownership := workspacelease.NewService()
	firstJobs := jobs.NewRegistry()
	defer firstJobs.KillAll()
	firstToken := ownership.NewToken("session one implementer")
	lifetime, err := ownership.Acquire(root, firstToken)
	if err != nil {
		t.Fatal(err)
	}
	defer lifetime.Release()
	firstWS := &Workspace{
		Root: root, Jobs: firstJobs, Emitter: event.NewEmitter(&captureRec{}, "coordinator"),
		Ownership: ownership, MutationToken: firstToken,
	}
	first := New()
	first.Add(Editing(firstWS)...)
	secondWS := &Workspace{Root: root, Ownership: ownership, MutationToken: ownership.NewToken("session two coordinator")}
	second := New()
	second.Add(Editing(secondWS)...)

	// The detached child retains the output pipe after kill_job has killed the
	// command's process group, keeping cmd.Wait (and therefore the lease) alive.
	res := dispatch(t, first, "Bash", `{"command":"setsid sh -c 'echo detached; sleep 1' & sleep 30","run_in_background":true}`)
	if res.IsError {
		t.Fatalf("start background Bash: %s", res.Content)
	}
	readyDeadline := time.Now().Add(time.Second)
	for {
		if got := dispatch(t, first, "job_output", `{"job_id":"job_1"}`); strings.Contains(got.Content, "detached") {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("detached child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := dispatch(t, second, "Read", `{"file_path":"."}`); got.IsError {
		t.Fatalf("read-only tool was blocked: %s", got.Content)
	}
	if got := dispatch(t, second, "Write", `{"file_path":"blocked","content":"x"}`); !got.IsError || !strings.Contains(got.Content, "session one") {
		t.Fatalf("cross-session Write was not refused with owner: %+v", got)
	}
	if got := dispatch(t, first, "Write", `{"file_path":"sibling","content":"x"}`); !got.IsError || !strings.Contains(got.Content, "background Bash") {
		t.Fatalf("worker write overlapped its asynchronous child: %+v", got)
	}
	if got := dispatch(t, first, "Bash", `{"command":"touch sibling-bg","run_in_background":true}`); !got.IsError {
		t.Fatalf("second asynchronous child overlapped first: %+v", got)
	}
	dispatch(t, first, "kill_job", `{"job_id":"job_1"}`)
	// The worker turn may unwind before its asynchronous process; releasing its
	// lifetime claim must not release the child's retained ownership.
	lifetime.Release()
	if got := dispatch(t, second, "Bash", `{"command":"touch too-early"}`); !got.IsError {
		t.Fatalf("killed-but-not-exited shell released lease early: %+v", got)
	}
	if got := dispatch(t, first, "Write", `{"file_path":"same-token-too-early","content":"x"}`); !got.IsError {
		t.Fatalf("kill status admitted parent before process exit: %+v", got)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		got := dispatch(t, second, "Write", `{"file_path":"after-exit","content":"ok"}`)
		if !got.IsError {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease not released after actual process exit: %s", got.Content)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// run_in_background is rejected clearly when the session has no job registry.
func TestBackgroundRejectedWithoutRegistry(t *testing.T) {
	reg := New()
	reg.Add(Worker(&Workspace{Root: t.TempDir()})...)
	res := dispatch(t, reg, "Bash", `{"command":"echo hi","run_in_background":true}`)
	if !res.IsError {
		t.Fatalf("expected error result, got %q", res.Content)
	}
	// Bash without a registry does not advertise run_in_background at all.
	var bashDef *gollama.Tool
	for _, td := range reg.tools {
		if td.Name == "Bash" {
			bashDef = td
		}
	}
	if bashDef == nil {
		t.Fatal("no Bash tool")
	}
	if strings.Contains(bashDef.Description, "run_in_background") {
		t.Fatal("Bash advertises run_in_background without a job registry")
	}
}

func TestBackgroundBashTimeoutIsJobRuntimeLimit(t *testing.T) {
	reg, jr, rec := jobsReg(t)
	res := dispatch(t, reg, "Bash", `{"command":"echo started; sleep 30","timeout_s":1,"run_in_background":true}`)
	if res.IsError || !strings.Contains(res.Content, "job_1") {
		t.Fatalf("Bash bg with timeout = %q (err=%v)", res.Content, res.IsError)
	}
	if live := jr.LiveMutating(); live == nil || live.ID() != "job_1" || !live.Mutates() {
		t.Fatalf("timed background Bash is not live/mutating: %#v", live)
	}

	res = dispatch(t, reg, "wait", `{"job_ids":["job_1"],"timeout_s":3}`)
	if res.IsError || !strings.Contains(res.Content, "[job job_1 failed]") ||
		!strings.Contains(res.Content, "command timed out after 1s") ||
		!strings.Contains(res.Content, "started") {
		t.Fatalf("timed background wait result = %q (err=%v)", res.Content, res.IsError)
	}
	job, ok := jr.Get("job_1")
	if !ok || job.Status() != jobs.Failed {
		t.Fatalf("timed job status = %v ok=%v, want failed", job.Status(), ok)
	}
	if live := jr.LiveMutating(); live != nil {
		t.Fatalf("timed-out job remains live: %s", live.ID())
	}
	if fin := rec.find(event.JobFinished); fin == nil || fin.Data["status"] != "failed" ||
		!strings.Contains(fin.Data["tail"].(string), "command timed out after 1s") {
		t.Fatalf("job_finished does not report timeout failure: %+v", fin)
	}

	out := dispatch(t, reg, "job_output", `{"job_id":"job_1"}`)
	if out.IsError || !strings.Contains(out.Content, "failed") || !strings.Contains(out.Content, "started") {
		t.Fatalf("job_output after timeout = %q (err=%v)", out.Content, out.IsError)
	}
	killed := dispatch(t, reg, "kill_job", `{"job_id":"job_1"}`)
	if killed.IsError || !strings.Contains(killed.Content, "already failed") {
		t.Fatalf("kill_job after timeout = %q (err=%v)", killed.Content, killed.IsError)
	}
}

func TestBackgroundBashTimeoutSchemaAndValidation(t *testing.T) {
	reg, _, _ := jobsReg(t)
	var bashDef *gollama.Tool
	for _, td := range reg.tools {
		if td.Name == "Bash" {
			bashDef = td
			break
		}
	}
	if bashDef == nil {
		t.Fatal("no Bash tool")
	}
	params, ok := bashDef.Params.(gollama.ToolFunctionParams)
	if !ok {
		t.Fatalf("Bash params type = %T", bashDef.Params)
	}
	timeout, ok := params.Properties["timeout_s"].(map[string]any)
	if !ok || timeout["minimum"] != 1 || timeout["maximum"] != maxBashTimeoutSeconds ||
		!strings.Contains(timeout["description"].(string), "background") {
		t.Fatalf("background timeout schema = %#v", params.Properties["timeout_s"])
	}
	background, ok := params.Properties["run_in_background"].(map[string]any)
	if !ok || background["type"] != "boolean" {
		t.Fatalf("background schema = %#v", params.Properties["run_in_background"])
	}

	for _, args := range []string{
		`{"command":"echo hi","timeout_s":0,"run_in_background":true}`,
		`{"command":"echo hi","timeout_s":3601,"run_in_background":true}`,
	} {
		res := dispatch(t, reg, "Bash", args)
		if !res.IsError || !strings.Contains(res.Content, "between 1 and 3600") {
			t.Fatalf("invalid background timeout result = %q (err=%v)", res.Content, res.IsError)
		}
	}
}
