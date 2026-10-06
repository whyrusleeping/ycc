package subusage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestUsageRefusesCredentialRedirect(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	fetcher := NewHTTPFetcher()
	fetcher.AnthropicURL = server.URL
	fetcher.AnthropicToken = func(context.Context) (string, error) { return "access-secret", nil }
	for _, defaults := range []bool{false, true} {
		if defaults {
			fetcher.Client = nil
		}
		if _, err := fetcher.Fetch(context.Background(), "anthropic"); err == nil {
			t.Fatal("redirect succeeded")
		}
	}
	if received.Load() != 0 {
		t.Fatalf("redirect target received %d requests", received.Load())
	}
}
