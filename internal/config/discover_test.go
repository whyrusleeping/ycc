package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCuratedModelIDs(t *testing.T) {
	ids := CuratedModelIDs("anthropic")
	want := map[string]bool{"claude-opus-4-8": true, "claude-sonnet-4-5": true, "claude-fable-5": true}
	for w := range want {
		found := false
		for _, id := range ids {
			if id == w {
				found = true
			}
		}
		if !found {
			t.Errorf("curated anthropic ids missing %q; got %v", w, ids)
		}
	}
	// Mutating the returned slice must not affect the package data.
	ids[0] = "mutated"
	if CuratedModelIDs("anthropic")[0] == "mutated" {
		t.Fatal("CuratedModelIDs returned a shared slice")
	}
	if got := CuratedModelIDs("nonesuch"); len(got) != 0 {
		t.Errorf("unknown backend curated ids = %v, want empty", got)
	}
}

func TestDiscoverModelsOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth header = %q", got)
		}
		w.Write([]byte(`{"data":[{"id":"gpt-5.5"},{"id":"gpt-4o"},{"id":"gpt-4o"}]}`))
	}))
	defer srv.Close()

	got, err := DiscoverModels(context.Background(), "openai", srv.URL, "sk-test")
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if want := []string{"gpt-4o", "gpt-5.5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want %v (sorted, deduped)", got, want)
	}
}

func TestDiscoverModelsAnthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "key" {
			t.Errorf("x-api-key = %q", got)
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing anthropic-version header")
		}
		w.Write([]byte(`{"data":[{"id":"claude-opus-4-8"},{"id":"claude-sonnet-4-5"}]}`))
	}))
	defer srv.Close()

	got, err := DiscoverModels(context.Background(), "anthropic", srv.URL, "key")
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if want := []string{"claude-opus-4-8", "claude-sonnet-4-5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

func TestDiscoverModelsOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, want /api/tags", r.URL.Path)
		}
		w.Write([]byte(`{"models":[{"name":"qwen2.5-coder"},{"name":"llama3.3"}]}`))
	}))
	defer srv.Close()

	got, err := DiscoverModels(context.Background(), "ollama", srv.URL, "")
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if want := []string{"llama3.3", "qwen2.5-coder"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

func TestDiscoverModelsErrors(t *testing.T) {
	if _, err := DiscoverModels(context.Background(), "anthropic", "", "k"); err == nil {
		t.Error("expected error for empty base_url")
	}
	if _, err := DiscoverModels(context.Background(), "mystery", "http://x", ""); err == nil {
		t.Error("expected error for unsupported backend")
	}
}
