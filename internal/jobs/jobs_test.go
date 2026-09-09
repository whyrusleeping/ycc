package jobs

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// An explicit wait returns retained evidence repeatably while suppressing a
// later automatic checkpoint notification.
func TestWaitIsRepeatableAndSuppressesNotification(t *testing.T) {
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
	if again, _ := r.Wait(context.Background(), []string{j.ID()}, "all", time.Second); len(again) != 1 || again[0].Result != reports[0].Result {
		t.Fatalf("repeat wait = %+v, want same retained report", again)
	}
	if got := r.DrainFinished("coordinator"); len(got) != 0 {
		t.Fatalf("DrainFinished after wait = %+v, want none", got)
	}
}

// A checkpoint notification does not destroy retained evidence: explicit waits
// after delivery can still retrieve it, without creating another notification.
func TestDrainThenWaitRetainsEvidence(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	j := r.Start("bash", "echo hi", "coordinator")
	j.Finish(Done, "exit 0")

	if got := r.DrainFinished("coordinator"); len(got) != 1 {
		t.Fatalf("DrainFinished = %+v, want 1", got)
	}
	reports, running := r.Wait(context.Background(), []string{j.ID()}, "all", time.Second)
	if len(reports) != 1 || reports[0].Result != "exit 0" {
		t.Fatalf("wait after drain returned reports %+v, want retained evidence", reports)
	}
	if len(running) != 0 {
		t.Fatalf("running = %v, want none", running)
	}
	if got := r.DrainFinished("coordinator"); len(got) != 0 {
		t.Fatalf("second DrainFinished = %+v, want no duplicate notification", got)
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

// Concurrent checkpoint claiming and wait preserve repeatable explicit evidence
// while allowing at most one automatic notification.
func TestConcurrentCheckpointVsWait(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		r := NewRegistry()
		j := r.Start("bash", "race", "coordinator")
		j.Finish(Done, "exit 0")

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var reports, notified []Report
		go func() {
			defer wg.Done()
			<-start
			reports, _ = r.Wait(context.Background(), []string{j.ID()}, "all", 2*time.Second)
		}()
		go func() {
			defer wg.Done()
			<-start
			notified = r.DrainFinished("coordinator")
		}()
		close(start)
		wg.Wait()

		if len(reports) != 1 || reports[0].Result != "exit 0" {
			t.Fatalf("iter %d: wait lost retained evidence: %+v", iter, reports)
		}
		if len(notified) > 1 {
			t.Fatalf("iter %d: duplicate notifications: %+v", iter, notified)
		}
		if duplicate := r.DrainFinished("coordinator"); len(duplicate) != 0 {
			t.Fatalf("iter %d: duplicate automatic notification: %+v", iter, duplicate)
		}
		r.KillAll()
	}
}

// Output uses explicit absolute cursors and can revisit retained bytes without
// changing notification delivery.
func TestOutputExplicitCursorIsRepeatable(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	j := r.Start("bash", "watch", "coordinator")

	j.Append([]byte("hello world"))
	first := j.Output(0, 6, 0)
	again := j.Output(0, 6, 0)
	if string(first.Data) != "hello " || string(again.Data) != "hello " || first.End != 6 {
		t.Fatalf("repeat output = %+v / %+v", first, again)
	}
	second := j.Output(first.End, 64, 0)
	if string(second.Data) != "world" || second.Start != 6 || second.End != 11 {
		t.Fatalf("continued output = %+v", second)
	}
	j.Finish(Done, "exit 0")
	if got := r.DrainFinished("coordinator"); len(got) != 1 {
		t.Fatalf("output read changed notification: %+v", got)
	}
}

func TestReadReportsIncrementalBufferEviction(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	j := r.Start("bash", "chatty", "coordinator")
	j.Append([]byte(strings.Repeat("x", maxJobBuf+123)))
	out := j.Output(0, maxJobBuf, 0)
	if out.GapStart != 0 || out.GapEnd != 123 || out.RetainedStart != 123 || len(out.Data) != maxJobBuf {
		t.Fatalf("eviction view = %+v, retained %d; want gap 0-123 and %d bytes", out, len(out.Data), maxJobBuf)
	}
}

func TestTailRemainsBoundedAfterInvalidUTF8Normalization(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	j := r.Start("bash", "invalid", "coordinator")
	data := make([]byte, maxJobReportBytes)
	for i := range data {
		if i%2 == 0 {
			data[i] = 0xff
		} else {
			data[i] = 'x'
		}
	}
	j.Append(data)
	tail := j.Tail(20)
	if len(tail) > maxJobReportBytes {
		t.Fatalf("normalized tail = %d bytes, cap %d", len(tail), maxJobReportBytes)
	}
	if !strings.Contains(tail, "job report tail truncated") {
		t.Fatalf("normalization expansion was not reported as truncation: %q", tail[:100])
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

func TestConcurrentTrackedStartAndShutdownCannotOrphanRunner(t *testing.T) {
	for i := 0; i < 100; i++ {
		r := NewRegistry()
		start := make(chan struct{})
		started := make(chan *Job, 1)
		cancelled := make(chan struct{}, 1)
		release := make(chan struct{})
		shutdown := make(chan struct{})
		go func() {
			<-start
			job, ok := r.TryStartMutatingTracked("bash", "race", "implementer")
			if !ok {
				started <- nil
				return
			}
			started <- job
			go func() {
				<-job.Context().Done()
				cancelled <- struct{}{}
				<-release
				job.ExecutionComplete()
			}()
		}()
		go func() {
			<-start
			r.KillAll()
			close(shutdown)
		}()
		close(start)
		job := <-started
		if job == nil {
			select {
			case <-shutdown:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not complete after declining concurrent start")
			}
			continue
		}
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("accepted concurrent runner was not cancelled")
		}
		select {
		case <-shutdown:
			t.Fatal("shutdown missed accepted tracked runner")
		case <-time.After(time.Millisecond):
		}
		close(release)
		select {
		case <-shutdown:
		case <-time.After(time.Second):
			t.Fatal("shutdown did not join accepted tracked runner")
		}
	}
}

func TestTrackedStartDeclinesAfterShutdownBarrier(t *testing.T) {
	r := NewRegistry()
	r.KillAll()
	if job, ok := r.TryStartTracked("agent", "late", "coordinator"); ok || job != nil {
		t.Fatalf("tracked start after shutdown = %#v, %v", job, ok)
	}
	if job, ok := r.TryStartMutatingTracked("bash", "late", "coordinator"); ok || job != nil {
		t.Fatalf("mutating tracked start after shutdown = %#v, %v", job, ok)
	}
}

// wait for="any" returns as soon as one target finishes and reports the still
// running ones on timeout.
func TestListIncludesOwnersTimingAndSafeAgentActivity(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	a := r.Start("agent", "review", "coordinator")
	r.Start("bash", "echo child", "implementer")
	a.UpdateActivity("Read", &Usage{Input: 10, Output: 4, Total: 14})

	own := r.List("coordinator", false)
	if len(own) != 1 || own[0].ID != a.ID() || own[0].Owner != "coordinator" || own[0].Started.IsZero() {
		t.Fatalf("owner-scoped list = %+v", own)
	}
	if own[0].Activity.CurrentTool != "Read" || own[0].Activity.Turns != 1 || own[0].Activity.Usage.Total != 14 {
		t.Fatalf("agent activity = %+v", own[0].Activity)
	}
	if all := r.List("coordinator", true); len(all) != 2 || all[1].Owner != "implementer" {
		t.Fatalf("session-wide list = %+v", all)
	}
}

func TestRestoredLostAndKilledJobsReserveIDs(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	r := NewRestored([]Restored{
		{ID: "job_7", Kind: "agent", Label: "lost", Owner: "coordinator", Status: Running, Started: start},
		{ID: "job_8", Kind: "bash", Label: "cancelled", Owner: "implementer", Status: Killed, Result: "killed", Started: start, Finished: start.Add(time.Second)},
	})
	defer r.KillAll()
	lost, _ := r.Get("job_7")
	if lost.Status() != Lost || !strings.Contains(lost.Report().Result, "daemon restarted") {
		t.Fatalf("restored unmatched job = %+v", lost.Report())
	}
	killed, _ := r.Get("job_8")
	if killed.Status() != Killed || killed.Report().Result != "killed" {
		t.Fatalf("restored killed job = %+v", killed.Report())
	}
	if got := r.DrainFinished("coordinator"); len(got) != 0 {
		t.Fatalf("restore duplicated replay notification: %+v", got)
	}
	if next := r.Start("bash", "next", "coordinator"); next.ID() != "job_9" {
		t.Fatalf("next id = %s, want job_9", next.ID())
	}
}

func TestResolveOwnerAccountsFinishedAndJoinsCancelledExecution(t *testing.T) {
	r := NewRegistry()
	finished, ok := r.TryStartTracked("bash", "failed check", "implementer")
	if !ok {
		t.Fatal("tracked start refused before shutdown")
	}
	finished.Finish(Failed, "exit 7")
	finished.ExecutionComplete()

	running, ok := r.TryStartMutatingTracked("bash", "watch", "implementer")
	if !ok {
		t.Fatal("mutating tracked start refused before shutdown")
	}
	cancelled := make(chan struct{})
	release := make(chan struct{})
	go func() {
		<-running.Context().Done()
		close(cancelled)
		<-release
		running.ExecutionComplete()
	}()

	type outcome struct {
		reports  []Report
		handoffs []Handoff
		rejected []string
	}
	resolved := make(chan outcome, 1)
	go func() {
		reports, handoffs, rejected := r.ResolveOwner("implementer", "coordinator", nil)
		resolved <- outcome{reports, handoffs, rejected}
	}()
	<-cancelled
	select {
	case <-resolved:
		t.Fatal("owner cleanup returned before tracked execution stopped")
	case <-time.After(20 * time.Millisecond):
	}
	if live := r.LiveMutating(); live != running {
		t.Fatalf("terminal-but-executing mutation was released: %#v", live)
	}
	close(release)
	got := <-resolved
	if len(got.reports) != 2 || got.reports[0].ID != finished.ID() || got.reports[0].Status != Failed ||
		got.reports[1].ID != running.ID() || got.reports[1].Status != Killed {
		t.Fatalf("resolved reports = %+v", got.reports)
	}
	if len(got.handoffs) != 0 || len(got.rejected) != 0 {
		t.Fatalf("unexpected handoff result: %+v %+v", got.handoffs, got.rejected)
	}
	if live := r.LiveMutating(); live != nil {
		t.Fatalf("mutation remained live after execution stop: %s", live.ID())
	}
	if duplicate := r.DrainFinished("implementer"); len(duplicate) != 0 {
		t.Fatalf("resolved reports notified twice: %+v", duplicate)
	}
	if rep := running.Report(); rep.Status != Killed {
		t.Fatalf("repeatable retained result lost: %+v", rep)
	}
}

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
