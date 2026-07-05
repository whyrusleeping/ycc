package engine

// This file centralizes classification of LLM API call failures so that retry
// decisions (retry.go), context-window detection (context.go), and the
// structured session_error events the loop emits (loop.go) all agree on what an
// error IS. gollama surfaces HTTP failures as plain strings ("API returned
// non-200 status code NNN: <body>"), so classification is necessarily textual:
// parse the status code when present, fall back to transport-error detection.

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// APIErrorKind is a coarse category for an LLM API call failure, recorded on
// session_error / retry events so consumers (TUI, logs) can render actionable
// hints without re-parsing provider error bodies.
type APIErrorKind string

const (
	// KindRateLimit: HTTP 429 — the provider is rate limiting. Retryable.
	KindRateLimit APIErrorKind = "rate_limit"
	// KindOverloaded: HTTP 503/529 — the provider is overloaded. Retryable.
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
	// Retryable reports whether the failure is transient — worth retrying with
	// backoff — as opposed to a permanent error that will repeat identically.
	Retryable bool
}

// statusCodeRe extracts the status from gollama's error strings:
// "API returned non-200 status code 503: ...".
var statusCodeRe = regexp.MustCompile(`status code (\d+)`)

// contextLengthSignatures are the provider phrasings of "the conversation is
// too large for the model's context window". We match these real signatures
// only — deliberately NOT generic "max_tokens"/output-truncation phrasing,
// which is a distinct, recoverable condition handled in loop.go (see
// maxTruncRetries).
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

// ClassifyAPIError classifies an LLM API call failure. nil returns the zero
// APIErrorInfo (Kind ""). See the APIErrorKind constants for the taxonomy; the
// Retryable field is what the loop's retry policy keys on.
func ClassifyAPIError(err error) APIErrorInfo {
	if err == nil {
		return APIErrorInfo{}
	}
	msg := err.Error()
	lower := strings.ToLower(msg)

	// Context-window exceeded is checked FIRST: it arrives as a 400, but it has
	// its own kind (and its own user-facing handling in loop.go).
	for _, sig := range contextLengthSignatures {
		if strings.Contains(lower, sig) {
			return APIErrorInfo{Kind: KindContextLength, Status: parseStatus(msg), Retryable: false}
		}
	}

	if code := parseStatus(msg); code != 0 {
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
			return APIErrorInfo{Kind: KindAuth, Status: code, Retryable: false}
		case code >= 400 && code <= 499:
			return APIErrorInfo{Kind: KindInvalidRequest, Status: code, Retryable: false}
		default:
			return APIErrorInfo{Kind: KindUnknown, Status: code, Retryable: false}
		}
	}

	// No HTTP status: transport/network failure detection.
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
