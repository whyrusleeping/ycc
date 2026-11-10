package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
)

// confirmAsker records confirmation prompts and answers them with ok.
type confirmAsker struct {
	noopAsker
	ok        bool
	mu        sync.Mutex
	questions []string
}

func (a *confirmAsker) Confirm(_ context.Context, q string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.questions = append(a.questions, q)
	return a.ok, nil
}

func (a *confirmAsker) asked() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.questions...)
}

func (r *captureRec) ofType(t event.Type) []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []event.Event
	for _, ev := range r.events {
		if ev.Type == t {
			out = append(out, ev)
		}
	}
	return out
}

func handoffDeps(t *testing.T, status docs.Status) (*Deps, *captureRec, *confirmAsker, string) {
	t.Helper()
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	asker := &confirmAsker{ok: true}
	d.Asker = asker
	d.Jobs = jobs.NewRegistry()
	t.Cleanup(d.Jobs.KillAll)
	task, err := d.Docs.CreateWithStatus("Hand me off", "## Description\nwork", 3, nil, nil, status)
	if err != nil {
		t.Fatal(err)
	}
	return d, rec, asker, task.ID
}

// start_work and its guidance are chat-only and need the session's hook.
func TestStartWorkOfferedInChatWithHook(t *testing.T) {
	d := depsFor(t)
	reg, system := BuildMode("chat", d, false)
	if hasTool(reg, "start_work") || strings.Contains(system, workHandoffGuidance) {
		t.Fatal("start_work offered without a StartWork hook")
	}
	d.StartWork = func(context.Context, WorkHandoff) (StartedWork, error) { return StartedWork{}, nil }
	reg, system = BuildMode("chat", d, false)
	if !hasTool(reg, "start_work") || !strings.Contains(system, workHandoffGuidance) {
		t.Fatal("chat must offer start_work with its guidance when the hook is set")
	}
	for _, mode := range []string{"pm", "work", "watch"} {
		if reg, _ := BuildMode(mode, d, false); hasTool(reg, "start_work") {
			t.Fatalf("%s mode must not offer start_work", mode)
		}
	}
}

// A task the work pipeline would refuse is rejected before the user is asked,
// and a declined confirmation starts nothing.
func TestStartWorkGates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  docs.Status
		held    bool
		decline bool
		want    string
	}{
		{name: "proposed", status: docs.StatusProposed, want: "user acceptance required"},
		{name: "done", status: docs.StatusDone, want: "task is complete"},
		{name: "held", status: docs.StatusTodo, held: true, want: "another live session"},
		{name: "declined", status: docs.StatusTodo, decline: true, want: "declined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, rec, asker, id := handoffDeps(t, tc.status)
			asker.ok = !tc.decline
			if tc.held {
				d.HeldTasks = func() map[string]string { return map[string]string{id: "s_other"} }
			}
			started := false
			d.StartWork = func(context.Context, WorkHandoff) (StartedWork, error) {
				started = true
				return StartedWork{}, nil
			}
			res, err := startWork(d).Call(context.Background(), map[string]any{"task_id": id, "handoff": "ctx"})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(res.Content, tc.want) {
				t.Fatalf("result = %q, want it to mention %q", res.Content, tc.want)
			}
			if started || len(rec.ofType(event.WorkHandoff)) != 0 || len(d.Jobs.List("", true)) != 0 {
				t.Fatal("a gated or declined hand-off started work")
			}
			if wantAsked := tc.decline; (len(asker.asked()) > 0) != wantAsked {
				t.Fatalf("confirmations = %q; asked only for an actionable task", asker.asked())
			}
		})
	}
}

// An approved hand-off starts exactly that task with the handoff, records the
// decision, keeps the chat running, and delivers the session outcome as a job.
func TestStartWorkHandsOffAndTracksOutcome(t *testing.T) {
	d, rec, asker, id := handoffDeps(t, docs.StatusTodo)
	release := make(chan struct{})
	var got WorkHandoff
	d.StartWork = func(_ context.Context, h WorkHandoff) (StartedWork, error) {
		got = h
		return StartedWork{SessionID: "s_work", Workstream: "ws1", Branch: "ycc/ws/ws1-" + id,
			Wait: func(ctx context.Context) (WorkOutcome, bool) {
				select {
				case <-release:
					return WorkOutcome{Result: "Committed abc: done"}, true
				case <-ctx.Done():
					return WorkOutcome{}, false
				}
			}}, nil
	}
	res, err := startWork(d).Call(context.Background(), map[string]any{
		"task_id": id, "handoff": "decided: use the cache", "separate_workstream": true,
	})
	if err != nil || res.IsError {
		t.Fatalf("start_work: %v %s", err, res.Content)
	}
	if res.Structured != nil {
		t.Fatalf("start_work must not end the chat: %+v", res.Structured)
	}
	if got.TaskID != id || got.Handoff != "decided: use the cache" || !got.Workstream {
		t.Fatalf("hand-off = %+v", got)
	}
	if q := asker.asked(); len(q) != 1 || !strings.Contains(q[0], id) || !strings.Contains(q[0], "use the cache") ||
		!strings.Contains(q[0], "separate workstream") {
		t.Fatalf("confirmation = %q", q)
	}
	if !strings.Contains(res.Content, "s_work") || !strings.Contains(res.Content, "job_1") {
		t.Fatalf("result = %q, want the session and job ids", res.Content)
	}
	handoffs := rec.ofType(event.WorkHandoff)
	if len(handoffs) != 1 || handoffs[0].Data["task"] != id || handoffs[0].Data["session"] != "s_work" ||
		handoffs[0].Data["workstream"] != "ws1" {
		t.Fatalf("work_handoff events = %+v", handoffs)
	}
	if infos := d.Jobs.List("", true); len(infos) != 1 || infos[0].Status != jobs.Running || infos[0].Kind != "session" {
		t.Fatalf("jobs = %+v, want one running session job", infos)
	}

	close(release)
	reports, running := d.Jobs.Wait(context.Background(), []string{"job_1"}, "all", 5*time.Second)
	if len(running) != 0 || len(reports) != 1 || reports[0].Status != jobs.Done || reports[0].Result != "Committed abc: done" {
		t.Fatalf("reports = %+v running = %v", reports, running)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(rec.ofType(event.JobFinished)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fin := rec.ofType(event.JobFinished); len(fin) != 1 || fin[0].Data["status"] != "done" {
		t.Fatalf("job_finished events = %+v", fin)
	}
}

// Killing the tracking job stops tracking only: the wait is cancelled and the
// job ends killed without a second terminal report.
func TestStartWorkKillStopsTrackingOnly(t *testing.T) {
	d, rec, _, id := handoffDeps(t, docs.StatusInProgress)
	waitEnded := make(chan struct{})
	d.StartWork = func(context.Context, WorkHandoff) (StartedWork, error) {
		return StartedWork{SessionID: "s_work", Wait: func(ctx context.Context) (WorkOutcome, bool) {
			<-ctx.Done()
			close(waitEnded)
			return WorkOutcome{}, false
		}}, nil
	}
	if res, err := startWork(d).Call(context.Background(), map[string]any{"task_id": id, "handoff": "h"}); err != nil || res.IsError {
		t.Fatalf("start_work: %v %v", err, res)
	}
	job, ok := d.Jobs.Get("job_1")
	if !ok || !job.Kill() {
		t.Fatal("kill job_1")
	}
	select {
	case <-waitEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("killing the job did not cancel the wait")
	}
	if job.Status() != jobs.Killed || len(rec.ofType(event.JobFinished)) != 0 {
		t.Fatalf("status = %s, job_finished = %v", job.Status(), rec.ofType(event.JobFinished))
	}
}
