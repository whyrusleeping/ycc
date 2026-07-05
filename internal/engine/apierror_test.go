package engine

import (
	"errors"
	"testing"
)

// TestClassifyAPIError covers the taxonomy: status-coded provider errors,
// context-window signatures (which arrive as 400s but get their own kind), and
// status-less transport failures.
func TestClassifyAPIError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		kind      APIErrorKind
		status    int
		retryable bool
	}{
		{"nil", nil, "", 0, false},
		{"rate limit", errors.New("API returned non-200 status code 429: rate limited"), KindRateLimit, 429, true},
		{"overloaded 529", errors.New("API returned non-200 status code 529: overloaded_error"), KindOverloaded, 529, true},
		{"overloaded 503", errors.New("API returned non-200 status code 503: unavailable"), KindOverloaded, 503, true},
		{"server 500", errors.New("API returned non-200 status code 500: boom"), KindServer, 500, true},
		{"server 502", errors.New("API returned non-200 status code 502: bad gateway"), KindServer, 502, true},
		{"timeout 408", errors.New("API returned non-200 status code 408: request timeout"), KindTimeout, 408, true},
		{"auth 401", errors.New("API returned non-200 status code 401: unauthorized"), KindAuth, 401, false},
		{"auth 403", errors.New("API returned non-200 status code 403: forbidden"), KindAuth, 403, false},
		{"invalid request 400", errors.New(`API returned non-200 status code 400: {"type":"error","error":{"type":"invalid_request_error","message":"tool_use ids were found without tool_result blocks"}}`), KindInvalidRequest, 400, false},
		{"not found 404", errors.New("API returned non-200 status code 404: no such model"), KindInvalidRequest, 404, false},
		{"context length anthropic", errors.New("API returned non-200 status code 400: prompt is too long: 250000 tokens > 200000 maximum"), KindContextLength, 400, false},
		{"context length openai", errors.New("API returned non-200 status code 400: context_length_exceeded"), KindContextLength, 400, false},
		{"network refused", errors.New("error sending request: dial tcp 1.2.3.4:443: connection refused"), KindNetwork, 0, true},
		{"network dns", errors.New("error sending request: lookup api.example.com: no such host"), KindNetwork, 0, true},
		{"transport timeout", errors.New("error sending request: context deadline exceeded"), KindTimeout, 0, true},
		{"unknown", errors.New("something completely different"), KindUnknown, 0, false},
	}
	for _, c := range cases {
		got := ClassifyAPIError(c.err)
		if got.Kind != c.kind || got.Status != c.status || got.Retryable != c.retryable {
			t.Errorf("%s: ClassifyAPIError(%v) = %+v, want kind=%s status=%d retryable=%v",
				c.name, c.err, got, c.kind, c.status, c.retryable)
		}
	}
}
