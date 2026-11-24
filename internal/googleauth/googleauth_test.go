package googleauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

func TestAuthorizeURLMatchesAgy(t *testing.T) {
	u, err := url.Parse(AuthorizeURL(PKCE{Verifier: "v", Challenge: "c"}, "st"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "accounts.google.com" || u.Path != "/o/oauth2/auth" {
		t.Fatalf("authorize endpoint = %s%s", u.Host, u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": ClientID, "redirect_uri": "https://antigravity.google/oauth-callback",
		"response_type": "code", "access_type": "offline", "prompt": "consent",
		"code_challenge": "c", "code_challenge_method": "S256", "state": "st",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	scopes := strings.Fields(q.Get("scope"))
	if len(scopes) != 7 || scopes[5] != "https://www.googleapis.com/auth/aicode" || scopes[6] != "openid" {
		t.Errorf("scopes = %v", scopes)
	}
	if q.Has("client_secret") {
		t.Error("authorize URL must not carry the client secret")
	}
}

func TestParsePasted(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"4/0AbCd-ef_gh", "4/0AbCd-ef_gh", true},
		{"  4/0AbCd \n", "4/0AbCd", true},
		{"https://antigravity.google/oauth-callback?state=st&code=4%2F0Ab&scope=x", "4/0Ab", true},
		{"state=st&code=abc", "abc", true},
		{"https://antigravity.google/oauth-callback?code=abc", "", false},
		{"https://antigravity.google/oauth-callback?state=st&code=a&code=b", "", false},
		{"https://antigravity.google/oauth-callback?state=other&code=abc", "", false},
		{"https://antigravity.google/oauth-callback?error=access_denied&state=st", "", false},
		{"https://antigravity.google/oauth-callback?state=st", "", false},
		{"two words", "", false},
		{"", "", false},
	} {
		got, err := ParsePasted(tc.in, "st")
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParsePasted(%q) = %q, %v; want %q ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

// tokenServer fakes Google's token and userinfo endpoints.
func tokenServer(t *testing.T, handle func(form url.Values) (int, any)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/userinfo" {
			if r.Header.Get("Authorization") != "Bearer at-new" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"email": "me@example.com"})
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("client_secret") != clientSecret {
			t.Error("token request lacks the installed-app client credentials")
		}
		status, body := handle(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	oldToken, oldInfo := TokenEndpoint, UserinfoEndpoint
	TokenEndpoint, UserinfoEndpoint = srv.URL+"/token", srv.URL+"/userinfo"
	t.Cleanup(func() { TokenEndpoint, UserinfoEndpoint = oldToken, oldInfo })
	return srv
}

func TestExchangeSendsVerifierAndFetchesEmail(t *testing.T) {
	isolate(t)
	tokenServer(t, func(f url.Values) (int, any) {
		if f.Get("grant_type") != "authorization_code" || f.Get("code") != "the-code" || f.Get("code_verifier") != "ver" || f.Get("redirect_uri") != RedirectURI {
			t.Errorf("exchange form = %v", f)
		}
		return 200, map[string]any{"access_token": "at-new", "refresh_token": "rt", "expires_in": 3599}
	})
	creds, err := Exchange(context.Background(), "the-code", PKCE{Verifier: "ver"})
	if err != nil {
		t.Fatal(err)
	}
	if creds.Email != "me@example.com" || creds.RefreshToken != "rt" || creds.Expired(time.Now()) {
		t.Fatalf("creds = %+v", creds)
	}
}

func TestAccessTokenRefreshesAndKeepsLoginState(t *testing.T) {
	isolate(t)
	var calls atomic.Int32
	tokenServer(t, func(f url.Values) (int, any) {
		calls.Add(1)
		if f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "rt" {
			t.Errorf("refresh form = %v", f)
		}
		return 200, map[string]any{"access_token": "at-new", "expires_in": 3599}
	})
	if err := Save(&Credentials{AccessToken: "at-old", RefreshToken: "rt", ExpiresAt: time.Now().Unix() - 1, Email: "me@example.com", ProjectID: "proj"}); err != nil {
		t.Fatal(err)
	}
	tok, err := AccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at-new" || tok.ProjectID != "proj" || tok.RefreshToken != "rt" {
		t.Fatalf("token = %+v", tok)
	}
	stored, _ := Load()
	if stored.RefreshToken != "rt" || stored.Email != "me@example.com" || stored.ProjectID != "proj" {
		t.Fatalf("stored = %+v", stored)
	}
	if _, err := AccessToken(context.Background()); err != nil || calls.Load() != 1 {
		t.Fatalf("second call refreshed again (calls=%d, err=%v)", calls.Load(), err)
	}
}

func TestInvalidGrantNeedsLogin(t *testing.T) {
	isolate(t)
	tokenServer(t, func(url.Values) (int, any) {
		return 400, map[string]any{"error": "invalid_grant", "error_description": "Token has been expired or revoked."}
	})
	if err := Save(&Credentials{AccessToken: "a", RefreshToken: "rt", ExpiresAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := AccessToken(context.Background()); !errors.Is(err, ErrReauth) {
		t.Fatalf("err = %v, want ErrReauth", err)
	}
}

func TestSuspendedLoginIsNeverUsed(t *testing.T) {
	isolate(t)
	if err := Save(&Credentials{AccessToken: "a", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := Update("rt", func(c *Credentials) { c.Suspended = "TOS_VIOLATION" }); err != nil {
		t.Fatal(err)
	}
	_, err := AccessToken(context.Background())
	var se *SuspendedError
	if !errors.As(err, &se) || strings.Contains(err.Error(), "a\"") {
		t.Fatalf("err = %v, want SuspendedError", err)
	}
	// An update for a replaced login leaves the new login alone.
	if err := Save(&Credentials{AccessToken: "b", RefreshToken: "rt2", ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := Update("rt", func(c *Credentials) { c.Suspended = "x" }); err != nil {
		t.Fatal(err)
	}
	if tok, err := AccessToken(context.Background()); err != nil || tok.AccessToken != "b" {
		t.Fatalf("new login: %+v, %v", tok, err)
	}
}
