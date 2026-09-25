package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
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

func (s *retryFakeTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
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
		// A codex in-stream server_error: no HTTP status, but transient. This is
		// the failure that used to strand a session until the user typed
		// "continue" by hand (task 0225).
		errors.New("codex: stream error: server_error: An error occurred while processing your request. You can retry your request, or contact us through our help center at help.openai.com if the error persists."),
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
		{&gollama.APIError{StatusCode: 401, Body: `{"error":{"code":"invalid_api_key","message":"bad key"}}`}, KindAuth},
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

func TestTypedContextFailureDoesNotRetry(t *testing.T) {
	orig := &gollama.APIError{StatusCode: 400, Body: `{"error":{"code":"context_length_exceeded"}}`}
	turner := &retryFakeTurner{errs: []error{orig}}
	loop, _, slept := newRetryLoop(t, turner, DefaultRetryPolicy())
	_, attempts, err := loop.runTurn(context.Background(), turner, gollama.RequestOptions{Model: "test"})
	if !errors.Is(err, orig) || attempts != 1 || turner.calls != 1 || len(*slept) != 0 {
		t.Fatalf("context failure: err=%v attempts=%d calls=%d sleeps=%v", err, attempts, turner.calls, *slept)
	}
}

func TestDefaultRetryPolicyCapsPersistentRateLimit(t *testing.T) {
	orig := errors.New("API returned non-200 status code 429: persistent")
	inner := &retryFakeTurner{errs: []error{orig, orig, orig, orig, orig}}
	// RetryPolicy{} exercises the default, including the rate-limit-specific cap.
	loop, rec, slept := newRetryLoop(t, inner, RetryPolicy{})
	_, err := loop.Run(context.Background())
	if !errors.Is(err, orig) {
		t.Fatalf("expected original rate-limit error, got %v", err)
	}
	if inner.calls != DefaultRateLimitMaxAttempts {
		t.Fatalf("calls = %d, want %d", inner.calls, DefaultRateLimitMaxAttempts)
	}
	if len(*slept) != DefaultRateLimitMaxAttempts-1 {
		t.Fatalf("sleeps = %d, want %d", len(*slept), DefaultRateLimitMaxAttempts-1)
	}
	errs := sessionErrors(rec)
	if len(errs) != 1 {
		t.Fatalf("session errors = %d, want 1", len(errs))
	}
	if attempts, _ := errs[0].Data["attempts"].(int); attempts != DefaultRateLimitMaxAttempts {
		t.Fatalf("recorded attempts = %v, want %d", errs[0].Data["attempts"], DefaultRateLimitMaxAttempts)
	}
}

func TestExplicitRetryPolicyRemainsAuthoritativeForRateLimit(t *testing.T) {
	orig := errors.New("API returned non-200 status code 429: persistent")
	policy := RetryPolicy{MaxAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	inner := &retryFakeTurner{errs: []error{orig, orig, orig, orig, orig}}
	loop, _, slept := newRetryLoop(t, inner, policy)
	_, _ = loop.Run(context.Background())
	if inner.calls != policy.MaxAttempts || len(*slept) != policy.MaxAttempts-1 {
		t.Fatalf("calls/sleeps = %d/%d, want %d/%d", inner.calls, len(*slept), policy.MaxAttempts, policy.MaxAttempts-1)
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
func TestLoopRetryCtxCancelDuringProviderWait(t *testing.T) {
	orig := &gollama.APIError{StatusCode: 503, Body: "transient", Header: http.Header{"Retry-After": []string{"2"}}}
	inner := &retryFakeTurner{errs: []error{orig, orig, orig}}
	ctx, cancel := context.WithCancel(context.Background())
	rec := &captureRecorder{}
	loop := newLoop(t, inner)
	loop.Emitter = event.NewEmitter(rec, "agent")
	loop.Retry = RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	loop.retryLogf = func(string, ...any) {}
	loop.retrySleep = func(c context.Context, d time.Duration) bool {
		if d != 2*time.Second {
			t.Errorf("provider wait = %v", d)
		}
		cancel() // the session is stopped mid-wait
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

func TestRetryProviderWaitAndBudgets(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	providerErr := func(seconds string) error {
		return &gollama.APIError{StatusCode: 429, Body: `{"error":{"code":"rate_limit_error","message":"slow down"}}`, Header: http.Header{"Retry-After": []string{seconds}, "X-Credential": []string{"secret123"}}}
	}
	policy := RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxRetryAfter: 5 * time.Second, MaxTotalWait: 3 * time.Second}
	for _, tc := range []struct {
		name   string
		values []string
		calls  int
		waits  []time.Duration
	}{
		{"retry after", []string{"2"}, 2, []time.Duration{2 * time.Second}},
		{"excessive", []string{"9"}, 1, nil},
		{"total budget", []string{"2", "2"}, 2, []time.Duration{2 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := make([]error, len(tc.values))
			for i, s := range tc.values {
				errs[i] = providerErr(s)
			}
			turner := &retryFakeTurner{errs: errs}
			loop, rec, slept := newRetryLoop(t, turner, policy)
			loop.retryNow = func() time.Time { return now }
			_, err := loop.Run(context.Background())
			if tc.calls == 1 || tc.name == "total budget" {
				if err == nil {
					t.Fatal("expected terminal provider error")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if turner.calls != tc.calls || len(*slept) != len(tc.waits) {
				t.Fatalf("calls=%d slept=%v", turner.calls, *slept)
			}
			for i, want := range tc.waits {
				if (*slept)[i] != want {
					t.Fatalf("sleep %d=%v, want %v", i, (*slept)[i], want)
				}
			}
			if tc.name == "excessive" {
				ev := sessionErrors(rec)
				if len(ev) != 1 || ev[0].Data["retry_after_ms"] != int64(9000) || ev[0].Data["retry_at"] != now.Add(9*time.Second).Format(time.RFC3339) || ev[0].Data["code"] != "rate_limit_error" {
					t.Fatalf("session error: %+v", ev)
				}
			}
		})
	}
}

func TestRetryEventProviderMetadata(t *testing.T) {
	log, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ch, cancel := log.Subscribe(0)
	defer cancel()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	orig := &gollama.APIError{StatusCode: 503, Body: `{"error":{"code":"server_error","message":"retry please"}}`, Header: http.Header{"Retry-After": []string{"2"}, "X-Credential": []string{"secret123"}}}
	turner := &retryFakeTurner{errs: []error{orig}}
	loop := newLoopWithRec(t, turner, log)
	loop.Retry = RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	loop.retryNow = func() time.Time { return now }
	loop.retrySleep = func(context.Context, time.Duration) bool { return true }
	loop.retryLogf = func(string, ...any) {}
	loop.Seed("go")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case ev := <-ch:
			if ev.Type != event.Retry {
				continue
			}
			if ev.Data["next_attempt_at"] != now.Add(2*time.Second).Format(time.RFC3339Nano) || ev.Data["reason"] != "retry_after" || ev.Data["retry_after_ms"] != int64(2000) || ev.Data["code"] != "server_error" || ev.Data["msg"] != "retry please" {
				t.Fatalf("retry: %+v", ev.Data)
			}
			if strings.Contains(fmt.Sprint(ev.Data), "secret123") {
				t.Fatalf("leaked header: %+v", ev.Data)
			}
			return
		case <-time.After(time.Second):
			t.Fatal("no retry event")
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
	retryEvents, cancelRetry := l.Subscribe(0)
	partialSeen := make(chan struct{}, 1)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range retryEvents {
			if ev.Type == event.Retry && ev.Data["partial"] == true {
				select {
				case partialSeen <- struct{}{}:
				default:
				}
			}
		}
	}()

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
	select {
	case <-partialSeen:
	case <-time.After(time.Second):
		cancelRetry()
		t.Fatal("partial stream failure did not mark transient retry event")
	}
	cancelRetry()
	<-drained

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
