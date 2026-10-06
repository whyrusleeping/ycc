package codex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/whyrusleeping/gollama"
)

func TestCodexRedactsCredentialErrors(t *testing.T) {
	const secret = "access-token-secret"
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"type\":\"error\",\"error\":{\"code\":\"server_error\",\"message\":%q}}\n\n", secret)
				} else {
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprint(w, "echo "+r.Header.Get("Authorization"))
				}
			}))
			defer server.Close()
			client := New(server.URL, func(context.Context) (string, string, error) { return secret, "account", nil })
			_, err := client.TurnCtx(context.Background(), gollama.RequestOptions{Model: "test"})
			if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
				t.Fatalf("error=%v", err)
			}
			if stream {
				var se *StreamError
				if !errors.As(err, &se) || se.Code != "server_error" || strings.Contains(se.ProviderErrorMessage(), secret) {
					t.Fatalf("metadata: %v", err)
				}
			} else {
				var ae *gollama.APIError
				if !errors.As(err, &ae) || ae.StatusCode != 429 || ae.Header.Get("Retry-After") != "7" {
					t.Fatalf("metadata: %v", err)
				}
			}
		})
	}
}

func TestCodexRefusesCredentialRedirect(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := New(server.URL, func(context.Context) (string, string, error) { return "access-secret", "account", nil })
	for _, reset := range []bool{false, true} {
		if reset {
			client.SetHTTPClient(nil)
		}
		_, err := client.TurnCtx(context.Background(), gollama.RequestOptions{Model: "test"})
		if err == nil {
			t.Fatal("redirect succeeded")
		}
	}
	if received.Load() != 0 {
		t.Fatalf("redirect target received %d requests", received.Load())
	}
}
