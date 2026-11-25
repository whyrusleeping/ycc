package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// writeStub is a scripted OpenAI-compatible chat model: for the latest user
// request "write <path>" it writes <path> in ws (creating or overwriting it)
// and answers "done" once the result is in.
func writeStub(t *testing.T, ws string) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu     sync.Mutex
		bodies []string
		n      atomic.Int64
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
		target, answered := "", false
		for _, m := range req.Messages {
			text := fmt.Sprint(m.Content)
			if m.Role == "user" && strings.HasPrefix(text, "write ") {
				target, answered = strings.TrimPrefix(text, "write "), false
			} else if m.Role == "tool" {
				answered = true
			}
		}
		msg := map[string]any{"role": "assistant", "content": "done"}
		finish := "stop"
		if target != "" && !answered {
			args := map[string]any{"file_path": target, "content": fmt.Sprintf("v%d\n", n.Add(1))}
			if data, err := os.ReadFile(filepath.Join(ws, target)); err == nil {
				sum := sha256.Sum256(data)
				args["policy"], args["expected_revision"] = "overwrite", hex.EncodeToString(sum[:])
			}
			raw, _ := json.Marshal(args)
			msg = map[string]any{"role": "assistant", "content": "", "tool_calls": []map[string]any{{
				"id": fmt.Sprintf("call_%d", n.Load()), "type": "function",
				"function": map[string]any{"name": "Write", "arguments": string(raw)}}}}
			finish = "tool_calls"
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			delta := map[string]any{"role": "assistant", "content": msg["content"]}
			if tc, ok := msg["tool_calls"].([]map[string]any); ok {
				tc[0]["index"] = 0
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

func lastWriteResult(t *testing.T, s *Session) string {
	t.Helper()
	results := eventsOf(s, event.ToolResult)
	if len(results) == 0 {
		t.Fatal("no tool result")
	}
	return fmt.Sprint(results[len(results)-1].Data["result"])
}

// Two sessions writing one file each hear of it once: the second writer in its
// tool result, the first at its next checkpoint without being woken. The
// tool lists both, path lookup includes ended sessions, and turning the
// setting off disables both.
func TestSessionCoordinationOverlapNoticesAndListSessions(t *testing.T) {
	ws := t.TempDir()
	sessionGitAt(t, ws, "init", "-q")
	sessionGitAt(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
	stub, bodies := writeStub(t, ws)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ReclaimAll)

	off, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "write off.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, off, 1)
	if _, err := off.deps.ListSessions(""); !errors.Is(err, orchestrator.ErrCoordinationDisabled) {
		t.Fatalf("list_sessions with the setting off and no lease held: %v", err)
	}
	if err := m.SetSessionCoordination("proj", true); err != nil {
		t.Fatal(err)
	}
	if on, _ := m.SessionCoordination("proj"); !on {
		t.Fatal("setting not persisted")
	}

	a, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "write shared.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, a, 1)
	if strings.Contains(lastWriteResult(t, a), "coordination notice") {
		t.Fatalf("first writer got a notice: %s", lastWriteResult(t, a))
	}
	// A session built before the setting was enabled neither gets nor causes
	// notices.
	if err := off.SendInput("write shared.go"); err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, off, 2)
	if strings.Contains(lastWriteResult(t, off), "coordination notice") || len(a.notices) != 0 {
		t.Fatalf("session built with the setting off took part: %s", lastWriteResult(t, off))
	}

	b, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "write shared.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, b, 1)
	if got := lastWriteResult(t, b); !strings.Contains(got, "[coordination notice] shared.go also holds uncommitted work from session "+a.ID) {
		t.Fatalf("second writer's result lacks the notice: %s", got)
	}
	if err := b.SendInput("write shared.go"); err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, b, 2)
	if got := lastWriteResult(t, b); strings.Contains(got, "coordination notice") {
		t.Fatalf("repeated write repeated the notice: %s", got)
	}

	// A was not woken: no new turn, no delivery yet.
	if got := len(eventsOf(a, event.SessionIdle)); got != 1 || len(eventsOf(a, event.CoordinationNotice)) != 0 {
		t.Fatalf("idle coordinator was woken: idles=%d notices=%d", got, len(eventsOf(a, event.CoordinationNotice)))
	}

	// list_sessions from A shows B with its claim; path= lists both.
	overview, err := m.listSessionsFor(ws, a.ID, "", time.Now())
	if err != nil || !strings.Contains(overview, b.ID) || strings.Contains(overview, "- "+a.ID) ||
		!strings.Contains(overview, "shared.go (just now)") || !strings.Contains(overview, "title: write shared.go") {
		t.Fatalf("overview err=%v:\n%s", err, overview)
	}
	// Clients get the same listing, structured; focus ids are canonical, so
	// an unpadded focus matches its backlog row.
	b.emitter.Emit(event.TaskFocus, map[string]any{"task": "7"})
	b.emitter.Emit(event.TaskFocus, map[string]any{"task": "0007"})
	live, err := m.ProjectLiveSessions("proj")
	if err != nil {
		t.Fatal(err)
	}
	var bLive *LiveSession
	for i := range live {
		if live[i].ID == b.ID {
			bLive = &live[i]
		}
	}
	if bLive == nil || !bLive.InPrimary || bLive.Activity != "idle" || bLive.Title != "write shared.go" ||
		len(bLive.Claims) != 1 || bLive.Claims[0].Path != "shared.go" || bLive.Claims[0].Since.IsZero() ||
		len(bLive.Focus) != 1 || bLive.Focus[0] != "0007" {
		t.Fatalf("structured listing = %+v", live)
	}

	// A's next run receives the notice once, at its checkpoint; A writing the
	// same file again does not notify B again.
	if err := a.SendInput("write shared.go"); err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, a, 2)
	notices := eventsOf(a, event.CoordinationNotice)
	if len(notices) != 1 || notices[0].Actor != "system" ||
		!strings.Contains(fmt.Sprint(notices[0].Data["text"]), "Session "+b.ID) {
		t.Fatalf("coordination notices = %+v", notices)
	}
	if strings.Contains(lastWriteResult(t, a), b.ID) || len(b.notices) != 0 {
		t.Fatalf("same pair notified again: %s (pending for b: %d)", lastWriteResult(t, a), len(b.notices))
	}
	reqs := bodies()
	found := false
	for _, body := range reqs {
		found = found || strings.Contains(body, "just wrote shared.go")
	}
	if !found {
		t.Fatal("the model never saw the notice")
	}
	var replayed []string
	for _, msg := range engine.ReplayHistory(a.Log().Snapshot()) {
		if msg.Role == "user" && strings.HasPrefix(msg.Content, "[coordination notice]") {
			replayed = append(replayed, msg.Content)
		}
	}
	if len(replayed) != 1 || replayed[0] != notices[0].Data["text"] {
		t.Fatalf("replayed notices = %q", replayed)
	}

	// A burst of overlaps reaches the peer in one bounded delivery.
	for i := 0; i < maxNoticeTexts+2; i++ {
		m.claimOverlap(workspacelease.Overlap{Worktree: ws, Path: fmt.Sprintf("burst%02d.go", i), Writer: b.ID, Others: []string{a.ID}})
	}
	if texts, err := a.drainCoordinationNotices(); err != nil || len(texts) != 1 ||
		strings.Count(texts[0], "just wrote") != maxNoticeTexts || !strings.Contains(texts[0], "2 more of your claimed files") {
		t.Fatalf("burst delivery err=%v: %q", err, texts)
	}

	// A project registered inside the same repository shares its claims but
	// never shows up in this project's lookups or notices.
	sub := filepath.Join(ws, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddProject(sub, "sub"); err != nil {
		t.Fatal(err)
	}
	foreign, err := m.Start(Config{Project: "sub", Mode: "chat", Prompt: "write x.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, foreign, 1)
	got, err := m.listSessionsFor(ws, a.ID, "sub/x.go", time.Now())
	if err != nil || strings.Contains(got, foreign.ID) || !strings.Contains(got, "No session of this project claims sub/x.go") {
		t.Fatalf("lookup across projects err=%v:\n%s", err, got)
	}
	if notice := m.claimOverlap(workspacelease.Overlap{Worktree: ws, Path: "sub/x.go", Writer: a.ID, Others: []string{foreign.ID}}); notice != "" {
		t.Fatalf("notice named another project's session: %s", notice)
	}
	elsewhere := t.TempDir()
	if _, err := m.listSessionsFor(ws, a.ID, filepath.Join(elsewhere, "x.go"), time.Now()); err == nil {
		t.Fatal("lookup outside the project succeeded")
	}
	if err := os.Symlink(elsewhere, filepath.Join(ws, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.listSessionsFor(ws, a.ID, "linked/x.go", time.Now()); err == nil {
		t.Fatal("lookup through a symlink out of the project succeeded")
	}

	// Path lookup includes an ended session.
	if err := m.Stop(b.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, live := m.Get(b.ID); return !live })
	claimants, err := m.listSessionsFor(ws, a.ID, "shared.go", time.Now())
	if err != nil || !strings.Contains(claimants, a.ID+" claimed") || !strings.Contains(claimants, b.ID+" claimed just now; ended chat session") {
		t.Fatalf("claimants err=%v:\n%s", err, claimants)
	}

	// Off: the tool reports disabled and pending notices are dropped.
	a.queueCoordinationNotice(overlapNotice{path: "x", session: "s", text: "pending"})
	if err := m.SetSessionCoordination("proj", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.listSessionsFor(ws, a.ID, "", time.Now()); !errors.Is(err, orchestrator.ErrCoordinationDisabled) {
		t.Fatalf("list_sessions with the setting off: %v", err)
	}
	if live, err := m.ProjectLiveSessions("proj"); err != nil || live != nil {
		t.Fatalf("structured listing with the setting off: %+v %v", live, err)
	}
	if texts, err := a.drainCoordinationNotices(); err != nil || texts != nil {
		t.Fatalf("notice delivered after the setting was turned off: %q %v", texts, err)
	}
	if docs.SessionCoordination(t.TempDir()) {
		t.Fatal("another project reads as enabled")
	}
}

// list_sessions lists a session's definite claims first and its ambiguous
// ones on a line of their own.
func TestClaimLinesSeparatesAmbiguousClaims(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	got := claimLines([]PathClaim{
		{Path: "gen.go", Since: now.Add(-2 * time.Minute), Ambiguous: true},
		{Path: "mine.go", Since: now.Add(-time.Hour)},
	}, now)
	want := "    claims 1 path(s): mine.go (1h ago)\n" +
		"    ambiguous claims on 1 path(s), changed while another session's shell also ran, so possibly not this session's: gen.go (2m ago)\n"
	if got != want {
		t.Fatalf("claimLines =\n%q\nwant\n%q", got, want)
	}
	if got := claimLines(nil, now); got != "" {
		t.Fatalf("claimLines(nil) = %q, want empty", got)
	}
}

// The list_backlog footer flags a session's ambiguous claims, bounded, while
// coordination is on, and leaves its definite claims to list_sessions.
func TestOtherSessionsNoteFlagsAmbiguousClaims(t *testing.T) {
	ws := t.TempDir()
	sessionGitAt(t, ws, "init", "-q")
	sessionGitAt(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
	stub, _ := writeStub(t, ws)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ReclaimAll)
	a, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "write a.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, a, 1)
	m.ownership.RecordAmbiguous(ws, []string{"g1.go", "g2.go", "g3.go", "g4.go", "g5.go"}, []string{a.ID, "s_other"})
	if note := m.otherSessionsNote(ws, "", time.Now()); strings.Contains(note, "ambiguous") {
		t.Fatalf("footer with coordination off mentions claims:\n%s", note)
	}
	if err := m.SetSessionCoordination("proj", true); err != nil {
		t.Fatal(err)
	}
	note := m.otherSessionsNote(ws, "", time.Now())
	if !strings.Contains(note, "ambiguous claims on 5 path(s)") || !strings.Contains(note, ", … 2 more") || strings.Contains(note, "a.go") {
		t.Fatalf("footer does not flag the ambiguous claims alone, bounded:\n%s", note)
	}
}
