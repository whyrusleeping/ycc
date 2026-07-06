package notify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
)

// capture records one received webhook request.
type capture struct {
	body     string
	title    string
	priority string
	tags     string
	auth     string
}

// sink is an httptest server that records every received notification.
type sink struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []capture
}

func newSink(t *testing.T) *sink {
	t.Helper()
	s := &sink{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, capture{
			body:     string(b),
			title:    r.Header.Get("Title"),
			priority: r.Header.Get("Priority"),
			tags:     r.Header.Get("Tags"),
			auth:     r.Header.Get("Authorization"),
		})
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *sink) all() []capture {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capture, len(s.reqs))
	copy(out, s.reqs)
	return out
}

func TestSendPostsBodyAndHeaders(t *testing.T) {
	s := newSink(t)
	n := New(config.Notify{URL: s.URL, Auth: "Bearer tk_abc"})
	if n == nil {
		t.Fatal("New returned nil for a configured URL")
	}
	n.Send(KindQuestion, "myproj", "sess123", "should I refactor the parser?")
	n.Flush()

	got := s.all()
	if len(got) != 1 {
		t.Fatalf("want 1 request, got %d", len(got))
	}
	r := got[0]
	if r.title != "ycc myproj: question" {
		t.Errorf("title = %q", r.title)
	}
	if r.priority != "high" {
		t.Errorf("priority = %q, want high", r.priority)
	}
	if r.tags != "question" {
		t.Errorf("tags = %q, want question", r.tags)
	}
	if r.auth != "Bearer tk_abc" {
		t.Errorf("auth = %q", r.auth)
	}
	for _, want := range []string{"should I refactor the parser?", "myproj", "sess123"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("body %q missing %q", r.body, want)
		}
	}
}

func TestPriorityDefaultForIdleAndDigest(t *testing.T) {
	s := newSink(t)
	n := New(config.Notify{URL: s.URL})
	n.Send(KindIdle, "p", "s", "done")
	n.Send(KindDigest, "p", "s", "loop finished")
	n.Flush()
	for _, r := range s.all() {
		if r.priority != "default" {
			t.Errorf("priority = %q, want default", r.priority)
		}
		if r.auth != "" {
			t.Errorf("auth = %q, want empty when unset", r.auth)
		}
	}
}

func TestEventMuting(t *testing.T) {
	s := newSink(t)
	// Only questions + digests enabled.
	n := New(config.Notify{URL: s.URL, Events: []string{"question", "digest"}})
	if !n.Enabled(KindQuestion) || !n.Enabled(KindDigest) {
		t.Fatal("question/digest should be enabled")
	}
	if n.Enabled(KindIdle) || n.Enabled(KindError) || n.Enabled(KindBlocked) {
		t.Fatal("idle/error/blocked should be muted")
	}
	n.Send(KindIdle, "p", "s", "muted")
	n.Send(KindError, "p", "s", "muted")
	n.Send(KindQuestion, "p", "s", "delivered")
	n.Flush()
	got := s.all()
	if len(got) != 1 {
		t.Fatalf("want 1 delivered (only question), got %d", len(got))
	}
	if got[0].tags != "question" {
		t.Errorf("delivered wrong kind: %q", got[0].tags)
	}
}

func TestEmptyURLDisables(t *testing.T) {
	if n := New(config.Notify{}); n != nil {
		t.Fatal("empty URL should yield a nil notifier")
	}
}

func TestNilNotifierSafe(t *testing.T) {
	var n *Notifier
	// None of these should panic.
	if n.Enabled(KindQuestion) {
		t.Error("nil notifier should not be enabled")
	}
	n.Send(KindQuestion, "p", "s", "line")
	n.Flush()
}
