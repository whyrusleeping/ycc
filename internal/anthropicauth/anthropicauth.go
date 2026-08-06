// Package anthropicauth implements Claude subscription (Pro/Max) OAuth
// authentication for the anthropic backend, as an alternative to API keys
// (spec §13). It follows the current Claude Code authorization-code + PKCE flow:
// `ycc login anthropic` sends the user to the browser, exchanges the pasted
// code for an access/refresh token pair, and persists it in the machine-local
// secrets store (internal/secrets, mode-0600 secrets.json) under the
// ANTHROPIC_OAUTH key. At request-build time AccessToken returns a live access
// token, transparently refreshing and re-persisting an expired one.
//
// OAuth-authenticated requests differ from API-key requests in two ways, both
// applied by config.Registry.Build: the credential travels as an
// "Authorization: Bearer" header (never x-api-key), and the request carries
// the Claude Code + OAuth beta opt-ins required by Anthropic's subscription
// inference surface.
package anthropicauth

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
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/whyrusleeping/ycc/internal/secrets"
)

const (
	// ClientID is Anthropic's public OAuth client id for CLI tooling (the one
	// Claude Code registers; public by design — PKCE, not a client secret,
	// protects the flow).
	ClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// These are the current Claude Code subscription-login endpoints and scopes.
	// Anthropic moved the browser entry point from claude.ai/oauth/authorize and
	// the token endpoint from console.anthropic.com in 2026. Keeping these values
	// together makes drift conspicuous and covered by TestPKCEAndAuthorizeURL.
	authorizeEndpoint = "https://claude.com/cai/oauth/authorize"
	redirectURI       = "https://platform.claude.com/oauth/code/callback"
	scopes            = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"

	// FlowVersion is persisted with credentials. Credentials created before this
	// marker used Anthropic's retired endpoints/scopes; they may still refresh but
	// inference currently rejects them with an opaque 429. Requiring a one-time
	// re-login avoids presenting that rejection as an exhausted allowance.
	FlowVersion = 2

	// SecretsKey is the secrets-store key the JSON-encoded Credentials live
	// under. It is deliberately shaped like a key_env name so `ycc token list`
	// / `ycc token rm ANTHROPIC_OAUTH` manage it like any other stored secret.
	SecretsKey = "ANTHROPIC_OAUTH"

	// BetaHeader opts /v1/messages into Claude Code subscription inference and
	// OAuth bearer authentication. The order mirrors current Claude Code.
	BetaHeader = "claude-code-20250219,oauth-2025-04-20"

	// AppHeader identifies the subscription inference surface. Current Claude
	// Code sends this alongside the beta opt-ins; ycc deliberately does not spoof
	// Claude Code's User-Agent or SDK telemetry headers.
	AppHeader = "cli"

	// TokenPrefix marks Anthropic OAuth access tokens (including long-lived
	// ones minted by `claude setup-token`). A key_env credential with this
	// prefix must be sent as a bearer token + beta header, not as x-api-key.
	TokenPrefix = "sk-ant-oat"

	// expirySkew refreshes slightly early so a token never expires mid-turn.
	expirySkew = 60 * time.Second
)

// TokenEndpoint is the OAuth token-exchange/refresh endpoint. Package variable
// so tests can point it at an httptest server.
var TokenEndpoint = "https://platform.claude.com/v1/oauth/token"

// Credentials is the persisted OAuth state.
type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// ExpiresAt is the access token's expiry as unix seconds (0 = unknown,
	// treated as expired so we refresh rather than send a dead token).
	ExpiresAt int64 `json:"expires_at"`
	// FlowVersion identifies the endpoint/scope generation that minted the
	// credentials. Zero is legacy (all credentials stored before this field was
	// introduced) and deliberately requires a one-time login migration.
	FlowVersion int `json:"flow_version"`
}

// Expired reports whether the access token is (about to be) unusable.
func (c *Credentials) Expired(now time.Time) bool {
	if c.AccessToken == "" {
		return true
	}
	return now.Add(expirySkew).Unix() >= c.ExpiresAt
}

// PKCE is one flow's proof-key pair. The verifier is also used as the OAuth
// state parameter, matching Anthropic's flow (the login page echoes it back in
// the pasted code as "code#state").
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

// AuthorizeURL builds the browser URL that starts the login flow.
func AuthorizeURL(p PKCE) string {
	q := url.Values{
		"code":                  {"true"},
		"client_id":             {ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {scopes},
		"code_challenge":        {p.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {p.Verifier},
	}
	return authorizeEndpoint + "?" + q.Encode()
}

// tokenResponse is the token endpoint's reply for both grant types.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// Exchange trades the code the user pasted after browser login for
// credentials. The pasted value is "code#state" (the login page shows both
// joined); a bare code is accepted too, in which case the PKCE verifier is
// sent as the state.
func Exchange(ctx context.Context, pasted string, p PKCE) (*Credentials, error) {
	code := strings.TrimSpace(pasted)
	state := p.Verifier
	if i := strings.IndexByte(code, '#'); i >= 0 {
		code, state = code[:i], code[i+1:]
	}
	if code == "" {
		return nil, errors.New("empty authorization code")
	}
	return tokenCall(ctx, map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"state":         state,
		"client_id":     ClientID,
		"redirect_uri":  redirectURI,
		"code_verifier": p.Verifier,
	})
}

// Refresh trades a refresh token for a fresh access token.
func Refresh(ctx context.Context, refreshToken string) (*Credentials, error) {
	if refreshToken == "" {
		return nil, errors.New("no refresh token stored")
	}
	creds, err := tokenCall(ctx, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     ClientID,
	})
	if err != nil {
		return nil, err
	}
	// Some refreshes do not rotate the refresh token; keep the old one then.
	if creds.RefreshToken == "" {
		creds.RefreshToken = refreshToken
	}
	return creds, nil
}

func tokenCall(ctx context.Context, body map[string]string) (*Credentials, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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
	return &Credentials{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).Unix(),
		FlowVersion:  FlowVersion,
	}, nil
}

// Load reads persisted credentials from the secrets store. ok is false when
// none are stored (or the stored value is unparsable).
func Load() (creds *Credentials, ok bool) {
	raw, ok := secrets.Lookup(SecretsKey)
	if !ok {
		return nil, false
	}
	var c Credentials
	if err := json.Unmarshal([]byte(raw), &c); err != nil || c.AccessToken == "" && c.RefreshToken == "" {
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

// mu serializes refreshes within this process so concurrent Build calls do not
// race to redeem the same refresh token.
var mu sync.Mutex

// AccessToken returns a currently-valid access token for OAuth-authenticated
// requests, refreshing and re-persisting the stored credentials if the access
// token has expired. It fails with a run-`ycc login anthropic` hint when no
// credentials are stored or the refresh is rejected.
//
// It re-reads the secrets store on every call, so a caller that resolves a
// token per request automatically picks up a refresh performed by another ycc
// process instead of holding a token that a rotation has invalidated.
func AccessToken(ctx context.Context) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	return accessToken(ctx, "")
}

// ForceRefresh returns a replacement for stale, an access token the provider
// has rejected as revoked or expired. Redeeming the refresh token is the last
// resort: Anthropic rotates the whole token family on every refresh, so if the
// store already holds a token other than stale (another ycc process — or the
// subscription-usage poller — refreshed underneath us, which is exactly what
// invalidated stale), that stored token is returned without a network call.
// Passing an empty stale always redeems.
func ForceRefresh(ctx context.Context, stale string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	return accessToken(ctx, stale)
}

// accessToken implements AccessToken/ForceRefresh; callers hold mu. A non-empty
// stale token additionally forces a refresh when the store still holds it, even
// though it has not expired by the clock.
func accessToken(ctx context.Context, stale string) (string, error) {
	creds, ok := Load()
	if !ok {
		return "", errors.New("no Anthropic subscription credentials stored; run `ycc login anthropic`")
	}
	if creds.FlowVersion < FlowVersion {
		return "", errors.New("Anthropic changed its Claude subscription OAuth flow; run `ycc login anthropic` once to update the stored credentials")
	}
	superseded := stale != "" && creds.AccessToken != stale
	if !creds.Expired(time.Now()) && (stale == "" || superseded) {
		return creds.AccessToken, nil
	}
	fresh, err := Refresh(ctx, creds.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("refreshing Anthropic subscription token (re-run `ycc login anthropic` if this persists): %w", err)
	}
	if err := Save(fresh); err != nil {
		return "", fmt.Errorf("persisting refreshed credentials: %w", err)
	}
	return fresh.AccessToken, nil
}
