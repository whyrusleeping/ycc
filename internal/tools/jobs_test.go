package tools

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
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
