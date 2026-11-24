package config

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

func TestGeminiModelDefaults(t *testing.T) {
	m := Model{Backend: "gemini", Model: "gemini-4-neon", KeyEnv: "GEMINI_API_KEY"}
	if err := m.Validate("neon"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	oauth := m
	oauth.Auth = "oauth"
	if err := oauth.Validate("neon"); err == nil {
		t.Fatal("gemini accepted auth = oauth")
	}
	if got := providerBaseURL("gemini", ""); got != gollama.DefaultGeminiBaseURL {
		t.Fatalf("default base URL = %q", got)
	}
	if got := m.SupportedThinkingLevels(); !reflect.DeepEqual(got, []string{"low", "medium", "high", "xhigh"}) {
		t.Fatalf("thinking levels = %v", got)
	}
	// Other Gemini models reject XHIGH, so it is not offered by default.
	argon := Model{Backend: "gemini", Model: "gemini-4-argon"}
	if got := argon.SupportedThinkingLevels(); !reflect.DeepEqual(got, []string{"low", "medium", "high"}) {
		t.Fatalf("argon thinking levels = %v", got)
	}
	argon.Effort = "xhigh"
	if got := argon.ResolveThinking().Effort; got != "high" {
		t.Fatalf("argon saved xhigh resolved to %q, want high", got)
	}
	if got := (Model{Backend: "gemini", Model: "models/gemini-4-neon"}).SupportedThinkingLevels(); !slices.Contains(got, "xhigh") {
		t.Fatalf("models/gemini-4-neon levels = %v", got)
	}
	if window, _ := m.ContextBudget(); window != 1048576 {
		t.Fatalf("context window = %d", window)
	}
}

// Gemini usage arrives with cached tokens already removed from Input (see the
// engine's normalization), billed at the cached rate with no write charge.
func TestGeminiNeonDefaultPricing(t *testing.T) {
	p := Model{Backend: "gemini", Model: "gemini-4-neon"}.EffectivePricing()
	if !p.Configured || p.Input != 3 || p.Output != 15 || p.CacheRead != 0.15 || p.CacheWrite != 0 {
		t.Fatalf("pricing = %+v", p)
	}
	cost, priced := p.Cost(event.Usage{Input: 200_000, Output: 100_000, CacheRead: 800_000})
	if want := 0.6 + 1.5 + 0.12; !priced || cost < want-1e-9 || cost > want+1e-9 {
		t.Fatalf("cost = %v (priced=%v), want %v", cost, priced, want)
	}
}

func TestDiscoverGeminiModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("x-goog-api-key") != "k" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("pageToken") == "" {
			_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-4-neon","supportedGenerationMethods":["generateContent","countTokens"]},{"name":"models/text-embedding-5","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"p2"}`)
			return
		}
		_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-4-hydrogen","supportedGenerationMethods":["generateContent"]}]}`)
	}))
	defer server.Close()
	ids, err := DiscoverModels(context.Background(), "gemini", server.URL, "k")
	if err != nil || !reflect.DeepEqual(ids, []string{"gemini-4-hydrogen", "gemini-4-neon"}) {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}

func TestGeminiBuildSendsAPIKeyHeader(t *testing.T) {
	t.Setenv("YCC_TEST_GEMINI_KEY", "gemini-secret")
	var gotKey, gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth, gotPath = r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization"), r.URL.Path
		body := `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			body = "data: " + body + "\n\n"
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	reg := NewRegistry(&Config{Models: map[string]Model{"neon": {Backend: "gemini", BaseURL: server.URL, Model: "gemini-4-neon", KeyEnv: "YCC_TEST_GEMINI_KEY"}}})
	client, model, err := reg.Build("neon")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.TurnCtx(context.Background(), gollama.RequestOptions{Model: model, Messages: []gollama.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("TurnCtx: %v", err)
	}
	if gotKey != "gemini-secret" || gotAuth != "" || !strings.Contains(gotPath, "/models/gemini-4-neon:") {
		t.Fatalf("x-goog-api-key=%q Authorization=%q path=%q", gotKey, gotAuth, gotPath)
	}
}
