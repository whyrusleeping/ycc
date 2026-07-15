package anthropicauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// isolate points the secrets store at a temp dir so tests never touch the real
// ~/.config/ycc/secrets.json.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

func TestPKCEAndAuthorizeURL(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if p.Verifier == "" || p.Challenge == "" {
		t.Fatal("empty PKCE pair")
	}
	sum := sha256.Sum256([]byte(p.Verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); p.Challenge != want {
		t.Fatalf("challenge is not S256(verifier): got %q want %q", p.Challenge, want)
	}
	u, err := url.Parse(AuthorizeURL(p))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id":             ClientID,
		"response_type":         "code",
		"code_challenge":        p.Challenge,
		"code_challenge_method": "S256",
		"state":                 p.Verifier,
		"scope":                 scopes,
		"redirect_uri":          redirectURI,
	} {
		if got := q.Get(k); got != want {
			t.Errorf("authorize URL %s = %q, want %q", k, got, want)
		}
	}
}

func TestExchangeAndRefresh(t *testing.T) {
	isolate(t)
	var gotBodies []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("bad body: %v", err)
		}
		gotBodies = append(gotBodies, body)
		switch body["grant_type"] {
		case "authorization_code":
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "sk-ant-oat01-access-1", "refresh_token": "rt-1", "expires_in": 3600,
			})
		case "refresh_token":
			// No rotated refresh token in the reply: caller must keep rt-1.
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "sk-ant-oat01-access-2", "expires_in": 3600,
			})
		default:
			http.Error(w, "bad grant", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	old := TokenEndpoint
	TokenEndpoint = srv.URL
	defer func() { TokenEndpoint = old }()

	p := PKCE{Verifier: "verif", Challenge: "chal"}
	creds, err := Exchange(context.Background(), "  the-code#the-state \n", p)
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessToken != "sk-ant-oat01-access-1" || creds.RefreshToken != "rt-1" {
		t.Fatalf("unexpected creds: %+v", creds)
	}
	if creds.Expired(time.Now()) {
		t.Error("fresh creds reported expired")
	}
	ex := gotBodies[0]
	if ex["code"] != "the-code" || ex["state"] != "the-state" || ex["code_verifier"] != "verif" {
		t.Errorf("exchange body wrong: %v", ex)
	}

	fresh, err := Refresh(context.Background(), creds.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.AccessToken != "sk-ant-oat01-access-2" {
		t.Fatalf("unexpected refreshed access token %q", fresh.AccessToken)
	}
	if fresh.RefreshToken != "rt-1" {
		t.Fatalf("refresh token not carried over: %q", fresh.RefreshToken)
	}
}

func TestAccessTokenRefreshesAndPersists(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "sk-ant-oat01-new", "refresh_token": "rt-2", "expires_in": 3600,
		})
	}))
	defer srv.Close()
	old := TokenEndpoint
	TokenEndpoint = srv.URL
	defer func() { TokenEndpoint = old }()

	// Expired stored creds -> AccessToken must refresh and persist.
	if err := Save(&Credentials{AccessToken: "stale", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(-time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	tok, err := AccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "sk-ant-oat01-new" {
		t.Fatalf("got token %q", tok)
	}
	stored, ok := Load()
	if !ok || stored.AccessToken != "sk-ant-oat01-new" || stored.RefreshToken != "rt-2" {
		t.Fatalf("refreshed creds not persisted: %+v ok=%v", stored, ok)
	}

	// Valid creds -> returned as-is, no endpoint call needed.
	if err := Save(&Credentials{AccessToken: "live", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	TokenEndpoint = "http://127.0.0.1:0" // would fail if contacted
	if tok, err = AccessToken(context.Background()); err != nil || tok != "live" {
		t.Fatalf("got %q, %v; want live token without refresh", tok, err)
	}
}

func TestAccessTokenErrors(t *testing.T) {
	isolate(t)
	if _, err := AccessToken(context.Background()); err == nil || !strings.Contains(err.Error(), "ycc login anthropic") {
		t.Fatalf("want login hint when nothing stored, got %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	old := TokenEndpoint
	TokenEndpoint = srv.URL
	defer func() { TokenEndpoint = old }()
	if err := Save(&Credentials{AccessToken: "stale", RefreshToken: "rt-dead", ExpiresAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := AccessToken(context.Background()); err == nil || !strings.Contains(err.Error(), "ycc login anthropic") {
		t.Fatalf("want login hint on rejected refresh, got %v", err)
	}
}
