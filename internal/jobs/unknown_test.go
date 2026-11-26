package jobs

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type staticSource struct {
	data  []byte
	start int64
}

func (s staticSource) Retained() ([]byte, int64) { return s.data, s.start }

// An output source replaces the in-memory buffer for cursor, tail, and report
// reads, keeping absolute offsets and eviction gaps.
func TestOutputSourceBacksOutputAndTail(t *testing.T) {
	r := NewRegistry()
	defer r.KillAll()
	j := r.Start("bash", "seq", "coordinator")
	j.SetOutputSource(staticSource{data: []byte("8\n9\n10\n"), start: 100})
	out := j.Output(0, 4, 0)
	if string(out.Data) != "8\n9\n" || out.Start != 100 || out.GapStart != 0 || out.GapEnd != 100 || out.RetainedEnd != 107 {
		t.Fatalf("Output = %+v", out)
	}
	if tail := j.Output(0, 100, 1); string(tail.Data) != "10\n" {
		t.Fatalf("tail = %q", tail.Data)
	}
	if got := j.Tail(2); got != "9\n10" {
		t.Fatalf("Tail = %q", got)
	}
}

// Lost supervision settles teardown without claiming a join: execution keeps
// running, the report is deliverable once, KillAllCause and ResolveOwner name
// the job, and a later confirmed exit clears the unknown state.
func TestExecutionUnknownIsReportedNotJoined(t *testing.T) {
	r := NewRegistry()
	j, _ := r.TryStartTracked("bash", "sleep", "implementer")
	j.Finish(Lost, "lost: supervision lost")
	if r.CompletionReady("implementer") {
		t.Fatal("report deliverable before execution settled")
	}
	j.MarkExecutionUnknown("[supervision lost]")
	if got := j.Report().Result; got != "lost: supervision lost\n[supervision lost]" {
		t.Fatalf("undelivered report = %q, want the note appended", got)
	}
	select {
	case <-r.Completions():
	default:
		t.Fatal("unknown execution did not signal completion")
	}
	if !j.ExecutionUnknown() || !j.ExecutionRunning() || !j.Info().ExecutionUnknown {
		t.Fatal("unknown execution reported as stopped")
	}
	joined := make(chan struct{})
	go func() { j.WaitExecution(); close(joined) }()
	select {
	case <-joined:
		t.Fatal("WaitExecution claimed a join for unknown execution")
	case <-time.After(20 * time.Millisecond):
	}
	_, _, _, unjoined := r.ResolveOwner("implementer", "coordinator", nil)
	if len(unjoined) != 1 || unjoined[0] != j.ID() {
		t.Fatalf("ResolveOwner unjoined = %v", unjoined)
	}
	var ue *UnjoinedError
	if err := r.KillAllCause(nil); !errors.As(err, &ue) || len(ue.IDs) != 1 {
		t.Fatalf("KillAllCause = %v", err)
	}
	j.ExecutionComplete()
	<-joined
	if j.ExecutionUnknown() || j.ExecutionRunning() {
		t.Fatal("a confirmed exit did not clear the unknown state")
	}
	if err := r.KillAllCause(nil); err != nil {
		t.Fatalf("KillAllCause after confirmation = %v", err)
	}
}

// A report already delivered keeps its delivery: lost supervision neither
// rewrites it nor makes it deliverable a second time.
func TestExecutionUnknownAfterDeliveryIsNotRenotified(t *testing.T) {
	r := NewRegistry()
	j, _ := r.TryStartTracked("bash", "sleep", "coordinator")
	j.Kill()
	reports, _ := r.Wait(t.Context(), []string{j.ID()}, "all", time.Second)
	if len(reports) != 1 || !reports[0].ClaimedNotification {
		t.Fatalf("wait = %+v", reports)
	}
	j.MarkExecutionUnknown("[supervision lost]")
	if got := r.DrainFinished("coordinator"); len(got) != 0 {
		t.Fatalf("re-delivered %+v", got)
	}
	if strings.Contains(j.Report().Result, "supervision lost") {
		t.Fatal("delivered report was rewritten")
	}
	_ = r.KillAll()
}
