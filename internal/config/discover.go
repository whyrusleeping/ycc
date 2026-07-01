package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// curatedModelIDs lists sensible built-in model ids per backend. They are used to
// prefill the connection form and as a fallback when live discovery
// (DiscoverModels) is unavailable (no network, no key, backend without a
// listing endpoint). They are suggestions only — the user can always add or
// remove ids by hand.
var curatedModelIDs = map[string][]string{
	"anthropic": {"claude-opus-4-8", "claude-sonnet-4-5", "claude-fable-5", "claude-haiku-4-5"},
	"openai":    {"gpt-5.5", "gpt-5-mini", "gpt-4o", "o3"},
	"glm":       {"glm-4.6", "glm-4.5-air"},
	"ollama":    {"qwen2.5-coder", "llama3.3", "deepseek-r1"},
}

// CuratedModelIDs returns the curated default model ids for a backend (spec §13).
// The returned slice is a copy the caller may mutate.
func CuratedModelIDs(backend string) []string {
	ids := curatedModelIDs[backend]
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// DiscoverModels queries a backend's model-listing endpoint and returns the
// available model ids (spec §13). It supports the OpenAI-compatible /models
// endpoint (openai, glm, openai-compatible), the Anthropic /v1/models endpoint,
// and the Ollama /api/tags endpoint. key is the resolved API credential (may be
// empty for keyless/local backends).
//
// It is a best-effort convenience for the connection form: on any failure the
// caller should fall back to CuratedModelIDs. The returned ids are sorted and
// de-duplicated.
func DiscoverModels(ctx context.Context, backend, baseURL, key string) ([]string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("discover models: base_url is required for backend %q", backend)
	}
	switch backend {
	case "openai", "openai-compatible", "glm":
		return discoverOpenAI(ctx, baseURL, key)
	case "anthropic":
		return discoverAnthropic(ctx, baseURL, key)
	case "ollama":
		return discoverOllama(ctx, baseURL)
	default:
		return nil, fmt.Errorf("discover models: unsupported backend %q", backend)
	}
}

// getJSON issues a GET and decodes a JSON body into out, applying the supplied
// headers. Non-2xx responses are surfaced as errors.
func getJSON(ctx context.Context, url string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// dataList is the shape shared by the OpenAI and Anthropic /models responses:
// {"data": [{"id": "..."}]}.
type dataList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func discoverOpenAI(ctx context.Context, baseURL, key string) ([]string, error) {
	var out dataList
	headers := map[string]string{"Authorization": "Bearer " + key}
	if key == "" {
		delete(headers, "Authorization")
	}
	if err := getJSON(ctx, baseURL+"/models", headers, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return normalizeIDs(ids), nil
}

func discoverAnthropic(ctx context.Context, baseURL, key string) ([]string, error) {
	// Anthropic exposes GET /v1/models. Account for a base_url that already
	// carries the /v1 suffix (mirrors gollama's anthropicEndpoint handling).
	url := baseURL + "/v1/models"
	if strings.HasSuffix(baseURL, "/v1") {
		url = baseURL + "/models"
	}
	var out dataList
	headers := map[string]string{
		"x-api-key":         key,
		"anthropic-version": "2023-06-01",
	}
	if err := getJSON(ctx, url, headers, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return normalizeIDs(ids), nil
}

func discoverOllama(ctx context.Context, baseURL string) ([]string, error) {
	var out struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := getJSON(ctx, baseURL+"/api/tags", nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		if m.Name != "" {
			ids = append(ids, m.Name)
		} else if m.Model != "" {
			ids = append(ids, m.Model)
		}
	}
	return normalizeIDs(ids), nil
}

// normalizeIDs sorts and de-duplicates a list of model ids.
func normalizeIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := ids[:0]
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
