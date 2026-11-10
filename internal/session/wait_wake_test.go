package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// A user message sent while the coordinator blocks in wait cuts the wait short:
// it returns with the job still running and a note, the job keeps running, and
// the message is delivered at the next checkpoint. A message already queued
// when wait starts makes it return immediately.
func TestUserInputInterruptsCoordinatorWait(t *testing.T) {
	s, _ := newSteerSession()
	s.setRunning(true)
	jr := jobs.NewRegistry()
	t.Cleanup(jr.KillAll)
	job := jr.Start("bash", "sleep", "coordinator")
	reg := tools.New()
	reg.Add(tools.JobTools(&tools.Workspace{Root: t.TempDir(), Jobs: jr, Emitter: s.emitter, InputWake: s.inputWake})...)
	wait := func() *gollama.ToolResult {
		return reg.Dispatch(context.Background(), gollama.ToolCall{ID: "w", Type: "function",
			Function: gollama.ToolCallFunction{Name: "wait", Arguments: `{"job_ids":["job_1"],"timeout_s":30}`}})
	}
	assertInterrupted := func(res *gollama.ToolResult) {
		t.Helper()
		if res.IsError || !strings.Contains(res.Content, "user sent a message") || !strings.Contains(res.Content, "job_1") {
			t.Fatalf("wait result = %q (err=%v), want an early return naming the running job", res.Content, res.IsError)
		}
		if job.Status() != jobs.Running {
			t.Fatalf("job status = %v, want still running", job.Status())
		}
	}

	done := make(chan *gollama.ToolResult, 1)
	go func() { done <- wait() }()
	// Send only once wait is blocked on the wake channel.
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.steerMu.Lock()
		armed := s.inputWakeCh != nil
		s.steerMu.Unlock()
		if armed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wait never armed the input wake")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.SendInput("stop, do X instead"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	select {
	case res := <-done:
		assertInterrupted(res)
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after a user message")
	}
	if msgs, err := s.Checkpoint(context.Background()); err != nil || len(msgs) != 1 || msgs[0] != "stop, do X instead" {
		t.Fatalf("Checkpoint = (%v, %v), want the user message", msgs, err)
	}

	if err := s.SendInput("and also Y"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	start := time.Now()
	assertInterrupted(wait())
	if time.Since(start) > 2*time.Second {
		t.Fatalf("wait with input already queued took %s", time.Since(start))
	}
}
