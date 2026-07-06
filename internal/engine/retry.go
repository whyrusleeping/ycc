package engine

import (
	"math/rand"
	"time"
)

// RetryPolicy controls automatic retry of transient LLM API call failures
// (spec §7.2, task 0050). MaxAttempts is the total number of attempts including
// the first; a value of 1 disables retry entirely, and 0 means "use the
// default" (see DefaultRetryPolicy). BaseDelay is the first backoff step and
// doubles each attempt; MaxDelay caps it.
//
// Retry is applied by the Loop itself (see Loop.runTurn): the loop is where the
// run's ctx (so a stopped session cancels a pending backoff instead of sleeping
// it out), the Emitter (so live subscribers see transient "retry" events), and
// the error classification (apierror.go) all meet.
//
// Layering note: gollama's HTTP transport can also retry 429/503/529 internally,
// but ycc disables that ring explicitly (SetMaxRetries(0) in
// config.Registry.Build) precisely because it uses uncancellable time.Sleep and
// is invisible to subscribers. The loop-level retry defined here is therefore the
// single, ctx-aware, event-visible retry ring covering rate limiting, other 5xx,
// 408, and transport/network failures alike.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryPolicy returns a sensible policy: eight total attempts (seven
// retries) with exponential backoff from 500ms capped at 30s (worst-case ≈60s of
// jittered backoff). This is the only retry ring — gollama's transport retry is
// disabled by ycc — so the budget deliberately tolerates transient rate limiting.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 8,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    30 * time.Second,
	}
}

// backoff computes the delay before the attempt following `attempt` (1-based)
// using exponential growth with equal jitter: the base delay doubles each
// attempt, is capped at MaxDelay, then half of it is fixed and the other half
// randomized via rnd.
func (p RetryPolicy) backoff(attempt int, rnd *rand.Rand) time.Duration {
	capped := p.MaxDelay
	if attempt >= 1 && attempt < 63 {
		shifted := p.BaseDelay << uint(attempt-1)
		if shifted > 0 && shifted < p.MaxDelay {
			capped = shifted
		}
	}
	if capped <= 0 {
		capped = p.BaseDelay
	}
	if capped <= 0 {
		return 0
	}
	half := capped / 2
	if half <= 0 {
		return capped
	}
	return half + time.Duration(rnd.Int63n(int64(half)+1))
}
