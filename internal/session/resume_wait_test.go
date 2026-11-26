package session

import (
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobshim"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// restartWaitingSession starts a session whose coordinator runs the scripted
// supervised background jobs and then blocks in wait, and suspends the daemon
// for restart while it waits.
func restartWaitingSession(t *testing.T, steps []*[2]string) (*Manager, string, string, *jobshim.Launcher) {
	t.Helper()
	launcher := supervisedTestLauncher(t)
	m, ws := shutdownTestManager(t, steps)
	m.SetJobLauncher(launcher)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return lastCoordinatorToolCall(s, "wait") && len(eventsOf(s, event.WaitStarted)) == 1 })
	m.ShutdownForRestart()
	return m, ws, s.ID, launcher
}

// appendToLog records ev types into a closed session log, as a crashed or
// suspended daemon could have left them.
func appendToLog(t *testing.T, ws, id string, add func(*event.Log)) {
	t.Helper()
	log, err := event.OpenLog(ws + "/.ycc/sessions/" + id + "/events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	add(log)
	log.Close()
}

func restoredWaitResult(evs []event.Event) (int, *event.Event) {
	for i := range evs {
		if evs[i].Type == event.ToolResult && evs[i].Data["name"] == "wait" && boolVal(evs[i].Data, "restored") {
			return i, &evs[i]
		}
	}
	return -1, nil
}

// A coordinator blocked in wait on a supervised job across a graceful restart
// resumes that wait on reopen: the reopen is marked wait_restored, the job's
// completion is the wait's restored tool_result (claimed once, never also
// notified), and the turn continues from it.
func TestRestartRestoresWaitOnSurvivingJob(t *testing.T) {
	m, _, id, launcher := restartWaitingSession(t, []*[2]string{
		{"Bash", `{"command":"sleep 1; echo survived","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1"],"timeout_s":60}`},
	})
	s := reopenAfterRestart(t, m, launcher, id)
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
	evs := s.Log().Snapshot()
	reopened := indexOf(evs, isType(event.SessionReopened))
	at, res := restoredWaitResult(evs)
	if reopened < 0 || !boolVal(evs[reopened].Data, "wait_restored") || res == nil || at < reopened {
		t.Fatalf("session_reopened at %d (%v), restored wait result at %d", reopened, evs[max(reopened, 0)].Data, at)
	}
	if res.Data["id"] != "call_1" || !strings.Contains(str(res.Data, "result"), "survived") {
		t.Fatalf("restored wait result = %v", res.Data)
	}
	if n := countType(evs, event.JobClaimed); n != 1 || countType(evs, event.JobNotified) != 0 {
		t.Fatalf("job_claimed=%d job_notified=%d, want the wait's one claim only", n, countType(evs, event.JobNotified))
	}
	if idle := eventsOf(s, event.SessionIdle)[0]; idle.Seq < res.Seq {
		t.Fatal("the turn did not continue after the restored wait")
	}
	found := false
	for _, msg := range s.currentLoop().History() {
		if msg.Role == "tool" && strings.Contains(msg.Content, "survived") {
			found = true
		}
		if msg.Content == engine.DanglingToolResult {
			t.Fatal("history kept the replay placeholder for the restored wait")
		}
	}
	if !found {
		t.Fatal("history lacks the restored wait's result")
	}
}

// A restored wait whose deadline passed while the daemon was down collects at
// once instead of blocking again.
func TestRestoredWaitPastDeadlineCollectsNow(t *testing.T) {
	m, _, id, launcher := restartWaitingSession(t, []*[2]string{
		{"Bash", `{"command":"sleep 30","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1"],"timeout_s":1}`},
	})
	time.Sleep(1100 * time.Millisecond)
	s := reopenAfterRestart(t, m, launcher, id)
	waitFor(t, func() bool { _, res := restoredWaitResult(s.Log().Snapshot()); return res != nil })
	_, res := restoredWaitResult(s.Log().Snapshot())
	if !strings.Contains(str(res.Data, "result"), "still running after 1s: job_1") {
		t.Fatalf("restored wait result = %v", res.Data)
	}
}

// Messages queued while the coordinator waited, but never delivered before the
// daemon went down, are delivered after the restored wait's result, in order;
// they end the restored wait early just as they end a live one.
func TestRestoredWaitDeliversQueuedInput(t *testing.T) {
	m, ws, id, launcher := restartWaitingSession(t, []*[2]string{
		{"Bash", `{"command":"sleep 30","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1"],"timeout_s":60}`},
	})
	var first, second event.Event
	appendToLog(t, ws, id, func(log *event.Log) {
		first = log.Record("user", event.UserInput, map[string]any{"text": "first", "queued": true})
		second = log.Record("user", event.UserInput, map[string]any{"text": "second", "queued": true})
	})
	s := reopenAfterRestart(t, m, launcher, id)
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
	evs := s.Log().Snapshot()
	at, res := restoredWaitResult(evs)
	if res == nil || !strings.Contains(str(res.Data, "result"), "returned early because the user sent a message") {
		t.Fatalf("restored wait result = %v", res)
	}
	var delivered []int
	for i, ev := range evs {
		if ev.Type == event.UserInputDelivered {
			if i < at {
				t.Fatal("queued input delivered before the restored wait's result")
			}
			delivered = append(delivered, intVal(ev.Data, "seq"))
		}
	}
	if len(delivered) != 2 || delivered[0] != first.Seq || delivered[1] != second.Seq {
		t.Fatalf("delivered seqs = %v, want [%d %d]", delivered, first.Seq, second.Seq)
	}
	// Replay reproduces the live history: wait result, then both messages.
	replayed := engine.ReplayHistory(evs)
	var tail []string
	for _, msg := range replayed {
		if msg.Role == "tool" && msg.ToolCallID == "call_1" && strings.Contains(msg.Content, "returned early") {
			tail = append(tail, "wait")
		} else if msg.Role == "user" && (msg.Content == "first" || msg.Content == "second") {
			tail = append(tail, msg.Content)
		}
	}
	if strings.Join(tail, ",") != "wait,first,second" {
		t.Fatalf("replayed order = %v", tail)
	}
}

// Input sent while a restored wait blocks takes the correction path and ends
// the wait early, as it would a live wait.
func TestInputEndsRestoredWait(t *testing.T) {
	m, _, id, launcher := restartWaitingSession(t, []*[2]string{
		{"Bash", `{"command":"sleep 30","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1"],"timeout_s":60}`},
	})
	s := reopenAfterRestart(t, m, launcher, id)
	if err := s.SendInput("are you there?"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
	evs := s.Log().Snapshot()
	at, res := restoredWaitResult(evs)
	if res == nil || !strings.Contains(str(res.Data, "result"), "returned early") {
		t.Fatalf("restored wait result = %v", res)
	}
	queued := eventsOf(s, event.UserInput)
	last := queued[len(queued)-1]
	if !boolVal(last.Data, "queued") {
		t.Fatalf("input during the restored wait was not queued: %v", last.Data)
	}
	d := eventsOf(s, event.UserInputDelivered)
	if len(d) != 1 || intVal(d[0].Data, "seq") != last.Seq || d[0].Seq < evs[at].Seq {
		t.Fatalf("user_input_delivered = %v", d)
	}
}

// A report a wait claimed before the daemon went down, but whose tool_result
// was never written, is part of the restored result without a second claim.
func TestRestoredWaitKeepsAlreadyClaimedReport(t *testing.T) {
	m, ws, id, launcher := restartWaitingSession(t, []*[2]string{
		{"Bash", `{"command":"echo early","run_in_background":true}`},
		{"Bash", `{"command":"sleep 1; echo late","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1","job_2"],"for":"all","timeout_s":60}`},
	})
	appendToLog(t, ws, id, func(log *event.Log) {
		log.Record("coordinator", event.JobClaimed, map[string]any{"id": "job_1", "kind": "bash",
			"label": "echo early", "status": "done", "result": "exit 0\nearly", "reason": "wait"})
	})
	s := reopenAfterRestart(t, m, launcher, id)
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
	_, res := restoredWaitResult(s.Log().Snapshot())
	if res == nil || !strings.Contains(str(res.Data, "result"), "early") || !strings.Contains(str(res.Data, "result"), "late") {
		t.Fatalf("restored wait result = %v", res)
	}
	claims := map[string]int{}
	for _, ev := range eventsOf(s, event.JobClaimed) {
		claims[str(ev.Data, "id")]++
	}
	if claims["job_1"] != 1 || claims["job_2"] != 1 {
		t.Fatalf("job_claimed per job = %v, want one each", claims)
	}
}

func waitCall(id string) event.Event {
	return event.Event{Type: event.ToolCall, Actor: "coordinator", Data: map[string]any{"id": id, "name": "wait"}}
}

func waitStarted(callID string) event.Event {
	spec := tools.WaitSpec{IDs: []string{"job_1"}, Mode: "any", Deadline: time.Now().Add(time.Minute), Timeout: time.Minute}
	return event.Event{Type: event.WaitStarted, Actor: "coordinator", Data: spec.EventData(callID)}
}

func queuedInput(seq int, text string) event.Event {
	return event.Event{Seq: seq, Type: event.UserInput, Actor: "user", Data: map[string]any{"text": text, "queued": true}}
}

func TestFindResumableWait(t *testing.T) {
	turn := event.Event{Type: event.ModelTurn, Actor: "coordinator", Data: map[string]any{"tool_calls": 2}}
	bash := event.Event{Type: event.ToolCall, Actor: "coordinator", Data: map[string]any{"id": "c0", "name": "Bash"}}
	bashResult := event.Event{Type: event.ToolResult, Actor: "coordinator", Data: map[string]any{"id": "c0", "name": "Bash"}}
	base := []event.Event{{Type: event.SessionStarted}, turn, bash, bashResult, waitCall("c1")}
	with := func(evs ...event.Event) []event.Event { return append(append([]event.Event(nil), base...), evs...) }

	w := findResumableWait(with(waitStarted("c1")))
	if w == nil || w.callID != "c1" || w.index != 1 || len(w.spec.IDs) != 1 {
		t.Fatalf("plain trailing wait = %+v", w)
	}
	for name, evs := range map[string][]event.Event{
		"no wait_started":  with(),
		"other call id":    with(waitStarted("c9")),
		"result recorded":  with(waitStarted("c1"), event.Event{Type: event.ToolResult, Actor: "coordinator", Data: map[string]any{"id": "c1"}}),
		"model moved on":   with(waitStarted("c1"), turn),
		"idle input":       with(waitStarted("c1"), event.Event{Type: event.UserInput, Actor: "user", Data: map[string]any{"text": "x"}}),
		"delivered input":  with(waitStarted("c1"), event.Event{Type: event.UserInputDelivered, Actor: "user", Data: map[string]any{"seq": 1}}),
		"job notified":     with(waitStarted("c1"), event.Event{Type: event.JobNotified, Actor: "user"}),
		"forked":           with(waitStarted("c1"), event.Event{Type: event.SessionForked}),
		"context rollover": with(waitStarted("c1"), event.Event{Type: event.ContextViewChanged, Actor: "coordinator"}),
		"subagent wait":    with(event.Event{Type: event.WaitStarted, Actor: "implementer", Data: waitStarted("c1").Data}),
		"not a wait call":  {turn, bash, event.Event{Type: event.WaitStarted, Actor: "coordinator", Data: waitStarted("c0").Data}},
		"invalid payload":  with(event.Event{Type: event.WaitStarted, Actor: "coordinator", Data: map[string]any{"call_id": "c1"}}),
	} {
		if w := findResumableWait(evs); w != nil {
			t.Errorf("%s: found a resumable wait %+v", name, w)
		}
	}

}

// Queued input of the active run, before or after wait_started, is pending
// unless already delivered; input queued in an earlier, finished run is not,
// and a reopen keeps only what it put back on the queue.
func TestPendingQueuedInputs(t *testing.T) {
	turn := event.Event{Type: event.ModelTurn, Actor: "coordinator", Data: map[string]any{"tool_calls": 2}}
	evs := []event.Event{
		queuedInput(1, "stale"),
		{Type: event.SessionIdle},
		turn, queuedInput(5, "delivered"),
		{Type: event.UserInputDelivered, Actor: "user", Data: map[string]any{"seq": 5}},
		queuedInput(7, "a"), waitCall("c1"), queuedInput(9, "b"), waitStarted("c1"),
		{Type: event.SessionReopened, Data: map[string]any{"requeued_inputs": []any{7.0, 9.0}}},
		queuedInput(12, "c"),
	}
	texts := func(cs []correction) string {
		var out []string
		for _, c := range cs {
			out = append(out, c.message.Text)
		}
		return strings.Join(out, ",")
	}
	if got := texts(pendingQueuedInputs(evs)); got != "a,b,c" {
		t.Fatalf("pending = %s, want a,b,c", got)
	}
	// A reopen that put nothing back dropped what was queued before it.
	evs[9] = event.Event{Type: event.SessionReopened, Data: map[string]any{}}
	if got := texts(pendingQueuedInputs(evs)); got != "c" {
		t.Fatalf("pending after a plain reopen = %s, want c", got)
	}
	// One of two requeued messages delivered before the next cut.
	evs[9] = event.Event{Type: event.SessionReopened, Data: map[string]any{"requeued_inputs": []int{7, 9}}}
	evs = append(evs, event.Event{Type: event.UserInputDelivered, Actor: "user", Data: map[string]any{"seq": 7}})
	if got := texts(pendingQueuedInputs(evs)); got != "b,c" {
		t.Fatalf("pending after a partial delivery = %s, want b,c", got)
	}
}

// The placeholder is found by the call's position, which replay keeps even
// when it rewrites ids; a raw id that equals another call's rewritten id does
// not mislead it.
func TestPlaceholderCallID(t *testing.T) {
	evs := []event.Event{
		{Type: event.UserInput, Actor: "user", Data: map[string]any{"text": "go"}},
		{Type: event.ModelTurn, Actor: "coordinator", Data: map[string]any{"tool_calls": 2}},
		waitCall("bad:id"),
		{Type: event.ToolResult, Actor: "coordinator", Data: map[string]any{"id": "bad:id", "name": "wait", "result": "done"}},
		waitCall("call_0"),
		waitStarted("call_0"),
	}
	history := engine.ReplayHistory(evs)
	w := findResumableWait(evs)
	if w == nil || w.index != 1 {
		t.Fatalf("resumable wait = %+v", w)
	}
	got := placeholderCallID(history, "wait", w.callID, w.index)
	calls := history[1].ToolCalls
	if len(calls) != 2 || got == "" || got != calls[1].ID {
		t.Fatalf("placeholder = %q, calls %+v", got, calls)
	}
	if got := placeholderCallID(history, "ask_user", "call_0", 1); got != "" {
		t.Fatalf("wrong tool matched = %q", got)
	}
	// Without a usable index the raw id still finds it.
	plain := []gollama.Message{
		{Role: "assistant", ToolCalls: []gollama.ToolCall{{ID: "w1", Function: gollama.ToolCallFunction{Name: "wait"}}}},
		{Role: "tool", ToolCallID: "w1", Content: engine.DanglingToolResult},
	}
	if got := placeholderCallID(plain, "wait", "w1", 5); got != "w1" {
		t.Fatalf("raw id fallback = %q", got)
	}
}

// Wake-eligible idle on reopen needs the durable state to still be idle
// awaiting jobs, with no human boundary since.
func TestReopenAwaitingJobsBoundaries(t *testing.T) {
	idle := func(awaiting, blocked bool) event.Event {
		data := map[string]any{"report": "r"}
		if awaiting {
			data["awaiting_jobs"] = true
		}
		if blocked {
			data["blocked"] = true
		}
		return event.Event{Type: event.SessionIdle, Actor: "coordinator", Data: data}
	}
	ev := func(t event.Type, data map[string]any) event.Event {
		return event.Event{Type: t, Actor: "user", Data: data}
	}
	for _, tc := range []struct {
		name string
		evs  []event.Event
		want bool
	}{
		{"awaiting", []event.Event{idle(true, false)}, true},
		{"reopened awaiting", []event.Event{idle(true, false), ev(event.SessionReopened, map[string]any{"awaiting_jobs": true})}, true},
		{"queued input", []event.Event{idle(true, false), ev(event.UserInput, map[string]any{"queued": true})}, true},
		{"job finished", []event.Event{idle(true, false), ev(event.JobFinished, nil)}, true},
		{"reopened since", []event.Event{idle(true, false), ev(event.SessionReopened, map[string]any{})}, true},
		{"stopped", []event.Event{idle(true, false), ev(event.SessionStopped, nil)}, false},
		{"cancelled pause", []event.Event{idle(true, false), ev(event.PauseRequested, nil), ev(event.PauseCancelled, nil)}, true},
		{"plain idle", []event.Event{idle(false, false)}, false},
		{"blocked report", []event.Event{idle(false, true)}, false},
		{"pending pause", []event.Event{idle(true, false), ev(event.PauseRequested, nil)}, false},
		{"paused", []event.Event{idle(true, false), ev(event.PauseRequested, nil), ev(event.PauseCancelled, nil), ev(event.PauseRequested, nil)}, false},
		{"error or refusal", []event.Event{idle(true, false), ev(event.SessionError, map[string]any{"kind": "refusal"})}, false},
		{"model switch gate", []event.Event{idle(true, false), ev(event.SessionError, map[string]any{"action": "switch_model"})}, false},
		{"forked", []event.Event{idle(true, false), ev(event.SessionForked, nil)}, false},
		{"question", []event.Event{idle(true, false), ev(event.QuestionAsked, nil)}, false},
		{"input", []event.Event{idle(true, false), ev(event.UserInput, map[string]any{"text": "x"})}, false},
		{"woken", []event.Event{idle(true, false), ev(event.JobNotified, nil)}, false},
	} {
		if got := reopenAwaitingJobs(tc.evs); got != tc.want {
			t.Errorf("%s: reopenAwaitingJobs = %v, want %v", tc.name, got, tc.want)
		}
		// Lifecycle markers disarm the wake but leave the finished turn idle.
		idleWant := true
		switch tc.name {
		case "forked", "question", "input", "woken":
			idleWant = false
		}
		if idle, _ := reopenBoundary(tc.evs); idle != idleWant {
			t.Errorf("%s: reopenBoundary idle = %v, want %v", tc.name, idle, idleWant)
		}
	}
}

// A log cut after the restored wait's result but before the messages queued
// during it were delivered still delivers them, after that result, on the
// next reopen, which goes on with the turn.
func TestRequeuedInputSurvivesCutAfterRestoredResult(t *testing.T) {
	m, ws, id, launcher := restartWaitingSession(t, []*[2]string{
		{"Bash", `{"command":"sleep 30","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1"],"timeout_s":60}`},
	})
	var queued, result event.Event
	appendToLog(t, ws, id, func(log *event.Log) {
		queued = log.Record("user", event.UserInput, map[string]any{"text": "hello", "queued": true})
		log.Record("coordinator", event.SessionReopened, map[string]any{"wait_restored": true, "requeued_inputs": []int{queued.Seq}})
		result = log.Record("coordinator", event.ToolResult, map[string]any{"name": "wait", "id": "call_1",
			"result": "wait returned early because the user sent a message", "restored": true})
	})
	s := reopenAfterRestart(t, m, launcher, id)
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
	reopened := eventsOf(s, event.SessionReopened)
	last := reopened[len(reopened)-1]
	if boolVal(last.Data, "wait_restored") || len(intList(last.Data["requeued_inputs"])) != 1 {
		t.Fatalf("session_reopened = %v", last.Data)
	}
	d := eventsOf(s, event.UserInputDelivered)
	if len(d) != 1 || intVal(d[0].Data, "seq") != queued.Seq || d[0].Seq < result.Seq {
		t.Fatalf("user_input_delivered = %v", d)
	}
}

// A reopen into wake-eligible idle is armed before the session is published:
// while startup still holds execution it already reports idle awaiting jobs.
func TestReopenAwaitingArmedBeforePublication(t *testing.T) {
	m, _, id, launcher := restartAwaitingSession(t, "sleep 30")
	m2, _ := shutdownTestManager(t, nil)
	m2.SetProjects(m.projects)
	m2.SetJobLauncher(launcher)
	m2.HoldExecution()
	s, err := m2.Reopen("proj", id)
	if err != nil {
		t.Fatal(err)
	}
	if st, awaiting := s.StatusWithJobContinuation(); st != event.StatusIdle || !awaiting {
		t.Fatalf("held reopen reports (%s, %t), want idle awaiting", st, awaiting)
	}
	m2.ReleaseExecution()
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionReopened)) == 1 })
	if !boolVal(eventsOf(s, event.SessionReopened)[0].Data, "awaiting_jobs") || !s.AwaitingJobs() {
		t.Fatal("released reopen is not awaiting its job")
	}
}

// A finished turn whose history ends on a tool result (a report_blocked or
// finish call) reopens idle instead of running another model turn, also after
// a stop or a pause request that only marked the lifecycle.
func TestReopenAfterControlToolFinishStaysIdle(t *testing.T) {
	m, ws := shutdownTestManager(t, nil)
	first, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "init"}) // prepares the workspace
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return first.Status() == event.StatusIdle })
	for _, after := range []event.Type{"", event.SessionStopped, event.PauseRequested} {
		id := "s_blocked_" + string(after)
		appendToLog(t, ws, id, func(log *event.Log) {
			log.Record("coordinator", event.SessionStarted, map[string]any{"mode": "chat"})
			log.Record("user", event.UserInput, map[string]any{"text": "go", "opening": true})
			log.Record("coordinator", event.ModelTurn, map[string]any{"text": "", "tool_calls": 1})
			log.Record("coordinator", event.ToolCall, map[string]any{"id": "c0", "name": "report_blocked", "args": "{}"})
			log.Record("coordinator", event.ToolResult, map[string]any{"id": "c0", "name": "report_blocked", "result": "blocked"})
			log.Record("coordinator", event.SessionIdle, map[string]any{"report": "blocked", "blocked": true})
			if after != "" {
				log.Record("user", after, map[string]any{})
			}
		})
		s, err := m.Reopen("proj", id)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return len(eventsOf(s, event.SessionReopened)) == 1 && s.Status() == event.StatusIdle })
		time.Sleep(200 * time.Millisecond)
		if n := len(eventsOf(s, event.ModelTurn)); n != 1 {
			t.Fatalf("after %q: reopen ran %d more model turns", after, n-1)
		}
	}
}

// Input accepted after a reopen is published but before its marker is
// written is listed in the marker, so a cut before its delivery keeps it.
func TestInputBeforeReopenMarkerIsRequeued(t *testing.T) {
	m, _, id, launcher := restartWaitingSession(t, []*[2]string{
		{"Bash", `{"command":"sleep 30","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1"],"timeout_s":60}`},
	})
	m2, _ := shutdownTestManager(t, nil)
	m2.SetProjects(m.projects)
	m2.SetJobLauncher(launcher)
	m2.HoldExecution()
	s, err := m2.Reopen("proj", id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendInput("early"); err != nil {
		t.Fatal(err)
	}
	inputs := eventsOf(s, event.UserInput)
	early := inputs[len(inputs)-1]
	if !boolVal(early.Data, "queued") {
		t.Fatalf("input before release was not queued: %v", early.Data)
	}
	m2.ReleaseExecution()
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionReopened)) == 1 })
	marker := eventsOf(s, event.SessionReopened)[0]
	if got := intList(marker.Data["requeued_inputs"]); len(got) != 1 || got[0] != early.Seq {
		t.Fatalf("requeued_inputs = %v, want [%d]", marker.Data["requeued_inputs"], early.Seq)
	}
	if pending := pendingQueuedInputs(s.Log().Snapshot()[:marker.Seq]); len(pending) != 1 {
		t.Fatalf("pending through the marker = %+v", pending)
	}
}
