// Package openaiauth implements ChatGPT subscription (Plus/Pro) OAuth
// authentication for OpenAI models (spec §13), as an alternative to API keys.
// It mirrors the flow of the official Codex CLI: an OAuth 2.0 authorization-
// code + PKCE flow against auth.openai.com using Codex's public client id,
// with the browser redirecting to a short-lived local callback server on
// http://localhost:1455/auth/callback (the redirect URI the public client id
// is registered with). Credentials — access/refresh/id tokens plus the
// ChatGPT account id extracted from the id_token JWT — are persisted in the
// machine-local secrets store (internal/secrets) under the OPENAI_OAUTH key.
//
// Subscription-authenticated inference does NOT go to the regular platform
// API: it targets the Codex Responses backend (internal/codex), which
// requires the access token as a bearer plus the chatgpt-account-id header.
// AccessToken returns both, transparently refreshing expired tokens.
package openaiauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/whyrusleeping/ycc/internal/secrets"
)

const (
	// ClientID is the public OAuth client id shipped with the official Codex
	// CLI (public by design — PKCE, not a secret, protects the flow). Reusing
	// it is what surfaces the ChatGPT-subscription-scoped consent screen.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// scope: openid/profile/email are standard; offline_access yields the
	// refresh token.
	scope = "openid profile email offline_access"

	// CallbackPort is fixed: the public client id is registered with
	// redirect_uri http://localhost:1455/auth/callback.
	CallbackPort = 1455

	// SecretsKey is the secrets-store key the JSON-encoded Credentials live
	// under (shaped like a key_env name so `ycc token list/rm` manage it).
	SecretsKey = "OPENAI_OAUTH"

	// Originator identifies this program to OpenAI's audit logs (sent as an
	// authorize-URL param and as a request header on codex calls).
	Originator = "ycc"

	// expirySkew refreshes slightly early so a token never expires mid-turn.
	expirySkew = 60 * time.Second
)

// Issuer is the OAuth authorization server; endpoints derive as
// Issuer + /oauth/{authorize,token}. Package variable so tests can point the
// token endpoint at an httptest server.
var Issuer = "https://auth.openai.com"

// Credentials is the persisted OAuth state.
type Credentials struct {
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// AccountID is the ChatGPT account id from the id_token JWT (claim
	// "https://api.openai.com/auth".chatgpt_account_id), required as the
	// chatgpt-account-id header on codex requests.
	AccountID string `json:"account_id"`
	// ExpiresAt is the access token's expiry as unix seconds (taken from the
	// access token JWT's exp claim; 0 = unknown, treated as expired).
	ExpiresAt int64 `json:"expires_at"`
}

// Expired reports whether the access token is (about to be) unusable.
func (c *Credentials) Expired(now time.Time) bool {
	if c.AccessToken == "" {
		return true
	}
	return now.Add(expirySkew).Unix() >= c.ExpiresAt
}

// PKCE is one flow's proof-key pair.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE generates a fresh S256 verifier/challenge pair.
func NewPKCE() (PKCE, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return PKCE{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{Verifier: verifier, Challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

// AuthorizeURL builds the browser URL that starts the login flow. The two
// non-standard params (id_token_add_organizations, codex_cli_simplified_flow)
// surface the subscription-scoped consent screen.
func AuthorizeURL(p PKCE, state, redirectURI string) string {
	q := url.Values{
		"response_type":              {"code"},
		"client_id":                  {ClientID},
		"redirect_uri":               {redirectURI},
		"scope":                      {scope},
		"code_challenge":             {p.Challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"state":                      {state},
		"originator":                 {Originator},
	}
	return Issuer + "/oauth/authorize?" + q.Encode()
}

// Login runs the full browser OAuth flow: bind the loopback callback server,
// surface the authorize URL via onAuthorizeURL (the caller prints and/or opens
// it), wait for the browser redirect, and exchange the code. The returned
// credentials are NOT persisted — callers Save them. ctx bounds the whole wait
// (callers should apply a generous timeout; browser logins involve a human).
func Login(ctx context.Context, onAuthorizeURL func(url string)) (*Credentials, error) {
	// Bind both loopback families: the redirect URI says "localhost" and the
	// browser may resolve it to 127.0.0.1 or ::1. v6 is best-effort.
	var lc net.ListenConfig
	v4, err := lc.Listen(ctx, "tcp4", fmt.Sprintf("127.0.0.1:%d", CallbackPort))
	if err != nil {
		return nil, fmt.Errorf("openai login: bind callback port %d (is another login or the codex CLI running?): %w", CallbackPort, err)
	}
	listeners := []net.Listener{v4}
	if v6, err6 := lc.Listen(ctx, "tcp6", fmt.Sprintf("[::1]:%d", CallbackPort)); err6 == nil {
		listeners = append(listeners, v6)
	}
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()

	pkce, err := NewPKCE()
	if err != nil {
		return nil, err
	}
	stateRaw := make([]byte, 16)
	if _, err := rand.Read(stateRaw); err != nil {
		return nil, err
	}
	state := base64.RawURLEncoding.EncodeToString(stateRaw)
	redirectURI := fmt.Sprintf("http://localhost:%d/auth/callback", CallbackPort)

	if onAuthorizeURL != nil {
		onAuthorizeURL(AuthorizeURL(pkce, state, redirectURI))
	}

	code, err := awaitCallback(ctx, listeners, state)
	if err != nil {
		return nil, err
	}
	return Exchange(ctx, code, pkce, redirectURI)
}

// awaitCallback serves GET /auth/callback on the listeners until a code (or
// error) arrives with the matching state.
func awaitCallback(ctx context.Context, listeners []net.Listener, expectedState string) (string, error) {
	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)
	deliver := func(r result) {
		select {
		case resultCh <- r:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			http.Error(w, "login failed: "+e, http.StatusBadRequest)
			deliver(result{err: fmt.Errorf("openai login: %s: %s", e, q.Get("error_description"))})
			return
		}
		if q.Get("state") != expectedState {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			deliver(result{err: errors.New("openai login: callback state mismatch")})
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			deliver(result{err: errors.New("openai login: callback missing code")})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body><h3>Signed in</h3><p>You can close this tab and return to ycc.</p></body></html>")
		deliver(result{code: code})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	for _, l := range listeners {
		go srv.Serve(l) //nolint:errcheck // Serve exits when the listeners close.
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("openai login: %w", ctx.Err())
	case r := <-resultCh:
		return r.code, r.err
	}
}

// tokenResponse is the subset of the /oauth/token payload we consume (shared
// by the code exchange and refresh, which return the same shape).
type tokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// Exchange redeems an authorization code for credentials. The token endpoint
// takes application/x-www-form-urlencoded for this grant (matching the
// official CLI's encoding).
func Exchange(ctx context.Context, code string, p PKCE, redirectURI string) (*Credentials, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {ClientID},
		"code_verifier": {p.Verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Issuer+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doToken(req)
}

// Refresh trades a refresh token for fresh credentials. This grant takes a
// JSON body (again matching the official CLI).
func Refresh(ctx context.Context, refreshToken string) (*Credentials, error) {
	if refreshToken == "" {
		return nil, errors.New("no refresh token stored")
	}
	body, err := json.Marshal(map[string]string{
		"client_id":     ClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Issuer+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	creds, err := doToken(req)
	if err != nil {
		return nil, err
	}
	// Rotation is optional (RFC 6749 §6): keep the old refresh token when the
	// server didn't return a new one.
	if creds.RefreshToken == "" {
		creds.RefreshToken = refreshToken
	}
	return creds, nil
}

func doToken(req *http.Request) (*Credentials, error) {
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var tr tokenResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return nil, fmt.Errorf("token endpoint: parsing response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("token endpoint: response missing access_token")
	}
	expires := jwtExpiry(tr.AccessToken)
	if expires == 0 && tr.ExpiresIn > 0 {
		expires = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).Unix()
	}
	return &Credentials{
		IDToken:      tr.IDToken,
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		AccountID:    jwtAccountID(tr.IDToken),
		ExpiresAt:    expires,
	}, nil
}

// jwtClaims decodes a JWT's payload segment without verifying the signature —
// these tokens come straight from the auth server over TLS, and we only read
// bookkeeping claims (exp, account id), never make trust decisions from them.
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}

// jwtExpiry returns the exp claim as unix seconds (0 when unparsable).
func jwtExpiry(token string) int64 {
	if exp, ok := jwtClaims(token)["exp"].(float64); ok {
		return int64(exp)
	}
	return 0
}

// jwtAccountID extracts the ChatGPT account id from an id_token's
// "https://api.openai.com/auth" claim ("" when absent).
func jwtAccountID(idToken string) string {
	auth, ok := jwtClaims(idToken)["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := auth["chatgpt_account_id"].(string)
	return id
}

// Load reads persisted credentials from the secrets store.
func Load() (creds *Credentials, ok bool) {
	raw, ok := secrets.Lookup(SecretsKey)
	if !ok {
		return nil, false
	}
	var c Credentials
	if err := json.Unmarshal([]byte(raw), &c); err != nil || (c.AccessToken == "" && c.RefreshToken == "") {
		return nil, false
	}
	return &c, true
}

// Save persists credentials to the secrets store.
func Save(c *Credentials) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return secrets.Set(SecretsKey, string(data))
}

// mu serializes refreshes within this process so concurrent Build calls do
// not race to redeem the same refresh token.
var mu sync.Mutex

// AccessToken returns a currently-valid access token plus the ChatGPT account
// id for subscription-authenticated codex requests, refreshing and
// re-persisting stored credentials when the access token has expired.
func AccessToken(ctx context.Context) (token, accountID string, err error) {
	mu.Lock()
	defer mu.Unlock()
	creds, ok := Load()
	if !ok {
		return "", "", errors.New("no ChatGPT subscription credentials stored; run `ycc login openai`")
	}
	if !creds.Expired(time.Now()) {
		return creds.AccessToken, creds.AccountID, nil
	}
	fresh, err := Refresh(ctx, creds.RefreshToken)
	if err != nil {
		return "", "", fmt.Errorf("refreshing ChatGPT subscription token (re-run `ycc login openai` if this persists): %w", err)
	}
	// Refresh responses may omit the id_token; keep the previously-stored
	// account id (it is stable for the account).
	if fresh.AccountID == "" {
		fresh.AccountID = creds.AccountID
	}
	if fresh.IDToken == "" {
		fresh.IDToken = creds.IDToken
	}
	if err := Save(fresh); err != nil {
		return "", "", fmt.Errorf("persisting refreshed credentials: %w", err)
	}
	return fresh.AccessToken, fresh.AccountID, nil
}
