package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/jobshim"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// restartEntries is a minimal engine.RestoreJobEntries over rec's log (the
// engine package imports tools, so tools tests cannot use it).
func restartEntries(evs []event.Event) []jobs.Restored {
	var out []jobs.Restored
	idx := map[string]int{}
	for _, ev := range evs {
		id, _ := ev.Data["id"].(string)
		switch ev.Type {
		case event.JobStarted:
			idx[id] = len(out)
			sup, _ := ev.Data["supervised"].(bool)
			label, _ := ev.Data["label"].(string)
			out = append(out, jobs.Restored{ID: id, Kind: "bash", Label: label, Owner: ev.Actor,
				Status: jobs.Running, Supervised: sup})
		case event.JobFinished:
			if i, ok := idx[id]; ok {
				status, _ := ev.Data["status"].(string)
				tail, _ := ev.Data["tail"].(string)
				out[i].Status, out[i].Result, out[i].Recorded = jobs.Status(status), tail, true
				if ev.Data["delivery"] == OwnerGoneDelivery {
					out[i].Notified = true
				}
			}
		case event.JobClaimed, event.JobNotified:
			if i, ok := idx[id]; ok {
				out[i].Notified = true
			}
		}
	}
	return out
}

func (c *captureRec) snapshot() []event.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]event.Event(nil), c.events...)
}

func (c *captureRec) count(t event.Type) int {
	n := 0
	for _, ev := range c.snapshot() {
		if ev.Type == t {
			n++
		}
	}
	return n
}

// reopenWorkspace is a reopened session's coordinator workspace over entries.
func reopenWorkspace(t *testing.T, sup *jobshim.Session, entries []jobs.Restored, rec event.Recorder) *Workspace {
	t.Helper()
	jr := jobs.NewRestored(entries)
	t.Cleanup(func() { _ = jr.KillAll() })
	return &Workspace{Root: filepath.Dir(filepath.Dir(filepath.Dir(sup.Dir()))), Jobs: jr, JobSupervisor: sup,
		Emitter: event.NewEmitter(rec, "coordinator")}
}

func waitReport(t *testing.T, jr *jobs.Registry, id string) jobs.Report {
	t.Helper()
	reports, running := jr.Wait(context.Background(), []string{id}, "all", 30*time.Second)
	if len(running) != 0 || len(reports) != 1 {
		t.Fatalf("wait %s: reports=%+v running=%v", id, reports, running)
	}
	return reports[0]
}

// checkOutputFile fails unless report names job id's output file and the
// file holds want.
func checkOutputFile(t *testing.T, sup *jobshim.Session, id, report, want string) {
	t.Helper()
	path := jobshim.ArtifactPath(sup.JobDir(id))
	if !strings.Contains(report, "[full output: "+path+": ") {
		t.Fatalf("report does not name %s: %q", path, report)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), want) {
		t.Fatalf("output file %s = %q, %v; want %q", path, data, err, want)
	}
}

// A graceful restart detaches the coordinator's supervised job, recording no
// result; the reopen re-attaches it as a live tracked job whose report, output
// and output file come back as if no restart happened, minus write attribution.
func TestReopenReattachesSurvivingJob(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"echo before; sleep 1; echo survived","run_in_background":true}`)
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatalf("SuspendForRestart: %v", err)
	}
	if n := rec.count(event.JobFinished); n != 0 {
		t.Fatalf("restart detach recorded %d job_finished", n)
	}
	entries := restartEntries(rec.snapshot())
	rec2 := &captureRec{}
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	if !entries[0].Live || entries[0].Status != jobs.Running {
		t.Fatalf("entry = %+v, want a live survivor", entries[0])
	}
	if got := r.Surviving(); len(got) != 1 || got[0] != "job_1" || !r.Reconciled()["job_1"] {
		t.Fatalf("surviving=%v reconciled=%v", got, r.Reconciled())
	}
	ws := reopenWorkspace(t, sup, entries, rec2)
	r.Activate(ws)
	if n := rec2.count(event.JobFinished); n != 0 {
		t.Fatalf("a survivor was recorded finished at reopen (%d)", n)
	}
	r.ReleaseMarker() // the reopened session recorded session_reopened
	job, _ := ws.Jobs.Get("job_1")
	if job.Status() != jobs.Running || !job.ExecutionRunning() || !job.Info().OutputAvailable {
		t.Fatalf("survivor status=%s running=%t output=%t", job.Status(), job.ExecutionRunning(), job.Info().OutputAvailable)
	}
	rep := waitReport(t, ws.Jobs, "job_1")
	if rep.Status != jobs.Done || !strings.Contains(rep.Result, "survived") || !strings.Contains(rep.Result, recoveredAttributionNote) {
		t.Fatalf("report = %+v", rep)
	}
	waitJobExecution(t, job)
	if fin := rec2.find(event.JobFinished); fin == nil || fin.Data["status"] != "done" {
		t.Fatalf("job_finished = %+v", fin)
	}
	checkOutputFile(t, sup, "job_1", rep.Result, "before\nsurvived")
}

func waitExitRecord(t *testing.T, sup *jobshim.Session, id string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := jobshim.ReadExit(sup.JobDir(id)); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never wrote its exit record", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A job that finished while no daemon was attached is a downtime finish: the
// reopen records its job_finished from the exit record and on-disk output,
// leaves it for one automatic delivery, and names its output file. A second
// reopen of the same log neither records nor delivers it again.
func TestReopenHarvestsDowntimeFinish(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 0.3; echo late; exit 3","run_in_background":true}`)
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	waitExitRecord(t, sup, "job_1")
	entries := restartEntries(rec.snapshot())
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	e := entries[0]
	if e.Live || e.Status != jobs.Failed || e.Notified || !strings.Contains(e.Result, "exit 3") ||
		!strings.Contains(e.Result, "late") || !strings.Contains(e.Result, recoveredAttributionNote) {
		t.Fatalf("entry = %+v, want an undelivered downtime finish", e)
	}
	if len(r.Surviving()) != 0 || !r.Reconciled()["job_1"] {
		t.Fatalf("surviving=%v reconciled=%v", r.Surviving(), r.Reconciled())
	}
	rec2 := &captureRec{}
	ws := reopenWorkspace(t, sup, entries, rec2)
	r.Activate(ws)
	fin := rec2.find(event.JobFinished)
	if fin == nil || fin.Data["status"] != "failed" || fin.Data["recovered"] != true || rec2.count(event.JobFinished) != 1 {
		t.Fatalf("job_finished = %+v", fin)
	}
	if !sup.Harvested("job_1") {
		t.Fatal("the harvested exit record was not marked")
	}
	delivered := ws.Jobs.DrainFinished("coordinator")
	if len(delivered) != 1 || delivered[0].ID != "job_1" || len(ws.Jobs.DrainFinished("coordinator")) != 0 {
		t.Fatalf("delivered = %+v, want exactly one delivery", delivered)
	}
	checkOutputFile(t, sup, "job_1", e.Result, "late")
	out := dispatchWith(t, ws, "job_output", `{"job_id":"job_1"}`)
	if !strings.Contains(out.Content, "late") {
		t.Fatalf("job_output = %q", out.Content)
	}

	// Reopen again: the log now holds the finish and its notification.
	evs := append(rec.snapshot(), rec2.snapshot()...)
	evs = append(evs, event.Event{Type: event.JobNotified, Actor: "coordinator", Data: map[string]any{"id": "job_1"}})
	entries = restartEntries(evs)
	r = ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	rec3 := &captureRec{}
	ws = reopenWorkspace(t, sup, entries, rec3)
	r.Activate(ws)
	if rec3.count(event.JobFinished) != 0 || len(ws.Jobs.DrainFinished("coordinator")) != 0 {
		t.Fatal("a repeated reopen recorded or delivered the downtime finish again")
	}
}

// dispatchWith runs one tool of ws's worker set.
func dispatchWith(t *testing.T, ws *Workspace, name, args string) *gollama.ToolResult {
	t.Helper()
	reg := New()
	reg.Add(Editing(ws)...)
	return dispatch(t, reg, name, args)
}

func startPIDJob(t *testing.T, reg *Registry) int {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	dispatch(t, reg, "Bash", `{"command":"echo $$ > `+pidFile+`; exec sleep 30","run_in_background":true}`)
	var child int
	for i := 0; i < 500 && child == 0; i++ {
		data, _ := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("command did not start")
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	return child
}

func waitLockReleased(t *testing.T, sup *jobshim.Session, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for jobshim.SessionBusy(sup.Dir()) == "job_1: a job supervisor is still running" {
		if time.Now().After(deadline) {
			t.Fatal("supervisor lock never released")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A supervisor that died during the downtime without an exit record is a
// loss whose execution is unknown: recorded lost before the reopen, delivered
// once with the supervision-lost warning, and never joined.
func TestReopenSupervisorDiedIsExecutionUnknown(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	startPIDJob(t, reg)
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(supervisorPID(t, sup.JobDir("job_1")), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitLockReleased(t, sup, "job_1")
	entries := restartEntries(rec.snapshot())
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	if e := entries[0]; e.Status != jobs.Lost || !e.ExecutionUnknown || e.Notified {
		t.Fatalf("entry = %+v", e)
	}
	rec2 := &captureRec{}
	ws := reopenWorkspace(t, sup, entries, rec2)
	r.Activate(ws)
	if fin := rec2.find(event.JobFinished); fin == nil || fin.Data["status"] != "lost" || fin.Data["execution_unknown"] != true {
		t.Fatalf("job_finished = %+v", fin)
	}
	job, _ := ws.Jobs.Get("job_1")
	if !job.ExecutionUnknown() || !job.ExecutionRunning() {
		t.Fatalf("unknown=%t running=%t", job.ExecutionUnknown(), job.ExecutionRunning())
	}
	delivered := ws.Jobs.DrainFinished("coordinator")
	if len(delivered) != 1 || !strings.Contains(delivered[0].Result, "may still be running") ||
		len(ws.Jobs.DrainFinished("coordinator")) != 0 {
		t.Fatalf("delivered = %+v", delivered)
	}
	if why := jobshim.SessionBusy(sup.Dir()); !strings.Contains(why, "unknown") {
		t.Fatalf("SessionBusy = %q, want the unknown marker to protect the session", why)
	}
	if err := ws.Jobs.KillAll(); err == nil {
		t.Fatal("KillAll reported a clean join of unknown execution")
	}
}

// A killed job already delivered before supervision was lost keeps its report
// and claim: the reopen restores its execution as unknown without a second
// notification.
func TestReopenKeepsClaimedReportWhenSupervisionLost(t *testing.T) {
	rec := &captureRec{}
	_, _, sup := supervisedWorkspace(t, rec)
	entries := []jobs.Restored{{ID: "job_1", Kind: "bash", Owner: "coordinator", Status: jobs.Killed,
		Result: "killed", Notified: true, Supervised: true}}
	// A job directory whose supervisor is gone without an exit record.
	dir := sup.JobDir("job_1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := `{"version":1,"job_id":"job_1","command":"sleep 30","dir":"/","socket":"` + sup.SocketPath("job_1") + `"}`
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	if e := entries[0]; e.Status != jobs.Killed || e.Result != "killed" || !e.ExecutionUnknown {
		t.Fatalf("entry = %+v", e)
	}
	rec2 := &captureRec{}
	ws := reopenWorkspace(t, sup, entries, rec2)
	r.Activate(ws)
	if rec2.count(event.JobFinished) != 0 || len(ws.Jobs.DrainFinished("coordinator")) != 0 {
		t.Fatal("supervision loss re-notified an already claimed report")
	}
	if job, _ := ws.Jobs.Get("job_1"); !job.ExecutionUnknown() || !job.ExecutionRunning() {
		t.Fatal("execution was asserted complete")
	}
}

// A subagent's shell does not outlive its subagent: a reopen that finds one
// still running (an unplanned exit left it; a graceful restart kills it) kills
// it over the control socket, records it killed without notifying anyone, and
// joins its execution.
func TestReopenKillsSubagentShell(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)
	// Leave it running and unfollowed, as a crashed daemon would.
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	entries := restartEntries(rec.snapshot())
	entries[0].Owner = "implementer"
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	if e := entries[0]; e.Status != jobs.Killed || !e.Notified || !e.ExecutionPending || e.Live {
		t.Fatalf("entry = %+v", e)
	}
	if len(r.Surviving()) != 0 {
		t.Fatalf("surviving = %v", r.Surviving())
	}
	rec2 := &captureRec{}
	ws := reopenWorkspace(t, sup, entries, rec2)
	r.Activate(ws)
	if fin := rec2.find(event.JobFinished); fin == nil || fin.Data["status"] != "killed" || fin.Actor != "implementer" {
		t.Fatalf("job_finished = %+v", fin)
	}
	if fin := rec2.find(event.JobFinished); fin.Data["delivery"] != OwnerGoneDelivery {
		t.Fatalf("job_finished = %+v, want the undelivered outcome marked in the same record", fin)
	}
	if again := restartEntries(append(rec.snapshot(), rec2.snapshot()...)); !again[0].Notified {
		t.Fatal("a later reopen would deliver the subagent's report")
	}
	job, _ := ws.Jobs.Get("job_1")
	// Activate joins the cancelled shell before the coordinator may run.
	if job.ExecutionRunning() {
		t.Fatal("the cancelled subagent shell was not joined")
	}
	exit, err := jobshim.ReadExit(sup.JobDir("job_1"))
	if err != nil || exit.State != jobshim.StateKilled {
		t.Fatalf("exit record = %+v, %v", exit, err)
	}
	if len(ws.Jobs.DrainFinished("implementer")) != 0 || len(ws.Jobs.DrainFinished("coordinator")) != 0 {
		t.Fatal("a killed subagent shell was delivered")
	}
}

// A graceful restart kills a subagent's shell rather than detaching it.
func TestSuspendForRestartKillsSubagentShell(t *testing.T) {
	rec := &captureRec{}
	_, jr, sup := supervisedWorkspace(t, rec)
	ws := &Workspace{Root: filepath.Dir(filepath.Dir(filepath.Dir(sup.Dir()))), Jobs: jr, JobSupervisor: sup,
		Emitter: event.NewEmitter(rec, "implementer")}
	reg := New()
	reg.Add(Editing(ws)...)
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	exit, err := jobshim.ReadExit(sup.JobDir("job_1"))
	if err != nil || exit.State != jobshim.StateKilled {
		t.Fatalf("exit record = %+v, %v; want the subagent shell killed and joined", exit, err)
	}
}

// gitWorkspace makes the supervised workspace's root a git worktree with
// scoped ownership, so recovered shells register for overlap.
func gitWorkspace(t *testing.T, ws *Workspace, svc *workspacelease.Service, scope string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "-q", ws.Root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	ws.Ownership, ws.MutationToken = svc, svc.NewScopedToken(scope, "session "+scope)
}

// A job whose control socket vanished while its supervisor still runs is
// restored with execution unknown: its shell keeps counting as running in the
// worktree for other scopes, and the supervisor's later exit record confirms
// the stop.
func TestReopenLostSupervisionKeepsOverlapUntilConfirmed(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)
	pid := supervisorPID(t, sup.JobDir("job_1"))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) })
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sup.SocketPath("job_1")); err != nil {
		t.Fatal(err)
	}
	entries := restartEntries(rec.snapshot())
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	if e := entries[0]; e.Status != jobs.Lost || !e.ExecutionUnknown {
		t.Fatalf("entry = %+v", e)
	}
	ws := reopenWorkspace(t, sup, entries, &captureRec{})
	svc := workspacelease.NewService()
	gitWorkspace(t, ws, svc, "s_test")
	r.Activate(ws)
	key, err := workspacelease.Canonical(ws.Root)
	if err != nil {
		t.Fatal(err)
	}
	other := svc.BeginShell(key, svc.NewScopedToken("s_other", "other"), true)
	if overlap := svc.EndShell(other); !slices.Contains(overlap.Ran, "s_test") {
		t.Fatalf("overlap = %+v, want the unknown shell counted as running", overlap)
	}
	job, _ := ws.Jobs.Get("job_1")
	if !job.ExecutionUnknown() || !job.ExecutionRunning() {
		t.Fatal("execution was asserted complete")
	}
	_ = syscall.Kill(pid, syscall.SIGTERM) // the supervisor kills its child and writes exit.json
	waitJobExecution(t, job)
	if job.ExecutionUnknown() || job.Status() != jobs.Lost {
		t.Fatalf("after the late exit: unknown=%t status=%s", job.ExecutionUnknown(), job.Status())
	}
}

// A killed job whose supervisor was still unwinding at the restart keeps its
// killed report; the reopen asks for the kill again, and the undelivered report
// is delivered once its exit is confirmed.
func TestReopenResendsKillForUnwindingJob(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	entries := restartEntries(rec.snapshot())
	entries[0].Status, entries[0].Result, entries[0].Recorded = jobs.Killed, "killed", true
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	if e := entries[0]; !e.ExecutionPending || e.Live || e.Status != jobs.Killed {
		t.Fatalf("entry = %+v", e)
	}
	rec2 := &captureRec{}
	ws := reopenWorkspace(t, sup, entries, rec2)
	r.Activate(ws)
	job, _ := ws.Jobs.Get("job_1")
	waitJobExecution(t, job)
	if exit, err := jobshim.ReadExit(sup.JobDir("job_1")); err != nil || exit.State != jobshim.StateKilled {
		t.Fatalf("exit = %+v, %v", exit, err)
	}
	if rec2.count(event.JobFinished) != 0 {
		t.Fatal("a second terminal report was recorded")
	}
	select {
	case <-ws.Jobs.Completions():
	case <-time.After(5 * time.Second):
		t.Fatal("no wake once the kill was confirmed")
	}
	if got := ws.Jobs.DrainFinished("coordinator"); len(got) != 1 || got[0].Result != "killed" {
		t.Fatalf("delivered %+v", got)
	}
}

// A subagent's terminal report left unclaimed (a crash between a reopen's
// writes) is claimed for nobody on the next reopen, never delivered to a later
// run of the same actor.
func TestReopenRepairsUnclaimedSubagentReport(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 0.3; echo gone","run_in_background":true}`)
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	waitExitRecord(t, sup, "job_1")
	entries := restartEntries(rec.snapshot())
	entries[0].Owner = "implementer"
	entries[0].Status, entries[0].Result = jobs.Done, "exit 0"
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	rec2 := &captureRec{}
	ws := reopenWorkspace(t, sup, entries, rec2)
	r.Activate(ws)
	if got := ws.Jobs.DrainFinished("implementer"); len(got) != 0 {
		t.Fatalf("a later implementer run received the dead subagent's report: %+v", got)
	}
	if claim := rec2.find(event.JobClaimed); claim == nil || claim.Data["reason"] != OwnerGoneDelivery {
		t.Fatalf("job_claimed = %+v, want the suppression recorded", claim)
	}
}

// A re-attached job whose completion could not be recorded keeps its exit
// record unconsumed, so a later reopen still reports it.
func TestReattachedFinishNotHarvestedWithoutDurableRecord(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 1; echo evidence","run_in_background":true}`)
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	entries := restartEntries(rec.snapshot())
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	if !entries[0].Live {
		t.Fatal("expected a survivor")
	}
	log, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	log.Close() // the completion's append cannot commit
	ws := reopenWorkspace(t, sup, entries, log)
	r.Activate(ws)
	r.ReleaseMarker()
	job, _ := ws.Jobs.Get("job_1")
	waitJobExecution(t, job)
	if sup.Harvested("job_1") {
		t.Fatal("the exit record was consumed without a durable terminal record")
	}
}

// A survivor that finishes before its session's reopen marker is recorded
// waits for the marker, so the marker never lists an already-finished job.
func TestSurvivorOutcomeWaitsForReopenMarker(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 0.2","run_in_background":true}`)
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	entries := restartEntries(rec.snapshot())
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	rec2 := &captureRec{}
	ws := reopenWorkspace(t, sup, entries, rec2)
	r.Activate(ws)
	waitExitRecord(t, sup, "job_1")
	time.Sleep(300 * time.Millisecond)
	if job, _ := ws.Jobs.Get("job_1"); job.Status() != jobs.Running || rec2.count(event.JobFinished) != 0 {
		t.Fatal("the survivor's outcome was recorded before the reopen marker")
	}
	r.ReleaseMarker()
	if rep := waitReport(t, ws.Jobs, "job_1"); rep.Status != jobs.Done {
		t.Fatalf("report = %+v", rep)
	}
}

// A teardown kill finalizes a re-attached job only in memory: its exit record
// is not consumed, so the next reopen still records the outcome.
func TestTeardownKillLeavesExitUnharvested(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	entries := restartEntries(rec.snapshot())
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	rec2 := &captureRec{}
	ws := reopenWorkspace(t, sup, entries, rec2)
	r.Activate(ws)
	r.ReleaseMarker()
	if err := ws.Jobs.KillAll(); err != nil {
		t.Fatal(err)
	}
	if rec2.count(event.JobFinished) != 0 || sup.Harvested("job_1") {
		t.Fatalf("finished=%d harvested=%t: an in-memory kill consumed the exit record", rec2.count(event.JobFinished), sup.Harvested("job_1"))
	}
	entries = restartEntries(append(rec.snapshot(), rec2.snapshot()...))
	ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	if e := entries[0]; e.Status != jobs.Failed || !strings.Contains(e.Result, "killed") {
		t.Fatalf("next reopen entry = %+v, want the kill recorded from the exit record", e)
	}
}
