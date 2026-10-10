package tools

import (
	"os"
	"path/filepath"
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

func TestKilledBackgroundBashRetainsStableOutputArtifact(t *testing.T) {
	reg, _, _ := jobsReg(t)
	started := dispatch(t, reg, "Bash", `{"command":"printf 'before-kill'; sleep 30","run_in_background":true}`)
	at := strings.Index(started.Content, "output_")
	if started.IsError || at < 0 {
		t.Fatalf("background Bash did not advertise its pending artifact: %+v", started)
	}
	end := at
	for end < len(started.Content) && started.Content[end] != ' ' && started.Content[end] != '\n' {
		end++
	}
	artifactID := started.Content[at:end]
	deadline := time.Now().Add(2 * time.Second)
	for {
		out := dispatch(t, reg, "job_output", `{"job_id":"job_1"}`)
		if strings.Contains(out.Content, "before-kill") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background output was not produced before kill: %q", out.Content)
		}
		time.Sleep(10 * time.Millisecond)
	}
	killed := dispatch(t, reg, "kill_job", `{"job_id":"job_1"}`)
	if killed.IsError || !strings.Contains(killed.Content, artifactID) || !strings.Contains(killed.Content, "not-ready response is temporary") {
		t.Fatalf("killed report lost artifact readiness path: %q", killed.Content)
	}
	args := `{"artifact_id":"` + artifactID + `"}`
	for {
		got := dispatch(t, reg, "tool_output", args)
		if !got.IsError {
			if !strings.Contains(got.Content, "before-kill") {
				t.Fatalf("completed killed capture = %q", got.Content)
			}
			break
		}
		if !strings.Contains(got.Content, "not ready") {
			t.Fatalf("killed artifact failed permanently: %q", got.Content)
		}
		if time.Now().After(deadline) {
			t.Fatalf("killed artifact did not become ready: %q", got.Content)
		}
		time.Sleep(10 * time.Millisecond)
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

func TestListJobsScopesOwnersAndShowsAgentActivity(t *testing.T) {
	reg, jr, _ := jobsReg(t)
	a := jr.Start("agent", "inspect safely", "coordinator")
	a.UpdateActivity("Search", &jobs.Usage{Input: 12, Output: 3, Total: 15})
	jr.Start("bash", "other actor", "implementer")

	own := dispatch(t, reg, "list_jobs", `{}`)
	if own.IsError || !strings.Contains(own.Content, "job_1 [running]") || !strings.Contains(own.Content, "owner=coordinator") ||
		!strings.Contains(own.Content, `current_tool="Search"`) || !strings.Contains(own.Content, "total:15") || strings.Contains(own.Content, "job_2") {
		t.Fatalf("owner list = %q", own.Content)
	}
	all := dispatch(t, reg, "list_jobs", `{"include_all_owners":true}`)
	if all.IsError || !strings.Contains(all.Content, "job_2") || !strings.Contains(all.Content, "owner=implementer") {
		t.Fatalf("all-owner list = %q", all.Content)
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
