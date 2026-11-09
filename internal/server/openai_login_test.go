package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/openaiauth"
	"github.com/whyrusleeping/ycc/internal/secrets"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func openaiLoginTestProvider(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	provider := httptest.NewServer(handler)
	old := openaiauth.Issuer
	openaiauth.Issuer = provider.URL
	t.Cleanup(func() { openaiauth.Issuer = old; provider.Close() })
}

func beginOpenAILogin(t *testing.T, s *Server) *v1.BeginOpenAILoginResponse {
	t.Helper()
	res, err := s.BeginOpenAILogin(context.Background(), connect.NewRequest(&v1.BeginOpenAILoginRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("sensitive login response is cacheable")
	}
	if res.Msg.AttemptId == "" || res.Msg.ExpiresAtUnix <= time.Now().Unix() {
		t.Fatal("invalid attempt")
	}
	return res.Msg
}

func openaiLoginCallback(t *testing.T, attempt *v1.BeginOpenAILoginResponse) string {
	t.Helper()
	u, err := url.Parse(attempt.AuthorizationUrl)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if !strings.HasPrefix(attempt.AuthorizationUrl, openaiauth.Issuer+"/oauth/authorize?") ||
		q.Get("redirect_uri") != "http://localhost:1455/auth/callback" || q.Get("state") == "" ||
		q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Fatal("invalid authorization URL")
	}
	return q.Get("redirect_uri") + "?" + url.Values{"code": {"private-code"}, "state": {q.Get("state")}}.Encode()
}

func completeOpenAILogin(s *Server, id, callback string) error {
	_, err := s.CompleteOpenAILogin(context.Background(), connect.NewRequest(&v1.CompleteOpenAILoginRequest{AttemptId: id, Callback: callback}))
	return err
}

func TestOpenAILoginPersistsAndCannotReplay(t *testing.T) {
	for _, form := range []string{"URL", "query", "prefixed query"} {
		t.Run(form, func(t *testing.T) {
			var exchanges atomic.Int32
			var challenge string
			openaiLoginTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				exchanges.Add(1)
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if r.URL.Path != "/oauth/token" || r.Form.Get("grant_type") != "authorization_code" ||
					r.Form.Get("code") != "private-code" || r.Form.Get("redirect_uri") != "http://localhost:1455/auth/callback" ||
					r.Form.Get("client_id") != openaiauth.ClientID || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
					t.Error("incorrect code exchange")
				}
				w.Write([]byte(`{"access_token":"private-access","refresh_token":"private-refresh","expires_in":3600}`))
			})
			s := New(nil)
			attempt := beginOpenAILogin(t, s)
			u, _ := url.Parse(attempt.AuthorizationUrl)
			challenge = u.Query().Get("code_challenge")
			callback := openaiLoginCallback(t, attempt)
			if form != "URL" {
				_, callback, _ = strings.Cut(callback, "?")
				if form == "prefixed query" {
					callback = "?" + callback
				}
			}
			if err := completeOpenAILogin(s, attempt.AttemptId, callback); err != nil {
				t.Fatal(err)
			}
			creds, ok := openaiauth.Load()
			if !ok || creds.AccessToken != "private-access" || creds.RefreshToken != "private-refresh" {
				t.Fatal("credentials not saved")
			}
			info, err := os.Stat(secrets.Path())
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("credentials not stored privately")
			}
			token, _, err := openaiauth.AccessToken(context.Background())
			if err != nil || token != "private-access" {
				t.Fatal("replacement credentials not usable")
			}
			if err := completeOpenAILogin(s, attempt.AttemptId, callback); connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("replay: %v", err)
			}
			if exchanges.Load() != 1 {
				t.Fatal("code was exchanged more than once")
			}
		})
	}
}

func TestOpenAILoginRejectsInvalidExpiredReplacedAndCancelledAttempts(t *testing.T) {
	var exchanges atomic.Int32
	openaiLoginTestProvider(t, func(w http.ResponseWriter, r *http.Request) { exchanges.Add(1); w.WriteHeader(500) })
	for _, name := range []string{"bare code", "missing state", "wrong state", "duplicate state", "provider error", "oversize", "expired", "replaced", "cancelled", "unknown"} {
		t.Run(name, func(t *testing.T) {
			s := New(nil)
			attempt := beginOpenAILogin(t, s)
			id, callback := attempt.AttemptId, openaiLoginCallback(t, attempt)
			want := connect.CodeInvalidArgument
			switch name {
			case "bare code":
				callback = "private-code"
			case "missing state":
				callback = "?code=private-code"
			case "wrong state":
				callback = "?code=private-code&state=wrong-state"
			case "duplicate state":
				callback += "&state=wrong-state"
			case "provider error":
				callback += "&error=private-error&error_description=private-description"
			case "oversize":
				callback += "&extra=" + strings.Repeat("x", 8193)
			case "expired":
				s.openaiLogin.pending.expires = time.Now().Add(-time.Second)
				want = connect.CodeFailedPrecondition
			case "replaced":
				beginOpenAILogin(t, s)
				want = connect.CodeFailedPrecondition
			case "cancelled":
				s.CancelOpenAILogin(context.Background(), connect.NewRequest(&v1.CancelOpenAILoginRequest{AttemptId: id}))
				want = connect.CodeFailedPrecondition
			case "unknown":
				id = "unknown"
				want = connect.CodeFailedPrecondition
			}
			err := completeOpenAILogin(s, id, callback)
			if connect.CodeOf(err) != want || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "wrong-state") {
				t.Fatalf("unsafe rejection: %v", err)
			}
			if want == connect.CodeInvalidArgument {
				if err := completeOpenAILogin(s, attempt.AttemptId, openaiLoginCallback(t, attempt)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
					t.Fatalf("invalid submit did not consume attempt: %v", err)
				}
			}
		})
	}
	if exchanges.Load() != 0 {
		t.Fatal("invalid attempt reached provider")
	}
}

func TestOpenAILoginStaleCancelAndIndependentProviders(t *testing.T) {
	s := New(nil)
	anthropic := beginLogin(t, s)
	old := beginOpenAILogin(t, s)
	fresh := beginOpenAILogin(t, s)
	s.CancelOpenAILogin(context.Background(), connect.NewRequest(&v1.CancelOpenAILoginRequest{AttemptId: old.AttemptId}))
	if s.openaiLogin.pending == nil || s.openaiLogin.pending.id != fresh.AttemptId {
		t.Fatal("stale cancel removed new login")
	}
	if s.anthropicLogin.pending == nil || s.anthropicLogin.pending.id != anthropic.AttemptId {
		t.Fatal("OpenAI begin replaced Anthropic login")
	}
	s.CancelAnthropicLogin(context.Background(), connect.NewRequest(&v1.CancelAnthropicLoginRequest{AttemptId: anthropic.AttemptId}))
	if s.openaiLogin.pending == nil {
		t.Fatal("Anthropic cancel removed OpenAI login")
	}
}

func TestOpenAILoginExchangeFailuresAreSanitizedAndPreserveCredentials(t *testing.T) {
	for _, name := range []string{"provider error", "malformed", "missing refresh", "save failure"} {
		t.Run(name, func(t *testing.T) {
			openaiLoginTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				switch name {
				case "provider error":
					http.Error(w, "private-code private-refresh private-access", 400)
				case "malformed":
					w.Write([]byte("private-code private-refresh"))
				case "missing refresh":
					w.Write([]byte(`{"access_token":"private-access"}`))
				case "save failure":
					w.Write([]byte(`{"access_token":"private-access","refresh_token":"private-refresh","expires_in":3600}`))
				}
			})
			if err := openaiauth.Save(&openaiauth.Credentials{AccessToken: "original"}); err != nil {
				t.Fatal(err)
			}
			if name == "save failure" {
				bad := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(bad, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("XDG_CONFIG_HOME", bad)
			}
			s := New(nil)
			attempt := beginOpenAILogin(t, s)
			err := completeOpenAILogin(s, attempt.AttemptId, openaiLoginCallback(t, attempt))
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe failure: %v", err)
			}
			if err := completeOpenAILogin(s, attempt.AttemptId, openaiLoginCallback(t, attempt)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("failed exchange reusable: %v", err)
			}
			if name != "save failure" {
				creds, ok := openaiauth.Load()
				if !ok || creds.AccessToken != "original" {
					t.Fatal("failure changed saved login")
				}
			}
		})
	}
}

func TestOpenAILoginSerializesCompletion(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	openaiLoginTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","expires_in":3600}`))
	})
	s := New(nil)
	attempt := beginOpenAILogin(t, s)
	callback := openaiLoginCallback(t, attempt)
	done := make(chan error, 1)
	go func() { done <- completeOpenAILogin(s, attempt.AttemptId, callback) }()
	<-entered
	_, err := s.BeginOpenAILogin(context.Background(), connect.NewRequest(&v1.BeginOpenAILoginRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("begin raced completing login: %v", err)
	}
	if err := completeOpenAILogin(s, attempt.AttemptId, callback); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("duplicate completion: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	beginOpenAILogin(t, s)
}

func TestOpenAILoginCompletesDuringOldRefresh(t *testing.T) {
	for _, oldFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "old refresh succeeds", true: "old refresh fails"}[oldFails], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			openaiLoginTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Content-Type") == "application/json" {
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					if body["grant_type"] != "refresh_token" {
						t.Error("unexpected request")
					}
					close(entered)
					<-release
					if oldFails {
						http.Error(w, "old account revoked", 400)
						return
					}
					w.Write([]byte(`{"access_token":"old-refreshed","refresh_token":"old","expires_in":3600}`))
				} else {
					w.Write([]byte(`{"access_token":"new-login","refresh_token":"new","expires_in":3600}`))
				}
			})
			if err := openaiauth.Save(&openaiauth.Credentials{AccessToken: "old", RefreshToken: "old", AccountID: "old-account"}); err != nil {
				t.Fatal(err)
			}
			refreshed := make(chan error, 1)
			go func() {
				token, account, err := openaiauth.AccessToken(context.Background())
				if err == nil && (token != "new-login" || account != "") {
					t.Error("old refresh returned stale account credentials")
				}
				refreshed <- err
			}()
			<-entered
			s := New(nil)
			attempt := beginOpenAILogin(t, s)
			callback := openaiLoginCallback(t, attempt)
			done := make(chan error, 1)
			go func() { done <- completeOpenAILogin(s, attempt.AttemptId, callback) }()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(2 * time.Second):
				t.Error("login completion blocked behind old refresh")
			}
			close(release)
			if err := <-refreshed; err != nil {
				t.Fatal(err)
			}
			creds, ok := openaiauth.Load()
			if !ok || creds.AccessToken != "new-login" || creds.RefreshToken != "new" || creds.AccountID != "" {
				t.Fatal("old refresh overwrote new login")
			}
		})
	}
}

func TestOpenAILoginRPCsRequireDaemonAuth(t *testing.T) {
	s := New(nil)
	path, handler := yccv1connect.NewSessionServiceHandler(s, connect.WithInterceptors(NewAuthInterceptor("daemon-secret")))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	client := yccv1connect.NewSessionServiceClient(httpServer.Client(), httpServer.URL)
	ctx := context.Background()
	_, err := client.BeginOpenAILogin(ctx, connect.NewRequest(&v1.BeginOpenAILoginRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated begin: %v", err)
	}
	_, err = client.CompleteOpenAILogin(ctx, connect.NewRequest(&v1.CompleteOpenAILoginRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated complete: %v", err)
	}
	_, err = client.CancelOpenAILogin(ctx, connect.NewRequest(&v1.CancelOpenAILoginRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated cancel: %v", err)
	}
	if s.openaiLogin.pending != nil {
		t.Fatal("unauthenticated request allocated login")
	}
	req := connect.NewRequest(&v1.BeginOpenAILoginRequest{})
	req.Header().Set("Authorization", "Bearer daemon-secret")
	res, err := client.BeginOpenAILogin(ctx, req)
	if err != nil || res.Msg.AttemptId == "" {
		t.Fatalf("authenticated begin: %v", err)
	}
}
