package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/secrets"
)

// callTool dispatches a single web tool directly (it needs no Workspace).
func callTool(t *testing.T, tool *gollama.Tool, args map[string]any) *gollama.ToolResult {
	t.Helper()
	res, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("%s.Call returned Go error: %v", tool.Name, err)
	}
	return res
}

func TestWebSearch(t *testing.T) {
	t.Setenv("EXA_API_KEY", "test-key")

	var gotNumResults int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing/incorrect x-api-key header: %q", r.Header.Get("x-api-key"))
		}
		if r.URL.Path != "/search" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		var body struct {
			NumResults int `json:"numResults"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		gotNumResults = body.NumResults
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"title":"Go Documentation","url":"https://go.dev/doc","highlights":["The Go programming language docs.","Effective Go."]},
			{"title":"Untitled","url":"https://example.com","text":"some body text here"}
		]}`))
	}))
	defer srv.Close()
	oldBase := exaBaseURL
	exaBaseURL = srv.URL
	defer func() { exaBaseURL = oldBase }()

	// num_results above the cap should clamp to exaMaxResults.
	res := callTool(t, webSearch(), map[string]any{"query": "golang docs", "num_results": float64(50)})
	if res.IsError {
		t.Fatalf("web_search errored: %s", res.Content)
	}
	if gotNumResults != exaMaxResults {
		t.Errorf("numResults = %d, want clamp to %d", gotNumResults, exaMaxResults)
	}
	for _, want := range []string{"Go Documentation", "https://go.dev/doc", "Effective Go", "https://example.com"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("result missing %q:\n%s", want, res.Content)
		}
	}
}

func TestFetchPage(t *testing.T) {
	t.Setenv("EXA_API_KEY", "test-key")

	bigText := strings.Repeat("x", exaFetchCap+500)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing x-api-key header")
		}
		if r.URL.Path != "/contents" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"results": []map[string]any{
			{"title": "Example", "url": "https://example.com", "text": bigText},
		}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	oldBase := exaBaseURL
	exaBaseURL = srv.URL
	defer func() { exaBaseURL = oldBase }()

	res := callTool(t, fetchPage(), map[string]any{"url": "https://example.com"})
	if res.IsError {
		t.Fatalf("fetch_page errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "Example") || !strings.Contains(res.Content, "https://example.com") {
		t.Errorf("missing title/url header:\n%s", res.Content[:200])
	}
	if !strings.Contains(res.Content, "[content truncated]") {
		t.Errorf("expected truncation marker for oversized text")
	}
	if len(res.Content) > exaFetchCap+200 {
		t.Errorf("output not bounded: %d chars", len(res.Content))
	}
}

func TestWebMissingKey(t *testing.T) {
	t.Setenv("EXA_API_KEY", "")
	// exaAPIKey falls back to the machine-local secrets store. Point the config
	// dir at an empty temp dir so the test doesn't depend on whether the
	// developer's machine has a stored key. UserConfigDir
	// derives from XDG_CONFIG_HOME (Linux), HOME
	// (darwin), and AppData (Windows) — override all three.
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("AppData", tmp)

	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		t.Errorf("HTTP request made despite missing key: %s", r.URL.Path)
	}))
	defer srv.Close()
	oldBase := exaBaseURL
	exaBaseURL = srv.URL
	defer func() { exaBaseURL = oldBase }()

	if res := callTool(t, webSearch(), map[string]any{"query": "x"}); !res.IsError {
		t.Errorf("web_search should error without key, got: %s", res.Content)
	}
	if res := callTool(t, fetchPage(), map[string]any{"url": "https://x.com"}); !res.IsError {
		t.Errorf("fetch_page should error without key, got: %s", res.Content)
	}
	if hit {
		t.Errorf("no HTTP request should be made when key is unset")
	}
}

func TestWebNon200(t *testing.T) {
	t.Setenv("EXA_API_KEY", "test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	oldBase := exaBaseURL
	exaBaseURL = srv.URL
	defer func() { exaBaseURL = oldBase }()

	if res := callTool(t, webSearch(), map[string]any{"query": "x"}); !res.IsError {
		t.Errorf("web_search should surface non-200 as error, got: %s", res.Content)
	}
}

func TestWebMalformedJSON(t *testing.T) {
	t.Setenv("EXA_API_KEY", "test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid json`))
	}))
	defer srv.Close()
	oldBase := exaBaseURL
	exaBaseURL = srv.URL
	defer func() { exaBaseURL = oldBase }()

	if res := callTool(t, fetchPage(), map[string]any{"url": "https://x.com"}); !res.IsError {
		t.Errorf("fetch_page should surface malformed JSON as error, got: %s", res.Content)
	}
}

func TestWebSecretReflectionSanitizedBeforeTruncation(t *testing.T) {
	const sentinel = "sk-test-BOUNDARY-SENTINEL-CREDENTIAL-0389"
	const reflectedPrefixLength = 12
	reflectedPrefix := sentinel[:reflectedPrefixLength]
	atBoundary := func(cap int) string {
		return strings.Repeat("x", cap-reflectedPrefixLength) + sentinel
	}
	t.Setenv("EXA_API_KEY", sentinel)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			var body struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode search request: %v", err)
			}
			if body.Query == "error" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(atBoundary(exaErrBodyCap)))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{
				"title": "result", "url": "https://example.com", "highlights": []string{atBoundary(exaSnippetCap)},
			}}})
		case "/contents":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{
				"title": "result", "url": "https://example.com", "text": atBoundary(exaFetchCap),
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldBase := exaBaseURL
	exaBaseURL = srv.URL
	defer func() { exaBaseURL = oldBase }()

	tests := []struct {
		name string
		call func(*testing.T) *gollama.ToolResult
	}{
		{name: "error body", call: func(t *testing.T) *gollama.ToolResult {
			return callTool(t, webSearch(), map[string]any{"query": "error"})
		}},
		{name: "search snippet", call: func(t *testing.T) *gollama.ToolResult {
			return callTool(t, webSearch(), map[string]any{"query": "success"})
		}},
		{name: "fetched content", call: func(t *testing.T) *gollama.ToolResult {
			return callTool(t, fetchPage(), map[string]any{"url": "https://example.com"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := tt.call(t)
			if strings.Contains(res.Content, reflectedPrefix) {
				t.Fatalf("model-visible truncated output returned credential prefix %q", reflectedPrefix)
			}
			if !strings.Contains(res.Content, "[REDACTED") {
				t.Fatalf("model-visible output lacks redaction marker: %q", res.Content)
			}
		})
	}
}

// A key from the secrets store (`ycc token set EXA_API_KEY`) is used directly,
// on every call, and is never reflected into model-visible output.
func TestStoredWebSecretIsUsedAndNeverReturned(t *testing.T) {
	const sentinel = "sk-test-WEB-SENTINEL-CREDENTIAL-0389"
	t.Setenv("EXA_API_KEY", "")
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("HOME", configDir)
	t.Setenv("AppData", configDir)
	if err := secrets.Set("EXA_API_KEY", sentinel); err != nil {
		t.Fatal(err)
	}

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("x-api-key"); got != sentinel {
			t.Errorf("request key = %q, want stored secret", got)
		}
		http.Error(w, "upstream reflected "+sentinel, http.StatusUnauthorized)
	}))
	defer srv.Close()
	oldBase := exaBaseURL
	exaBaseURL = srv.URL
	defer func() { exaBaseURL = oldBase }()

	for i, res := range []*gollama.ToolResult{
		callTool(t, webSearch(), map[string]any{"query": "x"}),
		callTool(t, webSearch(), map[string]any{"query": "y"}),
		callTool(t, fetchPage(), map[string]any{"url": "https://example.com"}),
	} {
		if !res.IsError {
			t.Fatalf("call %d: expected upstream error, got %s", i, res.Content)
		}
		if strings.Contains(res.Content, sentinel) {
			t.Fatalf("call %d: model-visible tool error returned the credential", i)
		}
		if !strings.Contains(res.Content, "Exa API returned") {
			t.Fatalf("call %d: expected the request to reach Exa, got %s", i, res.Content)
		}
	}
	if calls != 3 {
		t.Fatalf("upstream calls = %d, want 3 (stored key must not be single-use)", calls)
	}
}
