package session

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/notify"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// notifySink records the webhook notifications the watcher pushes.
type notifySink struct {
	*httptest.Server
	mu   sync.Mutex
	recs []notifyRec
}

type notifyRec struct {
	tags string // event kind
	body string
}

func newNotifySink(t *testing.T) *notifySink {
	t.Helper()
	s := &notifySink{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.recs = append(s.recs, notifyRec{tags: r.Header.Get("Tags"), body: string(b)})
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *notifySink) all() []notifyRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]notifyRec, len(s.recs))
	copy(out, s.recs)
	return out
}

// newTestManager builds a Manager with a notifier pointed at the sink.
func newTestManager(url string) (*Manager, *notify.Notifier) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"c": {Backend: "ollama", Model: "m"}},
		Roles:  config.Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
	})
	m := NewManager(reg, "")
	n := notify.New(config.Notify{URL: url})
	m.SetNotifier(n)
	return m, n
}

func TestNotifyWatcherMapsEvents(t *testing.T) {
	sink := newNotifySink(t)
	m, n := newTestManager(sink.URL)

	ws := t.TempDir()
	logPath := filepath.Join(ws, ".ycc", "sessions", "s1", "events.jsonl")
	log, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	defer log.Close()

	m.startNotifyWatcher(ws, "s1", log)
	em := event.NewEmitter(log, "coordinator")

	// Auto-answered autonomous ask: nobody is waiting, so it must NOT notify.
	em.Emit(event.QuestionAsked, askData("auto question", nil, true))
	// A real (blocking) question, multi-line — first line only.
	em.Emit(event.QuestionAsked, askData("Which approach?\nmore detail here", nil, false))
	// A batch ask: first prompt + "(+N more)".
	em.Emit(event.QuestionAsked, askManyData([]orchestrator.Question{
		{Prompt: "Q one"}, {Prompt: "Q two"}, {Prompt: "Q three"},
	}, false))
	em.Emit(event.SessionIdle, map[string]any{"report": "All done.\nsecond line"})
	em.Emit(event.SessionError, map[string]any{"msg": "boom happened"})
	em.Emit(event.SubagentFinished, map[string]any{"role": "implementer", "blocked": true})
	// A non-blocked subagent_finished must NOT notify.
	em.Emit(event.SubagentFinished, map[string]any{"role": "implementer"})

	// Give the watcher goroutine a moment to consume and dispatch, then flush the
	// async sends.
	waitFor(t, func() bool { return len(sink.all()) >= 5 })
	n.Flush()

	got := sink.all()
	var kinds []string
	byKind := map[string]string{}
	for _, r := range got {
		kinds = append(kinds, r.tags)
		byKind[r.tags] = r.body
	}

	// Exactly the five expected kinds, no auto-question, no non-blocked finish.
	countKind := func(k string) int {
		n := 0
		for _, r := range got {
			if r.tags == k {
				n++
			}
		}
		return n
	}
	if countKind("question") != 2 {
		t.Errorf("want 2 question notifications (real + batch), got %d (%v)", countKind("question"), kinds)
	}
	if countKind("idle") != 1 || countKind("error") != 1 || countKind("blocked") != 1 {
		t.Errorf("unexpected kind counts: %v", kinds)
	}
	if len(got) != 5 {
		t.Fatalf("want 5 notifications total, got %d: %v", len(got), kinds)
	}

	// Line content checks.
	assertBody := func(kind, want string) {
		found := false
		for _, r := range got {
			if r.tags == kind && strings.Contains(r.body, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no %s notification containing %q; got %v", kind, want, got)
		}
	}
	assertBody("question", "Which approach?")
	assertBody("question", "Q one (+2 more)")
	assertBody("idle", "All done.")
	assertBody("error", "boom happened")
	assertBody("blocked", "implementer blocked")

	// First line only for idle: the second line must not leak into the human line
	// (it may still appear in the context line, so check the idle report line).
	for _, r := range got {
		if r.tags == "idle" && strings.Contains(r.body, "second line") {
			t.Errorf("idle body leaked non-first line: %q", r.body)
		}
	}
}

// A watcher attached at LastSeq over a pre-populated log must not re-fire history
// (the reopen replay case).
func TestNotifyWatcherSkipsHistory(t *testing.T) {
	sink := newNotifySink(t)
	m, n := newTestManager(sink.URL)

	ws := t.TempDir()
	logPath := filepath.Join(ws, ".ycc", "sessions", "s2", "events.jsonl")
	log, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	defer log.Close()

	// Pre-populate the log with events that WOULD notify, before attaching.
	em := event.NewEmitter(log, "coordinator")
	em.Emit(event.SessionError, map[string]any{"msg": "old error"})
	em.Emit(event.QuestionAsked, askData("old question", nil, false))

	// Attach at the current LastSeq (reopen semantics).
	m.startNotifyWatcher(ws, "s2", log)

	// A fresh event after attach should fire.
	em.Emit(event.SessionIdle, map[string]any{"report": "fresh"})

	waitFor(t, func() bool { return len(sink.all()) >= 1 })
	n.Flush()

	got := sink.all()
	if len(got) != 1 {
		t.Fatalf("want exactly 1 notification (the post-attach one), got %d: %v", len(got), got)
	}
	if got[0].tags != "idle" || !strings.Contains(got[0].body, "fresh") {
		t.Errorf("wrong notification: %+v", got[0])
	}
}

// projectLabel resolves the base name when no project matches.
func TestProjectLabelFallback(t *testing.T) {
	m, _ := newTestManager("")
	if got := m.projectLabel("/home/x/myrepo"); got != "myrepo" {
		t.Errorf("projectLabel = %q, want myrepo", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within timeout")
}
