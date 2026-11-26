package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/tools"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// scriptedToolStub is an OpenAI-compatible model that answers the n-th request
// (counting from 0) with the n-th scripted tool call, or a final "done" text
// once the script is exhausted. A nil step blocks the request until the client
// gives up (an in-flight model turn).
func scriptedToolStub(t *testing.T, steps []*[2]string) *httptest.Server {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		n := 0
		for _, m := range req.Messages {
			if m.Role == "assistant" {
				n++
			}
		}
		msg := map[string]any{"role": "assistant", "content": "done"}
		finish := "stop"
		if n < len(steps) {
			switch step := steps[n]; {
			case step == nil:
				<-r.Context().Done()
				return
			case step[0] == "":
				// A text reply ends the turn.
				msg["content"] = step[1]
			default:
				msg = map[string]any{"role": "assistant", "content": "", "tool_calls": []map[string]any{{
					"index": 0, "id": fmt.Sprintf("call_%d", n), "type": "function",
					"function": map[string]any{"name": step[0], "arguments": step[1]},
				}}}
				finish = "tool_calls"
			}
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			delta := map[string]any{"role": "assistant", "content": msg["content"]}
			if tc, ok := msg["tool_calls"]; ok {
				delta["tool_calls"] = tc
			}
			for _, chunk := range []map[string]any{
				{"id": "c", "object": "chat.completion.chunk", "choices": []map[string]any{{"index": 0, "delta": delta}}},
				{"id": "c", "object": "chat.completion.chunk", "choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}},
			} {
				b, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", b)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		b, _ := json.Marshal(map[string]any{"id": "c", "object": "chat.completion",
			"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": finish}}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(stub.Close)
	return stub
}

func shutdownTestManager(t *testing.T, steps []*[2]string) (*Manager, string) {
	t.Helper()
	ws := t.TempDir()
	stub := scriptedToolStub(t, steps)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ReclaimAll)
	return m, ws
}

// lastCoordinatorToolCall reports whether the session's latest coordinator turn
// step is a tool_call of name.
func lastCoordinatorToolCall(s *Session, name string) bool {
	ev, ok := s.Log().LastByActor("coordinator", event.ModelTurn, event.ToolCall, event.ToolResult)
	if !ok || ev.Type != event.ToolCall {
		return false
	}
	got, _ := ev.Data["name"].(string)
	return got == name
}

// readLog returns the session's persisted events (a closed log keeps them and
// accepts no more).
func readLog(t *testing.T, s *Session) []event.Event {
	t.Helper()
	return s.Log().Snapshot()
}

// Restart suspension exits a blocked wait without a result: the durable log
// ends with the unresolved wait tool_call, records no job_claimed or
// session_stopped, and a coordinator blocked in wait does not delay the drain.
func TestShutdownForRestartLeavesWaitUnresolved(t *testing.T) {
	m, _ := shutdownTestManager(t, []*[2]string{
		{"Bash", `{"command":"sleep 30","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1"],"timeout_s":60}`},
	})
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return lastCoordinatorToolCall(s, "wait") })

	m.BeginShutdown()
	if _, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "late"}); err != ErrShuttingDown {
		t.Fatalf("Start during shutdown: err = %v, want ErrShuttingDown", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if busy := m.Drain(ctx); len(busy) != 0 {
		t.Fatalf("Drain busy = %v, want a session blocked in wait to count as drained", busy)
	}
	m.ShutdownForRestart()
	if _, ok := m.Get(s.ID); ok {
		t.Fatal("session still live after ShutdownForRestart")
	}

	evs := readLog(t, s)
	if n := len(evs); n < 2 || evs[n-2].Type != event.ToolCall || evs[n-2].Data["name"] != "wait" ||
		evs[n-1].Type != event.WaitStarted {
		t.Fatalf("log ends with %v, want the unresolved wait tool_call and its wait_started", evs[max(0, n-2):])
	}
	for _, ev := range evs {
		switch ev.Type {
		case event.SessionStopped, event.JobClaimed:
			t.Fatalf("restart suspension recorded %s", ev.Type)
		case event.ToolResult:
			if ev.Data["name"] == "wait" {
				t.Fatalf("restart suspension recorded a wait tool_result: %v", ev.Data)
			}
		}
	}
	// The suspended log reopens and resumes the wait: its unsupervised job
	// did not survive, so the restored wait returns the loss at once.
	m2, _ := shutdownTestManager(t, nil)
	m2.SetProjects(m.projects)
	s2, err := m2.Reopen("proj", s.ID)
	if err != nil {
		t.Fatalf("Reopen after restart suspension: %v", err)
	}
	waitFor(t, func() bool { return len(eventsOf(s2, event.SessionIdle)) == 1 })
	var result *event.Event
	for _, ev := range s2.Log().Snapshot() {
		if ev.Type == event.ToolResult && ev.Data["name"] == "wait" {
			result = &ev
		}
	}
	if result == nil || !boolVal(result.Data, "restored") || !strings.Contains(str(result.Data, "result"), "[job job_1 lost]") {
		t.Fatalf("restored wait result = %v", result)
	}
}

// Ordinary teardown (no shutdown begun) keeps its cancel contract: the
// cancelled wait records its result.
func TestReclaimAllStillResolvesWait(t *testing.T) {
	m, _ := shutdownTestManager(t, []*[2]string{
		{"Bash", `{"command":"sleep 30","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1"],"timeout_s":60}`},
	})
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return lastCoordinatorToolCall(s, "wait") })
	m.ReclaimAll()
	found := false
	for _, ev := range readLog(t, s) {
		if ev.Type == event.ToolResult && ev.Data["name"] == "wait" {
			found = true
		}
	}
	if !found {
		t.Fatal("ordinary reclaim no longer records the cancelled wait's tool_result")
	}
}

// A coordinator mid model turn delays the drain until its window expires.
func TestDrainWaitsForActiveModelTurn(t *testing.T) {
	m, _ := shutdownTestManager(t, []*[2]string{nil})
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return s.Status() == event.StatusRunning })
	m.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if busy := m.Drain(ctx); len(busy) != 1 || busy[0] != s.ID {
		t.Fatalf("Drain busy = %v, want the mid-turn session", busy)
	}
	m.ShutdownForRestart()
	for _, ev := range readLog(t, s) {
		if ev.Type == event.SessionStopped || ev.Type == event.SessionError {
			t.Fatalf("restart suspension of an in-flight turn recorded %s", ev.Type)
		}
	}
}

// Sessions registered while startup holds execution do not run until release.
func TestHoldExecutionDefersRunOwner(t *testing.T) {
	m, _ := shutdownTestManager(t, nil)
	m.HoldExecution()
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(s.Log().Snapshot()); n != 0 {
		t.Fatalf("held session wrote %d events before release", n)
	}
	m.ReleaseExecution()
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
}

// A held session that never ran is suspended without waiting for a run owner.
func TestShutdownSuspendsHeldSession(t *testing.T) {
	m, _ := shutdownTestManager(t, nil)
	m.HoldExecution()
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { m.ShutdownForRestart(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ShutdownForRestart hung on a held session")
	}
	// A release arriving after retirement never starts the run owner.
	m.ReleaseExecution()
	time.Sleep(50 * time.Millisecond)
	s.launchMu.Lock()
	launched := s.launched
	s.launchMu.Unlock()
	if launched {
		t.Fatal("retired session was launched")
	}
}

func TestIdleAwaitingJobs(t *testing.T) {
	idle := func(awaiting bool) event.Event {
		data := map[string]any{"report": "r"}
		if awaiting {
			data["awaiting_jobs"] = true
		}
		return event.Event{Type: event.SessionIdle, Data: data}
	}
	for _, tc := range []struct {
		name string
		evs  []event.Event
		want bool
	}{
		{"awaiting", []event.Event{idle(true)}, true},
		{"plain idle", []event.Event{idle(false)}, false},
		{"woken since", []event.Event{idle(true), {Type: event.JobNotified}}, false},
		{"stopped after awaiting", []event.Event{idle(true), {Type: event.SessionStopped}}, true},
		{"empty", nil, false},
	} {
		if got := idleAwaitingJobs(tc.evs); got != tc.want {
			t.Errorf("%s: idleAwaitingJobs = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A coordinator still blocked in a wait for all of its jobs stays drained after
// one of them finishes (job_finished is logged under the coordinator actor).
func TestDrainWaitAllAfterOneJobFinished(t *testing.T) {
	m, _ := shutdownTestManager(t, []*[2]string{
		{"Bash", `{"command":"true","run_in_background":true}`},
		{"Bash", `{"command":"sleep 30","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1","job_2"],"for":"all","timeout_s":60}`},
	})
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return lastCoordinatorToolCall(s, "wait") && len(eventsOf(s, event.JobFinished)) == 1 })
	m.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if busy := m.Drain(ctx); len(busy) != 0 {
		t.Fatalf("Drain busy = %v for a coordinator blocked in wait(all)", busy)
	}
	m.ShutdownForRestart()
}

// Running subagents hold the drain unless they are themselves blocked in wait.
func TestRestartQuietSubagentBoundary(t *testing.T) {
	s := newStopSession(t)
	jr := jobs.NewRegistry()
	defer jr.KillAll()
	s.deps = &orchestrator.Deps{Jobs: jr, Emitter: s.emitter}
	s.setIdle(true)
	agent := jr.Start("agent", "implementer", "coordinator")
	if s.restartQuiet() {
		t.Fatal("idle session with a subagent mid-turn counted as drained")
	}
	agent.UpdateActivity("wait", nil)
	if !s.restartQuiet() {
		t.Fatal("subagent blocked in wait held the drain")
	}
	agent.UpdateActivity("Bash", nil)
	if s.restartQuiet() {
		t.Fatal("subagent running a foreground tool counted as drained")
	}
}

// Once shutdown began, a daemon-owned integration session that its stopped
// worker ends is suspended for restart, not hard-stopped.
func TestShutdownSuspendsIntegrationSession(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.workstreams.SetStatus(ws.ID, workstream.StatusReady); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	m.integrationWG.Add(1)
	go func() {
		defer close(done)
		defer m.integrationWG.Done()
		m.runIntegrateSession(ws, 1, integrationOutcome{})
	}()
	var s *Session
	waitFor(t, func() bool {
		w, _ := m.workstreams.Get(ws.ID)
		s, _ = m.Get(w.IntegrateSessionID)
		if s == nil {
			return false
		}
		_, started := snapshotHasEvent(s, event.SessionStarted)
		return started
	})
	m.ShutdownForRestart()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("integration did not exit")
	}
	if _, stopped := snapshotHasEvent(s, event.SessionStopped); stopped {
		t.Fatal("restart shutdown hard-stopped the integration session")
	}
}

// Startup readiness derivation never hands a tree whose session is live to
// auto-integration, even when the stream is already durably ready.
func TestDeriveReadinessSkipsLiveReadyWorkstream(t *testing.T) {
	m, _ := autoIntegrationManager(t, "true")
	m.HoldExecution()
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "probe.txt", "probe\n", "probe commit")
	if err := m.workstreams.SetStatus(ws.ID, workstream.StatusReady); err != nil {
		t.Fatal(err)
	}
	m.DeriveWorkstreamReadiness()
	m.integrationWG.Wait()
	if got, _ := m.workstreams.Get(ws.ID); got.Status != workstream.StatusReady {
		t.Fatalf("live ready workstream moved to %s during startup derivation", got.Status)
	}
}

// Restart suspension leaves a pending ask_user unanswered and unresolved.
func TestShutdownForRestartLeavesAskUserUnresolved(t *testing.T) {
	m, _ := shutdownTestManager(t, []*[2]string{{"ask_user", `{"question":"proceed?"}`}})
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s.PendingQuestion)
	m.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if busy := m.Drain(ctx); len(busy) != 0 {
		t.Fatalf("Drain busy = %v, want a session parked on ask_user drained", busy)
	}
	m.ShutdownForRestart()
	evs := readLog(t, s)
	if last := evs[len(evs)-1]; last.Type != event.QuestionAsked {
		t.Fatalf("log ends with %s, want the pending question_asked", last.Type)
	}
	for _, ev := range evs {
		if ev.Type == event.ToolResult && ev.Data["name"] == "ask_user" {
			t.Fatalf("restart suspension recorded an ask_user tool_result: %v", ev.Data)
		}
	}
	if q := findResumableQuestion(evs); q == nil {
		t.Fatal("suspended question is not restorable on reopen")
	}
}

// Restart teardown cancels subagents with the restart cause, so a subagent's
// wait exits without claiming a partial report.
func TestShutdownSubagentWaitClaimsNothing(t *testing.T) {
	m, _ := shutdownTestManager(t, []*[2]string{nil})
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	reg := s.deps.Jobs
	agent, ok := reg.TryStartTracked("agent", "probe", "coordinator")
	if !ok {
		t.Fatal("could not start agent job")
	}
	finished := reg.Start("bash", "finished", "agent:probe")
	finished.Finish(jobs.Done, "result")
	blocked := reg.Start("bash", "blocked", "agent:probe")
	armed := make(chan struct{})
	ws := &tools.Workspace{Jobs: reg, Emitter: event.NewEmitter(s.Log(), "agent:probe"),
		InputWake: func() <-chan struct{} { close(armed); return nil }}
	for _, tool := range tools.JobTools(ws) {
		if tool.Name != "wait" {
			continue
		}
		go func() {
			defer agent.ExecutionComplete()
			_, _ = tool.Call(agent.Context(), map[string]any{"job_ids": []any{finished.ID(), blocked.ID()}, "for": "all"})
		}()
	}
	<-armed
	m.ShutdownForRestart()
	for _, ev := range readLog(t, s) {
		if ev.Type == event.JobClaimed {
			t.Fatalf("restart-suspended subagent wait recorded job_claimed: %v", ev.Data)
		}
	}
}

// A workstream watcher still evaluating an event when restart teardown begins
// is joined while the log is open, so its durable append is not dropped.
func TestShutdownJoinsWorkstreamWatcherBeforeClosingLog(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	m.HoldExecution()
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, w.WorktreePath, "test.txt", "test\n", "commit")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	entered, release := filepath.Join(bin, "entered"), filepath.Join(bin, "release")
	t.Setenv("PROBE_ENTERED", entered)
	t.Setenv("PROBE_RELEASE", release)
	// Pause readiness evaluation inside its git query until released.
	shim := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *' rev-list --count '*) touch \"$PROBE_ENTERED\"; "+
		"for i in $(seq 1 400); do [ -e \"$PROBE_RELEASE\" ] && break; sleep .02; done;; esac\nexec %q \"$@\"\n", gitPath)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	s.Log().Record("coordinator", event.SessionIdle, map[string]any{"report": "done"})
	waitFor(t, func() bool { _, err := os.Stat(entered); return err == nil })
	done := make(chan struct{})
	go func() { m.ShutdownForRestart(); close(done) }()
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if got, _ := m.workstreams.Get(w.ID); got.Status != workstream.StatusReady {
		t.Fatalf("watcher did not mark the stream ready: %s (%s)", got.Status, got.StatusReason)
	}
	if _, ok := snapshotHasEvent(s, event.WorkstreamReady); !ok {
		t.Fatal("watcher's workstream_ready append was dropped by restart teardown")
	}
}

// A stop during shutdown (a discard admitted before the fence) still joins the
// session before its caller removes the worktree, and records no stop marker.
func TestShutdownDiscardJoinsLiveSession(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(s, event.SessionStarted); return ok })
	m.BeginShutdown()
	if err := m.DiscardWorkstream(w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree not removed: %v", err)
	}
	if !jobs.RestartSuspended(s.ctx) {
		t.Fatal("discard removed the worktree of a session that was not suspended")
	}
	if _, stopped := snapshotHasEvent(s, event.SessionStopped); stopped {
		t.Fatal("stop during shutdown recorded session_stopped")
	}
}

// An ordinary ReclaimAll that races BeginShutdown hands its sessions to restart
// suspension instead of cancelling them the ordinary way.
func TestOrdinaryReclaimRacingShutdownSuspends(t *testing.T) {
	m, _ := shutdownTestManager(t, []*[2]string{nil})
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	m.gitSyncWG.Add(1) // hold the git join, as an in-flight fetch would
	done := make(chan struct{})
	go func() { m.ReclaimAll(); close(done) }()
	<-m.gitSyncCtx.Done()
	m.BeginShutdown()
	m.gitSyncWG.Done()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReclaimAll did not finish")
	}
	if !jobs.RestartSuspended(s.ctx) {
		t.Fatal("racing ordinary ReclaimAll cancelled the session without the restart cause")
	}
}

// A worker step that ignores cancellation (a rebase) delays only the final
// worker join, never the suspension of live sessions.
func TestShutdownSuspendsBeforeJoiningWorkers(t *testing.T) {
	m, _ := shutdownTestManager(t, []*[2]string{nil})
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	m.integrationWG.Add(1) // a non-interruptible integration step in flight
	done := make(chan struct{})
	go func() { m.ShutdownForRestart(); close(done) }()
	waitFor(t, func() bool { return s.Log().Record("probe", event.SessionNotice, nil).Seq == 0 })
	select {
	case <-done:
		t.Fatal("shutdown finished before joining the integration worker")
	default:
	}
	m.integrationWG.Done()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish after the worker returned")
	}
}

// A worker holding its own lock across a slow step (a watch starting a run
// holds watchMu) cannot delay suspension; only the final join waits for it.
func TestShutdownDoesNotWaitOnWorkerLocks(t *testing.T) {
	m, _ := shutdownTestManager(t, []*[2]string{nil})
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	m.watchMu.Lock()
	m.groomMu.Lock()
	m.loopMu.Lock()
	done := make(chan struct{})
	go func() { m.ShutdownForRestart(); close(done) }()
	select {
	case <-s.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("suspension waited behind a worker lock")
	}
	if !jobs.RestartSuspended(s.ctx) {
		t.Fatal("session cancelled without the restart cause")
	}
	m.loopMu.Unlock()
	m.groomMu.Unlock()
	m.watchMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish after the locks were released")
	}
}

// A passive reclaim that was waiting for the manager lock when shutdown began
// leaves the session to restart suspension rather than reaping it.
func TestReclaimDecidesTeardownUnderLock(t *testing.T) {
	m, _ := shutdownTestManager(t, []*[2]string{nil})
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	done := make(chan bool, 1)
	go func() { done <- m.reclaimIfCurrent(s) }()
	time.Sleep(50 * time.Millisecond) // let it park on m.mu
	m.shuttingDown.Store(true)        // BeginShutdown's transition, made under m.mu
	m.mu.Unlock()
	if !<-done {
		t.Fatal("reclaimIfCurrent = false for the live session")
	}
	if s.ctx.Err() != nil {
		t.Fatal("passive reclaim tore the session down after shutdown began")
	}
	m.ShutdownForRestart()
	if !jobs.RestartSuspended(s.ctx) {
		t.Fatal("session was not suspended for restart")
	}
}

// A workstream that becomes ready during the drain stays durably ready; the
// integration queue starts no new work once shutdown began.
func TestDrainStartsNoNewIntegration(t *testing.T) {
	m, _ := autoIntegrationManager(t, "true")
	m.HoldExecution()
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "test"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, w.WorktreePath, "test.txt", "test\n", "test")
	m.BeginShutdown()
	s.Log().Record("coordinator", event.SessionIdle, map[string]any{"report": "finished while draining"})
	waitFor(t, func() bool { _, ok := snapshotHasEvent(s, event.WorkstreamReady); return ok })
	m.stopWorkstreamWatcher(s.ID)
	m.integrationWG.Wait()
	if _, ok := snapshotHasEvent(s, event.WorkstreamIntegrating); ok {
		t.Error("an integration started during the drain")
	}
	if got, _ := m.workstreams.Get(w.ID); got.Status != workstream.StatusReady {
		t.Fatalf("ready workstream moved to %s (%s) during the drain", got.Status, got.StatusReason)
	}
}
