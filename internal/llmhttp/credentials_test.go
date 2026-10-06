package llmhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestCredentialRedirects(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Add(1)
	}))
	defer target.Close()
	for _, tls := range []bool{false, true} {
		t.Run(fmt.Sprintf("tls=%v", tls), func(t *testing.T) {
			var followed atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/same":
					http.Redirect(w, r, "/ok", http.StatusTemporaryRedirect)
				case "/cross":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				case "/loop":
					http.Redirect(w, r, "/loop", http.StatusTemporaryRedirect)
				default:
					if r.Header.Get("x-api-key") != "request-secret" {
						t.Error("same-origin redirect lost key")
					}
					followed.Add(1)
				}
			})
			var server *httptest.Server
			if tls {
				server = httptest.NewTLSServer(handler)
			} else {
				server = httptest.NewServer(handler)
			}
			defer server.Close()
			client := NewClient(Policy{})
			if tls {
				client.Transport = server.Client().Transport
			}
			for _, path := range []string{"/same", "/cross", "/loop"} {
				req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
				req.Header.Set("x-api-key", "request-secret")
				resp, err := client.Do(req)
				if resp != nil {
					resp.Body.Close()
				}
				if (err == nil) != (path == "/same") {
					t.Fatalf("%s: %v", path, err)
				}
			}
			if followed.Load() != 1 {
				t.Fatalf("followed=%d", followed.Load())
			}
		})
	}
	if leaked.Load() != 0 {
		t.Fatalf("redirect target received %d requests", leaked.Load())
	}
}

func TestRedirectOriginDefaultPorts(t *testing.T) {
	original, _ := url.Parse("https://EXAMPLE.com/start")
	for _, tc := range []struct {
		target  string
		allowed bool
	}{
		{"https://example.com:443/next", true},
		{"https://example.com:444/next", false},
		{"http://example.com/next", false},
		{"https://sub.example.com/next", false},
	} {
		target, _ := url.Parse(tc.target)
		err := CheckRedirect(&http.Request{URL: target}, []*http.Request{{URL: original}})
		if (err == nil) != tc.allowed {
			t.Errorf("%s: %v", tc.target, err)
		}
	}
}

func TestRedactCredentialBoundaries(t *testing.T) {
	for _, tc := range []struct{ secret, text, want string }{
		{"secret", `invalid key "secret"`, `invalid key "[REDACTED]"`},
		{"secret", "key=secret", "key=[REDACTED]"},
		{"secret", "secret secret", "[REDACTED] [REDACTED]"},
		{"secret", "secretive secret-key key-secret secret_ secret0 Asecret", "secretive secret-key key-secret secret_ secret0 Asecret"},
		{"x", "context_length_exceeded", "context_length_exceeded"},
		{"x", "key=x; x", "key=[REDACTED]; [REDACTED]"},
		{"none", "none: nonetheless none-key key-none", "[REDACTED]: nonetheless none-key key-none"},
		{"a.b", "key=a.b; xa.b a.bx", "key=[REDACTED]; xa.b a.bx"},
		{"", "context_length_exceeded", "context_length_exceeded"},
		{"long-secret", "prefixlong-secretsuffix", "prefix[REDACTED]suffix"},
	} {
		t.Run(tc.secret+"/"+tc.text, func(t *testing.T) {
			if got := Redact(tc.text, tc.secret); got != tc.want {
				t.Fatalf("Redact(%q, %q) = %q, want %q", tc.text, tc.secret, got, tc.want)
			}
		})
	}
}

func TestRedactErrorPreservesCause(t *testing.T) {
	err := fmt.Errorf("echo request-secret: %w", context.Canceled)
	got := RedactError(err, "", "request-secret")
	if got.Error() != "echo [REDACTED]: context canceled" || !errors.Is(got, context.Canceled) {
		t.Fatalf("redacted error = %v", got)
	}
}
