package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
)

func checkSummaryEventEquivalence(t testing.TB, line []byte) {
	t.Helper()
	var full, slim event.Event
	fullErr := json.Unmarshal(line, &full)
	slimErr := decodeSummaryEvent(line, &slim)
	if (fullErr == nil) != (slimErr == nil) {
		t.Fatalf("acceptance differs for %q: full=%v summary=%v", line, fullErr, slimErr)
	}
	if fullErr != nil {
		return
	}
	want := reduceSessionSummary("workspace", "session/events.jsonl", []event.Event{full})
	got := reduceSessionSummary("workspace", "session/events.jsonl", []event.Event{slim})
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("summary differs for %q:\nfull=%+v\nsummary=%+v", line, want, got)
	}
}

func TestSummaryReaderFieldTolerance(t *testing.T) {
	lines := []string{
		`null`, `{}`, `[]`, `true`, `42`, `"event"`, `{"data":null}`,
		`{"data":[]}`, `{"data":"bad"}`, `{"data":false}`, `{"data":1}`,
		`{"type":"user_input","data":{"text":"escaped \\\" \\ \u263a \ud800"}}`,
		`{"TYPE":"user_input","DATA":{"text":"first"},"data":{"text":"last"}}`,
		`{"type":"user_input","data":{"text":"first"},"DATA":null}`,
		`{"type":"session_started","data":{"mode":"chat"},"data":{"workspace":"here"}}`,
		`{"type":"user_input","data":{"TEXT":"ignored","te\u0078t":"selected"}}`,
		`{"Tſ":"2026-01-02T03:04:05Z","ſeq":1,"TYPE":"user_input","data":{"text":"folded envelope"}}`,
		`{"type":"tool_result","data":{"text":"title"},"type":"user_input"}`,
		`{"type":"user_input","data":{"text":"discarded"},"type":"tool_result"}`,
		`{"data":[],"data":null}`, `{"data":{"ignored":1e999},"data":null}`,
		`{"type":"model_turn","actor":null,"data":{"usage":{"total":1.9},"context_tokens_est":-1}}`,
		`{"type":"model_turn","data":{"usage":{"total":1e2},"context_tokens_est":2e2}}`,
		`{"type":"tool_call","data":{"args":{"deep":[true,false,null,{},[],{"x":"}]\\\""}]}}}`,
		`{"type":"tool_call","data":{"ignored":1e999}}`,
		`{"type":"tool_call","data":{"ignored":[{"x":-1e999}]}}`,
		`{"type":"tool_call","data":{"ignored":1e-999}}`,
		`{"type":"tool_call","unknown":1e999}`, // unknown envelope fields are NOT decoded into any
		`{"seq":1.2}`, `{"seq":"1"}`, `{"seq":9223372036854775808}`,
		`{"actor":[]}`, `{"type":5}`, `{"transient":"true"}`, `{"ts":10}`, `{"ts":"bad"}`,
		`{"ts":"2026-01-02T03:04:05.123456789+02:00"}`, `{"ts":null}`,
		`{"seq":1,"seq":null,"actor":"user","actor":null,"type":"user_input","type":null}`,
		`{"data":{"ignored":` + strings.Repeat("9", 309) + `}}`,
		`{"data":{"ignored":` + strings.Repeat("9", 308) + `}}`,
		`{"data":{"ignored":"bad\q"}}`, `{"data":{"ignored":[1,]}}`,
		`{"data":{"ignored":"partial`, `{"type":"session_idle"} garbage`,
		`{"data":{"mode":"work"},"type":"session_started"}`,
	}
	for _, typ := range []event.Type{event.SessionStarted, event.UserInput, event.TaskFocus, event.ModelTurn} {
		for _, value := range []string{`null`, `true`, `123`, `"value"`, `[]`, `{}`, `{"total":"bad"}`, `["x"]`} {
			lines = append(lines, fmt.Sprintf(`{"type":%q,"data":{"mode":%s,"workspace":%s,"text":%s,"task":%s,"title":%s,"model_name":%s,"usage":%s,"context_tokens_est":%s}}`, typ, value, value, value, value, value, value, value, value))
		}
	}
	for i, line := range lines {
		t.Run(fmt.Sprint(i), func(t *testing.T) { checkSummaryEventEquivalence(t, []byte(line)) })
	}
}

func TestSummaryReaderLogEquivalence(t *testing.T) {
	valid := strings.Join([]string{
		`{"ts":"2026-01-02T00:00:00Z","type":"session_started","data":{"mode":"work","workspace":"recorded"}}`,
		fmt.Sprintf(`{"type":"user_input","data":{"text":%q}}`, defaultPrompt("work")),
		`{"type":"task_focus","data":{"task":"001","title":"First task"}}`,
		`{"type":"task_focus","data":{"task":"001"}}`,
		`{"type":"session_error","data":{"msg":"failure"}}`,
		`{"type":"user_input_delivered"}`,
		`{"type":"model_turn","data":{"model_name":"one","usage":{"total":123},"context_tokens_est":456}}`,
		`{"type":"model_turn","actor":"implementer","data":{"model_name":"two","usage":{"total":321},"context_tokens_est":9999}}`,
		`{"type":"model_turn","data":{"model_name":"one","usage":{"total":100}}}`,
		`{"type":"tool_call","data":{"args":{"payload":[1,2,3]}}}`,
		`{"type":"tool_result","data":{"output":"large output is discarded"}}`,
		`{"type":"interrupted"}`, `{"type":"resumed"}`, `{"type":"session_idle"}`,
		`{"type":"session_stopped"}`, `{"type":"session_reopened"}`,
		`{"ts":"2026-01-02T01:00:00Z","type":"unknown_future_event","data":{"blob":{"a":1}}}`,
	}, "\n")
	for name, content := range map[string]string{
		"valid": valid, "empty": "\n", "null": "null\n",
		"partial": valid + "\n{\"data\":", "malformed": "bad\n" + valid + "\n[]\n{}\n",
		"scanner_limit": valid + "\n" + strings.Repeat("x", 8*1024*1024),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			compareSummaryLog(t, path)
		})
	}
	compareSummaryLog(t, filepath.Join(t.TempDir(), "missing"))
}

func compareSummaryLog(t testing.TB, path string) (time.Duration, time.Duration) {
	t.Helper()
	start := time.Now()
	full, fullOK := readEventsTolerantResult(path)
	var want SessionSummary
	if len(full) != 0 {
		want = reduceSessionSummary("workspace", path, full)
	}
	fullTime := time.Since(start)
	start = time.Now()
	slim, slimOK := readSummaryEventsTolerantResult(path)
	var got SessionSummary
	if len(slim) != 0 {
		got = reduceSessionSummary("workspace", path, slim)
	}
	slimTime := time.Since(start)
	if fullOK != slimOK || len(full) != len(slim) || !reflect.DeepEqual(want, got) {
		t.Fatalf("%s: full=(%d events, cacheable=%v, %+v) summary=(%d events, cacheable=%v, %+v)", path, len(full), fullOK, want, len(slim), slimOK, got)
	}
	return fullTime, slimTime
}

func TestSummaryReaderDiscardsPayload(t *testing.T) {
	var ev event.Event
	if err := decodeSummaryEvent([]byte(`{"type":"tool_result","data":{"output":"payload","args":[1,2],"nested":{"x":3}}}`), &ev); err != nil {
		t.Fatal(err)
	}
	if len(ev.Data) != 0 {
		t.Fatalf("retained unused data: %+v", ev.Data)
	}
}

func FuzzSummaryReaderEquivalence(f *testing.F) {
	for _, seed := range []string{
		`{}`, `null`, `{"type":"tool_result","data":{"output":[1,"two",null]}}`,
		`{"ts":"2026-01-02T00:00:00Z","type":"model_turn","data":{"usage":{"total":10},"context_tokens_est":20}}`,
		`{"type":"user_input","data":{"text":"hello"}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, line []byte) { checkSummaryEventEquivalence(t, line) })
}

// Opt-in read-only comparison against real logs. Run only on quiescent logs so
// the two reads see the same version. No local log or cache files are written.
func TestSummaryReaderLocalLogs(t *testing.T) {
	workspaces := os.Getenv("YCC_HISTORY_COMPARE_WORKSPACES")
	if workspaces == "" {
		t.Skip("set YCC_HISTORY_COMPARE_WORKSPACES (path-list) for read-only comparison")
	}
	var fullTime, slimTime, coldTime, warmTime time.Duration
	var logs int
	var bytes int64
	for _, ws := range filepath.SplitList(workspaces) {
		paths, err := filepath.Glob(filepath.Join(ws, ".ycc", "sessions", "*", "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			a, b := compareSummaryLog(t, path)
			fullTime += a
			slimTime += b
			logs++
			bytes += info.Size()
		}
		m := &Manager{}
		start := time.Now()
		if _, err := m.scanSessionHistoryCached(ws); err != nil {
			t.Fatal(err)
		}
		coldTime += time.Since(start)
		start = time.Now()
		if _, err := m.scanSessionHistoryCached(ws); err != nil {
			t.Fatal(err)
		}
		warmTime += time.Since(start)
	}
	t.Logf("%d logs, %.1f MiB: full read+reduce %s; selective read+reduce %s; manager cache cold %s / warm %s", logs, float64(bytes)/(1<<20), fullTime, slimTime, coldTime, warmTime)
}

func BenchmarkSessionHistoryPayloadReader(b *testing.B) {
	ws := b.TempDir()
	var evs []event.Event
	evs = append(evs, event.Event{TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "work"}})
	// Realistic large tool output plus structured tool arguments. Both escaped
	// strings and nested arbitrary values dominated the old reader's allocations.
	for i := 0; i < 300; i++ {
		evs = append(evs,
			event.Event{TS: ts(i + 2), Type: event.ModelTurn, Data: map[string]any{"model_name": "model", "usage": event.Usage{Total: 1000}, "context_tokens_est": 12000}},
			event.Event{Type: event.ToolCall, Data: map[string]any{"name": "Write", "args": map[string]any{"file_path": "source.go", "content": strings.Repeat("func example() { println(\"hello\") }\n", 200)}}},
			event.Event{Type: event.ToolResult, Data: map[string]any{"output": strings.Repeat("line\tquoted \"output\"\n", 2000), "details": []any{map[string]any{"a": 1, "b": true}, []any{1, 2, "three"}}}},
		)
	}
	writeSession(b, ws, "payload", evs)
	path := filepath.Join(ws, ".ycc", "sessions", "payload", "events.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	for _, reader := range []struct {
		name string
		read func(string) ([]event.Event, bool)
	}{{"full", readEventsTolerantResult}, {"selective", readSummaryEventsTolerantResult}} {
		b.Run(reader.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(info.Size())
			for i := 0; i < b.N; i++ {
				events, ok := reader.read(path)
				if !ok || len(events) != len(evs) {
					b.Fatal("incomplete read")
				}
				summary := reduceSessionSummary(ws, path, events)
				if summary.ToolCalls != 300 || summary.TotalTokens != 300000 {
					b.Fatal(summary)
				}
			}
		})
	}
}
