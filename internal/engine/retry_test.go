package engine

import (
	"errors"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
)

// retryFakeTurner returns the queued errors in order, then succeeds. It records
// how many times Turn was called.
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
	return &gollama.ResponseMessageGenerate{}, nil
}

// newTestRetry builds a retryTurner with deterministic sleep/rand/logf for tests
// and records the sleep durations.
func newTestRetry(inner Turner, policy RetryPolicy) (*retryTurner, *[]time.Duration) {
	var slept []time.Duration
	rt := WithRetry(inner, policy).(*retryTurner)
	rt.sleep = func(d time.Duration) { slept = append(slept, d) }
	rt.logf = func(string, ...any) {}
	return rt, &slept
}

func TestRetrySucceedsAfterTransientFailures(t *testing.T) {
	cases := []error{
		errors.New("API returned non-200 status code 503: server error"),
		errors.New("API returned non-200 status code 429: rate limited"),
		errors.New("error sending request: dial tcp: connection refused"),
		errors.New("API returned non-200 status code 500: boom"),
	}
	for _, transient := range cases {
		inner := &retryFakeTurner{errs: []error{transient}}
		rt, slept := newTestRetry(inner, DefaultRetryPolicy())
		resp, err := rt.Turn(gollama.RequestOptions{})
		if err != nil {
			t.Fatalf("expected success after retry for %v, got %v", transient, err)
		}
		if resp == nil {
			t.Fatalf("expected non-nil resp for %v", transient)
		}
		if inner.calls != 2 {
			t.Fatalf("expected 2 calls for %v, got %d", transient, inner.calls)
		}
		if len(*slept) != 1 {
			t.Fatalf("expected 1 sleep for %v, got %d", transient, len(*slept))
		}
	}
}

func TestRetryNonRetryableFailsImmediately(t *testing.T) {
	cases := []error{
		errors.New("API returned non-200 status code 401: unauthorized"),
		errors.New("API returned non-200 status code 403: forbidden"),
		errors.New("API returned non-200 status code 400: bad request"),
		errors.New("API returned non-200 status code 404: not found"),
	}
	for _, fatal := range cases {
		inner := &retryFakeTurner{errs: []error{fatal}}
		rt, slept := newTestRetry(inner, DefaultRetryPolicy())
		_, err := rt.Turn(gollama.RequestOptions{})
		if err == nil {
			t.Fatalf("expected error for %v", fatal)
		}
		if !errors.Is(err, fatal) {
			t.Fatalf("expected original error %v, got %v", fatal, err)
		}
		if inner.calls != 1 {
			t.Fatalf("expected exactly 1 call for %v, got %d", fatal, inner.calls)
		}
		if len(*slept) != 0 {
			t.Fatalf("expected no sleeps for %v, got %d", fatal, len(*slept))
		}
	}
}

func TestRetryExhaustionReturnsOriginalError(t *testing.T) {
	orig := errors.New("API returned non-200 status code 503: persistent")
	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: 10 * time.Millisecond, MaxDelay: time.Second}
	inner := &retryFakeTurner{errs: []error{orig, orig, orig, orig}}
	rt, slept := newTestRetry(inner, policy)
	_, err := rt.Turn(gollama.RequestOptions{})
	if !errors.Is(err, orig) {
		t.Fatalf("expected original error after exhaustion, got %v", err)
	}
	if inner.calls != policy.MaxAttempts {
		t.Fatalf("expected %d calls, got %d", policy.MaxAttempts, inner.calls)
	}
	// One sleep between each of the MaxAttempts attempts.
	if len(*slept) != policy.MaxAttempts-1 {
		t.Fatalf("expected %d sleeps, got %d", policy.MaxAttempts-1, len(*slept))
	}
}

func TestRetryBackoffGrows(t *testing.T) {
	orig := errors.New("error sending request: timeout")
	policy := RetryPolicy{MaxAttempts: 5, BaseDelay: 100 * time.Millisecond, MaxDelay: 10 * time.Second}
	inner := &retryFakeTurner{errs: []error{orig, orig, orig, orig, orig, orig}}
	rt, slept := newTestRetry(inner, policy)
	_, _ = rt.Turn(gollama.RequestOptions{})
	if len(*slept) < 3 {
		t.Fatalf("expected several sleeps, got %d", len(*slept))
	}
	// Equal-jitter backoff: each delay is within [half, full] of the doubling
	// base, so successive minimums grow. Check the lower bounds increase.
	for i := 1; i < len(*slept); i++ {
		// Lower bound for attempt i is BaseDelay<<(i-1)/2.
		base := policy.BaseDelay << uint(i-1)
		if base > policy.MaxDelay {
			base = policy.MaxDelay
		}
		min := base / 2
		if (*slept)[i] < min {
			t.Fatalf("sleep %d = %v below expected min %v", i, (*slept)[i], min)
		}
	}
}

func TestWithRetryDisabledReturnsInner(t *testing.T) {
	inner := &retryFakeTurner{}
	if got := WithRetry(inner, RetryPolicy{MaxAttempts: 1}); got != Turner(inner) {
		t.Fatalf("expected inner returned unchanged when retry disabled")
	}
	if got := WithRetry(inner, RetryPolicy{MaxAttempts: 0}); got != Turner(inner) {
		t.Fatalf("expected inner returned unchanged when MaxAttempts 0")
	}
}
