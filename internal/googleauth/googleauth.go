// Package googleauth implements Google subscription (Google AI Pro/Ultra)
// OAuth for the gemini backend's Antigravity transport (internal/antigravity).
// It follows the login flow of Google's own Antigravity CLI (`agy`): an
// authorization-code + PKCE flow with the CLI's installed-app client whose
// redirect is a hosted page (https://antigravity.google/oauth-callback) that
// shows a code for the user to paste back, so the same flow works from a
// terminal, over ssh and through the daemon's remote login RPCs.
//
// This path is unofficial. Google's Antigravity terms forbid reusing these
// credentials from third-party software and Google has suspended accounts for
// it; ycc only uses it for a model the user explicitly configures with
// `backend = "gemini"` and `auth = "oauth"`.
//
// Credentials (access/refresh token, account email, the discovered Code Assist
// project and any suspension Google reported) are persisted in the
// machine-local secrets store under GOOGLE_OAUTH. AccessToken refreshes an
// expired token with a compare-and-swap so a concurrent login always wins.
package googleauth

import (
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

	"github.com/whyrusleeping/ycc/internal/llmhttp"
	"github.com/whyrusleeping/ycc/internal/secrets"
)

const (
	authorizeEndpoint = "https://accounts.google.com/o/oauth2/auth"

	// RedirectURI is the hosted callback page agy uses; it displays the
	// authorization code instead of redirecting to a local port.
	RedirectURI = "https://antigravity.google/oauth-callback"

	// scopes are the ones agy 1.5.16 requests, in its order.
	scopes = "https://www.googleapis.com/auth/cloud-platform " +
		"https://www.googleapis.com/auth/userinfo.email " +
		"https://www.googleapis.com/auth/userinfo.profile " +
		"https://www.googleapis.com/auth/cclog " +
		"https://www.googleapis.com/auth/experimentsandconfigs " +
		"https://www.googleapis.com/auth/aicode " +
		"openid"

	// SecretsKey is the secrets-store key the JSON-encoded Credentials live
	// under (shaped like a key_env name so `ycc token list/rm` manage it).
	SecretsKey = "GOOGLE_OAUTH"

	// expirySkew refreshes slightly early so a token never expires mid-turn.
	expirySkew = 2 * time.Minute
)

// Endpoints are package variables so tests can point them at httptest servers.
var (
	TokenEndpoint    = "https://oauth2.googleapis.com/token"
	UserinfoEndpoint = "https://www.googleapis.com/oauth2/v2/userinfo"
)

// ErrReauth marks a refresh token Google no longer accepts (revoked, expired
// or from a removed grant): only a new `ycc login google` can recover.
var ErrReauth = errors.New("Google rejected the stored refresh token; run `ycc login google` again")

// ErrNoLogin means no Google subscription login is stored.
var ErrNoLogin = errors.New("no Google subscription credentials stored; run `ycc login google`")

// Credentials is the persisted OAuth state.
type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// ExpiresAt is the access token's expiry as unix seconds (0 = unknown,
	// treated as expired).
	ExpiresAt int64  `json:"expires_at"`
	Email     string `json:"email,omitempty"`
	// ProjectID caches the Code Assist project loadCodeAssist/onboardUser
	// assigned to this account; empty until the first inference.
	ProjectID string `json:"project_id,omitempty"`
	// Suspended records Google's terms-of-service suspension of this grant
	// (its message, and the appeal URL when given). A suspended login is never
	// used again; a fresh login clears it.
	Suspended string `json:"suspended,omitempty"`
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

// NewState returns a random OAuth state value.
func NewState() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// AuthorizeURL builds the browser URL that starts the login flow, with the
// same parameters agy sends.
func AuthorizeURL(p PKCE, state string) string {
	q := url.Values{
		"access_type":           {"offline"},
		"client_id":             {ClientID},
		"code_challenge":        {p.Challenge},
		"code_challenge_method": {"S256"},
		"prompt":                {"consent"},
		"redirect_uri":          {RedirectURI},
		"response_type":         {"code"},
		"scope":                 {scopes},
		"state":                 {state},
	}
	return authorizeEndpoint + "?" + q.Encode()
}

// ParsePasted extracts the authorization code from what the user pasted: the
// code the callback page shows, or the page's full URL (or its query string).
// A URL or query must carry exactly one code and a state matching
// expectedState; a bare code is bound to its login attempt by the PKCE
// verifier.
func ParsePasted(pasted, expectedState string) (string, error) {
	s := strings.TrimSpace(pasted)
	if s == "" {
		return "", errors.New("empty input")
	}
	if len(s) > 8192 {
		return "", errors.New("pasted value is too long")
	}
	qs := ""
	if i := strings.IndexByte(s, '?'); i >= 0 {
		qs = s[i+1:]
	} else if strings.Contains(s, "code=") || strings.Contains(s, "error=") {
		qs = s
	}
	if qs == "" {
		if strings.ContainsAny(s, " \t\r\n") {
			return "", errors.New("the pasted code contains whitespace")
		}
		return s, nil
	}
	if i := strings.IndexByte(qs, '#'); i >= 0 {
		qs = qs[:i]
	}
	q, err := url.ParseQuery(qs)
	if err != nil {
		return "", fmt.Errorf("parsing pasted URL: %w", err)
	}
	if q.Get("error") != "" {
		return "", errors.New("Google reported an error instead of a login; start a new login")
	}
	if len(q["code"]) != 1 || q.Get("code") == "" {
		return "", errors.New("pasted URL has no single code parameter")
	}
	if len(q["state"]) != 1 || q.Get("state") != expectedState {
		return "", errors.New("pasted URL is from a different login attempt (state missing or mismatched); paste the code or URL from the login you just started")
	}
	return q.Get("code"), nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// Exchange redeems an authorization code for credentials and looks up the
// account email (best-effort). The result is not persisted; callers Save it.
func Exchange(ctx context.Context, code string, p PKCE) (*Credentials, error) {
	creds, err := tokenCall(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {RedirectURI},
		"client_id":     {ClientID},
		"client_secret": {clientSecret},
		"code_verifier": {p.Verifier},
	}, code, p.Verifier)
	if err != nil {
		return nil, err
	}
	if creds.RefreshToken == "" {
		return nil, errors.New("Google returned no refresh token; start a new login")
	}
	if email, err := FetchEmail(ctx, creds.AccessToken); err == nil {
		creds.Email = email
	}
	return creds, nil
}

// Refresh trades a refresh token for a fresh access token. Google normally
// does not rotate refresh tokens, so the old one is kept when none is
// returned. A rejected grant returns an error wrapping ErrReauth.
func Refresh(ctx context.Context, refreshToken string) (*Credentials, error) {
	if refreshToken == "" {
		return nil, ErrReauth
	}
	creds, err := tokenCall(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {ClientID},
		"client_secret": {clientSecret},
	}, refreshToken)
	if err != nil {
		return nil, err
	}
	if creds.RefreshToken == "" {
		creds.RefreshToken = refreshToken
	}
	return creds, nil
}

func tokenCall(ctx context.Context, form url.Values, credentials ...string) (*Credentials, error) {
	credentials = append(credentials, clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{CheckRedirect: llmhttp.CheckRedirect, Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, llmhttp.RedactError(err, credentials...)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// Only Google's OAuth error code is reported; the body is never echoed.
		var oe struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &oe)
		if oe.Error == "invalid_grant" {
			return nil, ErrReauth
		}
		code := oe.Error
		if !validErrorCode(code) {
			code = "unrecognized error"
		}
		return nil, fmt.Errorf("Google token endpoint: %s: %s", resp.Status, code)
	}
	var tr tokenResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return nil, fmt.Errorf("Google token endpoint: parsing response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("Google token endpoint: response missing access_token")
	}
	return &Credentials{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).Unix(),
	}, nil
}

// validErrorCode reports whether s looks like an OAuth error code
// (e.g. invalid_client), so it is safe to show.
func validErrorCode(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

// FetchEmail returns the signed-in account's email address.
func FetchEmail(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UserinfoEndpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := (&http.Client{CheckRedirect: llmhttp.CheckRedirect, Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", llmhttp.RedactError(err, accessToken)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Google userinfo: %s", resp.Status)
	}
	var out struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", err
	}
	return out.Email, nil
}

// Load reads persisted credentials from the secrets store.
func Load() (creds *Credentials, ok bool) {
	creds, _, ok = loadRaw()
	return creds, ok
}

func loadRaw() (*Credentials, string, bool) {
	raw, ok := secrets.Lookup(SecretsKey)
	if !ok {
		return nil, raw, false
	}
	var c Credentials
	if err := json.Unmarshal([]byte(raw), &c); err != nil || (c.AccessToken == "" && c.RefreshToken == "") {
		return nil, raw, false
	}
	return &c, raw, true
}

// Save replaces the stored login (including any suspension marker) without
// waiting for an in-flight refresh, whose persistence is conditional.
func Save(c *Credentials) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return secrets.Set(SecretsKey, string(data))
}

// mu serializes refreshes and updates within this process.
var mu sync.Mutex

// blocked holds suspensions reported in this process, keyed by refresh
// token, so a grant stops everywhere even if persisting the marker failed.
var blocked = map[string]string{}

// Block records a terms-of-service suspension for the login identified by
// refreshToken: in memory at once, and on the stored login if it is still
// the current one.
func Block(refreshToken, detail string) error {
	mu.Lock()
	blocked[refreshToken] = detail
	mu.Unlock()
	return Update(refreshToken, func(c *Credentials) { c.Suspended = detail })
}

// SuspendedError reports a login Google suspended for a terms-of-service
// violation. It is permanent for that grant.
type SuspendedError struct{ Detail string }

func (e *SuspendedError) Error() string {
	return "Google suspended this account's Antigravity access (" + e.Detail + "); ycc will not use this login again. Resolve it with Google, then run `ycc login google`"
}

// Token is a live access token plus the login state the transport needs.
type Token struct {
	AccessToken string
	// RefreshToken identifies the login (for conditional updates); never
	// sent anywhere but Google's token endpoint.
	RefreshToken string
	ProjectID    string
}

// AccessToken returns a currently-valid access token, refreshing and
// re-persisting the stored credentials when it has expired.
func AccessToken(ctx context.Context) (Token, error) {
	mu.Lock()
	defer mu.Unlock()
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return Token{}, err
		}
		creds, raw, ok := loadRaw()
		if !ok {
			return Token{}, ErrNoLogin
		}
		if creds.Suspended != "" {
			return Token{}, &SuspendedError{Detail: creds.Suspended}
		}
		if detail := blocked[creds.RefreshToken]; detail != "" {
			return Token{}, &SuspendedError{Detail: detail}
		}
		if !creds.Expired(time.Now()) {
			return Token{AccessToken: creds.AccessToken, RefreshToken: creds.RefreshToken, ProjectID: creds.ProjectID}, nil
		}
		fresh, err := Refresh(ctx, creds.RefreshToken)
		if err != nil {
			if _, current, _ := loadRaw(); current != raw {
				continue
			}
			if errors.Is(err, ErrReauth) {
				return Token{}, err
			}
			return Token{}, fmt.Errorf("refreshing Google subscription token (re-run `ycc login google` if this persists): %w", err)
		}
		fresh.Email, fresh.ProjectID = creds.Email, creds.ProjectID
		data, err := json.Marshal(fresh)
		if err != nil {
			return Token{}, err
		}
		swapped, err := secrets.CompareAndSwap(SecretsKey, raw, string(data))
		if err != nil {
			return Token{}, fmt.Errorf("persisting refreshed Google credentials: %w", err)
		}
		if swapped {
			return Token{AccessToken: fresh.AccessToken, RefreshToken: fresh.RefreshToken, ProjectID: fresh.ProjectID}, nil
		}
	}
	return Token{}, errors.New("Google credentials changed repeatedly during refresh; retry the request")
}

// Update applies mutate to the stored credentials if they still belong to the
// login identified by refreshToken; a replaced or removed login is left alone.
func Update(refreshToken string, mutate func(*Credentials)) error {
	mu.Lock()
	defer mu.Unlock()
	for attempt := 0; attempt < 3; attempt++ {
		creds, raw, ok := loadRaw()
		if !ok || creds.RefreshToken != refreshToken {
			return nil
		}
		mutate(creds)
		data, err := json.Marshal(creds)
		if err != nil {
			return err
		}
		swapped, err := secrets.CompareAndSwap(SecretsKey, raw, string(data))
		if err != nil || swapped {
			return err
		}
	}
	return errors.New("Google credentials changed repeatedly during update")
}
