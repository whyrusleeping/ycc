package engine

import (
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
)

// Jobs a reopen reconciled itself (survivors, downtime finishes, losses) get
// no lost-on-restart note; other unfinished jobs keep the legacy note, and
// RestoreJobEntries carries the supervised flag and artifact through.
func TestReplayHistoryReconciledSkipsReconciledJobs(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "run them"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "started"}},
		{Seq: 3, Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_1", "kind": "bash", "label": "supervised", "supervised": true, "artifact": "output_ab"}},
		{Seq: 4, Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_2", "kind": "bash", "label": "direct"}},
	}
	legacy := ReplayHistory(events)
	if last := legacy[len(legacy)-1].Content; !strings.Contains(last, "[job job_1 lost") || !strings.Contains(last, "[job job_2 lost") {
		t.Fatalf("legacy replay tail = %q", last)
	}
	got := ReplayHistoryReconciled(events, map[string]bool{"job_1": true})
	last := got[len(got)-1].Content
	if strings.Contains(last, "job_1") || !strings.Contains(last, "[job job_2 lost: daemon restarted]") {
		t.Fatalf("reconciled replay tail = %q", last)
	}
	entries := RestoreJobEntries(events)
	if len(entries) != 2 || !entries[0].Supervised || entries[0].Artifact != "output_ab" || entries[0].Status != jobs.Running || entries[1].Supervised {
		t.Fatalf("entries = %+v", entries)
	}
	entries[0].Live = true
	r := FinalizeRestoredJobs(entries, events)
	if j, _ := r.Get("job_1"); j.Status() != jobs.Running {
		t.Fatalf("a live entry was finalized as %s", j.Status())
	}
	if j, _ := r.Get("job_2"); j.Status() != jobs.Lost {
		t.Fatalf("an unreconciled direct job restored as %s", j.Status())
	}
	// The live entry has no runner here, so the registry is not torn down.
}
