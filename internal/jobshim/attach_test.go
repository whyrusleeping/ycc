package jobshim

import (
	"strings"
	"testing"
	"time"
)

// Detach leaves the supervisor running without deciding an outcome; a later
// Attach (another daemon) follows the same job to its exit record, and its
// exit is observed through the released lock even though the attaching
// process is not the supervisor's parent.
func TestDetachThenAttachFollowsToExit(t *testing.T) {
	s := testSession(t)
	h := launch(t, s, "job_1", "sleep 0.5; echo after-detach", 0)
	if !h.Detach() {
		t.Fatal("Detach of a running job reported an outcome")
	}
	if out := h.Outcome(); !out.Detached || out.Exit != nil || out.Unknown != "" {
		t.Fatalf("detached outcome = %+v", out)
	}
	a := s.Attach("job_1")
	out := waitOutcome(t, a)
	if out.Exit == nil || out.Exit.State != StateExited || out.Exit.ExitCode != 0 {
		t.Fatalf("attached outcome = %+v", out)
	}
	select {
	case <-a.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("attached handle never saw the supervisor exit")
	}
	data, _ := a.Reader().Retained()
	if !strings.Contains(string(data), "after-detach") {
		t.Fatalf("retained output = %q", data)
	}
	// Attaching after the exit decides at once from the record on disk.
	late := s.Attach("job_1")
	select {
	case <-late.Done():
	default:
		t.Fatal("attach to a finished job did not decide at once")
	}
	if late.Outcome().Exit == nil {
		t.Fatalf("late outcome = %+v", late.Outcome())
	}
}

// Detach after the outcome is known reports false and keeps that outcome.
func TestDetachAfterExitKeepsOutcome(t *testing.T) {
	s := testSession(t)
	h := launch(t, s, "job_1", "true", 0)
	waitOutcome(t, h)
	if h.Detach() {
		t.Fatal("Detach after the exit reported detached")
	}
	if h.Outcome().Exit == nil {
		t.Fatalf("outcome = %+v", h.Outcome())
	}
}
