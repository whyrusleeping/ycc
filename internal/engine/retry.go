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
// Layering note: gollama's HTTP transport ALSO retries 429/503/529 internally
// (5 attempts, 5s→80s exponential backoff) before its error ever reaches us.
// The loop-level retry therefore mostly covers what gollama does not — other
// 5xx, 408, and transport/network failures — and adds a slower second ring for
// persistent rate limiting. A rate-limited request can thus stall for several
// minutes in gollama silently; the transient retry events emitted here only
// cover the loop-level ring.
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
