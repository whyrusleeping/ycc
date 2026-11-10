package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/project"
)

// stubRegistry configures one OpenAI-compatible "stub" model at url for every role.
func stubRegistry(t *testing.T, url string) *config.Registry {
	t.Helper()
	t.Setenv("YCC_STUB_TEST_KEY", "x")
	cfgPath := filepath.Join(t.TempDir(), "ycc.toml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(`[models.stub]
backend = "openai"
base_url = %q
model = "stub-model"
key_env = "YCC_STUB_TEST_KEY"

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

// planOnlyStub is a scripted OpenAI-compatible chat model. For each new user
// request "<name>" it writes <name>.go and plans/<name>.md in one tool batch,
// then answers "done" once the results are in.
func planOnlyStub(t *testing.T) (*httptest.Server, func() []string) {
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
		msg := map[string]any{"role": "assistant", "content": "done"}
		finish := "stop"
		if last := req.Messages[len(req.Messages)-1]; last.Role != "tool" {
			name := ""
			for _, m := range req.Messages {
				if text := fmt.Sprint(m.Content); m.Role == "user" && !strings.Contains(text, "[plan only") {
					name = text
				}
			}
			var calls []map[string]any
			for i, path := range []string{name + ".go", "plans/" + name + ".md"} {
				args, _ := json.Marshal(map[string]any{"file_path": path, "content": "x\n"})
				calls = append(calls, map[string]any{"index": i, "id": fmt.Sprintf("call_%s_%d", name, i), "type": "function",
					"function": map[string]any{"name": "Write", "arguments": string(args)}})
			}
			msg = map[string]any{"role": "assistant", "content": "", "tool_calls": calls}
			finish = "tool_calls"
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

func waitIdleCount(t *testing.T, s *Session, n int) {
	t.Helper()
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionIdle)) >= n && s.Status() == event.StatusIdle })
}

func exists(ws, rel string) bool {
	_, err := os.Stat(filepath.Join(ws, rel))
	return err == nil
}

// A chat started in plan only cannot write code; turning it off mid-session
// tells the model and lifts the gate; the state and the notes survive reopen.
func TestPlanOnlyToggleMidSessionAndReopen(t *testing.T) {
	ws := t.TempDir()
	stub, bodies := planOnlyStub(t)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ReclaimAll)

	if _, err := m.Start(Config{Project: "proj", Mode: "work", Prompt: "x", PlanOnly: true}); err != ErrPlanOnlyMode {
		t.Fatalf("plan-only work session: err = %v", err)
	}
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "alpha", PlanOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, s, 1)
	if exists(ws, "alpha.go") || !exists(ws, "plans/alpha.md") {
		t.Fatalf("plan-only first turn: alpha.go=%v plans/alpha.md=%v", exists(ws, "alpha.go"), exists(ws, "plans/alpha.md"))
	}
	if b := bodies(); !strings.Contains(b[0], "[plan only: on]") {
		t.Fatalf("first request did not tell the model about plan only:\n%s", b[0])
	}

	// Off while idle: recorded and enforced at once, told with the next turn.
	if err := s.SetPlanOnly(false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlanOnly(false); err != nil { // no-op
		t.Fatal(err)
	}
	if got := len(eventsOf(s, event.PlanOnlyChanged)); got != 2 {
		t.Fatalf("plan_only_changed events = %d, want 2 (start + off)", got)
	}
	if got := len(eventsOf(s, event.PlanOnlyDelivered)); got != 1 {
		t.Fatalf("plan_only_delivered before the next turn = %d, want 1", got)
	}
	if err := s.SendInput("beta"); err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, s, 2)
	if !exists(ws, "beta.go") {
		t.Fatal("beta.go was not written after plan only was turned off")
	}
	delivered := eventsOf(s, event.PlanOnlyDelivered)
	if len(delivered) != 2 || delivered[1].Data["plan_only"] != false {
		t.Fatalf("plan_only_delivered = %+v", delivered)
	}
	b := bodies()
	last := b[len(b)-2] // the request that started the beta turn
	if !strings.Contains(last, "[plan only: off]") || strings.Index(last, `"beta"`) > strings.Index(last, "[plan only: off]") {
		t.Fatalf("beta request did not carry the off note after the input:\n%s", last)
	}

	// The notes replay where the live loop posted them.
	history := engine.ReplayHistory(s.Log().Snapshot())
	var notes []string
	for _, msg := range history {
		if msg.Role == "user" && strings.HasPrefix(msg.Content, "[plan only") {
			notes = append(notes, msg.Content[:strings.Index(msg.Content, "]")+1])
		}
	}
	if strings.Join(notes, ",") != "[plan only: on],[plan only: off]" {
		t.Fatalf("replayed notes = %q", notes)
	}

	// On again while idle, then reopen: the state is restored and the note is
	// still owed to the model.
	if err := s.SetPlanOnly(true); err != nil {
		t.Fatal(err)
	}
	id := s.ID
	if err := m.Stop(id); err != nil {
		t.Fatal(err)
	}
	r, err := m.Reopen("proj", id)
	if err != nil {
		t.Fatal(err)
	}
	if !r.PlanOnly() || !r.deps.PlanOnly() {
		t.Fatal("reopen lost plan only")
	}
	idles := len(eventsOf(r, event.SessionIdle))
	if err := r.SendInput("gamma"); err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, r, idles+1)
	if exists(ws, "gamma.go") || !exists(ws, "plans/gamma.md") {
		t.Fatalf("after reopen: gamma.go=%v plans/gamma.md=%v", exists(ws, "gamma.go"), exists(ws, "plans/gamma.md"))
	}
	if delivered := eventsOf(r, event.PlanOnlyDelivered); len(delivered) != 3 || delivered[2].Data["plan_only"] != true {
		t.Fatalf("plan_only_delivered after reopen = %+v", delivered)
	}
}

// A context rollover replaces the history the plan-only note lived in, so the
// note is re-posted after the summary, live and on replay alike.
func TestPlanOnlyNoteSurvivesContextRollover(t *testing.T) {
	s := newStopSession(t)
	defer s.Stop()
	loop := &engine.Loop{Client: measuredRolloverTurner{}, Model: "test", Backend: "openai", Emitter: s.emitter}
	loop.SetHistory([]gollama.Message{{Role: "user", Content: strings.Repeat("old history ", 16_000)}})
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.loop = loop
	s.planOnly, s.planOnlyTold = true, true
	s.contextSummary = func(context.Context, []event.Event, []jobs.Info, string) (string, error) {
		return "summary", nil
	}
	if err := s.performContextRollover(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	live := loop.History()
	if len(live) != 2 || live[0].Content != "summary" || live[1].Content != orchestrator.PlanOnlyNote(true) {
		t.Fatalf("history after rollover = %+v", live)
	}
	if replayed := engine.ReplayHistory(s.log.Snapshot()); !reflect.DeepEqual(live, replayed) {
		t.Fatalf("live and replayed histories differ\nlive=%+v\nreplay=%+v", live, replayed)
	}
	if notes, _ := s.planOnlyNote(); notes != nil {
		t.Fatalf("note owed again after rollover re-told it: %q", notes)
	}
}
