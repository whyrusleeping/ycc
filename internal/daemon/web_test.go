package daemon

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/web"
)

// baseOptions is a minimal valid Options that reaches buildHandler without
// touching the network or persistence.
func baseOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Workspace: t.TempDir(),
		Model:     "claude-opus-4-8",
		BaseURL:   "https://api.anthropic.com",
		KeyEnv:    "ANTHROPIC_API_KEY",
		MaxTokens: 8192,
	}
}

// TestWebServesStaticAssetsUnauthenticated asserts the design decision
// (docs/design/web-client.md "Authentication"): with --web, the embedded SPA is
// served at "/" — its assets and client-side deep links — with no auth, while
// the RPC surface stays behind the bearer token and keeps its own prefix.
func TestWebServesStaticAssetsUnauthenticated(t *testing.T) {
	o := baseOptions(t)
	o.Web = true
	o.Token = "secret"
	h, _, err := buildHandler(o)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	entries, err := fs.ReadDir(web.Assets(), "assets")
	if err != nil || len(entries) == 0 {
		t.Fatalf("embedded assets: %v", err)
	}
	asset := "/assets/" + entries[0].Name()

	// Static assets and history-fallback routes: 200 with content, no
	// Authorization header.
	for _, path := range []string{"/", "/index.html", asset, "/p/proj/s/s_0123"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", path, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Errorf("GET %s: empty body", path)
		}
	}

	// RPC path without a token stays unauthorized (auth interceptor unchanged).
	rpc := srv.URL + "/ycc.v1.SessionService/ListSessions"
	resp, err := http.Post(rpc, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST %s: %v", rpc, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("RPC without token returned %d; want 401", resp.StatusCode)
	}

	// With the token the same RPC succeeds: the Connect prefix is not shadowed
	// by the SPA's history fallback.
	req, _ := http.NewRequest(http.MethodPost, rpc, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s with token: %v", rpc, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		t.Errorf("authorized RPC: status %d body %q", resp.StatusCode, body)
	}
}

// TestWebDisabledReturns404 asserts that without --web, "/" is not served (the
// Connect handler is the only registered handler) and RPC auth is unchanged.
func TestWebDisabledReturns404(t *testing.T) {
	o := baseOptions(t)
	o.Web = false
	o.Token = "secret"
	h, _, err := buildHandler(o)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET / without --web: status %d, want 404", resp.StatusCode)
	}

	// RPC path without a token still unauthorized.
	rpc := srv.URL + "/ycc.v1.SessionService/ListSessions"
	rpcResp, err := http.Post(rpc, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST %s: %v", rpc, err)
	}
	rpcResp.Body.Close()
	if rpcResp.StatusCode == http.StatusOK {
		t.Errorf("RPC without token returned 200; want auth rejection")
	}
}

// TestServeRefusesNonLoopbackWithoutTokenWithWeb asserts the non-loopback
// no-token guardrail (spec §12/§14) still fires with --web set: enabling the web
// client never relaxes the refusal to bind a network address without a token.
func TestServeRefusesNonLoopbackWithoutTokenWithWeb(t *testing.T) {
	o := baseOptions(t)
	o.Addr = "0.0.0.0:0"
	o.Web = true
	err := Serve(o)
	if err == nil {
		t.Fatal("Serve on 0.0.0.0 without a token (with --web) returned nil; want refusal")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("error = %q, want it to mention the missing token", err)
	}
}

// TestStartInProcessLeavesWebOff asserts the one-shot in-process daemon never
// serves the SPA even when the caller sets Web: "/" returns 404.
func TestStartInProcessLeavesWebOff(t *testing.T) {
	o := baseOptions(t)
	o.Web = true
	ip, err := StartInProcess(o)
	if err != nil {
		t.Fatalf("StartInProcess: %v", err)
	}
	defer ip.Close()

	resp, err := http.Get(ip.Addr + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("in-process GET / with Web:true: status %d, want 404", resp.StatusCode)
	}
}
