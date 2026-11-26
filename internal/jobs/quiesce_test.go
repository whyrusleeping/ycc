package jobs

import (
	"errors"
	"strings"
	"testing"
)

// Quiesce kills and joins running jobs and holds new registrations off until
// released, leaving the registry usable afterwards.
func TestQuiesceStopsJobsAndHoldsRegistration(t *testing.T) {
	r := NewRegistry()
	j, ok := r.TryStartTracked("bash", "sleep", "coordinator")
	if !ok {
		t.Fatal("start")
	}
	go func() { <-j.Context().Done(); j.ExecutionComplete() }()
	release, err := r.Quiesce("workstream w1 is being merged")
	if err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	if j.Status() != Killed || !strings.HasPrefix(j.Report().Result, "killed: workstream w1 is being merged") || j.ExecutionRunning() {
		t.Fatalf("job status=%s result=%q running=%t", j.Status(), j.Report().Result, j.ExecutionRunning())
	}
	if r.Start("bash", "late", "coordinator") != nil {
		t.Fatal("a job registered while quiesced")
	}
	release()
	release() // idempotent
	if r.Start("bash", "after", "coordinator") == nil {
		t.Fatal("registration stayed held after release")
	}
}

// A job whose stop cannot be confirmed makes Quiesce fail.
func TestQuiesceReportsUnjoined(t *testing.T) {
	r := NewRegistry()
	j, _ := r.TryStartTracked("bash", "sleep", "coordinator")
	go func() { <-j.Context().Done(); j.MarkExecutionUnknown("") }()
	release, err := r.Quiesce("merging")
	defer release()
	var u *UnjoinedError
	if !errors.As(err, &u) || len(u.IDs) != 1 || u.IDs[0] != j.ID() {
		t.Fatalf("Quiesce = %v, want job %s unjoined", err, j.ID())
	}
}
