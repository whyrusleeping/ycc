package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/googleauth"
)

type fakeStore struct {
	mu        sync.Mutex
	project   string
	suspended string
}

func (s *fakeStore) SetProject(rt, p string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rt == "rt" {
		s.project = p
	}
	return nil
}

func (s *fakeStore) MarkSuspended(rt, d string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rt == "rt" {
		s.suspended = d
	}
	return nil
}

func staticTokens(project string) TokenSource {
	return func(context.Context) (googleauth.Token, error) {
		return googleauth.Token{AccessToken: "at", RefreshToken: "rt", ProjectID: project}, nil
	}
}

// fakeBackend serves the v1internal endpoints; handlers keyed by path.
type fakeBackend struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []string
	handlers map[string]func(w http.ResponseWriter, body map[string]any)
}

func (f *fakeBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer at" {
		f.t.Errorf("%s: Authorization = %q", r.URL.Path, r.Header.Get("Authorization"))
	}
	if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "antigravity/") || !strings.Contains(ua, "auth_method=consumer") {
		f.t.Errorf("%s: User-Agent = %q", r.URL.Path, ua)
	}
	if r.Header.Get("Accept") != "" {
		f.t.Errorf("%s: unexpected Accept %q (agy sends none)", r.URL.Path, r.Header.Get("Accept"))
	}
	var body map[string]any
	if r.Method == http.MethodPost {
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &body); err != nil {
			f.t.Errorf("%s: body is not JSON: %s", r.URL.Path, data)
		}
	}
	h, ok := f.handlers[r.URL.Path]
	if !ok {
		f.t.Errorf("unexpected call %s", r.URL.RequestURI())
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h(w, body)
}

func newBackend(t *testing.T, handlers map[string]func(http.ResponseWriter, map[string]any)) (*fakeBackend, *httptest.Server) {
	f := &fakeBackend{t: t, handlers: handlers}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var catalog = func(w http.ResponseWriter, body map[string]any) {
	writeJSON(w, 200, map[string]any{"models": map[string]any{
		"gemini-4-neon-low": map[string]any{}, "gemini-4-neon-high": map[string]any{},
		"gemini-4-neon-xhigh": map[string]any{}, "gemini-3-flash": map[string]any{},
		"internal-model": map[string]any{"isInternal": true},
	}})
}

func toolOpts(effort string) gollama.RequestOptions {
	return gollama.RequestOptions{
		Model:    "gemini-4-neon",
		Messages: []gollama.Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "read go.mod"}},
		Tools: []gollama.ToolParam{{Type: "function", Function: &gollama.ToolFunction{
			Name: "read", Description: "Read a file",
			Parameters: map[string]any{
				"$schema": "http://json-schema.org/draft-07/schema#", "type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "default": "x"},
					"mode":  map[string]any{"const": "text"},
					"limit": map[string]any{"type": []any{"integer", "null"}},
				},
				"required": []any{"path", "missing"},
			},
		}}},
		Effort: effort,
	}
}

func TestStreamToolTurnThroughEnvelope(t *testing.T) {
	store := &fakeStore{}
	var sawStream map[string]any
	f, srv := newBackend(t, map[string]func(http.ResponseWriter, map[string]any){
		"/v1internal:loadCodeAssist": func(w http.ResponseWriter, body map[string]any) {
			if md, _ := body["metadata"].(map[string]any); md["ideType"] != "ANTIGRAVITY" {
				t.Errorf("loadCodeAssist metadata = %v", body["metadata"])
			}
			writeJSON(w, 200, map[string]any{"cloudaicompanionProject": "proj-1", "currentTier": map[string]any{"id": "free-tier"}, "paidTier": map[string]any{"id": "g1-ultra-tier"}})
		},
		"/v1internal:fetchAvailableModels": func(w http.ResponseWriter, body map[string]any) {
			if body["project"] != "proj-1" {
				t.Errorf("fetchAvailableModels project = %v", body["project"])
			}
			catalog(w, body)
		},
		"/v1internal:streamGenerateContent": func(w http.ResponseWriter, body map[string]any) {
			sawStream = body
			w.Header().Set("Content-Type", "text/event-stream")
			frames := []string{
				`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Reading.","thought":true}]}}]},"traceId":"t1"}`,
				`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"go.mod"}},"thoughtSignature":"sig-1"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}},"traceId":"t1"}`,
			}
			for _, fr := range frames {
				fmt.Fprintf(w, "data: %s\r\n\r\n", fr)
			}
		},
	})
	c := newClient(srv.URL, nil, staticTokens(""), store)
	resp, err := c.TurnStreamCtx(context.Background(), toolOpts("high"), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].Function.Name != "read" || !strings.Contains(calls[0].Function.Arguments, "go.mod") || calls[0].ThoughtSignature != "sig-1" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if store.project != "proj-1" {
		t.Errorf("project not persisted: %q", store.project)
	}
	if f.calls[len(f.calls)-1] != "POST /v1internal:streamGenerateContent?alt=sse" {
		t.Errorf("calls = %v", f.calls)
	}

	if sawStream["project"] != "proj-1" || sawStream["model"] != "gemini-4-neon-high" || sawStream["userAgent"] != "antigravity" ||
		sawStream["requestType"] != "agent" || len(strings.Split(fmt.Sprint(sawStream["requestId"]), "/")) != 5 ||
		!strings.HasPrefix(fmt.Sprint(sawStream["requestId"]), "agent/") {
		t.Errorf("envelope = %v", sawStream)
	}
	inner, _ := sawStream["request"].(map[string]any)
	sid, _ := inner["sessionId"].(string)
	if _, err := strconv.ParseInt(sid, 10, 64); err != nil || inner["systemInstruction"] == nil || inner["contents"] == nil {
		t.Errorf("inner request = %v", inner)
	}
	decl := inner["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
	if _, ok := decl["parametersJsonSchema"]; ok {
		t.Error("parametersJsonSchema must be converted to parameters")
	}
	params, _ := json.Marshal(decl["parameters"])
	want := `{"properties":{"limit":{"nullable":true,"type":"integer"},"mode":{"enum":["text"]},"path":{"type":"string"}},"required":["path"],"type":"object"}`
	if string(params) != want {
		t.Errorf("parameters = %s\nwant %s", params, want)
	}

	// A second turn on the same client reuses the project and catalog.
	before := len(f.calls)
	if _, err := c.TurnStreamCtx(context.Background(), toolOpts("max"), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[before:]; len(got) != 1 {
		t.Errorf("second turn calls = %v", got)
	}
	if sawStream["model"] != "gemini-4-neon-xhigh" {
		t.Errorf("max effort model = %v", sawStream["model"])
	}
	gc := sawStream["request"].(map[string]any)["generationConfig"].(map[string]any)
	if tc := gc["thinkingConfig"].(map[string]any); tc["thinkingLevel"] != "xhigh" {
		t.Errorf("thinkingConfig = %v", tc)
	}
}

func TestSuspensionIsPersistedAndNotRetryable(t *testing.T) {
	store := &fakeStore{}
	_, srv := newBackend(t, map[string]func(http.ResponseWriter, map[string]any){
		"/v1internal:fetchAvailableModels": catalog,
		"/v1internal:streamGenerateContent": func(w http.ResponseWriter, _ map[string]any) {
			writeJSON(w, 403, map[string]any{"error": map[string]any{
				"code": 403, "status": "PERMISSION_DENIED", "message": "This service has been disabled for violation of Terms of Service.",
				"details": []any{map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "TOS_VIOLATION",
					"domain": "cloudcode-pa.googleapis.com", "metadata": map[string]any{"appeal_url": "https://example.com/appeal"}}},
			}})
		},
	})
	c := newClient(srv.URL, nil, staticTokens("proj-1"), store)
	_, err := c.TurnStreamCtx(context.Background(), toolOpts("high"), func(string) {})
	info := engine.ClassifyAPIError(err)
	if info.Kind != engine.KindAuth || info.Retryable {
		t.Fatalf("classification = %+v (err %v)", info, err)
	}
	if !strings.Contains(store.suspended, "Terms of Service") || !strings.Contains(store.suspended, "https://example.com/appeal") {
		t.Fatalf("suspension marker = %q", store.suspended)
	}
}

func TestIneligibleAccountIsAnAuthError(t *testing.T) {
	_, srv := newBackend(t, map[string]func(http.ResponseWriter, map[string]any){
		"/v1internal:loadCodeAssist": func(w http.ResponseWriter, _ map[string]any) {
			writeJSON(w, 200, map[string]any{"ineligibleTiers": []any{map[string]any{
				"reasonCode": "INELIGIBLE_ACCOUNT", "reasonMessage": "Your account is not eligible", "tierId": "free-tier",
			}}})
		},
	})
	c := newClient(srv.URL, nil, staticTokens(""), &fakeStore{})
	_, err := c.TurnStreamCtx(context.Background(), toolOpts("high"), func(string) {})
	info := engine.ClassifyAPIError(err)
	if info.Kind != engine.KindAuth || info.Retryable || !strings.Contains(info.Message, "Your account is not eligible") {
		t.Fatalf("classification = %+v", info)
	}
}

func TestOnboardingPollsOperation(t *testing.T) {
	oldInterval := onboardPollInterval
	onboardPollInterval = time.Millisecond
	t.Cleanup(func() { onboardPollInterval = oldInterval })
	store := &fakeStore{}
	polls := 0
	_, srv := newBackend(t, map[string]func(http.ResponseWriter, map[string]any){
		"/v1internal:loadCodeAssist": func(w http.ResponseWriter, _ map[string]any) {
			writeJSON(w, 200, map[string]any{"allowedTiers": []any{map[string]any{"id": "standard-tier"}, map[string]any{"id": "free-tier", "isDefault": true}}})
		},
		"/v1internal:onboardUser": func(w http.ResponseWriter, body map[string]any) {
			if body["tierId"] != "free-tier" {
				t.Errorf("onboard tier = %v", body["tierId"])
			}
			writeJSON(w, 200, map[string]any{"name": "operations/onboard-1", "done": false})
		},
		"/v1internal/operations/onboard-1": func(w http.ResponseWriter, _ map[string]any) {
			polls++
			if polls < 2 {
				writeJSON(w, 200, map[string]any{"name": "operations/onboard-1", "done": false})
				return
			}
			writeJSON(w, 200, map[string]any{"name": "operations/onboard-1", "done": true, "response": map[string]any{"cloudaicompanionProject": map[string]any{"id": "proj-new"}}})
		},
		"/v1internal:fetchAvailableModels": catalog,
	})
	ids, err := listModels(context.Background(), newTransport(srv.URL, nil, staticTokens(""), store))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 4 || strings.Contains(strings.Join(ids, ","), "internal-model") {
		t.Errorf("ids = %v", ids)
	}
	if store.project != "proj-new" {
		t.Errorf("project = %q", store.project)
	}
}

func TestTokenFailures(t *testing.T) {
	for _, tc := range []struct {
		err       error
		retryable bool
	}{
		{googleauth.ErrNoLogin, false},
		{fmt.Errorf("refreshing: %w", googleauth.ErrReauth), false},
		{&googleauth.SuspendedError{Detail: "TOS_VIOLATION"}, false},
		{errors.New("refreshing Google subscription token: dial tcp: connection refused"), true},
	} {
		c := newClient("http://127.0.0.1:1", nil, func(context.Context) (googleauth.Token, error) {
			return googleauth.Token{}, tc.err
		}, &fakeStore{})
		_, err := c.TurnStreamCtx(context.Background(), toolOpts("high"), func(string) {})
		info := engine.ClassifyAPIError(err)
		if info.Retryable != tc.retryable || (!tc.retryable && info.Kind != engine.KindAuth) {
			t.Errorf("%v: classification = %+v", tc.err, info)
		}
	}
}

func TestCatalogSuspensionStopsBeforeInference(t *testing.T) {
	store := &fakeStore{}
	f, srv := newBackend(t, map[string]func(http.ResponseWriter, map[string]any){
		"/v1internal:fetchAvailableModels": func(w http.ResponseWriter, _ map[string]any) {
			writeJSON(w, 403, map[string]any{"error": map[string]any{"code": 403, "status": "PERMISSION_DENIED", "message": "disabled; token at was echoed",
				"details": []any{map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "TOS_VIOLATION"}}}})
		},
	})
	c := newClient(srv.URL, nil, staticTokens("proj-1"), store)
	for i := 0; i < 2; i++ {
		_, err := c.TurnStreamCtx(context.Background(), toolOpts("high"), func(string) {})
		if info := engine.ClassifyAPIError(err); info.Kind != engine.KindAuth || info.Retryable {
			t.Fatalf("turn %d: classification = %+v", i, info)
		}
	}
	// The second turn is refused in-process without contacting Google.
	if len(f.calls) != 1 || strings.Contains(store.suspended, " at ") || store.suspended == "" {
		t.Fatalf("calls = %v, marker = %q", f.calls, store.suspended)
	}
}

func TestImmediateOnboarding(t *testing.T) {
	store := &fakeStore{}
	_, srv := newBackend(t, map[string]func(http.ResponseWriter, map[string]any){
		"/v1internal:loadCodeAssist": func(w http.ResponseWriter, _ map[string]any) {
			writeJSON(w, 200, map[string]any{"currentTier": map[string]any{"id": "free-tier"}})
		},
		"/v1internal:onboardUser": func(w http.ResponseWriter, _ map[string]any) {
			writeJSON(w, 200, map[string]any{"name": "operations/x", "done": true, "response": map[string]any{"cloudaicompanionProject": map[string]any{"id": "proj-now"}}})
		},
		"/v1internal:fetchAvailableModels": catalog,
	})
	if _, err := listModels(context.Background(), newTransport(srv.URL, nil, staticTokens(""), store)); err != nil {
		t.Fatal(err)
	}
	if store.project != "proj-now" {
		t.Fatalf("project = %q", store.project)
	}
}

func TestSanitizeSchemaAllowlist(t *testing.T) {
	got, _ := json.Marshal(sanitizeSchema(map[string]any{
		"type":        "object",
		"prefixItems": []any{map[string]any{"type": "string"}},
		"properties": map[string]any{
			"n":    map[string]any{"const": json.Number("1")},
			"pick": map[string]any{"oneOf": []any{map[string]any{"type": "string", "examples": []any{"x"}}, map[string]any{"type": "integer"}}},
		},
	}))
	want := `{"properties":{"n":{"enum":["1"]},"pick":{"anyOf":[{"type":"string"},{"type":"integer"}]}},"type":"object"}`
	if string(got) != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestSanitizeSchemaKeepsPropertyNamedLikeKeywords(t *testing.T) {
	got, _ := json.Marshal(sanitizeSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"default": map[string]any{"type": "string"},
			"items":   map[string]any{"type": "array", "items": map[string]any{"type": "string", "format": "uri"}},
		},
		"required": []any{"default"},
	}))
	want := `{"properties":{"default":{"type":"string"},"items":{"items":{"type":"string"},"type":"array"}},"required":["default"],"type":"object"}`
	if string(got) != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}
