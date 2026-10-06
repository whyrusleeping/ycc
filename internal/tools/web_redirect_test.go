package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestExaRefusesCredentialRedirect(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	old := exaBaseURL
	exaBaseURL = server.URL
	t.Cleanup(func() { exaBaseURL = old })
	var out any
	if err := exaPost(context.Background(), "api-key-secret", "/search", map[string]string{"query": "test"}, &out); err == nil {
		t.Fatal("redirect succeeded")
	}
	if received.Load() != 0 {
		t.Fatalf("redirect target received %d requests", received.Load())
	}
}
