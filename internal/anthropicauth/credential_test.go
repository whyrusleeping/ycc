package anthropicauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTokenCredentialHygiene(t *testing.T) {
	old := TokenEndpoint
	t.Cleanup(func() { TokenEndpoint = old })
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	for _, redirect := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if redirect {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				return
			}
			data, _ := io.ReadAll(r.Body)
			w.WriteHeader(http.StatusUnauthorized)
			w.Write(data) // A proxy reflecting the submitted OAuth payload.
		}))
		TokenEndpoint = server.URL
		for _, grant := range []string{"refresh", "exchange"} {
			var err error
			if grant == "refresh" {
				_, err = Refresh(context.Background(), "refresh-secret")
			} else {
				_, err = Exchange(context.Background(), "authorization-secret", PKCE{Verifier: "verifier-secret"})
			}
			if err == nil {
				t.Fatalf("%s redirect=%v succeeded", grant, redirect)
			}
			for _, secret := range []string{"refresh-secret", "authorization-secret", "verifier-secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("credential in error: %v", err)
				}
			}
			if !redirect && !strings.Contains(err.Error(), "[REDACTED]") {
				t.Fatalf("no redaction: %v", err)
			}
		}
		server.Close()
	}
	if received.Load() != 0 {
		t.Fatalf("redirect target received %d OAuth requests", received.Load())
	}
}
