package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/anthropicauth"
	"github.com/whyrusleeping/ycc/internal/openaiauth"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestModelRPCsRejectUnsafeCredentials(t *testing.T) {
	// A server with no manager also ensures rejection happens before any manager
	// access, credential lookup, persistence, or provider network request.
	s := &Server{}
	for _, key := range []string{"YCC_TOKEN", anthropicauth.SecretsKey, openaiauth.SecretsKey} {
		t.Run(key, func(t *testing.T) {
			model := &v1.ModelConfig{Name: "draft", Backend: "openai", Model: "test", KeyEnv: key, BaseUrl: "https://provider.example"}
			_, err := s.UpsertModel(context.Background(), connect.NewRequest(&v1.UpsertModelRequest{Model: model}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("UpsertModel: %v", err)
			}
			_, err = s.TestModel(context.Background(), connect.NewRequest(&v1.TestModelRequest{Model: model}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("TestModel: %v", err)
			}
			_, err = s.DiscoverModels(context.Background(), connect.NewRequest(&v1.DiscoverModelsRequest{Backend: "openai", KeyEnv: key, BaseUrl: model.BaseUrl}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("DiscoverModels: %v", err)
			}
		})
	}
	for _, model := range []*v1.ModelConfig{
		{Name: "draft", Backend: "openai", Model: "test", KeyEnv: "PROVIDER_KEY", BaseUrl: "http://provider.example"},
		{Name: "draft", Backend: "anthropic", Model: "test", Auth: "oauth", BaseUrl: "http://provider.example"},
	} {
		_, err := s.UpsertModel(context.Background(), connect.NewRequest(&v1.UpsertModelRequest{Model: model}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("UpsertModel with insecure URL: %v", err)
		}
		_, err = s.TestModel(context.Background(), connect.NewRequest(&v1.TestModelRequest{Model: model}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("TestModel with insecure URL: %v", err)
		}
		_, err = s.DiscoverModels(context.Background(), connect.NewRequest(&v1.DiscoverModelsRequest{Backend: model.Backend, KeyEnv: model.KeyEnv, BaseUrl: model.BaseUrl}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("DiscoverModels with insecure URL: %v", err)
		}
	}
}
