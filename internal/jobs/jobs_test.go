package jobs

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A job finished before a wait returns its report to the waiter exactly once;
// a later DrainFinished sees nothing (exactly-once consumption, design §3.3).
func TestWaitConsumesExactlyOnce(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	j := r.Start("bash", "echo hi", "coordinator")
	j.Finish(Done, "exit 0\nhi")

	reports, running := r.Wait(context.Background(), []string{j.ID()}, "all", time.Second)
	if len(running) != 0 {
		t.Fatalf("running = %v, want none", running)
	}
	if len(reports) != 1 || reports[0].Status != Done || reports[0].Result != "exit 0\nhi" {
		t.Fatalf("reports = %+v", reports)
	}
	// Already consumed by wait: the checkpoint drain must get nothing.
	if got := r.DrainFinished("coordinator"); len(got) != 0 {
		t.Fatalf("DrainFinished after wait = %+v, want none", got)
	}
}

// The reverse: a DrainFinished consumes the report, and a later wait covering
// the same job returns no report for it (exactly once, other direction).
func TestDrainThenWaitExactlyOnce(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	j := r.Start("bash", "echo hi", "coordinator")
	j.Finish(Done, "exit 0")

	if got := r.DrainFinished("coordinator"); len(got) != 1 {
		t.Fatalf("DrainFinished = %+v, want 1", got)
	}
	reports, running := r.Wait(context.Background(), []string{j.ID()}, "all", time.Second)
	if len(reports) != 0 {
		t.Fatalf("wait after drain returned reports %+v, want none", reports)
	}
	if len(running) != 0 {
		t.Fatalf("running = %v, want none (job is done, just already consumed)", running)
	}
}

// DrainFinished only injects jobs owned by the given actor.
func TestDrainFiltersByOwner(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	c := r.Start("bash", "coord job", "coordinator")
	i := r.Start("bash", "impl job", "implementer")
	c.Finish(Done, "exit 0")
	i.Finish(Done, "exit 0")

	got := r.DrainFinished("coordinator")
	if len(got) != 1 || got[0].ID != c.ID() {
		t.Fatalf("DrainFinished(coordinator) = %+v, want only %s", got, c.ID())
	}
	// The implementer job is still deliverable to its own owner.
	if got := r.DrainFinished("implementer"); len(got) != 1 || got[0].ID != i.ID() {
		t.Fatalf("DrainFinished(implementer) = %+v, want only %s", got, i.ID())
	}
}

// Concurrent Finish and Wait: exactly one of them delivers the report, and Wait
// unblocks once the job finishes.
func TestConcurrentFinishVsWait(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		r := NewRegistry()
		j := r.Start("bash", "race", "coordinator")

		var wg sync.WaitGroup
		wg.Add(1)
		var reports []Report
		go func() {
			defer wg.Done()
			reports, _ = r.Wait(context.Background(), []string{j.ID()}, "all", 2*time.Second)
		}()
		// Finish concurrently with the waiter blocking.
		j.Finish(Done, "exit 0")
		wg.Wait()

		gotWait := len(reports) == 1
		drained := r.DrainFinished("coordinator")
		gotDrain := len(drained) == 1
		// Exactly one path consumed the final report.
		if gotWait == gotDrain {
			t.Fatalf("iter %d: exactly-once violated: wait=%v drain=%v", iter, gotWait, gotDrain)
		}
		r.KillAll()
	}
}

// job_output (Read) returns only NEW output on each call and never consumes the
// final report.
func TestReadIncrementalCursor(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	j := r.Start("bash", "watch", "coordinator")

	j.Append([]byte("hello "))
	out, st := j.Read()
	if out != "hello " || st != Running {
		t.Fatalf("first Read = %q/%s", out, st)
	}
	// No new output: empty.
	if out, _ := j.Read(); out != "" {
		t.Fatalf("second Read = %q, want empty", out)
	}
	j.Append([]byte("world"))
	if out, _ := j.Read(); out != "world" {
		t.Fatalf("third Read = %q, want \"world\"", out)
	}
	j.Finish(Done, "exit 0")
	// Reading after finish reflects status but does not consume the report.
	if _, st := j.Read(); st != Done {
		t.Fatalf("post-finish Read status = %s, want done", st)
	}
	if got := r.DrainFinished("coordinator"); len(got) != 1 {
		t.Fatalf("report was consumed by Read: DrainFinished = %+v", got)
	}
}

// Kill sets status killed and finalizes; a later natural Finish is a no-op, so
// the killed status wins.
func TestKillWins(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	j := r.Start("bash", "sleep", "coordinator")
	if !j.Kill() {
		t.Fatal("Kill did not fire")
	}
	if j.Status() != Killed {
		t.Fatalf("status = %s, want killed", j.Status())
	}
	// The process goroutine's Finish loses the race and must be a no-op.
	if j.Finish(Done, "exit 0") {
		t.Fatal("Finish fired after Kill; killed status should be terminal")
	}
	if j.Status() != Killed {
		t.Fatalf("status changed after late Finish: %s", j.Status())
	}
}

// KillAll cancels the root context and finalizes all running jobs as killed.
func TestKillAll(t *testing.T) {
	r := NewRegistry()
	a := r.Start("bash", "a", "coordinator")
	b := r.Start("bash", "b", "coordinator")
	b.Finish(Done, "exit 0")

	r.KillAll()
	if a.Status() != Killed {
		t.Fatalf("a status = %s, want killed", a.Status())
	}
	// A job that already finished keeps its terminal status.
	if b.Status() != Done {
		t.Fatalf("b status = %s, want done (already finished)", b.Status())
	}
	// The root context is cancelled, so a job's context is done.
	select {
	case <-a.Context().Done():
	default:
		t.Fatal("job context not cancelled after KillAll")
	}
}

// wait for="any" returns as soon as one target finishes and reports the still
// running ones on timeout.
func TestWaitAnyAndTimeout(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	a := r.Start("bash", "a", "coordinator")
	b := r.Start("bash", "b", "coordinator")
	go func() {
		time.Sleep(20 * time.Millisecond)
		a.Finish(Done, "exit 0")
	}()
	reports, _ := r.Wait(context.Background(), nil, "any", time.Second)
	if len(reports) != 1 || reports[0].ID != a.ID() {
		t.Fatalf("any-wait reports = %+v, want only %s", reports, a.ID())
	}
	// b is still running: an all-wait with a short timeout reports it running.
	reports, running := r.Wait(context.Background(), []string{b.ID()}, "all", 20*time.Millisecond)
	if len(reports) != 0 || len(running) != 1 || running[0] != b.ID() {
		t.Fatalf("timeout wait = reports %+v running %v", reports, running)
	}
}
