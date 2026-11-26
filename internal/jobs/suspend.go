package jobs

import (
	"context"
	"errors"
)

// ErrRestartSuspend is the cancellation cause a graceful daemon restart gives a
// session's run context (docs/design/daemon-restart.md §1). It differs from an
// ordinary stop: a blocked wait or ask_user exits without a result, so the
// trailing tool call stays unresolved in the durable log for recovery instead of
// recording a premature cancelled result.
var ErrRestartSuspend = errors.New("daemon restart: session suspended")

// RestartSuspended reports whether ctx was cancelled by a graceful daemon
// restart rather than by an ordinary stop.
func RestartSuspended(ctx context.Context) bool {
	return ctx.Err() != nil && errors.Is(context.Cause(ctx), ErrRestartSuspend)
}
