package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// SetThinking maps an unknown session to a NotFound connect error, mirroring the
// other settings RPCs. (A live session requires a running backend, so the
// happy/InvalidArgument paths are covered by the session package's unit tests.)
func TestSetThinkingUnknownSession(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	srv := New(session.NewManager(reg, t.TempDir()))

	_, err := srv.SetThinking(context.Background(), connect.NewRequest(&v1.SetThinkingRequest{
		SessionId: "nope", Level: "high",
	}))
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound", got)
	}
}
