package session

import (
	"context"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// A finished background job the model never waited on gets its final report
// injected at the next Checkpoint as a user-role message AND recorded as a
// user-actor job_notified event, so reopen replays the identical history
// (docs/design/async-jobs.md §3.3).
func TestCheckpointInjectsFinishedJob(t *testing.T) {
	s, rec := newSteerSession()
	jr := jobs.NewRegistry()
	defer jr.KillAll()
	s.deps = &orchestrator.Deps{Jobs: jr, Emitter: s.emitter}

	j := jr.Start("bash", "go test ./...", "coordinator")
	j.Finish(jobs.Done, "exit 0\nok")

	msgs, err := s.Checkpoint(context.Background())
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], "job_1") || !strings.Contains(msgs[0], "exit 0") {
		t.Fatalf("checkpoint msgs = %v, want one job note with exit 0", msgs)
	}

	notif := lastEvent(rec, event.JobNotified)
	if notif == nil {
		t.Fatal("no job_notified event recorded")
	}
	if notif.Actor != "user" {
		t.Fatalf("job_notified actor = %q, want user", notif.Actor)
	}
	if notif.Data["id"] != "job_1" || notif.Data["status"] != "done" {
		t.Fatalf("job_notified data = %+v", notif.Data)
	}

	// Exactly-once: a second checkpoint injects nothing (already consumed).
	if got, err := s.Checkpoint(context.Background()); err != nil || got != nil {
		t.Fatalf("second Checkpoint = (%v, %v), want (nil, nil)", got, err)
	}
}

// A still-running job is NOT injected at a checkpoint.
func TestCheckpointSkipsRunningJob(t *testing.T) {
	s, _ := newSteerSession()
	jr := jobs.NewRegistry()
	defer jr.KillAll()
	s.deps = &orchestrator.Deps{Jobs: jr, Emitter: s.emitter}
	jr.Start("bash", "watch", "coordinator")

	if got, err := s.Checkpoint(context.Background()); err != nil || got != nil {
		t.Fatalf("Checkpoint with running job = (%v, %v), want (nil, nil)", got, err)
	}
}
