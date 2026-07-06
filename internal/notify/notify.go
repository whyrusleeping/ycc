// Package notify implements the daemon-side push notifier (task 0142): a
// best-effort, asynchronous webhook (ntfy.sh-compatible) that reaches out to the
// user when an agent needs them — a question was asked, a session went idle with a
// final report, a session errored, a work-loop run finished (digest), or an
// implementer subagent reported blocked.
//
// Delivery is fire-and-forget: Send never blocks the caller and never returns an
// error; a failed POST is only logged. The notifier is intentionally trivial to
// disable — an empty webhook URL yields a nil *Notifier, and every method is
// nil-receiver-safe so callers need no guards.
package notify

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
)

// Event kinds. These mirror config.NotifyEventKinds and are the values accepted
// in the notify.events allow-list.
const (
	KindQuestion = "question"
	KindIdle     = "idle"
	KindError    = "error"
	KindDigest   = "digest"
	KindBlocked  = "blocked"
)

// sendTimeout bounds a single webhook POST so a slow/hung endpoint can never leak
// a goroutine forever.
const sendTimeout = 15 * time.Second

// Notifier posts best-effort webhook notifications. Construct with New; a nil
// *Notifier is valid and disables all notifications (every method is nil-safe).
type Notifier struct {
	url    string
	auth   string
	events map[string]bool // nil/empty => all kinds enabled
	client *http.Client
	wg     sync.WaitGroup // tracks in-flight sends so tests can Flush
}

// New builds a Notifier from config. It returns nil (notifications disabled) when
// the URL is empty. An empty Events list enables every kind; a non-empty list
// enables only the listed kinds.
func New(cfg config.Notify) *Notifier {
	if cfg.URL == "" {
		return nil
	}
	var events map[string]bool
	if len(cfg.Events) > 0 {
		events = make(map[string]bool, len(cfg.Events))
		for _, k := range cfg.Events {
			events[k] = true
		}
	}
	return &Notifier{
		url:    cfg.URL,
		auth:   cfg.Auth,
		events: events,
		client: &http.Client{Timeout: sendTimeout},
	}
}

// Enabled reports whether notifications for the given kind would be delivered
// (notifier configured and the kind not muted). Nil-safe.
func (n *Notifier) Enabled(kind string) bool {
	if n == nil {
		return false
	}
	if n.events == nil {
		return true
	}
	return n.events[kind]
}

// Send delivers a best-effort webhook notification for the given kind. It returns
// immediately; the POST runs on a background goroutine and never blocks the caller
// or surfaces an error (failures are logged). A no-op when the notifier is nil or
// the kind is muted.
func (n *Notifier) Send(kind, project, sessionID, line string) {
	if !n.Enabled(kind) {
		return
	}
	title := fmt.Sprintf("ycc %s: %s", project, kind)
	priority := "default"
	switch kind {
	case KindQuestion, KindError, KindBlocked:
		priority = "high"
	}
	body := line
	if body == "" {
		body = kind
	}
	body += fmt.Sprintf("\n%s · session %s", project, sessionID)

	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader([]byte(body)))
		if err != nil {
			log.Printf("ycc: notify: build request: %v", err)
			return
		}
		req.Header.Set("Title", title)
		req.Header.Set("Priority", priority)
		req.Header.Set("Tags", kind)
		if n.auth != "" {
			req.Header.Set("Authorization", n.auth)
		}
		resp, err := n.client.Do(req)
		if err != nil {
			log.Printf("ycc: notify: POST %s: %v", n.url, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			log.Printf("ycc: notify: POST %s: unexpected status %s", n.url, resp.Status)
		}
	}()
}

// Flush blocks until all in-flight sends have completed. Nil-safe. Intended for
// tests (and graceful shutdown) that need deterministic delivery.
func (n *Notifier) Flush() {
	if n == nil {
		return
	}
	n.wg.Wait()
}
