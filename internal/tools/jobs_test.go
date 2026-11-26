package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	t.Cleanup(func() { _ = jr.KillAll() })
	ws := &Workspace{Root: t.TempDir(), Jobs: jr, Emitter: event.NewEmitter(rec, "coordinator"),
		OutputDir: filepath.Join(t.TempDir(), "output")}
	reg := New()
	reg.Add(Editing(ws)...)
	return reg, jr, rec
}

// wait without "for" returns as soon as one target finishes, leaving the rest
// running; "all" still blocks for every target.
func TestWaitDefaultsToAny(t *testing.T) {
	reg, _, _ := jobsReg(t)
	dispatch(t, reg, "Bash", `{"command":"echo quick","run_in_background":true}`)
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)

	start := time.Now()
	res := dispatch(t, reg, "wait", `{"job_ids":["job_1","job_2"],"timeout_s":20}`)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("default wait blocked on the slow job (%s)", time.Since(start))
	}
	if res.IsError || !strings.Contains(res.Content, "quick") || !strings.Contains(res.Content, "still running") ||
		!strings.Contains(res.Content, "job_2") {
		t.Fatalf("default wait = %q, want job_1 report with job_2 still running", res.Content)
	}

	res = dispatch(t, reg, "wait", `{"job_ids":["job_1","job_2"],"for":"all","timeout_s":1}`)
	if !strings.Contains(res.Content, "still running after 1s: job_2") {
		t.Fatalf("for=all wait = %q, want timeout with job_2 running", res.Content)
	}
}

// A work-loop coordinator's wait on shell jobs alone is capped and tells it to
// end its turn; a wait that includes a subagent run keeps the requested timeout.
func TestShellWaitCapBoundsShellOnlyWait(t *testing.T) {
	rec := &captureRec{}
	jr := jobs.NewRegistry()
	t.Cleanup(func() { _ = jr.KillAll() })
	ws := &Workspace{Root: t.TempDir(), Jobs: jr, Emitter: event.NewEmitter(rec, "coordinator"),
		ShellWaitCap: 300 * time.Millisecond}
	reg := New()
	reg.Add(Editing(ws)...)
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)

	start := time.Now()
	res := dispatch(t, reg, "wait", `{"job_ids":["job_1"],"timeout_s":20}`)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("shell-only wait ignored the cap (%s)", time.Since(start))
	}
	if !strings.Contains(res.Content, "still running after 300ms: job_1") || !strings.Contains(res.Content, "end your turn") {
		t.Fatalf("capped wait = %q, want the end-your-turn reminder", res.Content)
	}

	agent := jr.Start("agent", "reviewers", "coordinator")
	start = time.Now()
	res = dispatch(t, reg, "wait", `{"job_ids":["job_1","`+agent.ID()+`"],"timeout_s":1}`)
	if time.Since(start) < time.Second || strings.Contains(res.Content, "end your turn") {
		t.Fatalf("mixed wait = %q after %s, want the full 1s timeout without the reminder", res.Content, time.Since(start))
	}
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
	claim := rec.find(event.JobClaimed)
	if claim == nil {
		t.Fatal("wait did not durably claim terminal evidence")
	}
	claimResult, _ := claim.Data["result"].(string)
	if claim.Data["id"] != "job_1" || claim.Data["kind"] != "bash" || claim.Data["status"] != "done" ||
		claim.Data["label"] != "sleep 0.4 && echo done" || !strings.Contains(claimResult, "exit 0") {
		t.Fatalf("wait claim lacks terminal evidence: %+v", claim)
	}
}

// job_output exposes explicit repeatable cursors rather than hidden read state.
func TestJobOutputExplicitCursor(t *testing.T) {
	reg, _, _ := jobsReg(t)
	res := dispatch(t, reg, "Bash", `{"command":"echo first; sleep 0.5; echo second","run_in_background":true}`)
	if res.IsError {
		t.Fatalf("Bash bg: %s", res.Content)
	}
	// Give the first echo time to land while the job is still running.
	time.Sleep(150 * time.Millisecond)
	out := dispatch(t, reg, "job_output", `{"job_id":"job_1","cursor":0}`)
	if !strings.Contains(out.Content, "first") || !strings.Contains(out.Content, "running") || !strings.Contains(out.Content, "next_cursor=6") {
		t.Fatalf("first job_output = %q", out.Content)
	}
	again := dispatch(t, reg, "job_output", `{"job_id":"job_1","cursor":0}`)
	if !strings.Contains(again.Content, "first") {
		t.Fatalf("repeat job_output did not revisit data: %q", again.Content)
	}
	// Wait for completion, then continue from the explicit cursor.
	dispatch(t, reg, "wait", `{"job_ids":["job_1"]}`)
	out = dispatch(t, reg, "job_output", `{"job_id":"job_1","cursor":6}`)
	if strings.Contains(out.Content, "first") || !strings.Contains(out.Content, "second") {
		t.Fatalf("continued job_output = %q", out.Content)
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

func TestKilledBackgroundBashKeepsItsOutputFile(t *testing.T) {
	reg, _, _ := jobsReg(t)
	started := dispatch(t, reg, "Bash", `{"command":"printf 'before-kill'; sleep 30","run_in_background":true}`)
	path := between(started.Content, "Its output is written to ", " while it runs")
	if started.IsError || !filepath.IsAbs(path) {
		t.Fatalf("background Bash did not name its output file: %+v", started)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(path)
		if string(data) == "before-kill" {
			break // the file is written while the job runs
		}
		if time.Now().After(deadline) {
			t.Fatalf("output file %s = %q, want the running job's output", path, data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	killed := dispatch(t, reg, "kill_job", `{"job_id":"job_1"}`)
	if killed.IsError || !strings.Contains(killed.Content, path) || !strings.Contains(killed.Content, "complete once the process has exited") {
		t.Fatalf("killed report lost its output file: %q", killed.Content)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "before-kill" {
		t.Fatalf("killed job's output file = %q, %v", data, err)
	}
}

// Shell commands take no worktree lease: another scope's running shell never
// blocks a session, and a session's shells are admitted while another scope
// holds the lease. File tools still respect the lease.
func TestShellCommandsDoNotParticipateInWorktreeLease(t *testing.T) {
	root := t.TempDir()
	ownership := workspacelease.NewService()
	firstJobs := jobs.NewRegistry()
	defer firstJobs.KillAll()
	firstWS := &Workspace{
		Root: root, Jobs: firstJobs, Emitter: event.NewEmitter(&captureRec{}, "coordinator"),
		Ownership: ownership, MutationToken: ownership.NewToken("session one coordinator"),
	}
	first := New()
	first.Add(Editing(firstWS)...)
	secondJobs := jobs.NewRegistry()
	defer secondJobs.KillAll()
	secondWS := &Workspace{
		Root: root, Jobs: secondJobs, Emitter: event.NewEmitter(&captureRec{}, "coordinator"),
		Ownership: ownership, MutationToken: ownership.NewToken("session two coordinator"),
		WriteWait: 50 * time.Millisecond,
	}
	second := New()
	second.Add(Editing(secondWS)...)

	// A long-running background shell in session one blocks nothing.
	if res := dispatch(t, first, "Bash", `{"command":"sleep 30","run_in_background":true}`); res.IsError {
		t.Fatalf("start background Bash: %s", res.Content)
	}
	if live := firstJobs.LiveMutating(); live != nil {
		t.Fatalf("background Bash counted as a mutating job: %s", live.ID())
	}
	if got := dispatch(t, second, "Write", `{"file_path":"from-two","content":"x"}`); got.IsError {
		t.Fatalf("Write refused beside another session's background Bash: %s", got.Content)
	}
	if got := dispatch(t, second, "Bash", `{"command":"true"}`); got.IsError {
		t.Fatalf("Bash refused beside another session's background Bash: %s", got.Content)
	}

	// An operation-scoped section held by another scope (a commit or merge)
	// holds off foreign file writes until it ends — they wait, then name the
	// owner — but never foreign shell commands.
	holder, err := ownership.Acquire(root, ownership.NewToken("session one mutating agent"))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if got := dispatch(t, second, "Write", `{"file_path":"blocked","content":"x"}`); !got.IsError || !strings.Contains(got.Content, "session one mutating agent") {
		t.Fatalf("cross-scope Write was not refused with owner: %+v", got)
	}
	if got := dispatch(t, second, "Bash", `{"command":"true"}`); got.IsError {
		t.Fatalf("foreground Bash refused while another scope holds the lease: %s", got.Content)
	}
	if got := dispatch(t, second, "Bash", `{"command":"true","run_in_background":true}`); got.IsError {
		t.Fatalf("background Bash refused while another scope holds the lease: %s", got.Content)
	}
}

func TestJobOutputFinalReportRepeatableAfterCheckpointNotification(t *testing.T) {
	reg, jr, _ := jobsReg(t)
	j := jr.Start("agent", "completed agent", "coordinator")
	j.Finish(jobs.Done, "stable report")
	if notified := jr.DrainFinished("coordinator"); len(notified) != 1 {
		t.Fatalf("checkpoint notification = %+v", notified)
	}
	for i := 0; i < 2; i++ {
		got := dispatch(t, reg, "job_output", `{"job_id":"job_1"}`)
		if got.IsError || !strings.Contains(got.Content, "stable report") {
			t.Fatalf("job_output final report #%d = %+v", i+1, got)
		}
	}
	if duplicate := jr.DrainFinished("coordinator"); len(duplicate) != 0 {
		t.Fatalf("retrieval created duplicate notification: %+v", duplicate)
	}
}

func TestJobOutputFinalReportDoesNotClaimNotification(t *testing.T) {
	reg, jr, rec := jobsReg(t)
	j := jr.Start("bash", "completed command", "coordinator")
	j.Append([]byte("captured output\n"))
	j.Finish(jobs.Failed, "final report: exit 1")
	for i := 0; i < 2; i++ {
		got := dispatch(t, reg, "job_output", `{"job_id":"job_1"}`)
		if got.IsError || !strings.Contains(got.Content, "final report: exit 1") || strings.Contains(got.Content, "captured output") {
			t.Fatalf("default read lost final report: %+v", got)
		}
	}
	raw := dispatch(t, reg, "job_output", `{"job_id":"job_1","cursor":0}`)
	if raw.IsError || !strings.Contains(raw.Content, "captured output") || !strings.Contains(raw.Content, "next_cursor=16") {
		t.Fatalf("explicit captured-output read lost data/range: %+v", raw)
	}
	if rec.find(event.JobClaimed) != nil {
		t.Fatal("peeking claimed completion")
	}
	if notified := jr.DrainFinished("coordinator"); len(notified) != 1 || notified[0].Result != "final report: exit 1" {
		t.Fatalf("peeking consumed notification: %+v", notified)
	}
}

func TestWaitOmittedIDsDoesNotExpandEmptyOwnerScope(t *testing.T) {
	for _, actor := range []string{"coordinator", "implementer"} {
		t.Run(actor, func(t *testing.T) {
			jr := jobs.NewRegistry()
			defer jr.KillAll()
			rec := &captureRec{}
			reg := New()
			reg.Add(JobTools(&Workspace{Jobs: jr, Emitter: event.NewEmitter(rec, actor)})...)

			completedOwner := "coordinator"
			if actor == "coordinator" {
				completedOwner = actor
			}
			completed := jr.Start("bash", "already done", completedOwner)
			completed.Finish(jobs.Done, "retained result")
			otherOwner := "coordinator"
			if actor == "coordinator" {
				otherOwner = "implementer"
			}
			otherLive := jr.Start("bash", "someone else's live job", otherOwner)

			start := time.Now()
			got := dispatch(t, reg, "wait", `{"timeout_s":1}`)
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatalf("omitted-id wait blocked for %s with no owned live jobs", elapsed)
			}
			if got.IsError || got.Content != "wait: no matching jobs." {
				t.Fatalf("omitted-id wait = %+v", got)
			}
			if otherLive.Status() != jobs.Running {
				t.Fatalf("wait affected another actor's live job: %s", otherLive.Status())
			}
			// The completed job remains unclaimed, whether it belongs to this actor
			// or another one; omitted wait considers owned LIVE jobs only.
			if pending := jr.DrainFinished(completedOwner); len(pending) != 1 || pending[0].ID != completed.ID() {
				t.Fatalf("wait claimed another target set: %+v", pending)
			}
			if claim := rec.find(event.JobClaimed); claim != nil {
				t.Fatalf("empty owner scope emitted a claim: %+v", claim)
			}
		})
	}
}

func TestJobOutputReportsEvictionGapAndCanRevisitTail(t *testing.T) {
	reg, jr, _ := jobsReg(t)
	j := jr.Start("bash", "chatty", "coordinator")
	j.Append([]byte(strings.Repeat("x", 300*1024)))

	got := dispatch(t, reg, "job_output", `{"job_id":"job_1","cursor":0,"limit":32}`)
	if got.IsError || !strings.Contains(got.Content, "retention gap: bytes 0-") || !strings.Contains(got.Content, "next_cursor=") {
		t.Fatalf("evicted output = %q", got.Content)
	}
	tail := dispatch(t, reg, "job_output", `{"job_id":"job_1","tail_lines":1}`)
	again := dispatch(t, reg, "job_output", `{"job_id":"job_1","tail_lines":1}`)
	if tail.IsError || again.IsError || tail.Content != again.Content || !strings.Contains(tail.Content, "retained") {
		t.Fatalf("repeat tails differ: %q / %q", tail.Content, again.Content)
	}
}

func TestJobToolsRejectUnknownIDs(t *testing.T) {
	reg, _, _ := jobsReg(t)
	for _, tc := range []struct{ name, args string }{
		{"job_output", `{"job_id":"job_404"}`},
		{"wait", `{"job_ids":["job_404"]}`},
		{"kill_job", `{"job_id":"job_404"}`},
	} {
		got := dispatch(t, reg, tc.name, tc.args)
		if !got.IsError || !strings.Contains(got.Content, "no such job") {
			t.Fatalf("%s unknown id = %+v", tc.name, got)
		}
	}
}

func TestBackgroundBashDeclinesAfterRegistryShutdown(t *testing.T) {
	root := t.TempDir()
	jr := jobs.NewRegistry()
	jr.KillAll()
	ownership := workspacelease.NewService()
	owner := ownership.NewToken("session one coordinator")
	ws := &Workspace{Root: root, Jobs: jr, Emitter: event.NewEmitter(&captureRec{}, "coordinator"),
		Ownership: ownership, MutationToken: owner}
	reg := New()
	reg.Add(Editing(ws)...)

	res := dispatch(t, reg, "Bash", `{"command":"touch late","run_in_background":true}`)
	if !res.IsError || !strings.Contains(res.Content, "shutting down") {
		t.Fatalf("background Bash after shutdown = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "late")); !os.IsNotExist(err) {
		t.Fatalf("declined background Bash ran: %v", err)
	}
	other := ownership.NewToken("session two coordinator")
	lease, err := ownership.Acquire(root, other)
	if err != nil {
		t.Fatalf("declined background Bash held a mutation lease: %v", err)
	}
	lease.Release()
}

// run_in_background is rejected clearly when the session has no job registry.
func TestBackgroundRejectedWithoutRegistry(t *testing.T) {
	reg := New()
	reg.Add(Worker(&Workspace{Root: t.TempDir()})...)
	res := dispatch(t, reg, "Bash", `{"command":"echo hi","run_in_background":true}`)
	if !res.IsError {
		t.Fatalf("expected error result, got %q", res.Content)
	}
}

func TestBackgroundBashTimeoutIsJobRuntimeLimit(t *testing.T) {
	reg, jr, rec := jobsReg(t)
	res := dispatch(t, reg, "Bash", `{"command":"echo started; sleep 30","timeout_s":1,"run_in_background":true}`)
	if res.IsError || !strings.Contains(res.Content, "job_1") {
		t.Fatalf("Bash bg with timeout = %q (err=%v)", res.Content, res.IsError)
	}
	if job, ok := jr.Get("job_1"); !ok || job.Status() != jobs.Running || job.Mutates() {
		t.Fatalf("timed background Bash is not a live, non-mutating job: %#v ok=%v", job, ok)
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
	if job.Status() == jobs.Running {
		t.Fatalf("timed-out job remains live: %s", job.ID())
	}
	// wait can return the terminal report before the runner emits its final event.
	job.WaitExecution()
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

// wait records its resolved targets (expanded when omitted) and effective
// deadline, after the default timeout and the work-loop shell cap, before
// blocking; the payload round-trips through ParseWaitStarted.
func TestWaitRecordsWaitStarted(t *testing.T) {
	rec := &captureRec{}
	jr := jobs.NewRegistry()
	t.Cleanup(func() { _ = jr.KillAll() })
	ws := &Workspace{Root: t.TempDir(), Jobs: jr, Emitter: event.NewEmitter(rec, "coordinator"),
		ShellWaitCap: 200 * time.Millisecond}
	reg := New()
	reg.Add(Editing(ws)...)
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)
	before := time.Now()
	dispatch(t, reg, "wait", `{"for":"all","timeout_s":60}`)
	ev := rec.find(event.WaitStarted)
	if ev == nil {
		t.Fatal("wait recorded no wait_started")
	}
	callID, spec, ok := ParseWaitStarted(ev.Data)
	if !ok || callID != "x" || len(spec.IDs) != 1 || spec.IDs[0] != "job_1" || spec.Mode != "all" || !spec.ShellOnly ||
		spec.Timeout != 200*time.Millisecond {
		t.Fatalf("wait_started = %v (parsed %q %+v %t)", ev.Data, callID, spec, ok)
	}
	if spec.Deadline.Before(before.Add(200*time.Millisecond)) || spec.Deadline.After(time.Now().Add(200*time.Millisecond)) {
		t.Fatalf("deadline %s does not include the capped timeout", spec.Deadline)
	}

	// Without a cap the omitted timeout is the 600s default.
	rec2 := &captureRec{}
	ws2 := &Workspace{Root: t.TempDir(), Jobs: jr, Emitter: event.NewEmitter(rec2, "coordinator")}
	reg2 := New()
	reg2.Add(JobTools(ws2)...)
	jr.Start("agent", "probe", "coordinator").Finish(jobs.Done, "ok")
	dispatch(t, reg2, "wait", `{"job_ids":["job_2"]}`)
	ev = rec2.find(event.WaitStarted)
	if ev == nil {
		t.Fatal("wait recorded no wait_started")
	}
	if _, spec, _ = ParseWaitStarted(ev.Data); spec.Timeout != defaultWaitTimeout || spec.ShellOnly {
		t.Fatalf("default wait_started = %v", ev.Data)
	}
}

// A restored wait whose deadline already passed collects without blocking —
// never treating the spent timeout as "no timeout" — and a report an earlier
// run already claimed is returned again without a second job_claimed.
func TestResumeWaitExpiredDeadlineCollectsNow(t *testing.T) {
	rec := &captureRec{}
	jr := jobs.NewRegistry()
	t.Cleanup(func() { _ = jr.KillAll() })
	ws := &Workspace{Jobs: jr, Emitter: event.NewEmitter(rec, "coordinator")}
	done := jr.Start("bash", "quick", "coordinator")
	done.Finish(jobs.Done, "exit 0\nquick")
	jr.Wait(context.Background(), []string{done.ID()}, "all", time.Second) // claimed by an earlier run
	slow := jr.Start("bash", "slow", "coordinator")
	spec := WaitSpec{IDs: []string{done.ID(), slow.ID()}, Mode: "all", Deadline: time.Now().Add(-time.Second), Timeout: time.Minute}
	start := time.Now()
	res, err := ResumeWait(context.Background(), ws, spec, time.Now())
	if err != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("expired restored wait: err=%v after %s", err, time.Since(start))
	}
	if !strings.Contains(res.Content, "quick") || !strings.Contains(res.Content, "still running after 1m0s: "+slow.ID()) {
		t.Fatalf("expired restored wait = %q", res.Content)
	}
	if ev := rec.find(event.JobClaimed); ev != nil {
		t.Fatalf("restored wait re-claimed an already claimed report: %v", ev.Data)
	}

	// Before the deadline it blocks on the remaining targets.
	go func() { time.Sleep(100 * time.Millisecond); slow.Finish(jobs.Done, "exit 0\nslow") }()
	spec.Deadline = time.Now().Add(time.Minute)
	res, err = ResumeWait(context.Background(), ws, spec, time.Now())
	if err != nil || !strings.Contains(res.Content, "slow") || strings.Contains(res.Content, "still running") {
		t.Fatalf("restored wait = %q, %v", res.Content, err)
	}
	if ev := rec.find(event.JobClaimed); ev == nil || ev.Data["id"] != slow.ID() {
		t.Fatalf("restored wait did not claim the newly collected report: %v", ev)
	}
}
