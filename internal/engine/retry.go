package engine

import (
	"errors"
	"log"
	"math/rand"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"
)

// RetryPolicy controls automatic retry of transient LLM API call failures
// (spec task 0050). MaxAttempts is the total number of attempts including the
// first; a value <= 1 disables retry entirely. BaseDelay is the first backoff
// step and doubles each attempt; MaxDelay caps it.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryPolicy returns a sensible policy: three total attempts (two
// retries) with exponential backoff from 500ms capped at 30s.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    30 * time.Second,
	}
}

// WithRetry wraps a Turner so that transient (network/timeout/429/5xx) failures
// are retried with exponential backoff and jitter, up to policy.MaxAttempts.
// Non-retryable errors (auth/4xx bad request) surface immediately. When the
// policy disables retry (MaxAttempts <= 1) the inner Turner is returned
// unchanged so there is no overhead.
//
// The wrapper is capability-preserving: when inner also implements StreamTurner
// the returned value implements StreamTurner too (so the loop's type assertion
// stays accurate and streaming keeps working under retry); otherwise a plain
// retrying Turner is returned. Because turn_delta payloads are snapshots (the
// full accumulated text so far), a retried streaming attempt simply restarts
// from a short snapshot with no reset protocol needed.
func WithRetry(inner Turner, policy RetryPolicy) Turner {
	if policy.MaxAttempts <= 1 {
		return inner
	}
	rt := &retryTurner{
		inner:  inner,
		policy: policy,
		sleep:  time.Sleep,
		rand:   rand.New(rand.NewSource(time.Now().UnixNano())),
		logf:   log.Printf,
	}
	if st, ok := inner.(StreamTurner); ok {
		return &streamRetryTurner{retryTurner: rt, stream: st}
	}
	return rt
}

// retryTurner is the WithRetry implementation. sleep, rand and logf are fields
// so tests can inject deterministic behaviour.
type retryTurner struct {
	inner  Turner
	policy RetryPolicy
	sleep  func(time.Duration)
	rand   *rand.Rand
	logf   func(string, ...any)
}

// Turn runs the inner Turn, retrying transient failures with backoff. The
// ORIGINAL error is surfaced once retries are exhausted or the error is not
// retryable.
func (r *retryTurner) Turn(opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	var lastErr error
	for attempt := 1; attempt <= r.policy.MaxAttempts; attempt++ {
		resp, err := r.inner.Turn(opts)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryable(err) || attempt == r.policy.MaxAttempts {
			return nil, err
		}
		delay := r.backoff(attempt)
		r.logf("ycc: LLM API call failed (attempt %d/%d), retrying in %v: %v",
			attempt, r.policy.MaxAttempts, delay, err)
		r.sleep(delay)
	}
	return nil, lastErr
}

// streamRetryTurner is the capability-preserving variant of retryTurner returned
// by WithRetry when the inner Turner also streams. It embeds retryTurner (so Turn
// is inherited unchanged) and adds a TurnStream that retries the inner
// TurnStream under the same policy. A retried attempt restarts snapshots
// cleanly: onDelta always carries the full accumulated text so far, so the
// caller's live tail is simply replaced when a fresh attempt begins.
type streamRetryTurner struct {
	*retryTurner
	stream StreamTurner
}

// TurnStream runs the inner TurnStream, retrying transient failures with backoff
// exactly like Turn. onDelta is forwarded to each attempt as-is; because deltas
// are snapshots, no cross-attempt reset is required.
func (r *streamRetryTurner) TurnStream(opts gollama.RequestOptions, onDelta func(text string)) (*gollama.ResponseMessageGenerate, error) {
	var lastErr error
	for attempt := 1; attempt <= r.policy.MaxAttempts; attempt++ {
		resp, err := r.stream.TurnStream(opts, onDelta)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryable(err) || attempt == r.policy.MaxAttempts {
			return nil, err
		}
		delay := r.backoff(attempt)
		r.logf("ycc: LLM API call failed (attempt %d/%d), retrying in %v: %v",
			attempt, r.policy.MaxAttempts, delay, err)
		r.sleep(delay)
	}
	return nil, lastErr
}

// "API returned non-200 status code 503: ...".
var statusCodeRe = regexp.MustCompile(`status code (\d+)`)

// isRetryable reports whether err is a transient failure worth retrying. HTTP
// errors are retried only for 408/429/5xx; all other parsed status codes
// (401/403/400/404/...) are non-retryable. Errors with no status code are
// treated as transport/network failures and detected via net.Error / url.Error
// and a set of substring fallbacks.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if m := statusCodeRe.FindStringSubmatch(msg); m != nil {
		code, convErr := strconv.Atoi(m[1])
		if convErr == nil {
			if code == 408 || code == 429 || (code >= 500 && code <= 599) {
				return true
			}
			return false
		}
	}

	// No HTTP status code: treat as a transport/network error.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}

	lower := strings.ToLower(msg)
	for _, frag := range []string{
		"error sending request",
		"timeout",
		"timed out",
		"connection refused",
		"connection reset",
		"deadline exceeded",
		"no such host",
		"tls handshake",
		"eof",
	} {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}

// backoff computes the delay before the next attempt using exponential growth
// with equal jitter: the base delay doubles each attempt, is capped at MaxDelay,
// then half of it is fixed and the other half is randomized.
func (r *retryTurner) backoff(attempt int) time.Duration {
	capped := r.policy.MaxDelay
	if attempt >= 1 && attempt < 63 {
		shifted := r.policy.BaseDelay << uint(attempt-1)
		if shifted > 0 && shifted < r.policy.MaxDelay {
			capped = shifted
		}
	}
	if capped <= 0 {
		capped = r.policy.BaseDelay
	}
	if capped <= 0 {
		return 0
	}
	half := capped / 2
	if half <= 0 {
		return capped
	}
	return half + time.Duration(r.rand.Int63n(int64(half)+1))
}
