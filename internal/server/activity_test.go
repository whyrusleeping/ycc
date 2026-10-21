package server

import (
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/sessionview"
)

func TestOverlayActivityResolvesOpenWork(t *testing.T) {
	done := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	agent := func() *sessionview.ActivityNode {
		return &sessionview.ActivityNode{ID: "agent:agent_1", Actor: "agent:agent_1", Status: "running", CurrentTool: "Bash",
			Spans: []sessionview.ActivitySpan{{JobID: "job_1", Status: "done"}, {JobID: "job_2", Status: "running"}}}
	}
	infos := map[string]jobs.Info{
		"job_2": {ID: "job_2", Status: jobs.Running},
		"job_3": {ID: "job_3", Status: jobs.Killed, Finished: done},
	}

	n := agent()
	overlayActivity(n, infos, true, time.Time{})
	if n.Status != "running" || n.CurrentTool != "Bash" {
		t.Fatalf("live running agent = %+v", n)
	}

	// The registry finalized the round before its finish event was indexed.
	infos["job_2"] = jobs.Info{ID: "job_2", Status: jobs.Killed, Finished: done}
	n = agent()
	overlayActivity(n, infos, true, time.Time{})
	if n.Status != "killed" || !n.Finished.Equal(done) || n.CurrentTool != "" {
		t.Fatalf("live killed agent = %+v", n)
	}

	// A persisted session's open round is lost at the session's last event.
	n = agent()
	overlayActivity(n, nil, false, done)
	if n.Status != "lost" || !n.Finished.Equal(done) || n.Spans[0].Status != "done" || n.CurrentTool != "" {
		t.Fatalf("persisted agent = %+v", n)
	}

	shell := &sessionview.ActivityNode{ID: "job_3", JobID: "job_3", Status: "running"}
	overlayActivity(shell, infos, true, time.Time{})
	if shell.Status != "killed" || !shell.Finished.Equal(done) {
		t.Fatalf("killed shell job = %+v", shell)
	}
}
