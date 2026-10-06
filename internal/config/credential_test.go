package config

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/anthropicauth"
	"github.com/whyrusleeping/ycc/internal/engine"
)

func TestRegistryRedactsProviderCredential(t *testing.T) {
	for _, tc := range []struct{ backend, secret string }{
		{"anthropic", "provider-key-secret"},
		{"openai-compatible", "provider-key-secret"},
		{"anthropic", "secret"},
		{"openai-compatible", "secret"},
	} {
		t.Run(tc.backend+"/"+tc.secret, func(t *testing.T) {
			backend, secret := tc.backend, tc.secret
			t.Setenv("YCC_TEST_PROVIDER_KEY", secret)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				credential := r.Header.Get("x-api-key")
				if credential == "" {
					credential = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				}
				if credential != secret {
					t.Errorf("credential=%q", credential)
				}
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprintf(w, `{"error":{"type":"rate_limit_error","message":"echo %s"}}`, credential)
			}))
			defer server.Close()
			reg := NewRegistry(&Config{Models: map[string]Model{"test": {Backend: backend, BaseURL: server.URL, Model: "test", KeyEnv: "YCC_TEST_PROVIDER_KEY"}}})
			client, _, err := reg.Build("test")
			if err != nil {
				t.Fatal(err)
			}
			for _, stream := range []bool{false, true} {
				if stream {
					_, err = client.(engine.StreamTurner).TurnStreamCtx(context.Background(), gollama.RequestOptions{Model: "test"}, nil)
				} else {
					_, err = client.TurnCtx(context.Background(), gollama.RequestOptions{Model: "test"})
				}
				if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
					t.Fatalf("error=%v", err)
				}
				var ae *gollama.APIError
				info := engine.ClassifyAPIError(err)
				if !errors.As(err, &ae) || ae.StatusCode != 429 || info.Kind != engine.KindRateLimit || !info.Retryable || !info.HasRetryAfter || info.RetryAfter.Seconds() != 7 || strings.Contains(info.Message, secret) {
					t.Fatalf("classification=%+v err=%v", info, err)
				}
			}
		})
	}
}

func TestOAuthRedactionFollowsTokenRotation(t *testing.T) {
	isolateSecrets(t)
	save := func(token string) {
		t.Helper()
		if err := anthropicauth.Save(&anthropicauth.Credentials{AccessToken: token, RefreshToken: "refresh-secret", ExpiresAt: time.Now().Add(time.Hour).Unix(), FlowVersion: anthropicauth.FlowVersion}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, `{"error":{"type":"rate_limit_error","message":"echo %s"}}`, r.Header.Get("Authorization"))
	}))
	defer server.Close()
	save("initial-access-secret")
	reg := NewRegistry(&Config{Models: map[string]Model{"test": {Backend: "anthropic", BaseURL: server.URL, Model: "test", Auth: "oauth"}}})
	client, _, err := reg.Build("test")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"initial-access-secret", "rotated-access-secret"} {
		save(token)
		_, err := client.TurnCtx(context.Background(), gollama.RequestOptions{Model: "test"})
		if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "[REDACTED]") {
			t.Fatalf("error=%v", err)
		}
	}
}

func TestDiscoveryAndInferenceRefuseCredentialRedirects(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	t.Setenv("YCC_TEST_PROVIDER_KEY", "provider-secret")
	for _, backend := range []string{"anthropic", "openai-compatible"} {
		if _, err := DiscoverModels(context.Background(), backend, server.URL, "provider-secret"); err == nil {
			t.Fatal("discovery redirect succeeded")
		}
		reg := NewRegistry(&Config{Models: map[string]Model{"test": {Backend: backend, BaseURL: server.URL, Model: "test", KeyEnv: "YCC_TEST_PROVIDER_KEY"}}})
		client, _, err := reg.Build("test")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.TurnCtx(context.Background(), gollama.RequestOptions{Model: "test"}); err == nil {
			t.Fatal("inference redirect succeeded")
		}
	}
	if received.Load() != 0 {
		t.Fatalf("redirect target received %d requests", received.Load())
	}
}
