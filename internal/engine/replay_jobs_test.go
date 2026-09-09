package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
)

// A checkpoint-injected job notification (job_notified, recorded as a user-actor
// event) replays as a user message at its position, keeping user/assistant
// alternation — the same rule as a steer correction.
func TestReplayJobNotifiedAsUserMessage(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "build it"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "starting build"}},
		{Seq: 3, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"name": "Bash", "args": "{}", "id": "c1"}},
		{Seq: 4, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "c1", "result": "started job_1"}},
		{Seq: 5, Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_1", "kind": "bash", "label": "go build ./..."}},
		{Seq: 6, Actor: "coordinator", Type: event.JobFinished, Data: map[string]any{"id": "job_1", "status": "done", "tail": "exit 0"}},
		// Injected at the checkpoint after the tool result, before the next turn.
		{Seq: 7, Actor: "user", Type: event.JobNotified, Data: map[string]any{"id": "job_1", "text": "[job job_1 done] go build ./...\nexit 0"}},
		{Seq: 8, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "build passed"}},
	}

	got := ReplayHistory(events)
	want := []gollama.Message{
		{Role: "user", Content: "build it"},
		{Role: "assistant", Content: "starting build", ToolCalls: []gollama.ToolCall{{ID: "c1", Type: "function", Function: gollama.ToolCallFunction{Name: "Bash", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "c1", Content: "started job_1"},
		{Role: "user", Content: "[job job_1 done] go build ./...\nexit 0"},
		{Role: "assistant", Content: "build passed"},
	}
	if len(got) != len(want) {
		t.Fatalf("history len = %d, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Fatalf("msg[%d] = {%s %q}, want {%s %q}", i, got[i].Role, got[i].Content, want[i].Role, want[i].Content)
		}
	}
}

// A job notification recorded at a MID-BATCH checkpoint — between two tool
// results of the SAME multi-tool-call assistant turn (the log interleaves
// tool_call/tool_result per dispatch, so the second tool_call appears AFTER the
// job_notified event) — replays AFTER the batch's last tool result, matching
// the live loop's deferred Post. Injecting it in place would orphan the later
// tool calls and split the tool_result blocks Anthropic requires immediately
// after their tool_use message.
func TestReplayJobNotifiedMidBatchDeferred(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "build it"}},
		// The turn's total tool-call count is recorded on model_turn; replay uses
		// it to know the batch is still open when the notification arrives.
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "two reads", "tool_calls": 2}},
		{Seq: 3, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"name": "read_file", "args": "{}", "id": "c1"}},
		{Seq: 4, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "c1", "result": "one"}},
		// Mid-batch checkpoint: the finished job's report is recorded here, but
		// the turn still owes tool_call/tool_result c2.
		{Seq: 5, Actor: "user", Type: event.JobNotified, Data: map[string]any{"id": "job_1", "text": "[job job_1 done] build ok"}},
		{Seq: 6, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"name": "read_file", "args": "{}", "id": "c2"}},
		{Seq: 7, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "c2", "result": "two"}},
		{Seq: 8, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "done"}},
	}

	got := ReplayHistory(events)
	want := []gollama.Message{
		{Role: "user", Content: "build it"},
		{Role: "assistant", Content: "two reads"},
		{Role: "tool", ToolCallID: "c1", Content: "one"},
		{Role: "tool", ToolCallID: "c2", Content: "two"},
		{Role: "user", Content: "[job job_1 done] build ok"},
		{Role: "assistant", Content: "done"},
	}
	if len(got) != len(want) {
		t.Fatalf("history len = %d, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Fatalf("msg[%d] = {%s %q}, want {%s %q}", i, got[i].Role, got[i].Content, want[i].Role, want[i].Content)
		}
		if want[i].ToolCallID != "" && got[i].ToolCallID != want[i].ToolCallID {
			t.Fatalf("msg[%d] tool_call_id = %q, want %q", i, got[i].ToolCallID, want[i].ToolCallID)
		}
	}
	// Both tool calls must be attached to the assistant turn (neither orphaned).
	if n := len(got[1].ToolCalls); n != 2 {
		t.Fatalf("assistant turn has %d tool calls, want 2", n)
	}
	// No synthetic repair results should have been inserted.
	for _, m := range got {
		if strings.Contains(m.Content, "no result recorded") {
			t.Fatalf("synthetic repair leaked into history: %+v", got)
		}
	}
}

// A job that started but whose finish was never recorded (daemon restart mid
// flight) gets a synthesized "(job lost)" note so histories stay valid.
func TestReplayLostJobSynthesized(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "run tests"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "kicking off"}},
		{Seq: 3, Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_1", "kind": "bash", "label": "go test ./..."}},
		// No job_finished / job_notified: the daemon died with job_1 in flight.
	}
	got := ReplayHistory(events)
	last := got[len(got)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "job_1 lost") {
		t.Fatalf("last message = {%s %q}, want a user lost-job note", last.Role, last.Content)
	}
	if !strings.Contains(last.Content, "go test ./...") {
		t.Fatalf("lost-job note missing label: %q", last.Content)
	}
}

// A finished job (job_finished recorded, e.g. consumed by wait) is NOT treated
// as lost — no synthesized note.
func TestRestoreJobsRetainsResultsOwnersAndReservesIDs(t *testing.T) {
	start := time.Now().Add(-2 * time.Second)
	events := []event.Event{
		{Seq: 1, TS: start, Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_4", "kind": "agent", "label": "unfinished", "mutates": true}},
		{Seq: 2, TS: start.Add(time.Second), Actor: "implementer", Type: event.JobStarted, Data: map[string]any{"id": "job_7", "kind": "bash", "label": "cancelled"}},
		{Seq: 3, TS: start.Add(1500 * time.Millisecond), Actor: "implementer", Type: event.JobFinished, Data: map[string]any{"id": "job_7", "status": "killed", "tail": "killed by user"}},
		{Seq: 4, TS: start.Add(1600 * time.Millisecond), Actor: "implementer", Type: event.JobClaimed, Data: map[string]any{"id": "job_7", "reason": "wait"}},
	}
	r := RestoreJobs(events)
	defer r.KillAll()
	all := r.List("coordinator", true)
	if len(all) != 2 || all[0].ID != "job_4" || all[0].Status != jobs.Lost || all[0].Owner != "coordinator" || !all[0].Mutates {
		t.Fatalf("restored jobs = %+v", all)
	}
	if duplicate := r.DrainFinished("coordinator"); len(duplicate) != 0 {
		t.Fatalf("restored lost job duplicated ReplayHistory's restart note: %+v", duplicate)
	}
	cancelled, ok := r.Get("job_7")
	if !ok || cancelled.Status() != jobs.Killed || cancelled.Report().Result != "killed by user" {
		t.Fatalf("restored cancelled job = %+v ok=%v", cancelled, ok)
	}
	if duplicate := r.DrainFinished("implementer"); len(duplicate) != 0 {
		t.Fatalf("restored wait claim duplicated notification: %+v", duplicate)
	}
	if next := r.Start("bash", "new", "coordinator"); next.ID() != "job_8" {
		t.Fatalf("next restored id = %s, want job_8", next.ID())
	}
}

func TestRestoreJobsPreservesExplicitHandoffOwnerAndPolicy(t *testing.T) {
	start := time.Now().Add(-time.Second)
	events := []event.Event{
		{Seq: 1, TS: start, Actor: "implementer", Type: event.JobStarted,
			Data: map[string]any{"id": "job_1", "kind": "bash", "label": "watch", "mutates": true}},
		{Seq: 2, TS: start.Add(time.Millisecond), Actor: "coordinator", Type: event.JobHandedOff,
			Data: map[string]any{"id": "job_1", "owner": "coordinator", "purpose": "watch deploy", "delivery": jobs.ParentCheckpointDelivery}},
		{Seq: 3, TS: start.Add(2 * time.Millisecond), Actor: "implementer", Type: event.JobFinished,
			Data: map[string]any{"id": "job_1", "kind": "bash", "status": "done", "tail": "exit 0"}},
	}
	r := RestoreJobs(events)
	defer r.KillAll()
	info := r.List("coordinator", false)
	if len(info) != 1 || info[0].Owner != "coordinator" || info[0].Purpose != "watch deploy" ||
		info[0].Delivery != jobs.ParentCheckpointDelivery {
		t.Fatalf("restored handoff = %+v", info)
	}
	if reports := r.DrainFinished("coordinator"); len(reports) != 1 || reports[0].ID != "job_1" {
		t.Fatalf("restored handoff was not delivered to parent: %+v", reports)
	}
}

func TestRestoreTerminalClaimOrNotificationWithoutJobFinished(t *testing.T) {
	start := time.Now().Add(-time.Second)
	for _, tc := range []struct {
		name       string
		eventType  event.Type
		actor      string
		status     jobs.Status
		result     string
		notifyText string
	}{
		{name: "checkpoint notification", eventType: event.JobNotified, actor: "user", status: jobs.Done,
			result: "exit 0\nok", notifyText: "[job job_1 done] build\nexit 0\nok"},
		{name: "wait claim", eventType: event.JobClaimed, actor: "coordinator", status: jobs.Failed,
			result: "exit 1\nfailed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]any{"id": "job_1", "kind": "bash", "label": "build",
				"status": string(tc.status), "result": tc.result}
			if tc.notifyText != "" {
				data["text"] = tc.notifyText
			}
			events := []event.Event{
				{Seq: 1, TS: start, Actor: "coordinator", Type: event.JobStarted,
					Data: map[string]any{"id": "job_1", "kind": "bash", "label": "build"}},
				{Seq: 2, TS: start.Add(500 * time.Millisecond), Actor: tc.actor, Type: tc.eventType, Data: data},
			}

			for _, msg := range ReplayHistory(events) {
				if strings.Contains(msg.Content, "lost: daemon restarted") {
					t.Fatalf("durably claimed terminal job replayed as lost: %+v", msg)
				}
			}
			r := RestoreJobs(events)
			defer r.KillAll()
			job, ok := r.Get("job_1")
			if !ok || job.Status() != tc.status {
				t.Fatalf("restored terminal job = %#v ok=%v", job, ok)
			}
			first, second := job.Report(), job.Report()
			if first.Result != tc.result || second != first {
				t.Fatalf("repeatable result = %+v then %+v, want %q", first, second, tc.result)
			}
			if duplicate := r.DrainFinished("coordinator"); len(duplicate) != 0 {
				t.Fatalf("restored claim duplicated notification: %+v", duplicate)
			}
		})
	}
}

func TestRestoreTerminalFinishAndClaimEitherOrder(t *testing.T) {
	start := time.Now().Add(-time.Second)
	claim := event.Event{TS: start.Add(500 * time.Millisecond), Actor: "coordinator", Type: event.JobClaimed,
		Data: map[string]any{"id": "job_1", "kind": "bash", "label": "build", "status": "killed", "result": "killed", "reason": "wait"}}
	finish := event.Event{TS: start.Add(600 * time.Millisecond), Actor: "coordinator", Type: event.JobFinished,
		Data: map[string]any{"id": "job_1", "kind": "bash", "label": "build", "status": "killed", "tail": "killed"}}
	for _, tc := range []struct {
		name string
		tail []event.Event
	}{
		{name: "claim then finish", tail: []event.Event{claim, finish}},
		{name: "finish then claim", tail: []event.Event{finish, claim}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []event.Event{{TS: start, Actor: "coordinator", Type: event.JobStarted,
				Data: map[string]any{"id": "job_1", "kind": "bash", "label": "build"}}}
			events = append(events, tc.tail...)
			r := RestoreJobs(events)
			defer r.KillAll()
			job, ok := r.Get("job_1")
			if !ok || job.Status() != jobs.Killed || job.Report().Result != "killed" {
				t.Fatalf("restored terminal job = %#v ok=%v", job, ok)
			}
			info := r.List("coordinator", false)[0]
			if !info.Finished.Equal(finish.TS) {
				t.Fatalf("finished time = %s, want durable job_finished time %s", info.Finished, finish.TS)
			}
			if duplicate := r.DrainFinished("coordinator"); len(duplicate) != 0 {
				t.Fatalf("restored claim duplicated notification: %+v", duplicate)
			}
		})
	}
}

func TestRestoreLegacyNotificationTextAsResult(t *testing.T) {
	events := []event.Event{
		{Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_1", "kind": "bash", "label": "build"}},
		{Actor: "user", Type: event.JobNotified, Data: map[string]any{"id": "job_1", "kind": "bash", "label": "build", "status": "done",
			"text": "[job job_1 done] build\nexit 0\nlegacy output"}},
	}
	r := RestoreJobs(events)
	defer r.KillAll()
	job, _ := r.Get("job_1")
	if got := job.Report().Result; got != "exit 0\nlegacy output" {
		t.Fatalf("legacy notification result = %q", got)
	}
}

func TestRestoreFinishedUnnotifiedJobKeepsPendingNotification(t *testing.T) {
	events := []event.Event{
		{Seq: 1, TS: time.Now().Add(-time.Second), Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_1", "kind": "bash", "label": "done"}},
		{Seq: 2, TS: time.Now(), Actor: "coordinator", Type: event.JobFinished, Data: map[string]any{"id": "job_1", "status": "done", "tail": "exit 0"}},
	}
	r := RestoreJobs(events)
	defer r.KillAll()
	notes := r.DrainFinished("coordinator")
	if len(notes) != 1 || notes[0].Result != "exit 0" {
		t.Fatalf("pending restored notification = %+v", notes)
	}
	if job, _ := r.Get("job_1"); job.Report().Result != "exit 0" {
		t.Fatalf("notification destroyed restored evidence: %+v", job.Report())
	}
}

func TestReplayFinishedJobNotLost(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "run tests"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "kicking off"}},
		{Seq: 3, Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_1", "kind": "bash", "label": "go test ./..."}},
		{Seq: 4, Actor: "coordinator", Type: event.JobFinished, Data: map[string]any{"id": "job_1", "status": "done", "tail": "exit 0"}},
	}
	got := ReplayHistory(events)
	for _, m := range got {
		if strings.Contains(m.Content, "lost") {
			t.Fatalf("finished job wrongly synthesized a lost note: %+v", got)
		}
	}
}
