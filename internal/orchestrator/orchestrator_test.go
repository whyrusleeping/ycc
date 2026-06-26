package orchestrator

import "testing"

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
