// Package llmhttp applies request-wide and stream-inactivity limits to provider HTTP clients.
package llmhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sync"
	"time"
)

var (
	ErrStreamStalled = errors.New("provider stream stalled")
	ErrTotalTimeout  = errors.New("provider total timeout")
)

// Policy separates the total request budget from SSE read inactivity. Zero disables
// either limit; caller cancellation always remains effective.
type Policy struct {
	StreamIdle time.Duration
	Total      time.Duration
}

// DefaultPolicy tolerates silent high-effort reasoning for five minutes while
// allowing progressing turns up to one hour. Anthropic SSE ping frames reset idle.
func DefaultPolicy() Policy { return Policy{StreamIdle: 5 * time.Minute, Total: time.Hour} }

// NewClient installs the same policy for gollama and Codex. Unlike http.Client.Timeout,
// these limits do not impose a five-minute cap on an actively progressing stream.
func NewClient(p Policy) *http.Client {
	return &http.Client{Transport: &transport{base: http.DefaultTransport.(*http.Transport).Clone(), policy: p}, CheckRedirect: CheckRedirect}
}

type transport struct {
	base   http.RoundTripper
	policy Policy
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	caller := req.Context()
	var ctx context.Context
	var cancel context.CancelFunc
	if t.policy.Total > 0 {
		ctx, cancel = context.WithTimeout(caller, t.policy.Total)
	} else {
		ctx, cancel = context.WithCancel(caller)
	}
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		mapped := requestError(err, caller, ctx, t.policy.Total)
		cancel()
		return nil, mapped
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	body := &watchedBody{ReadCloser: resp.Body, caller: caller, ctx: ctx, cancel: cancel, total: t.policy.Total}
	if mediaType == "text/event-stream" {
		body.progress, _ = caller.Value(progressKey{}).(*Progress)
		if t.policy.StreamIdle > 0 {
			body.idle = t.policy.StreamIdle
			body.timer = time.AfterFunc(body.idle, func() { body.stall(0) })
		}
	}
	resp.Body = body
	return resp, nil
}

func requestError(err error, caller, ctx context.Context, total time.Duration) error {
	if caller.Err() != nil {
		return caller.Err()
	}
	if total > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w after %s: %v", ErrTotalTimeout, total, err)
	}
	return err
}

type watchedBody struct {
	io.ReadCloser
	caller, ctx     context.Context
	cancel          context.CancelFunc
	total, idle     time.Duration
	mu              sync.Mutex
	timer           *time.Timer
	stalled, closed bool
	generation      uint64
	progress        *Progress
	lines           sseLines
}

func (b *watchedBody) stall(generation uint64) {
	b.mu.Lock()
	if b.closed || b.generation != generation {
		b.mu.Unlock()
		return
	}
	b.stalled = true
	b.mu.Unlock()
	b.cancel() // interrupts a blocked transport Read; no watchdog goroutine stays blocked
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 && b.progress != nil {
		b.lines.feed(p[:n], b.progress)
	}
	b.mu.Lock()
	if n > 0 && b.timer != nil && !b.stalled && !b.closed {
		b.generation++
		generation := b.generation
		b.timer.Stop()
		b.timer = time.AfterFunc(b.idle, func() { b.stall(generation) })
	}
	stalled := b.stalled
	b.mu.Unlock()
	switch {
	case b.caller.Err() != nil:
		err = b.caller.Err()
	case b.total > 0 && errors.Is(b.ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("%w after %s", ErrTotalTimeout, b.total)
	case stalled:
		err = fmt.Errorf("%w after %s", ErrStreamStalled, b.idle)
	}
	if err != nil {
		_ = b.Close()
	}
	return n, err
}

func (b *watchedBody) Close() error {
	b.mu.Lock()
	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
	}
	b.mu.Unlock()
	b.cancel()
	return b.ReadCloser.Close()
}
