package openaiauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

// fakeJWT builds an unsigned JWT with the given payload claims.
func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	seg := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	return seg([]byte(`{"alg":"none"}`)) + "." + seg(payload) + ".sig"
}

func TestAuthorizeURL(t *testing.T) {
	p := PKCE{Verifier: "v", Challenge: "c"}
	u, err := url.Parse(AuthorizeURL(p, "the-state", "http://localhost:1455/auth/callback"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id":                  ClientID,
		"response_type":              "code",
		"code_challenge":             "c",
		"code_challenge_method":      "S256",
		"state":                      "the-state",
		"redirect_uri":               "http://localhost:1455/auth/callback",
		"codex_cli_simplified_flow":  "true",
		"id_token_add_organizations": "true",
		"originator":                 Originator,
	} {
		if got := q.Get(k); got != want {
			t.Errorf("authorize URL %s = %q, want %q", k, got, want)
		}
	}
}

func TestExchangeParsesJWTClaims(t *testing.T) {
	isolate(t)
	exp := time.Now().Add(time.Hour).Unix()
	access := fakeJWT(t, map[string]any{"exp": float64(exp)})
	id := fakeJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-123"},
	})
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "x-www-form-urlencoded") {
			t.Errorf("exchange content-type = %q", ct)
		}
		r.ParseForm()
		gotForm = r.PostForm
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": id, "access_token": access, "refresh_token": "rt-1",
		})
	}))
	defer srv.Close()
	old := Issuer
	Issuer = srv.URL
	defer func() { Issuer = old }()

	creds, err := Exchange(context.Background(), "the-code", PKCE{Verifier: "verif"}, "http://localhost:1455/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	if gotForm.Get("code") != "the-code" || gotForm.Get("code_verifier") != "verif" || gotForm.Get("grant_type") != "authorization_code" {
		t.Errorf("exchange form wrong: %v", gotForm)
	}
	if creds.AccountID != "acct-123" {
		t.Errorf("account id = %q, want acct-123", creds.AccountID)
	}
	if creds.ExpiresAt != exp {
		t.Errorf("expires_at = %d, want %d (from JWT exp)", creds.ExpiresAt, exp)
	}
	if creds.Expired(time.Now()) {
		t.Error("fresh creds reported expired")
	}
}

func TestAccessTokenRefreshesAndPersists(t *testing.T) {
	isolate(t)
	exp := time.Now().Add(time.Hour).Unix()
	newAccess := fakeJWT(t, map[string]any{"exp": float64(exp)})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("refresh body not JSON: %v", err)
		}
		if body["grant_type"] != "refresh_token" || body["refresh_token"] != "rt-1" {
			t.Errorf("refresh body wrong: %v", body)
		}
		// No rotated refresh token and no id_token: both must be preserved.
		json.NewEncoder(w).Encode(map[string]any{"access_token": newAccess})
	}))
	defer srv.Close()
	old := Issuer
	Issuer = srv.URL
	defer func() { Issuer = old }()

	if err := Save(&Credentials{
		AccessToken: "stale", RefreshToken: "rt-1", AccountID: "acct-9", IDToken: "idt",
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	tok, acct, err := AccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != newAccess || acct != "acct-9" {
		t.Fatalf("got token %q acct %q", tok, acct)
	}
	stored, ok := Load()
	if !ok || stored.AccessToken != newAccess || stored.RefreshToken != "rt-1" || stored.AccountID != "acct-9" || stored.IDToken != "idt" {
		t.Fatalf("refreshed creds not persisted/merged: %+v ok=%v", stored, ok)
	}

	// Live creds are returned without contacting the endpoint.
	Issuer = "http://127.0.0.1:0"
	if tok, acct, err = AccessToken(context.Background()); err != nil || tok != newAccess || acct != "acct-9" {
		t.Fatalf("live path failed: %q %q %v", tok, acct, err)
	}
}

func TestAccessTokenErrors(t *testing.T) {
	isolate(t)
	if _, _, err := AccessToken(context.Background()); err == nil || !strings.Contains(err.Error(), "ycc login openai") {
		t.Fatalf("want login hint when nothing stored, got %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	old := Issuer
	Issuer = srv.URL
	defer func() { Issuer = old }()
	if err := Save(&Credentials{AccessToken: "stale", RefreshToken: "rt-dead", ExpiresAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AccessToken(context.Background()); err == nil || !strings.Contains(err.Error(), "ycc login openai") {
		t.Fatalf("want login hint on rejected refresh, got %v", err)
	}
}

// TestLoginCallbackFlow drives the full local-callback dance: Login binds
// :1455, we simulate the browser hitting the callback with the code, and the
// exchange resolves against a stub token endpoint.
func TestLoginCallbackFlow(t *testing.T) {
	isolate(t)
	access := fakeJWT(t, map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "rt"})
	}))
	defer srv.Close()
	old := Issuer
	Issuer = srv.URL
	defer func() { Issuer = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	urlCh := make(chan string, 1)
	credCh := make(chan *Credentials, 1)
	errCh := make(chan error, 1)
	go func() {
		creds, err := Login(ctx, func(u string) { urlCh <- u })
		credCh <- creds
		errCh <- err
	}()
	authURL := <-urlCh
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")
	// Simulate the browser redirect to the local callback.
	cb := fmt.Sprintf("http://127.0.0.1:%d/auth/callback?code=abc&state=%s", CallbackPort, url.QueryEscape(state))
	resp, err := http.Get(cb)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status %d", resp.StatusCode)
	}
	creds, err := <-credCh, <-errCh
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessToken != access || creds.RefreshToken != "rt" {
		t.Fatalf("unexpected creds: %+v", creds)
	}
}

func TestParsePastedCallback(t *testing.T) {
	for _, tc := range []struct {
		name, pasted, state string
		want                string
		wantErr             string
	}{
		{name: "full URL", pasted: "http://localhost:1455/auth/callback?code=abc&state=st-1", state: "st-1", want: "abc"},
		{name: "full URL padded", pasted: "  http://localhost:1455/auth/callback?code=abc&state=st-1 \n", state: "st-1", want: "abc"},
		{name: "query string only", pasted: "code=abc&state=st-1", state: "st-1", want: "abc"},
		{name: "bare code", pasted: "abc", state: "st-1", want: "abc"},
		{name: "state mismatch", pasted: "http://localhost:1455/auth/callback?code=abc&state=other", state: "st-1", wantErr: "state mismatch"},
		{name: "error param", pasted: "http://localhost:1455/auth/callback?error=access_denied&error_description=nope", state: "st-1", wantErr: "access_denied"},
		{name: "missing code", pasted: "http://localhost:1455/auth/callback?state=st-1", state: "st-1", wantErr: "no code"},
		{name: "empty", pasted: "   ", state: "st-1", wantErr: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePastedCallback(tc.pasted, tc.state)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLoginPasteFlow completes the login by pasting the callback URL instead
// of the browser reaching localhost:1455 — with the callback port already
// occupied (as it would effectively be unreachable over ssh, or held by the
// codex CLI), which must degrade to paste-only rather than fail.
func TestLoginPasteFlow(t *testing.T) {
	isolate(t)
	access := fakeJWT(t, map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())})
	var gotCode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotCode = r.PostForm.Get("code")
		json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "rt"})
	}))
	defer srv.Close()
	old := Issuer
	Issuer = srv.URL
	defer func() { Issuer = old }()

	// Occupy the callback port so binding fails.
	hog, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", CallbackPort))
	if err != nil {
		t.Skipf("cannot bind port %d to simulate conflict: %v", CallbackPort, err)
	}
	defer hog.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	urlCh := make(chan string, 1)
	pasteCh := make(chan string, 1)
	credCh := make(chan *Credentials, 1)
	errCh := make(chan error, 1)
	go func() {
		creds, err := LoginWithPaste(ctx, func(u string) { urlCh <- u }, pasteCh)
		credCh <- creds
		errCh <- err
	}()
	u, err := url.Parse(<-urlCh)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")
	pasteCh <- fmt.Sprintf("http://localhost:%d/auth/callback?code=pasted-code&state=%s", CallbackPort, url.QueryEscape(state))
	creds, err := <-credCh, <-errCh
	if err != nil {
		t.Fatal(err)
	}
	if gotCode != "pasted-code" {
		t.Fatalf("exchanged code = %q, want pasted-code", gotCode)
	}
	if creds.AccessToken != access || creds.RefreshToken != "rt" {
		t.Fatalf("unexpected creds: %+v", creds)
	}
}
