package jobs

import (
	"context"
	"testing"
	"time"
)

// Reopen reconciliation restores a surviving supervised job as a live tracked
// job: running, undelivered, joined only once its runner confirms the exit,
// and woken on like any other completion.
func TestRestoredLiveJobIsTrackedRunning(t *testing.T) {
	r := NewRestored([]Restored{
		{ID: "job_3", Kind: "bash", Label: "long", Owner: "coordinator", Status: Running, Supervised: true, Live: true},
		{ID: "job_4", Kind: "bash", Label: "unwinding", Owner: "coordinator", Status: Killed, Result: "killed", Notified: true, ExecutionPending: true},
		{ID: "job_5", Kind: "bash", Label: "lost", Owner: "coordinator", Status: Lost, Result: "lost: supervision", ExecutionUnknown: true},
	})
	live, _ := r.Get("job_3")
	if live.Status() != Running || !live.ExecutionRunning() || !r.PendingContinuation("coordinator") {
		t.Fatalf("live job status=%s running=%t", live.Status(), live.ExecutionRunning())
	}
	if info := live.Info(); !info.Restored || info.OutputAvailable {
		t.Fatalf("info = %+v, want restored with no output source yet", info)
	}
	unwinding, _ := r.Get("job_4")
	if !unwinding.ExecutionRunning() || unwinding.ExecutionUnknown() {
		t.Fatal("a kill still unwinding was restored as stopped")
	}
	lost, _ := r.Get("job_5")
	if !lost.ExecutionUnknown() || !lost.ExecutionRunning() {
		t.Fatal("unknown execution was restored as complete")
	}
	if got := r.DrainFinished("coordinator"); len(got) != 1 || got[0].ID != "job_5" {
		t.Fatalf("drained = %+v, want only the undelivered loss", got)
	}
	if live.Finish(Done, "exit 0") != true {
		t.Fatal("live job could not finish")
	}
	if len(r.DrainFinished("coordinator")) != 0 {
		t.Fatal("a tracked job was delivered before its execution was confirmed")
	}
	live.ExecutionComplete()
	select {
	case <-r.Completions():
	case <-time.After(time.Second):
		t.Fatal("no completion wake after the confirmed exit")
	}
	if got := r.DrainFinished("coordinator"); len(got) != 1 || got[0].ID != "job_3" {
		t.Fatalf("drained = %+v", got)
	}
	unwinding.ExecutionComplete()
	if err := r.KillAll(); err == nil {
		t.Fatal("KillAll reported a clean join of unknown execution")
	}
}

// A graceful restart detaches the keeper's detachable running jobs (no
// terminal report, no join, no kill), and kills and joins every other job.
func TestSuspendForRestartDetachesKeeperJobs(t *testing.T) {
	r := NewRegistry()
	kept, _ := r.TryStartTracked("bash", "kept", "coordinator")
	detached := false
	kept.SetDetacher(func() bool { detached = true; return true })
	stopped := false
	sub, _ := r.TryStartTracked("bash", "sub", "implementer")
	sub.SetDetacher(func() bool { t.Error("a subagent's job was detached"); return true })
	sub.SetStopper(func() error { stopped = true; sub.ExecutionComplete(); return nil })
	plain, _ := r.TryStartTracked("bash", "plain", "coordinator")
	go func() { <-plain.Context().Done(); plain.ExecutionComplete() }()
	if err := r.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	if !detached || kept.Status() != Running {
		t.Fatalf("kept detached=%t status=%s", detached, kept.Status())
	}
	if !stopped || sub.Status() != Killed || plain.Status() != Killed {
		t.Fatalf("sub stopped=%t status=%s plain=%s", stopped, sub.Status(), plain.Status())
	}
	if !RestartSuspended(kept.Context()) {
		t.Fatal("kept job context lacks the restart cause")
	}
	// A later teardown leaves the detached job alone.
	done := make(chan error, 1)
	go func() { done <- r.KillAllCause(context.Canceled) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("teardown waited on a detached job")
	}
}

// A stop requested while the job's runner is still launching (no stopper
// installed yet) is honoured once the stopper arrives, so a teardown joining
// that execution is never left waiting on a command nobody stops.
func TestSetStopperHonoursPendingStop(t *testing.T) {
	r := NewRegistry()
	job, _ := r.TryStartTracked("bash", "launching", "coordinator")
	done := make(chan error, 1)
	go func() { done <- r.SuspendForRestart("coordinator") }()
	time.Sleep(50 * time.Millisecond)
	job.SetStopper(func() error { job.ExecutionComplete(); return nil })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("teardown never stopped the late-wired job")
	}
}

// A restored undelivered report waiting on its execution wakes its owner once
// the exit is confirmed or supervision is found lost.
func TestRestoredPendingReportWakesOwner(t *testing.T) {
	for _, outcome := range []string{"confirmed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			r := NewRestored([]Restored{{ID: "job_1", Kind: "bash", Owner: "coordinator", Status: Killed, ExecutionPending: true}})
			j, _ := r.Get("job_1")
			if !r.PendingContinuation("coordinator") || r.CompletionReady("coordinator") {
				t.Fatal("want an outstanding report blocked on execution")
			}
			if outcome == "confirmed" {
				j.ExecutionComplete()
			} else {
				j.MarkExecutionUnknown("the command may still be running")
			}
			select {
			case <-r.Completions():
			default:
				t.Fatal("the report became deliverable without a wake")
			}
			if got := r.DrainFinished("coordinator"); len(got) != 1 {
				t.Fatalf("drained %+v", got)
			}
		})
	}
}
