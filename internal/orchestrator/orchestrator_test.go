package orchestrator

import (
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

func TestParseReviewJSON(t *testing.T) {
	rv := parseReview(`{"verdict":"accept","summary":"looks good","findings":[{"severity":"nit","message":"rename x"}]}`)
	if rv.Verdict != "accept" || rv.Summary != "looks good" {
		t.Fatalf("parsed = %+v", rv)
	}
	if len(rv.Findings) != 1 || rv.Findings[0].Severity != "nit" {
		t.Fatalf("findings = %+v", rv.Findings)
	}
}

// A reviewer that yielded plain text (never called submit_review) degrades to an
// "unknown" verdict with the text as the summary, rather than crashing.
func TestParseReviewPlainTextFallback(t *testing.T) {
	rv := parseReview("I think this looks fine overall.")
	if rv.Verdict != "unknown" {
		t.Fatalf("verdict = %q, want unknown", rv.Verdict)
	}
	if rv.Summary != "I think this looks fine overall." {
		t.Fatalf("summary = %q", rv.Summary)
	}
}

// newLoop copies Deps.Retry onto the built subagent loop so the configured
// transient-failure retry policy reaches implementer/reviewer loops (task 0133).
func TestNewLoopCopiesRetry(t *testing.T) {
	pol := engine.RetryPolicy{MaxAttempts: 1, BaseDelay: 42 * time.Millisecond, MaxDelay: 99 * time.Millisecond}
	d := &Deps{
		Emitter: event.NewEmitter(nil, "coordinator"),
		Retry:   pol,
	}
	spec := AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return nil }}
	loop := d.newLoop(spec, "sys", tools.New(), "implementer")
	if loop.Retry != pol {
		t.Fatalf("loop.Retry = %+v, want %+v", loop.Retry, pol)
	}
}
