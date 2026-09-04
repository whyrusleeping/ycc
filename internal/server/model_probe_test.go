package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestModelProbeUsesDraftWithoutMutatingRegistry(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"probe\",\"object\":\"chat.completion.chunk\",\"model\":\"draft-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer provider.Close()

	active := config.Model{Backend: "openai", BaseURL: "https://active.invalid/v1", Model: "active-model"}
	configPath := filepath.Join(t.TempDir(), "ycc.toml")
	if err := config.Save(configPath, &config.Config{
		Models: map[string]config.Model{"active": active},
		Roles:  config.Roles{Coordinator: "active", Implementer: "active", Reviewers: []string{"active"}},
	}); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	beforeFile, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	reg := config.NewRegistry(cfg)
	reg.SetPath(configPath)
	manager := session.NewManager(reg, t.TempDir())
	defer manager.ReclaimAll()
	srv := New(manager)

	response, err := srv.TestModel(context.Background(), connect.NewRequest(&v1.TestModelRequest{
		Model: &v1.ModelConfig{
			Name: "draft", Backend: "openai", BaseUrl: provider.URL, Model: "draft-model",
			Thinking: "adaptive", Effort: "low", Disabled: boolPtr(true),
		},
	}))
	if err != nil {
		t.Fatalf("TestModel: %v", err)
	}
	if !response.Msg.Success || response.Msg.Message == "" || response.Msg.ErrorKind != "" {
		t.Fatalf("TestModel response = %+v", response.Msg)
	}
	if _, ok := reg.GetModel("draft"); ok {
		t.Fatal("probe installed draft model in live registry")
	}
	if got, ok := reg.GetModel("active"); !ok || !reflect.DeepEqual(got, active) {
		t.Fatalf("active model changed: got=%+v ok=%v want=%+v", got, ok, active)
	}
	afterFile, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterFile, beforeFile) {
		t.Fatal("probe changed persisted ycc.toml")
	}
}

func TestModelProbeReturnsProviderFailureAsDiagnostic(t *testing.T) {
	const credential = "credential-that-must-not-cross-rpc"
	t.Setenv("MODEL_PROBE_KEY", credential)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `unknown model id; echoed authorization: `+credential, http.StatusNotFound)
	}))
	defer provider.Close()

	reg := config.NewRegistry(&config.Config{})
	manager := session.NewManager(reg, t.TempDir())
	defer manager.ReclaimAll()
	srv := New(manager)
	response, err := srv.TestModel(context.Background(), connect.NewRequest(&v1.TestModelRequest{
		Model: &v1.ModelConfig{
			Name: "draft", Backend: "openai", BaseUrl: provider.URL, Model: "wrong", KeyEnv: "MODEL_PROBE_KEY",
		},
	}))
	if err != nil {
		t.Fatalf("provider failure became RPC failure: %v", err)
	}
	if response.Msg.Success || response.Msg.Status != http.StatusNotFound || response.Msg.ErrorKind != "invalid_request" {
		t.Fatalf("diagnostic response = %+v", response.Msg)
	}
	if response.Msg.Message == "" {
		t.Fatal("diagnostic response omitted actionable message")
	}
	if strings.Contains(response.Msg.Message, credential) || strings.Contains(response.Msg.Message, "echoed authorization") {
		t.Fatalf("raw provider error crossed RPC boundary: %q", response.Msg.Message)
	}
}

func TestModelProbeTimeoutAndCallerCancellation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cancel     bool
		wantResult bool
	}{
		{name: "daemon timeout", wantResult: true},
		{name: "caller cancellation", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestStarted := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				close(requestStarted)
				select {
				case <-r.Context().Done():
				case <-time.After(250 * time.Millisecond):
				}
			}))
			defer provider.Close()

			manager := session.NewManager(config.NewRegistry(&config.Config{}), t.TempDir())
			defer manager.ReclaimAll()
			srv := New(manager)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				go func() {
					<-requestStarted
					cancel()
				}()
			}
			request := connect.NewRequest(&v1.TestModelRequest{Model: &v1.ModelConfig{
				Name: "slow", Backend: "openai", BaseUrl: provider.URL, Model: "slow",
			}})
			response, err := srv.testModel(ctx, request, 25*time.Millisecond)
			if tc.wantResult {
				if err != nil || response.Msg.Success || response.Msg.ErrorKind != "timeout" {
					t.Fatalf("timeout response=%+v err=%v", response, err)
				}
				return
			}
			if err == nil || connect.CodeOf(err) != connect.CodeCanceled {
				t.Fatalf("cancellation error = %v, want canceled", err)
			}
		})
	}
}

func TestModelProbeRejectsInvalidDraft(t *testing.T) {
	reg := config.NewRegistry(&config.Config{})
	manager := session.NewManager(reg, t.TempDir())
	defer manager.ReclaimAll()
	srv := New(manager)
	_, err := srv.TestModel(context.Background(), connect.NewRequest(&v1.TestModelRequest{
		Model: &v1.ModelConfig{Name: "draft", Backend: "not-a-backend", Model: "m"},
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid draft error = %v, want invalid_argument", err)
	}
}
