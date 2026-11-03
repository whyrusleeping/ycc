package session

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/project"
)

// A real watch-mode session (stubbed model) ends through watch_report: the
// report is recorded, the run is finished, and its session is reclaimed.
func TestWatchRunEndToEndThroughWatchReport(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		n := len(bodies)
		mu.Unlock()
		msg := map[string]any{"role": "assistant", "content": "Done."}
		finish := "stop"
		if n == 1 {
			args, _ := json.Marshal(map[string]any{"status": "ok", "summary": "lag 1s, disks fine", "notes": "baseline lag 1s"})
			msg = map[string]any{"role": "assistant", "content": "", "tool_calls": []map[string]any{{
				"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "watch_report", "arguments": string(args)},
			}}}
			finish = "tool_calls"
		}
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if stream, _ := req["stream"].(bool); stream {
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
	defer stub.Close()

	t.Setenv("YCC_WATCH_TEST_KEY", "x")
	cfgPath := filepath.Join(t.TempDir(), "ycc.toml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(`[models.stub]
backend = "openai"
base_url = %q
model = "stub-model"
key_env = "YCC_WATCH_TEST_KEY"

[roles]
coordinator = "stub"
implementer = "stub"
reviewers = ["stub"]
`, stub.URL)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	m := NewManager(config.NewRegistry(cfg), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	m.watchPoll = 5 * time.Millisecond
	t.Cleanup(m.ReclaimAll)

	if _, err := m.CreateWatch("proj", WatchDefinition{Name: "live", Brief: "check the live system", Interval: Duration(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	m.tickWatches()
	var rec watchRecord
	waitFor(t, func() bool {
		m.watchMu.Lock()
		defer m.watchMu.Unlock()
		rec, _ = loadWatch(ws, "live")
		return len(rec.Runs) == 1 && !rec.Runs[0].Finished.IsZero()
	})
	if rec.Last == nil || rec.Last.Kind != WatchReportRun || rec.Last.Status != "ok" || rec.Notes != "baseline lag 1s" {
		t.Fatalf("recorded state = last %+v notes %q", rec.Last, rec.Notes)
	}
	if _, live := m.Get(rec.Runs[0].SessionID); live {
		t.Fatal("finished run session was not reclaimed")
	}
	mu.Lock()
	first := bodies[0]
	mu.Unlock()
	if !strings.Contains(first, "check the live system") || !strings.Contains(first, "Watch run context") || !strings.Contains(first, `"watch_report"`) {
		t.Fatalf("first model request lacks brief, run context, or watch_report tool:\n%.2000s", first)
	}
}
