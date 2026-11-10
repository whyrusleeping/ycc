package session

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/project"
)

// handoffStub is a scripted OpenAI-compatible model for the chat → work
// hand-off. Chat requests call start_work once, acknowledge it, then summarize
// the delivered job report; work-session requests reply with a final report.
func handoffStub(t *testing.T, taskID string) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu     sync.Mutex
		bodies []string
	)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		system := fmt.Sprint(req.Messages[0].Content)
		last := req.Messages[len(req.Messages)-1]
		msg := map[string]any{"role": "assistant", "content": "Implemented and committed task " + taskID + "."}
		finish := "stop"
		if strings.Contains(system, "open-ended coding assistant") {
			switch {
			case last.Role == "tool":
				msg["content"] = "Handed off; we can keep talking."
			case strings.Contains(fmt.Sprint(last.Content), "[job "):
				msg["content"] = "The work session reported back."
			default:
				args, _ := json.Marshal(map[string]any{"task_id": taskID, "handoff": "Decided: cache in memory. Verify with go test ./..."})
				msg = map[string]any{"role": "assistant", "content": "", "tool_calls": []map[string]any{{
					"index": 0, "id": "call_1", "type": "function",
					"function": map[string]any{"name": "start_work", "arguments": string(args)},
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
	return stub, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func stubRegistry(t *testing.T, url string) *config.Registry {
	t.Helper()
	t.Setenv("YCC_HANDOFF_TEST_KEY", "x")
	cfgPath := filepath.Join(t.TempDir(), "ycc.toml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(`[models.stub]
backend = "openai"
base_url = %q
model = "stub-model"
key_env = "YCC_HANDOFF_TEST_KEY"

[roles]
coordinator = "stub"
implementer = "stub"
reviewers = ["stub"]
`, url)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return config.NewRegistry(cfg)
}

func eventsOf(s *Session, typ event.Type) []event.Event {
	var out []event.Event
	for _, ev := range s.Log().Snapshot() {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

// A chat hands one task to the work pipeline: the user confirms, a work session
// starts on exactly that task with the handoff in its opening context, both
// sessions record the hand-off, the chat stays open, and the work session's
// outcome comes back to the chat as a background job report.
func TestChatStartWorkEndToEnd(t *testing.T) {
	ws := t.TempDir()
	task, err := docs.NewStore(ws).CreateWithStatus("Add a cache", "## Description\nCache lookups.", 3, nil, nil, docs.StatusTodo)
	if err != nil {
		t.Fatal(err)
	}
	// Another task cited in the handoff must not defeat the explicit preload.
	if _, err := docs.NewStore(ws).Create("Unrelated", "x", 3, nil, nil); err != nil {
		t.Fatal(err)
	}
	stub, bodies := handoffStub(t, task.ID)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ReclaimAll)
	prev := handoffPollInterval
	handoffPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { handoffPollInterval = prev })

	chat, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "Let's plan the cache, then hand it off."})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, chat.PendingQuestion)
	asked := eventsOf(chat, event.QuestionAsked)
	if len(asked) != 1 || !strings.Contains(fmt.Sprint(asked[0].Data), task.ID) || !strings.Contains(fmt.Sprint(asked[0].Data), "cache in memory") {
		t.Fatalf("confirmation = %+v", asked)
	}
	if err := chat.Answer("Yes"); err != nil {
		t.Fatal(err)
	}

	var handoff event.Event
	waitFor(t, func() bool {
		evs := eventsOf(chat, event.WorkHandoff)
		if len(evs) == 1 {
			handoff = evs[0]
		}
		return len(evs) == 1
	})
	workID, _ := handoff.Data["session"].(string)
	if handoff.Data["task"] != task.ID || workID == "" {
		t.Fatalf("chat work_handoff = %+v", handoff.Data)
	}
	work, ok := m.Get(workID)
	if !ok || work.Mode != "work" || work.Workspace != chat.Workspace {
		t.Fatalf("work session %q: ok=%v %+v", workID, ok, work)
	}
	waitForSessionEvent(t, work, event.SessionIdle)
	recv := eventsOf(work, event.WorkHandoff)
	if len(recv) != 1 || recv[0].Data["task"] != task.ID || recv[0].Data["from_session"] != chat.ID {
		t.Fatalf("work session work_handoff = %+v", recv)
	}
	opening := eventsOf(work, event.UserInput)
	if len(opening) == 0 || !strings.Contains(fmt.Sprint(opening[0].Data["text"]), "Implement task "+task.ID) ||
		!strings.Contains(fmt.Sprint(opening[0].Data["text"]), "Decided: cache in memory") {
		t.Fatalf("work opening input = %+v", opening)
	}
	calls := eventsOf(work, event.ToolCall)
	if len(calls) == 0 || calls[0].Data["name"] != "get_task" || !strings.Contains(fmt.Sprint(calls[0].Data["args"]), task.ID) {
		t.Fatalf("work session did not preload get_task(%s): %+v", task.ID, calls)
	}

	// The work session's outcome arrives in the chat as its job report, and the
	// chat answers it in a new turn: it was never ended.
	waitFor(t, func() bool {
		return len(eventsOf(chat, event.JobNotified)) == 1 && chat.Status() == event.StatusIdle && !chat.AwaitingJobs()
	})
	note := eventsOf(chat, event.JobNotified)[0]
	if note.Data["status"] != "done" || !strings.Contains(fmt.Sprint(note.Data["text"]), "Implemented and committed task "+task.ID) {
		t.Fatalf("job notification = %+v", note.Data)
	}
	if chat.Mode != "chat" {
		t.Fatalf("chat mode = %q after hand-off", chat.Mode)
	}
	sawReport := false
	for _, b := range bodies() {
		if strings.Contains(b, "open-ended coding assistant") && strings.Contains(b, "Work session "+workID) {
			sawReport = true
		}
	}
	if !sawReport {
		t.Fatal("the chat model never saw the work session report")
	}
}

// A separate-workstream hand-off runs in a new worktree and records the
// workstream. The worktree forks committed HEAD, so an uncommitted task is
// refused, and a chat already inside a workstream must use one.
func TestStartWorkHandoffWorkstream(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	task, err := docs.NewStore(proj).CreateWithStatus("Planned in chat", "## Description\nx", 3, nil, nil, docs.StatusTodo)
	if err != nil {
		t.Fatal(err)
	}
	h := orchestrator.WorkHandoff{TaskID: task.ID, Handoff: "use the cache", Workstream: true}
	if _, err := m.startWorkHandoff(proj, "s_chat", h); err == nil || !strings.Contains(err.Error(), "commit it first") {
		t.Fatalf("hand-off of an uncommitted task: err = %v", err)
	}
	if len(m.workstreams.List()) != 0 {
		t.Fatal("a refused hand-off created a workstream")
	}
	for _, args := range [][]string{{"add", "backlog"}, {"commit", "-qm", "add task"}} {
		if out, err := exec.Command("git", append([]string{"-C", proj}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	out, err := m.startWorkHandoff(proj, "s_chat", h)
	if err != nil {
		t.Fatalf("startWorkHandoff: %v", err)
	}
	ws, ok := m.workstreams.Get(out.Workstream)
	if !ok || ws.SessionID != out.SessionID || ws.TaskID != task.ID || ws.Branch != out.Branch {
		t.Fatalf("workstream = %+v ok=%v, started = %+v", ws, ok, out)
	}
	s, ok := m.Get(out.SessionID)
	if !ok || s.Workspace != ws.WorktreePath || s.Mode != "work" {
		t.Fatalf("session = %+v ok=%v", s, ok)
	}
	if s.startupPreload.TaskID != task.ID {
		t.Fatalf("workstream session preloaded %q, want %s", s.startupPreload.TaskID, task.ID)
	}
	ev := waitForSessionEvent(t, s, event.WorkHandoff)
	if ev.Data["from_session"] != "s_chat" || ev.Data["task"] != task.ID {
		t.Fatalf("work_handoff = %+v", ev.Data)
	}
	if _, err := m.startWorkHandoff(ws.WorktreePath, "s_chat", orchestrator.WorkHandoff{TaskID: task.ID}); err == nil ||
		!strings.Contains(err.Error(), "separate_workstream") {
		t.Fatalf("shared hand-off from a workstream chat: err = %v", err)
	}
	// A chat inside a workstream may hand off its task while its definition
	// matches the project's HEAD; one that differs, or a revised primary task
	// file, is refused as stale.
	if _, err := m.startWorkHandoff(ws.WorktreePath, "s_chat", h); err != nil {
		t.Fatalf("hand-off from a workstream chat: %v", err)
	}
	wsTask, err := docs.NewStore(ws.WorktreePath).Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(wsTask.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsTask.Path, append(data, "\nA requirement agreed in this chat.\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.startWorkHandoff(ws.WorktreePath, "s_chat", h); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("hand-off of a task revised in a workstream chat: err = %v", err)
	}
	if err := os.WriteFile(task.Path, append(data, "\nRevised in the primary tree.\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.startWorkHandoff(proj, "s_chat", h); err == nil || !strings.Contains(err.Error(), "commit it first") {
		t.Fatalf("hand-off of a revised primary task: err = %v", err)
	}
	if n := len(m.workstreams.List()); n != 2 {
		t.Fatalf("refused hand-offs created workstreams: %d", n)
	}
}
