package tools

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/jobshim"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// passiveWorkspace is a closed session's worktree workspace for passive
// supervision, registered for shell overlap under scope s_test.
func passiveWorkspace(t *testing.T, sup *jobshim.Session, svc *workspacelease.Service) *Workspace {
	t.Helper()
	ws := &Workspace{Root: filepath.Dir(filepath.Dir(filepath.Dir(sup.Dir())))}
	gitWorkspace(t, ws, svc, "s_test")
	return ws
}

func shellRanBy(t *testing.T, svc *workspacelease.Service, root, scope string) bool {
	t.Helper()
	key, err := workspacelease.Canonical(root)
	if err != nil {
		t.Fatal(err)
	}
	other := svc.BeginShell(key, svc.NewScopedToken("s_other", "other"), true)
	return slices.Contains(svc.EndShell(other, time.Minute).Ran, scope)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A closed session's surviving coordinator job is held passively: it guards
// its worktree and counts as a running shell there, without recording
// anything. The session's reopen takes the held handle over, and the job's
// outcome then reaches the reopened session as if no restart happened.
func TestPassiveHoldsSurvivorUntilReopenTakesIt(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 2; echo survived","run_in_background":true}`)
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	logged := len(rec.snapshot())
	svc := workspacelease.NewService()
	pws := passiveWorkspace(t, sup, svc)
	p := NewPassiveJobs()
	if stopping := p.Supervise(sup, restartEntries(rec.snapshot()), "coordinator", pws); len(stopping) != 0 {
		t.Fatalf("stopped %d coordinator jobs", len(stopping))
	}
	if got := p.Held(sup.Dir()); !slices.Equal(got, []string{"job_1"}) {
		t.Fatalf("held = %v", got)
	}
	if why := p.Unsettled(sup.Dir()); !strings.Contains(why, "job_1") {
		t.Fatalf("unsettled = %q", why)
	}
	if !shellRanBy(t, svc, pws.Root, "s_test") {
		t.Fatal("the held job does not count as a running shell in its worktree")
	}
	if n := len(rec.snapshot()); n != logged {
		t.Fatalf("passive supervision recorded %d events", n-logged)
	}

	handles, end := p.Take(sup.Dir())
	if handles["job_1"] == nil || len(p.Held(sup.Dir())) != 0 || p.Unsettled(sup.Dir()) != "" {
		t.Fatalf("take: handles=%v held=%v", handles, p.Held(sup.Dir()))
	}
	entries := restartEntries(rec.snapshot())
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), handles)
	if !entries[0].Live || !slices.Equal(r.Surviving(), []string{"job_1"}) {
		t.Fatalf("entry = %+v surviving = %v", entries[0], r.Surviving())
	}
	ws := reopenWorkspace(t, sup, entries, &captureRec{})
	r.Activate(ws)
	end()
	r.ReleaseMarker()
	if rep := waitReport(t, ws.Jobs, "job_1"); rep.Status != jobs.Done || !strings.Contains(rep.Result, "survived") {
		t.Fatalf("report = %+v", rep)
	}
}

// A subagent's shell left running in a closed session is stopped at once: the
// caller joins the returned handle, and the job is dropped once its exit
// record confirms the stop.
func TestPassiveStopsSubagentShell(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	entries := restartEntries(rec.snapshot())
	entries[0].Owner = "implementer"
	p := NewPassiveJobs()
	stopping := p.Supervise(sup, entries, "coordinator", passiveWorkspace(t, sup, workspacelease.NewService()))
	if len(stopping) != 1 {
		t.Fatalf("stopping = %d handles", len(stopping))
	}
	select {
	case <-stopping[0].Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the subagent shell was not stopped")
	}
	if exit, err := jobshim.ReadExit(sup.JobDir("job_1")); err != nil || exit.State != jobshim.StateKilled {
		t.Fatalf("exit record = %+v, %v", exit, err)
	}
	waitUntil(t, "the stopped shell to be dropped", func() bool { return len(p.Held(sup.Dir())) == 0 })
}

// A job whose supervision is lost stays held, as unknown, until the exit
// record its supervisor leaves while still holding shim.lock confirms the
// stop. Exited and operator-cleared jobs are never held.
func TestPassiveLostSupervisionHeldUntilExit(t *testing.T) {
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
	p := NewPassiveJobs()
	p.Supervise(sup, restartEntries(rec.snapshot()), "coordinator", passiveWorkspace(t, sup, workspacelease.NewService()))
	if why := p.Unsettled(sup.Dir()); !strings.Contains(why, "lost supervision") {
		t.Fatalf("unsettled = %q", why)
	}
	_ = syscall.Kill(pid, syscall.SIGTERM) // the supervisor kills its child and writes exit.json
	waitUntil(t, "the confirmed job to be dropped", func() bool { return len(p.Held(sup.Dir())) == 0 })
	// A second scan finds its exit record and holds nothing.
	p.Supervise(sup, restartEntries(rec.snapshot()), "coordinator", passiveWorkspace(t, sup, workspacelease.NewService()))
	if got := p.Held(sup.Dir()); len(got) != 0 {
		t.Fatalf("held after exit = %v", got)
	}
}

// An operator's cleared marker settles a job without an exit record: it is
// not held, no longer blocks cleanup, and a reopen reports it lost without
// unknown execution.
func TestClearedJobIsSettled(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 3","run_in_background":true}`)
	pid := supervisorPID(t, sup.JobDir("job_1"))
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	// The supervisor dies without an exit record (SIGKILL leaves its child
	// running, so the operator stops the child and clears the job).
	_ = syscall.Kill(pid, syscall.SIGKILL)
	dir := sup.JobDir("job_1")
	waitUntil(t, "the dead supervisor's lock to be released", func() bool {
		return !strings.Contains(jobshim.SessionUnsettled(sup.Dir()), "still running")
	})
	if why := jobshim.SessionUnsettled(sup.Dir()); why == "" {
		t.Fatal("a job with no exit record was settled before it was cleared")
	}
	if err := jobshim.MarkCleared(dir, "operator: confirmed stopped"); err != nil {
		t.Fatal(err)
	}
	if why := jobshim.SessionUnsettled(sup.Dir()); why != "" {
		t.Fatalf("unsettled after clearing = %q", why)
	}
	if why := jobshim.SessionBusy(sup.Dir()); why != "" {
		t.Fatalf("busy after clearing = %q", why)
	}
	p := NewPassiveJobs()
	p.Supervise(sup, restartEntries(rec.snapshot()), "coordinator", passiveWorkspace(t, sup, workspacelease.NewService()))
	if got := p.Held(sup.Dir()); len(got) != 0 {
		t.Fatalf("held = %v", got)
	}
	entries := restartEntries(rec.snapshot())
	r := ReconcileSupervisedJobs(sup, entries, "coordinator", time.Now(), nil)
	if e := entries[0]; e.Status != jobs.Lost || e.ExecutionUnknown || e.Live {
		t.Fatalf("entry = %+v", e)
	}
	rec2 := &captureRec{}
	r.Activate(reopenWorkspace(t, sup, entries, rec2))
	if fin := rec2.find(event.JobFinished); fin == nil || fin.Data["execution_unknown"] != nil {
		t.Fatalf("job_finished = %+v", fin)
	}
}

// A held job whose supervisor died without an exit record stays held until
// an operator clears it, which retires the entry and its shell registration.
func TestPassiveHeldUnknownRetiredWhenCleared(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 3","run_in_background":true}`)
	pid := supervisorPID(t, sup.JobDir("job_1"))
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	svc := workspacelease.NewService()
	pws := passiveWorkspace(t, sup, svc)
	p := NewPassiveJobs()
	p.Supervise(sup, restartEntries(rec.snapshot()), "coordinator", pws)
	waitUntil(t, "the loss to be seen", func() bool { return strings.Contains(p.Unsettled(sup.Dir()), "lost supervision") })
	time.Sleep(300 * time.Millisecond)
	if got := p.Held(sup.Dir()); len(got) != 1 {
		t.Fatalf("held = %v, want the unknown job kept", got)
	}
	if err := jobshim.MarkCleared(sup.JobDir("job_1"), "operator"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the cleared job to be retired", func() bool { return len(p.Held(sup.Dir())) == 0 })
	if shellRanBy(t, svc, pws.Root, "s_test") {
		t.Fatal("the retired job still counts as a running shell")
	}
}

// An exit record that names another job confirms nothing about this one.
func TestForeignExitRecordIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "job_3")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "exit.json"), []byte(`{"version":1,"job_id":"job_9","state":"exited"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := jobshim.ReadExit(dir); err == nil {
		t.Fatal("a foreign exit record was accepted")
	}
}

// A cleanup that asks whether a cleared job is settled retires it at once, so
// no registration outlives the evidence the cleanup is about to remove.
func TestPassiveUnsettledRetiresClearedJob(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 3","run_in_background":true}`)
	pid := supervisorPID(t, sup.JobDir("job_1"))
	if err := jr.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	svc := workspacelease.NewService()
	pws := passiveWorkspace(t, sup, svc)
	p := NewPassiveJobs()
	p.Supervise(sup, restartEntries(rec.snapshot()), "coordinator", pws)
	waitUntil(t, "the loss to be seen", func() bool { return strings.Contains(p.Unsettled(sup.Dir()), "lost supervision") })
	if err := jobshim.MarkCleared(sup.JobDir("job_1"), "operator"); err != nil {
		t.Fatal(err)
	}
	if why := p.Unsettled(sup.Dir()); why != "" || len(p.Held(sup.Dir())) != 0 {
		t.Fatalf("unsettled=%q held=%v, want the cleared job retired", why, p.Held(sup.Dir()))
	}
	if shellRanBy(t, svc, pws.Root, "s_test") {
		t.Fatal("the retired job still counts as a running shell")
	}
}
