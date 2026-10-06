package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerHostPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, token, host, bearer string
		want                      int
	}{
		{"tokenless local", "", "127.0.0.1:8787", "", http.StatusOK},
		{"tokenless foreign", "", "evil.example", "", http.StatusForbidden},
		{"authorized foreign", "secret", "evil.example", "secret", http.StatusOK},
		{"unauthorized foreign", "secret", "evil.example", "", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := baseOptions(t)
			o.Token = tc.token
			h, mgr, err := buildHandler(o)
			if err != nil {
				t.Fatal(err)
			}
			defer mgr.ReclaimAll()
			for _, path := range []string{"/ycc.v1.SessionService/ListModes", "/debug/latency"} {
				method := http.MethodPost
				if path == "/debug/latency" {
					method = http.MethodGet
				}
				req := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader("{}"))
				req.Host = tc.host
				req.Header.Set("Content-Type", "application/json")
				if tc.bearer != "" {
					req.Header.Set("Authorization", "Bearer "+tc.bearer)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != tc.want {
					t.Errorf("%s: status = %d, want %d: %s", path, w.Code, tc.want, w.Body.String())
				}
			}
		})
	}
}

func TestServeWebRequiresToken(t *testing.T) {
	// Invalid config proves the guard runs before constructing the handler,
	// let alone opening a listener, even for a loopback bind.
	err := Serve(Options{Addr: "127.0.0.1:0", Web: true, ConfigPath: "nonexistent-config.toml"})
	if err == nil || !strings.Contains(err.Error(), "web client without a token") {
		t.Fatalf("Serve without web token: %v", err)
	}
}

func TestInProcessTokens(t *testing.T) {
	a, err := StartInProcess(baseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := StartInProcess(baseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.Token == "" || b.Token == "" || a.Token == b.Token {
		t.Fatal("in-process instances must have independent non-empty tokens")
	}
	if Reachable(a.Addr, b.Token) {
		t.Fatal("one instance accepted another instance's token")
	}
	o := baseOptions(t)
	o.Token = "explicit"
	c, err := StartInProcess(o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Token != o.Token || !Reachable(c.Addr, o.Token) {
		t.Fatal("explicit in-process token not honored")
	}
}
