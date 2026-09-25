package engine

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
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
		// In-stream provider server failures (HTTP 200, so no status to parse).
		// The real codex frame, verbatim: retrying is what the provider asks for.
		{"codex in-stream server_error", errors.New(`codex: stream error: server_error: An error occurred while processing your request. You can retry your request, or contact us through our help center at help.openai.com if the error persists. Please include the request ID a66a36ef-2cb7-4c1c-be17-22f116c1a0ba in your message.`), KindServer, 0, true},
		{"codex response.failed server_error", errors.New("codex: server_error: boom"), KindServer, 0, true},
		{"provider internal error", errors.New("codex: stream error: internal_error: transient blip"), KindServer, 0, true},
		{"anthropic in-stream api_error", errors.New("anthropic stream error (api_error): Internal server error"), KindServer, 0, true},
		{"anthropic in-stream overloaded", errors.New("anthropic stream error (overloaded_error): Overloaded"), KindOverloaded, 0, true},
		{"codex in-stream overloaded", errors.New("codex: stream error: server_is_overloaded: Our servers are currently overloaded. Please try again later."), KindOverloaded, 0, true},
		{"codex usage limit", errors.New(`codex: stream error: usage_limit_reached: You have 0 weighted tokens left`), KindRateLimit, 0, true},
		{"provider rate limit error", errors.New("anthropic: rate_limit_error: allowance exhausted"), KindRateLimit, 0, true},
		{"provider usage limit text", errors.New("provider usage limit reached; try later"), KindRateLimit, 0, true},
		// A 4xx body mentioning server_error keeps its status-based (permanent)
		// classification — the signature must not override a parsed status.
		{"400 mentioning server_error", errors.New(`API returned non-200 status code 400: {"error":{"code":"not_server_error"}}`), KindInvalidRequest, 400, false},
		{"400 mentioning usage limit", errors.New(`API returned non-200 status code 400: {"error":{"message":"usage limit is invalid"}}`), KindInvalidRequest, 400, false},
		{"400 mentioning overloaded", errors.New("API returned non-200 status code 400: the field overloaded is invalid"), KindInvalidRequest, 400, false},
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

func TestStructuredProviderErrors(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		status        int
		body          string
		kind          APIErrorKind
		code, message string
	}{
		{429, `{"error":{"type":"rate_limit_error","message":"slow down"}}`, KindRateLimit, "rate_limit_error", "slow down"},
		{503, `{"type":"error","error":{"code":"server_error","message":"try again"}}`, KindOverloaded, "server_error", "try again"},
		{529, "overloaded", KindOverloaded, "", "overloaded"},
		{500, `{"code":"internal_error","message":"boom"}`, KindServer, "internal_error", "boom"},
		{401, "unauthorized", KindAuth, "", "unauthorized"},
		{403, "forbidden", KindAuth, "", "forbidden"},
		{400, `{"error":{"code":"server_error","message":"bad input"}}`, KindInvalidRequest, "server_error", "bad input"},
		{400, `{"error":{"code":"context_length_exceeded","message":"maximum context length"}}`, KindContextLength, "context_length_exceeded", "maximum context length"},
		{413, `{"message":"too many total text bytes"}`, KindContextLength, "", "too many total text bytes"},
		{429, `{"message":"prompt is too long"}`, KindRateLimit, "", "prompt is too long"},
		{503, `{"message":"context window unavailable"}`, KindOverloaded, "", "context window unavailable"},
	}
	for _, tc := range cases {
		got := ClassifyAPIErrorAt(&gollama.APIError{StatusCode: tc.status, Body: tc.body}, now)
		if got.Kind != tc.kind || got.Status != tc.status || got.Code != tc.code || got.Message != tc.message || got.Retryable != (tc.kind == KindRateLimit || tc.kind == KindOverloaded || tc.kind == KindServer) {
			t.Errorf("status %d body %q: %+v", tc.status, tc.body, got)
		}
	}
	// Error text mentioning a fake status must not override a typed HTTP status.
	got := ClassifyAPIErrorAt(&gollama.APIError{StatusCode: 401, Body: "status code 503"}, now)
	if got.Kind != KindAuth {
		t.Fatalf("typed status ignored: %+v", got)
	}
	for _, tc := range []struct {
		code  string
		kind  APIErrorKind
		retry bool
	}{
		{"usage_limit_reached", KindRateLimit, true}, {"server_is_overloaded", KindOverloaded, true},
		{"internal_error", KindServer, true}, {"context_length_exceeded", KindContextLength, false},
		{"server_error", KindServer, true},
	} {
		err := &testProviderError{code: tc.code, text: "legacy text mentioning rate_limit_error"}
		got := ClassifyAPIErrorAt(err, now)
		if got.Kind != tc.kind || got.Code != tc.code || got.Retryable != tc.retry {
			t.Errorf("%s: %+v", tc.code, got)
		}
	}
}

type testProviderError struct{ code, text string }

func (e *testProviderError) Error() string                { return e.text }
func (e *testProviderError) ProviderErrorCode() string    { return e.code }
func (e *testProviderError) ProviderErrorMessage() string { return e.text }

func TestRetryAfterMetadata(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name, retryAfter, ms string
		want                 time.Duration
		ok                   bool
	}{
		{"missing", "", "", 0, false}, {"bad", "nope", "", 0, false},
		{"negative", "-1", "", 0, false}, {"past", now.Add(-time.Minute).Format(http.TimeFormat), "", 0, true},
		{"future", now.Add(15 * time.Second).Format(http.TimeFormat), "", 15 * time.Second, true},
		{"seconds", "8", "", 8 * time.Second, true}, {"ms fallback", "bad", "1200", 1200 * time.Millisecond, true},
		{"negative ms", "", "-1", 0, false}, {"bad ms", "", "1.2", 0, false},
		{"huge ms", "", "999999999999999999999", 0, false},
		{"precedence", "2", "9000", 2 * time.Second, true},
	}
	for _, tc := range cases {
		h := http.Header{}
		if tc.retryAfter != "" {
			h.Set("Retry-After", tc.retryAfter)
		}
		if tc.ms != "" {
			h.Set("Retry-After-Ms", tc.ms)
		}
		got := ClassifyAPIErrorAt(&gollama.APIError{StatusCode: 429, Body: "limited", Header: h}, now)
		if got.HasRetryAfter != tc.ok || got.RetryAfter != tc.want {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
	long := "line1\n" + strings.Repeat("界", 350)
	got := ClassifyAPIErrorAt(&gollama.APIError{StatusCode: 500, Body: long, Header: http.Header{"X-Secret": []string{"credential"}}}, now)
	if strings.Contains(got.Message, "\n") || strings.Contains(got.Message, "credential") || len([]rune(got.Message)) > 301 {
		t.Fatalf("unsafe diagnostic: %q", got.Message)
	}
}

func TestAnthropicResetFallback(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	reset := func(seconds int) string { return now.Add(time.Duration(seconds) * time.Second).Format(time.RFC3339) }
	cases := []struct {
		name    string
		status  int
		headers http.Header
		want    time.Duration
		ok      bool
	}{
		{"exhausted requests", 429, http.Header{
			"Anthropic-Ratelimit-Requests-Remaining": {" 0 "},
			"Anthropic-Ratelimit-Requests-Reset":     {reset(20)},
		}, 20 * time.Second, true},
		{"latest exhausted reset", 429, http.Header{
			"Anthropic-Ratelimit-Requests-Remaining":      {"0"},
			"Anthropic-Ratelimit-Requests-Reset":          {reset(20)},
			"Anthropic-Ratelimit-Input-Tokens-Remaining":  {"0"},
			"Anthropic-Ratelimit-Input-Tokens-Reset":      {reset(45)},
			"Anthropic-Ratelimit-Output-Tokens-Remaining": {"1"},
			"Anthropic-Ratelimit-Output-Tokens-Reset":     {reset(90)},
		}, 45 * time.Second, true},
		{"nonzero remaining", 429, http.Header{
			"Anthropic-Ratelimit-Tokens-Remaining": {"1"},
			"Anthropic-Ratelimit-Tokens-Reset":     {reset(20)},
		}, 0, false},
		{"malformed reset", 429, http.Header{
			"Anthropic-Ratelimit-Tokens-Remaining": {"0"},
			"Anthropic-Ratelimit-Tokens-Reset":     {"tomorrow"},
		}, 0, false},
		{"past reset", 429, http.Header{
			"Anthropic-Ratelimit-Output-Tokens-Remaining": {"0"},
			"Anthropic-Ratelimit-Output-Tokens-Reset":     {reset(-20)},
		}, 0, true},
		{"retry after precedence", 429, http.Header{
			"Retry-After":                            {"3"},
			"Anthropic-Ratelimit-Requests-Remaining": {"0"},
			"Anthropic-Ratelimit-Requests-Reset":     {reset(20)},
		}, 3 * time.Second, true},
		{"milliseconds precedence", 429, http.Header{
			"Retry-After-Ms":                         {"5000"},
			"Anthropic-Ratelimit-Requests-Remaining": {"0"},
			"Anthropic-Ratelimit-Requests-Reset":     {reset(20)},
		}, 5 * time.Second, true},
		{"bad retry headers fall back", 429, http.Header{
			"Retry-After": {"invalid"}, "Retry-After-Ms": {"invalid"},
			"Anthropic-Ratelimit-Requests-Remaining": {"0"},
			"Anthropic-Ratelimit-Requests-Reset":     {reset(20)},
		}, 20 * time.Second, true},
		{"non-429 ignores reset", 503, http.Header{
			"Anthropic-Ratelimit-Requests-Remaining": {"0"},
			"Anthropic-Ratelimit-Requests-Reset":     {reset(20)},
		}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyAPIErrorAt(&gollama.APIError{StatusCode: tc.status, Body: "limited", Header: tc.headers}, now)
			if got.HasRetryAfter != tc.ok || got.RetryAfter != tc.want {
				t.Fatalf("retry metadata = %+v, want duration %v present %t", got, tc.want, tc.ok)
			}
		})
	}
}
