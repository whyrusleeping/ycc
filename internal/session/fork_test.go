package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/usage"
)

const forkPic = "a_0123456789abcdef0123456789abcdef"

func forkParent(t *testing.T) (ws, logPath string) {
	t.Helper()
	ws = t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	absWS, _ := filepath.Abs(ws)
	writeSession(t, ws, "s_parent", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "chat", "workspace": absWS}},
		{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "first",
			"images": []any{map[string]any{"attachment_id": forkPic, "media_type": "image/png"}}}},
		{Seq: 3, TS: ts(3), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "one", "tool_calls": 1}},
		{Seq: 4, TS: ts(4), Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"id": "c1", "name": "Bash", "args": "{}"}},
		// A steer queued mid-run, delivered after the tool result.
		{Seq: 5, TS: ts(5), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "steer", "queued": true}},
		{Seq: 6, TS: ts(6), Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "c1", "result": "ok"}},
		{Seq: 7, TS: ts(7), Actor: "user", Type: event.UserInputDelivered, Data: map[string]any{"seq": 5, "text": "steer"}},
		{Seq: 8, TS: ts(8), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "two"}},
		{Seq: 9, TS: ts(9), Type: event.SessionIdle, Data: map[string]any{"report": "two"}},
		{Seq: 10, TS: ts(10), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "second"}},
		{Seq: 11, TS: ts(11), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "three"}},
	})
	dir := filepath.Join(ws, ".ycc", "sessions", "s_parent")
	if err := os.MkdirAll(filepath.Join(dir, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attachments", forkPic), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	return ws, filepath.Join(dir, "events.jsonl")
}

func seqs(evs []event.Event) []int {
	var out []int
	for _, ev := range evs {
		out = append(out, ev.Seq)
	}
	return out
}

func TestForkSessionCutsBeforeUserTurn(t *testing.T) {
	ws, parentLog := forkParent(t)
	before, err := os.ReadFile(parentLog)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(testRegistry(), ws)
	fork, err := m.ForkSession("", "s_parent", 10)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if fork.Text != "second" || fork.SessionID == "" || fork.SessionID == "s_parent" {
		t.Fatalf("fork = %+v", fork)
	}
	after, _ := os.ReadFile(parentLog)
	if string(after) != string(before) {
		t.Fatal("parent log changed")
	}
	evs, err := event.ReadLog(filepath.Join(ws, ".ycc", "sessions", fork.SessionID, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := seqs(evs), []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fork seqs = %v, want %v", got, want)
	}
	last := evs[len(evs)-1]
	if last.Type != event.SessionForked || last.Data["parent"] != "s_parent" || last.Data["at_seq"] != float64(10) {
		t.Fatalf("marker = %+v", last)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, ".ycc", "sessions", fork.SessionID, "attachments", forkPic)); string(got) != "png" {
		t.Fatalf("attachment not copied: %q", got)
	}

	sum := reduceSessionSummary(ws, filepath.Join(ws, ".ycc", "sessions", fork.SessionID, "events.jsonl"), evs)
	if sum.ForkedFrom != "s_parent" || sum.Status != event.StatusIdle {
		t.Fatalf("summary forked_from=%q status=%q", sum.ForkedFrom, sum.Status)
	}

	// The first input reopens the fork on the copied history, not the parent's.
	sess, err := m.Reopen("", fork.SessionID)
	if err != nil {
		t.Fatalf("Reopen fork: %v", err)
	}
	defer sess.Stop()
	if got, want := sess.currentLoop().History(), forkedHistory(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened history = %+v, want %+v", got, want)
	}
	for _, msg := range sess.currentLoop().History() {
		if msg.Content == "second" || msg.Content == "three" {
			t.Fatalf("history includes the cut turn: %+v", msg)
		}
	}
}

// forkedHistory is the history the fork's copied events replay to.
func forkedHistory(evs []event.Event) []gollama.Message {
	return []gollama.Message{
		{Role: "user", Content: "first\n\n[The original message included 1 picture attachment(s); image bytes are unavailable after session replay.]"},
		{Role: "assistant", Content: "one", ToolCalls: []gollama.ToolCall{{ID: "c1", Type: "function", Function: gollama.ToolCallFunction{Name: "Bash", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "c1", Content: "ok"},
		{Role: "user", Content: "steer"},
		{Role: "assistant", Content: "two"},
	}
}

// A steer is cut where the model received it, and the fork's first input
// takes its place: the model's first request ends with the edited steer after
// the tool result it originally followed, with nothing run before it.
func TestForkSessionSteerResumesWithEditedSteer(t *testing.T) {
	ws, _ := forkParent(t)
	stub, bodies := writeStub(t, ws)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	t.Cleanup(m.ReclaimAll)
	fork, err := m.ForkSession("", "s_parent", 5)
	if err != nil {
		t.Fatal(err)
	}
	if fork.Text != "steer" {
		t.Fatalf("text = %q", fork.Text)
	}
	evs, _ := event.ReadLog(filepath.Join(ws, ".ycc", "sessions", fork.SessionID, "events.jsonl"))
	if got, want := seqs(evs), []int{1, 2, 3, 4, 6, 7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fork seqs = %v, want %v", got, want)
	}
	sess, err := m.Reopen("", fork.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.SendInput("edited steer"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(bodies()) > 0 })
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(bodies()[0]), &req); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, msg := range req.Messages {
		if msg.Role != "system" {
			got = append(got, msg.Role+":"+fmt.Sprint(msg.Content))
		}
	}
	want := []string{
		"user:first\n\n[The original message included 1 picture attachment(s); image bytes are unavailable after session replay.]",
		"assistant:one", "tool:ok", "user:edited steer",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("first request messages = %q, want %q", got, want)
	}
}

// A steer delivered while its turn's tool-call batch was still open reached
// the model only after the batch's last result.
func TestForkSessionSteerDeliveredMidBatch(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	writeSession(t, ws, "s_b", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "chat"}},
		{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "go"}},
		{Seq: 3, TS: ts(3), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"tool_calls": 2}},
		{Seq: 4, TS: ts(4), Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"id": "c1", "name": "Bash"}},
		{Seq: 5, TS: ts(5), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "steer", "queued": true}},
		{Seq: 6, TS: ts(6), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "later", "queued": true}},
		{Seq: 7, TS: ts(7), Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "c1", "result": "one"}},
		{Seq: 8, TS: ts(8), Actor: "user", Type: event.UserInputDelivered, Data: map[string]any{"seq": 5, "text": "steer"}},
		{Seq: 9, TS: ts(9), Actor: "user", Type: event.UserInputDelivered, Data: map[string]any{"seq": 6, "text": "later"}},
		{Seq: 10, TS: ts(10), Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"id": "c2", "name": "Bash"}},
		{Seq: 11, TS: ts(11), Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "c2", "result": "two"}},
		{Seq: 12, TS: ts(12), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "ok"}},
	})
	m := NewManager(testRegistry(), ws)
	fork, err := m.ForkSession("", "s_b", 5)
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := event.ReadLog(filepath.Join(ws, ".ycc", "sessions", fork.SessionID, "events.jsonl"))
	// The later steer was posted after this one, so neither its delivery nor
	// its echo is copied.
	if got, want := seqs(evs), []int{1, 2, 3, 4, 7, 10, 11, 12}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fork seqs = %v, want %v", got, want)
	}
}

// What followed the steer in the parent stays out of the fork, even when its
// batch ended early (finish skips the rest) or a job report was delivered
// after it, and a pause does not end the batch: the model's first request
// ends with the edited steer after the batch's results.
func TestForkSessionSteerCutBoundaries(t *testing.T) {
	for _, mode := range []string{"control stop", "job report", "pause"} {
		t.Run(mode, func(t *testing.T) {
			ws := t.TempDir()
			if _, err := git.Open(ws); err != nil {
				t.Fatal(err)
			}
			var evs []event.Event
			add := func(actor string, typ event.Type, data map[string]any) {
				n := len(evs) + 1
				evs = append(evs, event.Event{Seq: n, TS: ts(n), Actor: actor, Type: typ, Data: data})
			}
			calls, second := 2, "Bash"
			if mode == "control stop" {
				calls, second = 3, "finish"
			}
			add("coordinator", event.SessionStarted, map[string]any{"mode": "chat"})
			add("user", event.UserInput, map[string]any{"text": "initial"})
			add("coordinator", event.ModelTurn, map[string]any{"text": "checking", "tool_calls": calls})
			add("coordinator", event.ToolCall, map[string]any{"id": "c1", "name": "Bash", "args": "{}"})
			add("user", event.UserInput, map[string]any{"text": "steer", "queued": true}) // seq 5
			if mode == "job report" {
				add("coordinator", event.JobStarted, map[string]any{"id": "j1", "kind": "shell", "label": "job"})
			}
			add("coordinator", event.ToolResult, map[string]any{"id": "c1", "result": "one"})
			add("user", event.UserInputDelivered, map[string]any{"seq": 5, "text": "steer"})
			if mode == "job report" {
				add("coordinator", event.JobFinished, map[string]any{"id": "j1", "kind": "shell", "status": "done", "tail": "job result"})
				add("user", event.JobNotified, map[string]any{"id": "j1", "kind": "shell", "status": "done", "text": "job result"})
			}
			if mode == "pause" {
				add("coordinator", event.Interrupted, nil)
				add("coordinator", event.Resumed, nil)
			}
			add("coordinator", event.ToolCall, map[string]any{"id": "c2", "name": second, "args": "{}"})
			add("coordinator", event.ToolResult, map[string]any{"id": "c2", "result": "two"})
			if mode == "control stop" {
				add("coordinator", event.SessionIdle, map[string]any{"report": "stopped early"})
				add("user", event.UserInput, map[string]any{"text": "later turn"})
			}
			add("coordinator", event.ModelTurn, map[string]any{"text": "reply"})
			writeSession(t, ws, "s_parent", evs)
			stub, bodies := writeStub(t, ws)
			m := NewManager(stubRegistry(t, stub.URL), ws)
			t.Cleanup(m.ReclaimAll)
			fork, err := m.ForkSession("", "s_parent", 5)
			if err != nil {
				t.Fatal(err)
			}
			sess, err := m.Reopen("", fork.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.SendInput("edited steer"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { return len(bodies()) > 0 })
			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content any    `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal([]byte(bodies()[0]), &req); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, msg := range req.Messages {
				if msg.Role != "system" {
					got = append(got, msg.Role+":"+fmt.Sprint(msg.Content))
				}
			}
			want := []string{"user:initial", "assistant:checking", "tool:one", "tool:two", "user:edited steer"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("first request messages = %q, want %q", got, want)
			}
			waitIdleCount(t, sess, 1)
			if len(bodies()) != 1 {
				t.Fatalf("model requests = %d, want 1 (no inherited report afterwards)", len(bodies()))
			}
		})
	}
}

// An ask_user question still unanswered at the cut is dropped: the fork's
// first input is its replacement turn, not the question's answer.
func TestForkSessionDropsPendingQuestion(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	writeSession(t, ws, "s_q", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "chat"}},
		{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "go"}},
		{Seq: 3, TS: ts(3), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"tool_calls": 1}},
		{Seq: 4, TS: ts(4), Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"id": "q1", "name": "ask_user"}},
		{Seq: 5, TS: ts(5), Actor: "coordinator", Type: event.QuestionAsked, Data: map[string]any{"question": "which?"}},
		{Seq: 6, TS: ts(6), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "steer", "queued": true}},
	})
	m := NewManager(testRegistry(), ws)
	t.Cleanup(m.ReclaimAll)
	fork, err := m.ForkSession("", "s_q", 6)
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := event.ReadLog(filepath.Join(ws, ".ycc", "sessions", fork.SessionID, "events.jsonl"))
	if findResumableQuestion(evs[:len(evs)-1]) == nil {
		t.Fatal("fixture: copied prefix should end on a restorable question")
	}
	if sessionWaitingInput(evs) {
		t.Fatal("fork summary still waits on the question")
	}
	sess, err := m.Reopen("", fork.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.PendingQuestion() {
		t.Fatal("fork restored the parent's question")
	}
}

// A steer echo with no delivery before the cut never reached the model there,
// so the fork leaves it out rather than showing it queued forever.
func TestForkSessionDropsUndeliveredSteer(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	writeSession(t, ws, "s_p", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "chat"}},
		{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "a"}},
		{Seq: 3, TS: ts(3), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "working"}},
		{Seq: 4, TS: ts(4), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "b", "queued": true}},
		{Seq: 5, TS: ts(5), Type: event.SessionStopped},
		{Seq: 6, TS: ts(6), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "c"}},
	})
	m := NewManager(testRegistry(), ws)
	fork, err := m.ForkSession("", "s_p", 6)
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := event.ReadLog(filepath.Join(ws, ".ycc", "sessions", fork.SessionID, "events.jsonl"))
	if got, want := seqs(evs), []int{1, 2, 3, 5, 6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fork seqs = %v, want %v", got, want)
	}
}

// Background jobs still running at the cut belong to the parent: the fork
// settles them as lost without a replayed note the model would answer first,
// and without an automatic notification.
func TestForkSessionSettlesRunningJobs(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	writeSession(t, ws, "s_jobs", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "work"}},
		{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "go"}},
		{Seq: 3, TS: ts(3), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"tool_calls": 1}},
		{Seq: 4, TS: ts(4), Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"id": "c1", "name": "Bash"}},
		{Seq: 5, TS: ts(5), Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "j1", "kind": "shell", "label": "make test"}},
		{Seq: 6, TS: ts(6), Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "c1", "result": "started j1"}},
		{Seq: 7, TS: ts(7), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "waiting on tests"}},
		{Seq: 8, TS: ts(8), Type: event.SessionIdle, Data: map[string]any{"report": "waiting on tests", "awaiting_jobs": true}},
		{Seq: 9, TS: ts(9), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "next"}},
	})
	m := NewManager(testRegistry(), ws)
	fork, err := m.ForkSession("", "s_jobs", 9)
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := event.ReadLog(filepath.Join(ws, ".ycc", "sessions", fork.SessionID, "events.jsonl"))
	if got, want := seqs(evs), []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fork seqs = %v, want %v", got, want)
	}
	if claim := evs[8]; claim.Type != event.JobClaimed || claim.Data["id"] != "j1" || claim.Data["status"] != "lost" {
		t.Fatalf("job claim = %+v", claim)
	}
	if h := engine.ReplayHistory(evs); h[len(h)-1].Role != "assistant" {
		t.Fatalf("replay ends with %q, want the answered turn: %+v", h[len(h)-1].Role, h)
	}
	job, ok := engine.RestoreJobs(evs).Get("j1")
	if !ok || job.Status() != jobs.Lost {
		t.Fatalf("restored job = %+v, %v", job, ok)
	}
}

// The fork accounts only for its own work: copied turns and commits stay the
// parent's, and the fork reviews against its own baseline.
func TestForkSessionOwnsOnlyItsOwnWork(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PersistBaseline("s_parent", repo.OpenBaseline()); err != nil {
		t.Fatal(err)
	}
	writeSession(t, ws, "s_parent", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "work"}},
		{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "first"}},
		{Seq: 3, TS: ts(3), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "done", "model_name": "a",
			"usage": event.Usage{Input: 90, Output: 10, Total: 100}}},
		{Seq: 4, TS: ts(4), Type: event.CommitMade, Data: map[string]any{"sha": "abc", "message": "first commit"}},
		{Seq: 5, TS: ts(5), Type: event.BudgetWarning},
		{Seq: 6, TS: ts(6), Type: event.SessionIdle},
		{Seq: 7, TS: ts(7), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "second"}},
	})
	m := NewManager(testRegistry(), ws)
	before, err := m.UsageReport("", usage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := m.ForkSession("", "s_parent", 7)
	if err != nil {
		t.Fatal(err)
	}
	after, err := m.UsageReport("", usage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if before.Total.Tokens.Total != 100 || after.Total.Tokens.Total != 100 {
		t.Fatalf("project tokens before=%d after=%d, want 100 both", before.Total.Tokens.Total, after.Total.Tokens.Total)
	}
	got := m.linkCommits("", []git.Commit{{SHA: "rewritten", Subject: "first commit", AuthoredAt: ts(4)}})
	if got[0].SessionID != "s_parent" {
		t.Fatalf("commit linked to %q, want s_parent (fork %s)", got[0].SessionID, fork.SessionID)
	}
	evs, _ := event.ReadLog(filepath.Join(ws, ".ycc", "sessions", fork.SessionID, "events.jsonl"))
	if sum := reduceSessionSummary(ws, "", evs); sum.TotalTokens != 0 || len(sum.ModelUsage) != 0 {
		t.Fatalf("fork summary tokens = %d %+v", sum.TotalTokens, sum.ModelUsage)
	}
	if _, err := repo.LoadBaseline(fork.SessionID); err != nil {
		t.Fatalf("fork baseline: %v", err)
	}
	if _, _, err := m.WorkingChanges("", fork.SessionID, ""); err != nil {
		t.Fatalf("fork WorkingChanges: %v", err)
	}
	sess, err := m.Reopen("", fork.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Stop()
	sess.mu.Lock()
	warned := sess.budgetWarned
	sess.mu.Unlock()
	if warned {
		t.Fatal("fork inherited the parent's budget warning")
	}
}

func TestForkSessionRejects(t *testing.T) {
	ws, _ := forkParent(t)
	m := NewManager(testRegistry(), ws)
	for _, seq := range []int{0, 3, 99} {
		if _, err := m.ForkSession("", "s_parent", seq); !errors.Is(err, ErrInvalidForkPoint) {
			t.Fatalf("seq %d: err = %v, want ErrInvalidForkPoint", seq, err)
		}
	}
	for _, id := range []string{"s_missing", "../s_parent", ""} {
		if _, err := m.ForkSession("", id, 10); !errors.Is(err, ErrUnknownSession) {
			t.Fatalf("id %q: err = %v, want ErrUnknownSession", id, err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(ws, ".ycc", "sessions"))
	if len(entries) != 1 {
		t.Fatalf("rejected forks left %d session dirs", len(entries))
	}
}

// A fork that cannot capture its own git baseline would open with review and
// commit disabled, so it is not created at all.
func TestForkSessionRequiresBaseline(t *testing.T) {
	ws, _ := forkParent(t)
	if out, err := exec.Command("git", "-C", ws, "symbolic-ref", "HEAD", "refs/heads/unborn").CombinedOutput(); err != nil {
		t.Fatalf("unborn HEAD: %v %s", err, out)
	}
	m := NewManager(testRegistry(), ws)
	if _, err := m.ForkSession("", "s_parent", 10); err == nil {
		t.Fatal("fork succeeded without a baseline")
	}
	entries, _ := os.ReadDir(filepath.Join(ws, ".ycc", "sessions"))
	if len(entries) != 1 {
		t.Fatalf("failed fork left %d session dirs", len(entries))
	}
}
