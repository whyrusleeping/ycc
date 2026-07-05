package engine

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

// retryFakeTurner returns the queued errors in order, then succeeds with a
// plain text turn (which ends the loop). It records how many times Turn was
// called.
type retryFakeTurner struct {
	errs  []error // returned in order; once exhausted, success is returned
	calls int
}

func (s *retryFakeTurner) Turn(gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	idx := s.calls
	s.calls++
	if idx < len(s.errs) {
		return nil, s.errs[idx]
	}
	return assistantText("ok"), nil
}

// newRetryLoop builds a Loop over turner with deterministic retry seams: sleeps
// are recorded (not slept), logging is silenced, and events are captured.
func newRetryLoop(t *testing.T, turner Turner, policy RetryPolicy) (*Loop, *captureRecorder, *[]time.Duration) {
	t.Helper()
	rec := &captureRecorder{}
	loop := newLoop(t, turner)
	loop.Emitter = event.NewEmitter(rec, "agent")
	loop.Retry = policy
	var slept []time.Duration
	loop.retrySleep = func(ctx context.Context, d time.Duration) bool {
		slept = append(slept, d)
		return ctx.Err() == nil
	}
	loop.retryLogf = func(string, ...any) {}
	loop.Seed("go")
	return loop, rec, &slept
}

// sessionErrors filters the captured session_error events.
func sessionErrors(rec *captureRecorder) []event.Event {
	var out []event.Event
	for _, ev := range rec.evs {
		if ev.Type == event.SessionError {
			out = append(out, ev)
		}
	}
	return out
}

func TestLoopRetrySucceedsAfterTransientFailures(t *testing.T) {
	cases := []error{
		errors.New("API returned non-200 status code 503: server error"),
		errors.New("API returned non-200 status code 429: rate limited"),
		errors.New("error sending request: dial tcp: connection refused"),
		errors.New("API returned non-200 status code 500: boom"),
		errors.New("API returned non-200 status code 529: overloaded"),
	}
	for _, transient := range cases {
		inner := &retryFakeTurner{errs: []error{transient}}
		loop, rec, slept := newRetryLoop(t, inner, DefaultRetryPolicy())
		res, err := loop.Run(context.Background())
		if err != nil {
			t.Fatalf("expected success after retry for %v, got %v", transient, err)
		}
		if res.Report != "ok" {
			t.Fatalf("report = %q, want ok (for %v)", res.Report, transient)
		}
		if inner.calls != 2 {
			t.Fatalf("expected 2 calls for %v, got %d", transient, inner.calls)
		}
		if len(*slept) != 1 {
			t.Fatalf("expected 1 sleep for %v, got %d", transient, len(*slept))
		}
		// A retried-then-successful turn must not record any session_error.
		if errs := sessionErrors(rec); len(errs) != 0 {
			t.Fatalf("expected no session_error after recovery, got %v", errs)
		}
	}
}

func TestLoopRetryNonRetryableFailsImmediately(t *testing.T) {
	cases := []struct {
		err  error
		kind APIErrorKind
	}{
		{errors.New("API returned non-200 status code 401: unauthorized"), KindAuth},
		{errors.New("API returned non-200 status code 403: forbidden"), KindAuth},
		{errors.New("API returned non-200 status code 400: bad request"), KindInvalidRequest},
		{errors.New("API returned non-200 status code 404: not found"), KindInvalidRequest},
	}
	for _, c := range cases {
		inner := &retryFakeTurner{errs: []error{c.err}}
		loop, rec, slept := newRetryLoop(t, inner, DefaultRetryPolicy())
		_, err := loop.Run(context.Background())
		if err == nil {
			t.Fatalf("expected error for %v", c.err)
		}
		if !errors.Is(err, c.err) {
			t.Fatalf("expected original error %v wrapped, got %v", c.err, err)
		}
		var te *TurnError
		if !errors.As(err, &te) {
			t.Fatalf("expected a *TurnError (already-emitted marker), got %T: %v", err, err)
		}
		if inner.calls != 1 {
			t.Fatalf("expected exactly 1 call for %v, got %d", c.err, inner.calls)
		}
		if len(*slept) != 0 {
			t.Fatalf("expected no sleeps for %v, got %d", c.err, len(*slept))
		}
		errs := sessionErrors(rec)
		if len(errs) != 1 {
			t.Fatalf("expected exactly 1 session_error for %v, got %d", c.err, len(errs))
		}
		if kind, _ := errs[0].Data["kind"].(string); kind != string(c.kind) {
			t.Fatalf("session_error kind = %q, want %q (for %v)", kind, c.kind, c.err)
		}
		if retryable, _ := errs[0].Data["retryable"].(bool); retryable {
			t.Fatalf("session_error retryable = true, want false (for %v)", c.err)
		}
	}
}

func TestLoopRetryExhaustionEmitsStructuredError(t *testing.T) {
	orig := errors.New("API returned non-200 status code 503: persistent")
	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: 10 * time.Millisecond, MaxDelay: time.Second}
	inner := &retryFakeTurner{errs: []error{orig, orig, orig, orig}}
	loop, rec, slept := newRetryLoop(t, inner, policy)
	_, err := loop.Run(context.Background())
	if !errors.Is(err, orig) {
		t.Fatalf("expected original error after exhaustion, got %v", err)
	}
	if inner.calls != policy.MaxAttempts {
		t.Fatalf("expected %d calls, got %d", policy.MaxAttempts, inner.calls)
	}
	if len(*slept) != policy.MaxAttempts-1 {
		t.Fatalf("expected %d sleeps, got %d", policy.MaxAttempts-1, len(*slept))
	}
	errs := sessionErrors(rec)
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 session_error, got %d", len(errs))
	}
	data := errs[0].Data
	if kind, _ := data["kind"].(string); kind != string(KindOverloaded) {
		t.Fatalf("kind = %q, want %q", kind, KindOverloaded)
	}
	if attempts, _ := data["attempts"].(int); attempts != 3 {
		t.Fatalf("attempts = %v, want 3", data["attempts"])
	}
	if status, _ := data["status"].(int); status != 503 {
		t.Fatalf("status = %v, want 503", data["status"])
	}
	if retryable, _ := data["retryable"].(bool); !retryable {
		t.Fatal("retryable = false, want true (retries were exhausted on a transient failure)")
	}
}

func TestLoopRetryBackoffGrows(t *testing.T) {
	orig := errors.New("error sending request: timeout")
	policy := RetryPolicy{MaxAttempts: 5, BaseDelay: 100 * time.Millisecond, MaxDelay: 10 * time.Second}
	inner := &retryFakeTurner{errs: []error{orig, orig, orig, orig, orig, orig}}
	loop, _, slept := newRetryLoop(t, inner, policy)
	_, _ = loop.Run(context.Background())
	if len(*slept) < 3 {
		t.Fatalf("expected several sleeps, got %d", len(*slept))
	}
	// Equal-jitter backoff: each delay is within [half, full] of the doubling
	// base, so successive minimums grow. Check the lower bounds increase.
	for i := 1; i < len(*slept); i++ {
		base := policy.BaseDelay << uint(i)
		if base > policy.MaxDelay {
			base = policy.MaxDelay
		}
		min := base / 2
		if (*slept)[i] < min {
			t.Fatalf("sleep %d = %v below expected min %v", i, (*slept)[i], min)
		}
	}
}

// MaxAttempts 1 disables retry: one call, no sleeps.
func TestLoopRetryDisabled(t *testing.T) {
	orig := errors.New("API returned non-200 status code 503: transient")
	inner := &retryFakeTurner{errs: []error{orig}}
	loop, _, slept := newRetryLoop(t, inner, RetryPolicy{MaxAttempts: 1})
	if _, err := loop.Run(context.Background()); !errors.Is(err, orig) {
		t.Fatalf("expected the original error, got %v", err)
	}
	if inner.calls != 1 || len(*slept) != 0 {
		t.Fatalf("calls=%d sleeps=%d, want 1/0", inner.calls, len(*slept))
	}
}

// Cancelling the run ctx during a retry backoff stops the loop promptly with
// the ctx error and does NOT record a session_error (a stopped session is not
// an API failure).
func TestLoopRetryCtxCancelDuringBackoff(t *testing.T) {
	orig := errors.New("API returned non-200 status code 503: transient")
	inner := &retryFakeTurner{errs: []error{orig, orig, orig}}
	ctx, cancel := context.WithCancel(context.Background())
	rec := &captureRecorder{}
	loop := newLoop(t, inner)
	loop.Emitter = event.NewEmitter(rec, "agent")
	loop.Retry = RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	loop.retryLogf = func(string, ...any) {}
	loop.retrySleep = func(c context.Context, d time.Duration) bool {
		cancel() // the session is stopped mid-backoff
		return false
	}
	loop.Seed("go")
	_, err := loop.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected no further attempts after cancel, got %d calls", inner.calls)
	}
	if errs := sessionErrors(rec); len(errs) != 0 {
		t.Fatalf("expected no session_error on cancellation, got %v", errs)
	}
}

// Retries are visible to live subscribers: each backoff broadcasts a transient
// "retry" event (never persisted) carrying attempt/delay/classification.
func TestLoopRetryBroadcastsTransientEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := event.OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ch, cancelSub := l.Subscribe(0)
	var mu sync.Mutex
	var retries []event.Event
	done := make(chan struct{})
	go func() {
		for ev := range ch {
			if ev.Transient && ev.Type == event.Retry {
				mu.Lock()
				retries = append(retries, ev)
				mu.Unlock()
			}
		}
		close(done)
	}()

	orig := errors.New("API returned non-200 status code 429: rate limited")
	inner := &retryFakeTurner{errs: []error{orig, orig}}
	loop := newLoopWithRec(t, inner, l)
	loop.Retry = RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	loop.retryLogf = func(string, ...any) {}
	loop.retrySleep = func(context.Context, time.Duration) bool { return true }
	loop.Seed("go")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	time.Sleep(80 * time.Millisecond) // let transients drain
	cancelSub()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(retries) != 2 {
		t.Fatalf("expected 2 transient retry events, got %d: %v", len(retries), retries)
	}
	first := retries[0]
	if kind, _ := first.Data["kind"].(string); kind != string(KindRateLimit) {
		t.Fatalf("retry kind = %q, want %q", kind, KindRateLimit)
	}
	if attempt, _ := first.Data["attempt"].(int); attempt != 1 {
		t.Fatalf("retry attempt = %v, want 1", first.Data["attempt"])
	}
	if first.Seq != 0 || !first.Transient {
		t.Fatalf("retry event must be transient/seq-less, got seq=%d transient=%v", first.Seq, first.Transient)
	}
	// Nothing persisted: the durable log has no retry events.
	for _, ev := range l.Snapshot() {
		if ev.Type == event.Retry {
			t.Fatal("retry event leaked into the persisted log")
		}
	}
}

// A retried STREAMING attempt restarts snapshots cleanly: the failed attempt's
// partial tail is cleared by its done-delta, and the fresh attempt begins new
// full snapshots (snapshot semantics — no reset protocol needed).
func TestLoopRetryStreamRestartsSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := event.OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	stop := collectDeltas(t, l)

	inner := &scriptStreamTurner{attempts: []streamAttempt{
		{snaps: []string{"a1"}, err: errors.New("timeout talking to API")}, // retryable
		{snaps: []string{"b1"}, resp: assistantText("b1")},
	}}
	loop := newLoopWithRec(t, inner, l)
	loop.Retry = RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	loop.retryLogf = func(string, ...any) {}
	loop.retrySleep = func(context.Context, time.Duration) bool { return true }
	loop.Seed("go")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if inner.streamCalls != 2 {
		t.Fatalf("streamCalls = %d, want 2 (one retry)", inner.streamCalls)
	}

	deltas := stop()
	// Expect: "a1", clearing done delta (failed attempt), "b1", clearing done
	// delta (successful attempt).
	var texts []string
	var dones int
	for _, ev := range deltas {
		if d, _ := ev.Data["done"].(bool); d {
			dones++
			continue
		}
		if s, _ := ev.Data["text"].(string); s != "" {
			texts = append(texts, s)
		}
	}
	if dones != 2 {
		t.Fatalf("expected 2 clearing done-deltas (one per attempt), got %d: %v", dones, deltas)
	}
	if len(texts) != 2 || texts[0] != "a1" || texts[1] != "b1" {
		t.Fatalf("snapshots = %v, want [a1 b1]", texts)
	}
}
