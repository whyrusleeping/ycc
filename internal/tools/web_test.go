package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
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
