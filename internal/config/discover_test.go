package config

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/anthropicauth"
)

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

func TestDiscoverModelsAnthropicOAuthHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-ant-oat01-test" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got != anthropicauth.BetaHeader {
			t.Errorf("anthropic-beta = %q", got)
		}
		if got := r.Header.Get("x-app"); got != anthropicauth.AppHeader {
			t.Errorf("x-app = %q", got)
		}
		if got := r.Header.Get("x-api-key"); got != "" {
			t.Errorf("x-api-key = %q, want empty", got)
		}
		w.Write([]byte(`{"data":[{"id":"claude-fable-5"}]}`))
	}))
	defer srv.Close()

	got, err := DiscoverModels(context.Background(), "anthropic", srv.URL, "sk-ant-oat01-test")
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if want := []string{"claude-fable-5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

func TestDiscoverModelsAnthropicDefaultsBaseURL(t *testing.T) {
	oldTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got, want := r.URL.String(), DefaultAnthropicBaseURL+"/v1/models"; got != want {
			t.Errorf("URL = %q, want %q", got, want)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"claude-sonnet-4-5"}]}`)),
			Request:    r,
		}, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = oldTransport })

	got, err := DiscoverModels(context.Background(), "anthropic", "", "key")
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if want := []string{"claude-sonnet-4-5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
	if _, err := DiscoverModels(context.Background(), "ollama", "", ""); err == nil {
		t.Error("expected error for empty base_url without a provider default")
	}
	if _, err := DiscoverModels(context.Background(), "mystery", "http://x", ""); err == nil {
		t.Error("expected error for unsupported backend")
	}
}
