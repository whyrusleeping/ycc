package engine

// This file centralizes classification of LLM API call failures so that retry
// decisions (retry.go), context-window detection (context.go), and the
// structured session_error events the loop emits (loop.go) all agree on what an
// error IS. Structured provider status/code takes precedence; textual signatures
// remain for legacy transports and untyped in-stream failures.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/llmhttp"
)

// APIErrorKind is a coarse category for an LLM API call failure, recorded on
// session_error / retry events so consumers (TUI, logs) can render actionable
// hints without re-parsing provider error bodies.
type APIErrorKind string

const (
	// KindRateLimit: HTTP 429 — the provider is rate limiting. Retryable.
	KindRateLimit APIErrorKind = "rate_limit"
	// KindOverloaded: HTTP 503/529 or an equivalent in-stream provider error —
	// the provider is overloaded. Retryable.
	KindOverloaded APIErrorKind = "overloaded"
	// KindServer: any other 5xx. Retryable.
	KindServer APIErrorKind = "server"
	// KindTimeout: HTTP 408 or a transport-level timeout. Retryable.
	KindTimeout APIErrorKind = "timeout"
	// KindNetwork: a transport failure with no HTTP status (connection refused/
	// reset, DNS, TLS, EOF, ...). Retryable.
	KindNetwork APIErrorKind = "network"
	// KindAuth: HTTP 401/403 — bad or missing credentials. NOT retryable;
	// the user must fix the key/config.
	KindAuth APIErrorKind = "auth"
	// KindContextLength: the conversation no longer fits the model's context
	// window (a 400 with provider-specific phrasing). NOT retryable — the same
	// request will fail identically forever; the session needs a fresh start or
	// narrower scope.
	KindContextLength APIErrorKind = "context_length"
	// KindInvalidRequest: HTTP 400/404/422 and other 4xx — the request itself
	// is malformed (e.g. an inconsistent transcript). NOT retryable.
	KindInvalidRequest APIErrorKind = "invalid_request"
	// KindUnknown: unclassifiable. NOT retryable (retrying an unknown failure
	// blindly risks burning tokens on a permanent error).
	KindUnknown APIErrorKind = "unknown"
)

// APIErrorInfo is the classification of one LLM API call failure.
type APIErrorInfo struct {
	Kind APIErrorKind
	// Status is the HTTP status code when one could be parsed from the error,
	// else 0 (transport failures have no status).
	Status int
	// Retryable reports whether the failure is transient.
	Retryable     bool
	Code          string        // provider code/type, when available
	Message       string        // bounded, single-line diagnostic; never response headers
	RetryAfter    time.Duration // provider-requested minimum wait relative to classification time
	HasRetryAfter bool
}

// providerErrorMetadata is implemented by in-stream errors without coupling
// the backend transport to the engine (the engine itself imports that backend).
type providerErrorMetadata interface {
	ProviderErrorCode() string
	ProviderErrorMessage() string
}

// safeDiagnostic bounds diagnostics before they reach live or durable events.
func safeDiagnostic(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

func boundedErrorText(s string) string {
	r := []rune(s)
	if len(r) > 2000 {
		return string(r[:2000]) + "…"
	}
	return s
}

func providerBody(body string) (code, message string) {
	var raw struct {
		Code, Type, Message string
		Error               *struct{ Code, Type, Message string }
	}
	if json.Unmarshal([]byte(body), &raw) == nil {
		if raw.Error != nil {
			code, message = raw.Error.Code, raw.Error.Message
			if code == "" {
				code = raw.Error.Type
			}
		} else {
			code, message = raw.Code, raw.Message
			if code == "" && raw.Type != "error" {
				code = raw.Type
			}
		}
	}
	if message == "" {
		message = body
	}
	return safeDiagnostic(code), safeDiagnostic(message)
}

func statusKind(code int, body string) APIErrorInfo {
	if code >= 400 && code < 500 && code != 429 && hasContextSignature(strings.ToLower(body)) {
		return APIErrorInfo{Kind: KindContextLength, Status: code}
	}
	switch {
	case code == 429:
		return APIErrorInfo{Kind: KindRateLimit, Status: code, Retryable: true}
	case code == 503 || code == 529:
		return APIErrorInfo{Kind: KindOverloaded, Status: code, Retryable: true}
	case code == 408:
		return APIErrorInfo{Kind: KindTimeout, Status: code, Retryable: true}
	case code >= 500 && code <= 599:
		return APIErrorInfo{Kind: KindServer, Status: code, Retryable: true}
	case code == 401 || code == 403:
		return APIErrorInfo{Kind: KindAuth, Status: code}
	case code >= 400 && code <= 499:
		return APIErrorInfo{Kind: KindInvalidRequest, Status: code}
	default:
		return APIErrorInfo{Kind: KindUnknown, Status: code}
	}
}

// anthropicResetWait only uses exhausted buckets. Multiple exhausted limits
// must all reset before another attempt is useful, hence the latest timestamp.
func anthropicResetWait(h http.Header, now time.Time) (time.Duration, bool) {
	var latest time.Time
	for _, bucket := range []string{"requests", "tokens", "input-tokens", "output-tokens"} {
		prefix := "anthropic-ratelimit-" + bucket + "-"
		if strings.TrimSpace(h.Get(prefix+"remaining")) != "0" {
			continue
		}
		reset, err := time.Parse(time.RFC3339, strings.TrimSpace(h.Get(prefix+"reset")))
		if err == nil && (latest.IsZero() || reset.After(latest)) {
			latest = reset
		}
	}
	if latest.IsZero() {
		return 0, false
	}
	d := latest.Sub(now)
	if d < 0 {
		d = 0
	}
	return d, true
}

func hasContextSignature(lower string) bool {
	for _, sig := range contextLengthSignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// statusCodeRe extracts the status from gollama's error strings:
// "API returned non-200 status code 503: ...".
var statusCodeRe = regexp.MustCompile(`status code (\d+)`)

// contextLengthSignatures are the provider phrasings of "the conversation is
// too large for the model's context window". We match these real signatures
// only — deliberately NOT generic "max_tokens"/output-truncation phrasing,
// which requires a larger output cap or less thinking, not context compaction.
var contextLengthSignatures = []string{
	"prompt is too long",                // Anthropic
	"context_length_exceeded",           // OpenAI-compatible error code
	"maximum context length",            // OpenAI-compatible message
	"reduce the length of the messages", // OpenAI-compatible hint
	"context window",                    // generic
	"too many total text bytes",         // some gateways
}

// timeoutSignatures and networkSignatures are substring fallbacks for transport
// failures that reach us as plain strings (gollama wraps http errors with
// fmt.Errorf, losing types). Timeout phrasings are checked first so a wrapped
// "error sending request: context deadline exceeded" classifies as a timeout,
// not a generic network failure.
var timeoutSignatures = []string{
	"timeout",
	"timed out",
	"deadline exceeded",
}

var networkSignatures = []string{
	"error sending request",
	"connection refused",
	"connection reset",
	"no such host",
	"tls handshake",
	"eof",
}

// providerServerSignatures are provider-reported SERVER-side failure codes that
// reach us with no HTTP status, because they are delivered inside an otherwise
// healthy (HTTP 200) response stream rather than as an HTTP error. The codex
// backend does this: `{"type":"error","error":{"code":"server_error",...}}` mid
// stream, with a message that explicitly tells the client to retry; Anthropic
// similarly emits `api_error` for an in-stream 500 equivalent. These are
// transient — the request/transcript is still valid and the next attempt
// normally succeeds — so they classify exactly like their 5xx equivalents
// instead of falling through to the non-retryable `unknown` bucket. Matched
// only when no status code was parsed, so a 4xx body mentioning "server_error"
// is unaffected. These signatures are checked after rate-limit signatures so
// provider-specific rate-limit errors retain their more specific classification.
var providerServerSignatures = []string{
	"server_error",
	"internal_error",
	"internal server error",
	"api_error",
}

// providerRateLimitSignatures are provider-reported subscription/rate-limit
// failures delivered inside an HTTP 200 response stream. Codex uses
// `usage_limit_reached` for subscription exhaustion, so there is no HTTP 429 for
// the status parser to see. As with providerServerSignatures, these are consulted
// only after status-code classification so text in a real 4xx body cannot
// override its HTTP classification.
var providerRateLimitSignatures = []string{
	"usage_limit_reached",
	"rate_limit_error",
	"usage limit",
}

// providerOverloadedSignatures are provider-reported overload failures delivered
// inside an HTTP 200 response stream. Anthropic emits `overloaded_error` as its
// in-stream 529 equivalent, while codex emits `server_is_overloaded`. The generic
// signature catches other provider phrasings. These are consulted only when no
// status code was parsed, so text in a real 4xx body cannot override its HTTP
// classification.
var providerOverloadedSignatures = []string{
	"overloaded_error",
	"server_is_overloaded",
	"overloaded",
}

// ClassifyAPIError classifies an LLM API call failure. nil returns the zero
// APIErrorInfo (Kind ""). See the APIErrorKind constants for the taxonomy; the
// Retryable field is what the loop's retry policy keys on.
func ClassifyAPIError(err error) APIErrorInfo { return ClassifyAPIErrorAt(err, time.Now()) }

// ClassifyAPIErrorAt classifies with an injected clock for HTTP-date guidance.
func ClassifyAPIErrorAt(err error, now time.Time) (result APIErrorInfo) {
	if err == nil {
		return APIErrorInfo{}
	}
	defer func() {
		if result.Message == "" {
			result.Message = safeDiagnostic(err.Error())
		}
	}()
	if errors.Is(err, context.Canceled) {
		return APIErrorInfo{Kind: KindUnknown}
	}
	if errors.Is(err, llmhttp.ErrStreamStalled) || errors.Is(err, llmhttp.ErrTotalTimeout) {
		return APIErrorInfo{Kind: KindTimeout, Retryable: true}
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if ae, ok := gollama.AsAPIError(err); ok {
		info := statusKind(ae.StatusCode, ae.Body)
		info.Code, info.Message = providerBody(ae.Body)
		if d, ok := ae.RetryAfter(now); ok {
			info.RetryAfter, info.HasRetryAfter = d, true
		} else if ms := strings.TrimSpace(ae.Header.Get("retry-after-ms")); ms != "" {
			if n, err := strconv.ParseInt(ms, 10, 64); err == nil && n >= 0 && n <= int64((1<<63-1)/int64(time.Millisecond)) {
				info.RetryAfter, info.HasRetryAfter = time.Duration(n)*time.Millisecond, true
			}
		}
		// Anthropic reset timestamps are a fallback only when neither retry
		// header supplied valid guidance on a rate-limited response.
		if !info.HasRetryAfter && ae.StatusCode == 429 {
			info.RetryAfter, info.HasRetryAfter = anthropicResetWait(ae.Header, now)
		}
		return info
	}
	var pe providerErrorMetadata
	if errors.As(err, &pe) {
		info := APIErrorInfo{Code: safeDiagnostic(pe.ProviderErrorCode()), Message: safeDiagnostic(pe.ProviderErrorMessage())}
		if info.Message == "" {
			info.Message = safeDiagnostic(msg)
		}
		switch strings.ToLower(pe.ProviderErrorCode()) {
		case "usage_limit_reached", "rate_limit_error", "rate_limit_exceeded":
			info.Kind, info.Retryable = KindRateLimit, true
		case "server_is_overloaded", "overloaded_error":
			info.Kind, info.Retryable = KindOverloaded, true
		case "server_error", "internal_error", "api_error":
			info.Kind, info.Retryable = KindServer, true
		case "context_length_exceeded":
			info.Kind = KindContextLength
		}
		if info.Kind != "" {
			return info
		}
		defer func() { result.Code, result.Message = info.Code, info.Message }()
	}
	// Legacy transports still return untyped error strings.
	if hasContextSignature(lower) {
		return APIErrorInfo{Kind: KindContextLength, Status: parseStatus(msg), Message: safeDiagnostic(msg)}
	}
	if code := parseStatus(msg); code != 0 {
		info := statusKind(code, msg)
		info.Message = safeDiagnostic(msg)
		return info
	}

	// No HTTP status. A provider may still report a rate limit, overload, or
	// server-side failure inside a 200 stream; treat it like the 429/5xx it stands
	// for (checked before generic transport heuristics, which it would otherwise
	// fall past into `unknown`).
	for _, sig := range providerRateLimitSignatures {
		if strings.Contains(lower, sig) {
			return APIErrorInfo{Kind: KindRateLimit, Retryable: true}
		}
	}
	for _, sig := range providerOverloadedSignatures {
		if strings.Contains(lower, sig) {
			return APIErrorInfo{Kind: KindOverloaded, Retryable: true}
		}
	}
	for _, sig := range providerServerSignatures {
		if strings.Contains(lower, sig) {
			return APIErrorInfo{Kind: KindServer, Retryable: true}
		}
	}

	// A truncated provider stream is transient, even when its EOF has no HTTP
	// status and did not include a transport-level timeout.
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return APIErrorInfo{Kind: KindNetwork, Retryable: true}
	}
	// Transport/network failure detection.
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return APIErrorInfo{Kind: KindTimeout, Retryable: true}
		}
		return APIErrorInfo{Kind: KindNetwork, Retryable: true}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return APIErrorInfo{Kind: KindNetwork, Retryable: true}
	}
	for _, frag := range timeoutSignatures {
		if strings.Contains(lower, frag) {
			return APIErrorInfo{Kind: KindTimeout, Retryable: true}
		}
	}
	for _, frag := range networkSignatures {
		if strings.Contains(lower, frag) {
			return APIErrorInfo{Kind: KindNetwork, Retryable: true}
		}
	}
	return APIErrorInfo{Kind: KindUnknown, Retryable: false}
}

// parseStatus extracts an HTTP status code from a gollama error string, or 0.
func parseStatus(msg string) int {
	m := statusCodeRe.FindStringSubmatch(msg)
	if m == nil {
		return 0
	}
	code, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return code
}
