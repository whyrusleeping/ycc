package server

import (
	"context"
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
	"github.com/whyrusleeping/ycc/internal/anthropicauth"
	"github.com/whyrusleeping/ycc/internal/secrets"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func loginTestProvider(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	provider := httptest.NewServer(handler)
	old := anthropicauth.TokenEndpoint
	anthropicauth.TokenEndpoint = provider.URL
	t.Cleanup(func() { anthropicauth.TokenEndpoint = old; provider.Close() })
}

func beginLogin(t *testing.T, s *Server) *v1.BeginAnthropicLoginResponse {
	t.Helper()
	res, err := s.BeginAnthropicLogin(context.Background(), connect.NewRequest(&v1.BeginAnthropicLoginRequest{}))
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

func loginCode(t *testing.T, attempt *v1.BeginAnthropicLoginResponse) string {
	t.Helper()
	u, err := url.Parse(attempt.AuthorizationUrl)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "claude.com" {
		t.Fatal("unexpected authorization host")
	}
	return "private-code#" + u.Query().Get("state")
}

func completeLogin(s *Server, id, code string) error {
	_, err := s.CompleteAnthropicLogin(context.Background(), connect.NewRequest(&v1.CompleteAnthropicLoginRequest{AttemptId: id, Code: code}))
	return err
}

func TestAnthropicLoginPersistsAndCannotReplay(t *testing.T) {
	var exchanges atomic.Int32
	loginTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		exchanges.Add(1)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["grant_type"] != "authorization_code" || body["code"] != "private-code" || body["state"] != body["code_verifier"] {
			t.Error("incorrect code exchange")
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "private-access", "refresh_token": "private-refresh", "expires_in": 3600})
	})
	s := New(nil) // Login must not interact with session/config/work-loop machinery.
	attempt := beginLogin(t, s)
	code := loginCode(t, attempt)
	if err := completeLogin(s, attempt.AttemptId, code); err != nil {
		t.Fatal(err)
	}
	creds, ok := anthropicauth.Load()
	if !ok || creds.AccessToken != "private-access" || creds.RefreshToken != "private-refresh" {
		t.Fatal("credentials not saved")
	}
	info, err := os.Stat(secrets.Path())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credentials not stored privately")
	}
	// A live consumer immediately picks up the new credentials, no restart.
	token, err := anthropicauth.AccessToken(context.Background())
	if err != nil || token != "private-access" {
		t.Fatal("replacement credentials not usable")
	}
	if err := completeLogin(s, attempt.AttemptId, code); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("replay: %v", err)
	}
	if exchanges.Load() != 1 {
		t.Fatal("code was exchanged more than once")
	}
}

func TestAnthropicLoginRejectsInvalidExpiredReplacedAndCancelledAttempts(t *testing.T) {
	var exchanges atomic.Int32
	loginTestProvider(t, func(w http.ResponseWriter, r *http.Request) { exchanges.Add(1); w.WriteHeader(500) })
	for _, name := range []string{"missing state", "wrong state", "oversize", "expired", "replaced", "cancelled", "unknown"} {
		t.Run(name, func(t *testing.T) {
			s := New(nil)
			attempt := beginLogin(t, s)
			id, code := attempt.AttemptId, loginCode(t, attempt)
			switch name {
			case "missing state":
				code = "private-code"
			case "wrong state":
				code = "private-code#wrong-state"
			case "oversize":
				code = strings.Repeat("x", 8193) + code
			case "expired":
				s.anthropicLogin.pending.expires = time.Now().Add(-time.Second)
			case "replaced":
				beginLogin(t, s)
			case "cancelled":
				s.CancelAnthropicLogin(context.Background(), connect.NewRequest(&v1.CancelAnthropicLoginRequest{AttemptId: id}))
			case "unknown":
				id = "unknown"
			}
			err := completeLogin(s, id, code)
			if err == nil || strings.Contains(err.Error(), "private-code") || strings.Contains(err.Error(), "wrong-state") {
				t.Fatalf("unsafe rejection: %v", err)
			}
			if name == "missing state" || name == "wrong state" || name == "oversize" {
				if err := completeLogin(s, attempt.AttemptId, loginCode(t, attempt)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
					t.Fatalf("invalid submit did not consume attempt: %v", err)
				}
			}
		})
	}
	if exchanges.Load() != 0 {
		t.Fatal("invalid attempt reached provider")
	}
}

func TestAnthropicLoginStaleCancelDoesNotCancelNewAttempt(t *testing.T) {
	s := New(nil)
	old := beginLogin(t, s)
	fresh := beginLogin(t, s)
	s.CancelAnthropicLogin(context.Background(), connect.NewRequest(&v1.CancelAnthropicLoginRequest{AttemptId: old.AttemptId}))
	if s.anthropicLogin.pending == nil || s.anthropicLogin.pending.id != fresh.AttemptId {
		t.Fatal("stale cancel removed new login")
	}
}

func TestAnthropicLoginExchangeFailuresAreSanitizedAndPreserveCredentials(t *testing.T) {
	for _, name := range []string{"provider error", "malformed", "missing refresh", "save failure"} {
		t.Run(name, func(t *testing.T) {
			loginTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
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
			if err := anthropicauth.Save(&anthropicauth.Credentials{AccessToken: "original"}); err != nil {
				t.Fatal(err)
			}
			if name == "save failure" {
				// A file where the config directory should be makes saving fail,
				// independent of whether the test process has root permissions.
				bad := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(bad, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("XDG_CONFIG_HOME", bad)
			}
			s := New(nil)
			attempt := beginLogin(t, s)
			err := completeLogin(s, attempt.AttemptId, loginCode(t, attempt))
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe failure: %v", err)
			}
			if err := completeLogin(s, attempt.AttemptId, loginCode(t, attempt)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("failed exchange reusable: %v", err)
			}
			if name != "save failure" {
				creds, ok := anthropicauth.Load()
				if !ok || creds.AccessToken != "original" {
					t.Fatal("failure changed saved login")
				}
			}
		})
	}
}

func TestAnthropicLoginSerializesCompletion(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	loginTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","expires_in":3600}`))
	})
	s := New(nil)
	attempt := beginLogin(t, s)
	code := loginCode(t, attempt)
	done := make(chan error, 1)
	go func() { done <- completeLogin(s, attempt.AttemptId, code) }()
	<-entered
	_, err := s.BeginAnthropicLogin(context.Background(), connect.NewRequest(&v1.BeginAnthropicLoginRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("begin raced completing login: %v", err)
	}
	if err := completeLogin(s, attempt.AttemptId, code); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("duplicate completion: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	beginLogin(t, s) // slot released after completion
}

func TestAnthropicLoginCompletesDuringOldRefresh(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	loginTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] == "refresh_token" {
			close(entered)
			<-release
			w.Write([]byte(`{"access_token":"old-refreshed","refresh_token":"old","expires_in":3600}`))
		} else {
			w.Write([]byte(`{"access_token":"new-login","refresh_token":"new","expires_in":3600}`))
		}
	})
	if err := anthropicauth.Save(&anthropicauth.Credentials{AccessToken: "old", RefreshToken: "old", FlowVersion: anthropicauth.FlowVersion}); err != nil {
		t.Fatal(err)
	}
	refreshed := make(chan error, 1)
	go func() { _, err := anthropicauth.AccessToken(context.Background()); refreshed <- err }()
	<-entered
	s := New(nil)
	attempt := beginLogin(t, s)
	code := loginCode(t, attempt)
	done := make(chan error, 1)
	go func() { done <- completeLogin(s, attempt.AttemptId, code) }()
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
	creds, ok := anthropicauth.Load()
	if !ok || creds.AccessToken != "new-login" {
		t.Fatal("old refresh overwrote new login")
	}
	beginLogin(t, s)
}

func TestAnthropicLoginRPCsRequireDaemonAuth(t *testing.T) {
	s := New(nil)
	path, handler := yccv1connect.NewSessionServiceHandler(s, connect.WithInterceptors(NewAuthInterceptor("daemon-secret")))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	client := yccv1connect.NewSessionServiceClient(httpServer.Client(), httpServer.URL)
	ctx := context.Background()
	_, err := client.BeginAnthropicLogin(ctx, connect.NewRequest(&v1.BeginAnthropicLoginRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated begin: %v", err)
	}
	_, err = client.CompleteAnthropicLogin(ctx, connect.NewRequest(&v1.CompleteAnthropicLoginRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated complete: %v", err)
	}
	_, err = client.CancelAnthropicLogin(ctx, connect.NewRequest(&v1.CancelAnthropicLoginRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated cancel: %v", err)
	}
	if s.anthropicLogin.pending != nil {
		t.Fatal("unauthenticated request allocated login")
	}
	req := connect.NewRequest(&v1.BeginAnthropicLoginRequest{})
	req.Header().Set("Authorization", "Bearer daemon-secret")
	res, err := client.BeginAnthropicLogin(ctx, req)
	if err != nil || res.Msg.AttemptId == "" {
		t.Fatalf("authenticated begin: %v", err)
	}
}
