package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"

	"github.com/whyrusleeping/ycc/internal/secrets"
)

// Web tools let the agent look up documentation and read web content via Exa
// (https://exa.ai), which exposes both web search and content retrieval. The
// Exa API key is resolved from config/secrets (never hardcoded) and all error
// paths surface as tool error results so the agent can recover.

const (
	// exaDefaultResults is the default number of web_search results.
	exaDefaultResults = 5
	// exaMaxResults caps web_search results so output stays bounded.
	exaMaxResults = 10
	// exaSnippetCap caps each search-result snippet (chars) so a list of
	// results can't blow the context window.
	exaSnippetCap = 400
	// exaFetchCap caps fetch_page text (chars) for the same reason.
	exaFetchCap = 16000
	// exaErrBodyCap caps how much of a non-2xx response body we echo back.
	exaErrBodyCap = 512
)

// exaBaseURL is the Exa API root. It is a package var so tests can point it at
// an httptest server.
var exaBaseURL = "https://api.exa.ai"

// exaHTTPClient is the HTTP client used for Exa calls (overridable in tests).
var exaHTTPClient = &http.Client{Timeout: 30 * time.Second}

// exaAPIKey resolves the Exa API key following the project's key precedence:
// the environment first, then the machine-local secrets store. ok is false when
// no non-empty key is configured.
func exaAPIKey() (string, bool) {
	if v := strings.TrimSpace(os.Getenv("EXA_API_KEY")); v != "" {
		return v, true
	}
	if v, ok := secrets.Lookup("EXA_API_KEY"); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), true
	}
	return "", false
}

// Web returns the web tools (web_search, fetch_page). They are workspace-free.
func Web() []*gollama.Tool {
	return []*gollama.Tool{webSearch(), fetchPage()}
}

// exaPost marshals body, POSTs it to {exaBaseURL}{path} with the Exa auth
// headers, and decodes a 2xx JSON response into out. It returns a descriptive
// error (not a panic) for build/network/non-2xx/decode failures so callers can
// turn it into a tool error result.
func exaPost(ctx context.Context, key, path string, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exaBaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)

	resp, err := exaHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(data))
		if len(snippet) > exaErrBodyCap {
			snippet = snippet[:exaErrBodyCap] + "…"
		}
		return fmt.Errorf("Exa API returned %s: %s", resp.Status, snippet)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// truncate caps s to n chars, appending an ellipsis marker when it was cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimRight(s[:n], " \n\t") + "…"
}

// webSearch is the web_search tool: it queries the web via Exa and returns a
// numbered list of ranked results (title, URL, snippet).
func webSearch() *gollama.Tool {
	return &gollama.Tool{
		Name: "web_search",
		Description: "Search the web via Exa and return ranked results (title, URL, and a short snippet) for a " +
			"query. Use this to look up documentation, APIs, or current information. Requires the EXA_API_KEY to be " +
			"configured.",
		Params: obj(map[string]any{
			"query": strProp("the search query"),
			"num_results": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("number of results to return (default %d, max %d)", exaDefaultResults, exaMaxResults),
			},
		}, "query"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			query, ok := getString(params, "query")
			if !ok {
				return errResult("web_search: missing 'query'"), nil
			}
			key, ok := exaAPIKey()
			if !ok {
				return errResult("web_search: EXA_API_KEY is not set; set it in the environment or save it in the ycc secrets store"), nil
			}
			n := getInt(params, "num_results", exaDefaultResults)
			if n < 1 {
				n = 1
			}
			if n > exaMaxResults {
				n = exaMaxResults
			}

			reqBody := map[string]any{
				"query":      query,
				"numResults": n,
				"contents": map[string]any{
					"highlights": map[string]any{
						"numSentences":     2,
						"highlightsPerUrl": 2,
					},
				},
			}
			var out struct {
				Results []struct {
					Title      string   `json:"title"`
					URL        string   `json:"url"`
					Highlights []string `json:"highlights"`
					Text       string   `json:"text"`
				} `json:"results"`
			}
			if err := exaPost(ctx, key, "/search", reqBody, &out); err != nil {
				return errResult("web_search: %v", err), nil
			}
			if len(out.Results) == 0 {
				return okResult(fmt.Sprintf("No results found for %q.", query)), nil
			}

			var b strings.Builder
			fmt.Fprintf(&b, "Search results for %q:\n\n", query)
			for i, r := range out.Results {
				title := strings.TrimSpace(r.Title)
				if title == "" {
					title = "(untitled)"
				}
				snippet := strings.TrimSpace(strings.Join(r.Highlights, " "))
				if snippet == "" {
					snippet = strings.TrimSpace(r.Text)
				}
				snippet = truncate(strings.Join(strings.Fields(snippet), " "), exaSnippetCap)
				fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, title, strings.TrimSpace(r.URL))
				if snippet != "" {
					fmt.Fprintf(&b, "   %s\n", snippet)
				}
				b.WriteString("\n")
			}
			return okResult(strings.TrimRight(b.String(), "\n")), nil
		},
	}
}

// fetchPage is the fetch_page tool: it retrieves the readable text/markdown
// content of a URL via Exa for the agent to read.
func fetchPage() *gollama.Tool {
	return &gollama.Tool{
		Name: "fetch_page",
		Description: "Fetch the readable text content of a URL via Exa for you to read. Use this to read a web page " +
			"or documentation found via web_search. Output is truncated if very large. Requires the EXA_API_KEY to be " +
			"configured.",
		Params: obj(map[string]any{
			"url": strProp("the URL of the page to fetch"),
		}, "url"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			url, ok := getString(params, "url")
			if !ok {
				return errResult("fetch_page: missing 'url'"), nil
			}
			key, ok := exaAPIKey()
			if !ok {
				return errResult("fetch_page: EXA_API_KEY is not set; set it in the environment or save it in the ycc secrets store"), nil
			}

			reqBody := map[string]any{
				"urls": []string{url},
				"text": true,
			}
			var out struct {
				Results []struct {
					Title string `json:"title"`
					URL   string `json:"url"`
					Text  string `json:"text"`
				} `json:"results"`
			}
			if err := exaPost(ctx, key, "/contents", reqBody, &out); err != nil {
				return errResult("fetch_page: %v", err), nil
			}
			if len(out.Results) == 0 {
				return errResult("fetch_page: no content returned for %q", url), nil
			}

			r := out.Results[0]
			var b strings.Builder
			if t := strings.TrimSpace(r.Title); t != "" {
				fmt.Fprintf(&b, "# %s\n", t)
			}
			if u := strings.TrimSpace(r.URL); u != "" {
				fmt.Fprintf(&b, "%s\n", u)
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			text := strings.TrimSpace(r.Text)
			if len(text) > exaFetchCap {
				text = text[:exaFetchCap] + "\n…[content truncated]"
			}
			b.WriteString(text)
			return okResult(b.String()), nil
		},
	}
}
