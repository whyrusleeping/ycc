// Package antigravity is the transport for `backend = "gemini"` models with
// `auth = "oauth"`: Gemini inference on a Google AI Pro/Ultra subscription
// through Google's Antigravity backend (the private Code Assist v1internal API
// the Antigravity app and CLI use), authenticated with googleauth's login.
//
// It reuses gollama's native Gemini client unchanged. An http.RoundTripper
// turns each generateContent / streamGenerateContent call into the
// Antigravity form:
//   - URL: <host>/v1internal:<method> instead of /models/<model>:<method>
//   - body: the Gemini request wrapped as {project, model, request, userAgent,
//     requestType, requestId}, with request.sessionId set and tool schemas
//     converted from parametersJsonSchema to the sanitized legacy parameters
//   - response: each SSE frame (and a unary body) unwrapped from
//     {response:{…}, traceId}
//   - auth: a per-request bearer token from googleauth, plus the Code Assist
//     project discovered through loadCodeAssist / onboardUser on first use
//
// The wire model id carries the thinking level (gemini-4-neon-high), chosen
// from the account's fetchAvailableModels catalog.
//
// Failures that must not be retried (no login, a rejected refresh token, an
// ineligible account, a terms-of-service suspension) are returned as HTTP
// 401/403 responses with a Google-style error body, so gollama reports them as
// APIErrors and the engine classifies them as non-retryable auth errors. A
// suspension is also persisted on the login so it is never used again.
//
// The protocol is unofficial and reverse-engineered; see backlog task 0601.
package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/googleauth"
	"github.com/whyrusleeping/ycc/internal/llmhttp"
)

// DefaultBaseURL is the Antigravity Code Assist host agy uses.
const DefaultBaseURL = "https://daily-cloudcode-pa.googleapis.com"

// clientBase is the placeholder base URL handed to gollama; the transport
// rewrites every request under it.
const clientBase = "https://antigravity.invalid/v1beta"

// UserAgent is sent on every Antigravity request. It follows the format agy
// 1.5.16 sent in an offline capture against a local mock (2026-11-24):
// "antigravity/cli/1.5.16 (aidev_client; os_type=linux; arch=amd64;
// cl=1028571191; auth_method=adc)". That capture used ADC auth; the value agy
// sends for a consumer Google login was not observed, and "oauth" is a guess.
// YCC_ANTIGRAVITY_USER_AGENT overrides it when Google's expected version
// drifts.
var UserAgent = func() string {
	if ua := strings.TrimSpace(os.Getenv("YCC_ANTIGRAVITY_USER_AGENT")); ua != "" {
		return ua
	}
	return "antigravity/cli/1.5.16 (aidev_client; os_type=" + runtime.GOOS + "; arch=" + runtime.GOARCH + "; cl=1028571191; auth_method=oauth)"
}()

// TokenSource returns a live access token for the current login.
type TokenSource func(ctx context.Context) (googleauth.Token, error)

// Store persists per-login state discovered by the transport. Both methods
// apply only while the login identified by refreshToken is still stored.
type Store interface {
	SetProject(refreshToken, project string) error
	MarkSuspended(refreshToken, detail string) error
}

type googleStore struct{}

func (googleStore) SetProject(rt, project string) error {
	return googleauth.Update(rt, func(c *googleauth.Credentials) { c.ProjectID = project })
}

func (googleStore) MarkSuspended(rt, detail string) error {
	return googleauth.Block(rt, detail)
}

// NewClient returns a gollama Gemini client that runs on the Antigravity
// backend. baseURL "" means DefaultBaseURL. httpClient supplies the
// underlying transport (its Transport is wrapped, not mutated).
func NewClient(baseURL string, httpClient *http.Client, tokens TokenSource) *gollama.Client {
	return newClient(baseURL, httpClient, tokens, googleStore{})
}

func newClient(baseURL string, httpClient *http.Client, tokens TokenSource, store Store) *gollama.Client {
	hc := &http.Client{}
	if httpClient != nil {
		*hc = *httpClient
	}
	hc.Transport = newTransport(baseURL, httpClient, tokens, store)
	c := gollama.NewClient(clientBase)
	c.SetGeminiMode(true)
	c.SetHTTPClient(hc)
	c.SetMaxRetries(0)
	return c
}

func newTransport(baseURL string, httpClient *http.Client, tokens TokenSource, store Store) *Transport {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	base := http.DefaultTransport
	if httpClient != nil && httpClient.Transport != nil {
		base = httpClient.Transport
	}
	return &Transport{
		base:      base,
		baseURL:   strings.TrimRight(baseURL, "/"),
		tokens:    tokens,
		store:     store,
		sessionID: randomID(),
	}
}

// Transport rewrites gollama Gemini requests into Antigravity requests.
type Transport struct {
	base      http.RoundTripper
	baseURL   string
	tokens    TokenSource
	store     Store
	sessionID string

	mu      sync.Mutex
	project string          // resolved project for projectFor
	projFor string          // refresh token project was resolved for
	catalog map[string]bool // fetchAvailableModels ids; nil = not loaded
	catFor  string          // refresh token the catalog was loaded for
	catTime time.Time
	// blocked holds suspensions seen by this transport, keyed by refresh
	// token, so a grant stops even if persisting the marker failed.
	blocked map[string]string
}

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// googleError builds a response carrying a Google-style error body.
func googleError(req *http.Request, status int, googleStatus, message string) *http.Response {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code": status, "status": googleStatus, "message": message,
	}})
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	model, method, ok := parseGeminiPath(req.URL.Path)
	if !ok || req.Method != http.MethodPost {
		return googleError(req, http.StatusNotFound, "NOT_FOUND", "the Antigravity transport supports only generateContent and streamGenerateContent"), nil
	}
	ctx := req.Context()
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	tok, err := t.tokens(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var se *googleauth.SuspendedError
		switch {
		case errors.As(err, &se):
			return googleError(req, http.StatusForbidden, "PERMISSION_DENIED", err.Error()), nil
		case errors.Is(err, googleauth.ErrNoLogin), errors.Is(err, googleauth.ErrReauth):
			return googleError(req, http.StatusUnauthorized, "UNAUTHENTICATED", err.Error()), nil
		}
		// Anything else (network, token service) is transient; the engine
		// classifies a transport error as retryable.
		return nil, err
	}
	if detail := t.blockedDetail(tok.RefreshToken); detail != "" {
		return googleError(req, http.StatusForbidden, "PERMISSION_DENIED", (&googleauth.SuspendedError{Detail: detail}).Error()), nil
	}
	project, failure, err := t.projectFor(ctx, req, tok)
	if err != nil || failure != nil {
		return failure, err
	}
	inner, thinkingLevel, err := prepareInner(body, t.sessionID)
	if err != nil {
		return nil, err
	}
	wireModel, failure, err := t.wireModel(ctx, tok, project, model, thinkingLevel)
	if err != nil || failure != nil {
		return failure, err
	}
	matchThinkingLevel(inner, wireModel)
	envelope, err := json.Marshal(map[string]any{
		"project":     project,
		"model":       wireModel,
		"request":     inner,
		"userAgent":   "antigravity",
		"requestType": "agent",
		"requestId":   "agent-" + randomID(),
	})
	if err != nil {
		return nil, err
	}
	stream := method == "streamGenerateContent"
	path := "/v1internal:" + method
	if stream {
		path += "?alt=sse"
	}
	resp, err := t.post(ctx, tok, path, envelope, stream)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return t.inspectError(resp, tok), nil
	}
	if stream {
		resp.Body = newSSEUnwrapper(resp.Body)
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		return resp, nil
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	data = unwrapResponse(data)
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	resp.Header.Del("Content-Length")
	return resp, nil
}

// parseGeminiPath extracts model and method from ".../models/<model>:<method>".
func parseGeminiPath(path string) (model, method string, ok bool) {
	i := strings.LastIndex(path, "/models/")
	if i < 0 {
		return "", "", false
	}
	rest := path[i+len("/models/"):]
	j := strings.LastIndexByte(rest, ':')
	if j <= 0 {
		return "", "", false
	}
	model, method = rest[:j], rest[j+1:]
	if method != "generateContent" && method != "streamGenerateContent" {
		return "", "", false
	}
	return model, method, true
}

func (t *Transport) post(ctx context.Context, tok googleauth.Token, path string, body []byte, stream bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	// agy sends no Accept header on unary calls; the streaming call was not
	// captured, so it keeps the SSE Accept.
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	return t.base.RoundTrip(req)
}

// postJSON performs a unary v1internal call and decodes a 200 reply into out.
// A non-200 reply is returned (already inspected) as the response.
func (t *Transport) postJSON(ctx context.Context, tok googleauth.Token, path string, in, out any) (*http.Response, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	resp, err := t.post(ctx, tok, path, body, false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return t.inspectError(resp, tok), nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, fmt.Errorf("antigravity %s: decoding response: %w", path, err)
	}
	return nil, nil
}

// inspectError buffers a non-200 body (with the access token redacted, in
// case a proxy echoes request headers), records a terms-of-service
// suspension on the login, and returns an equivalent response for gollama.
func (t *Transport) inspectError(resp *http.Response, tok googleauth.Token) *http.Response {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	data = []byte(llmhttp.Redact(string(data), tok.AccessToken))
	if detail, ok := suspension(resp.StatusCode, data); ok {
		t.mu.Lock()
		if t.blocked == nil {
			t.blocked = map[string]string{}
		}
		t.blocked[tok.RefreshToken] = detail
		t.mu.Unlock()
		if t.store != nil {
			_ = t.store.MarkSuspended(tok.RefreshToken, detail)
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	resp.Header.Del("Content-Length")
	return resp
}

// googleErrorBody is the google.rpc.Status error envelope.
type googleErrorBody struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type     string            `json:"@type"`
			Reason   string            `json:"reason"`
			Metadata map[string]string `json:"metadata"`
		} `json:"details"`
	} `json:"error"`
}

func (t *Transport) blockedDetail(refreshToken string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.blocked[refreshToken]
}

// suspension reports whether an error body is a terms-of-service suspension
// (403 with ErrorInfo.reason TOS_VIOLATION) and returns a short detail
// (message plus appeal URL) for the stored marker.
func suspension(status int, body []byte) (string, bool) {
	if status != http.StatusForbidden {
		return "", false
	}
	var eb googleErrorBody
	if json.Unmarshal(body, &eb) != nil {
		return "", false
	}
	found := false
	detail := strings.TrimSpace(eb.Error.Message)
	for _, d := range eb.Error.Details {
		if d.Reason != "TOS_VIOLATION" {
			continue
		}
		found = true
		if u := d.Metadata["appeal_url"]; u != "" {
			detail = strings.TrimSpace(detail + " appeal: " + u)
		}
	}
	if !found {
		return "", false
	}
	if detail == "" {
		detail = "TOS_VIOLATION"
	}
	if r := []rune(detail); len(r) > 500 {
		detail = string(r[:500])
	}
	return detail, true
}

// unwrapResponse returns the inner response of {response:{…}, traceId}, or
// data unchanged when it has no response member (e.g. an error object).
func unwrapResponse(data []byte) []byte {
	var env struct {
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(data, &env) == nil && len(env.Response) > 0 && env.Response[0] == '{' {
		return env.Response
	}
	return data
}

// sseUnwrapper rewrites each "data:" line of an Antigravity SSE stream to the
// plain Gemini frame it wraps; every other line passes through unchanged.
type sseUnwrapper struct {
	src     io.ReadCloser
	r       *bufio.Reader
	pending []byte
	err     error
}

func newSSEUnwrapper(src io.ReadCloser) io.ReadCloser {
	return &sseUnwrapper{src: src, r: bufio.NewReaderSize(src, 64<<10)}
}

func (u *sseUnwrapper) Read(p []byte) (int, error) {
	for len(u.pending) == 0 {
		if u.err != nil {
			return 0, u.err
		}
		line, err := u.r.ReadBytes('\n')
		if len(line) > 0 {
			u.pending = rewriteSSELine(line)
		}
		if err != nil {
			u.err = err
		}
	}
	n := copy(p, u.pending)
	u.pending = u.pending[n:]
	return n, nil
}

func (u *sseUnwrapper) Close() error { return u.src.Close() }

func rewriteSSELine(line []byte) []byte {
	trimmed := bytes.TrimRight(line, "\r\n")
	payload, ok := bytes.CutPrefix(trimmed, []byte("data:"))
	if !ok {
		return line
	}
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || payload[0] != '{' {
		return line
	}
	inner := unwrapResponse(payload)
	if len(inner) == len(payload) {
		return line
	}
	out := make([]byte, 0, len(inner)+8)
	out = append(out, "data: "...)
	out = append(out, inner...)
	return append(out, '\n')
}
