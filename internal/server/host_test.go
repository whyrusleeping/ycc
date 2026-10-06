package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Host rejection must precede body decoding, just like bearer rejection.
type unreadableHostBody struct{ t *testing.T }

func (b unreadableHostBody) Read([]byte) (int, error) {
	b.t.Fatal("Host check read the request body")
	return 0, nil
}
func (unreadableHostBody) Close() error { return nil }

func TestRequireLoopbackHost(t *testing.T) {
	for _, tc := range []struct {
		host string
		want int
	}{
		{"127.0.0.1:8787", http.StatusNoContent},
		{"127.42.0.7", http.StatusNoContent},
		{"localhost", http.StatusNoContent},
		{"localhost:8787", http.StatusNoContent},
		{"[::1]:8787", http.StatusNoContent},
		{"[::1]", http.StatusNoContent},
		{"evil.example", http.StatusForbidden},
		{"127.0.0.1.evil.com", http.StatusForbidden},
		{"foo.localhost", http.StatusForbidden},
		{"192.168.1.1:8787", http.StatusForbidden},
		{"", http.StatusForbidden},
	} {
		t.Run(tc.host, func(t *testing.T) {
			h := RequireLoopbackHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodPost, "http://localhost/", nil)
			req.Host = tc.host
			req.Body = unreadableHostBody{t}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
