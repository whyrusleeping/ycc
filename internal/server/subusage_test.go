package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/session"
	"github.com/whyrusleeping/ycc/internal/subusage"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

type staticSubUsage struct{}

func (staticSubUsage) Fetch(context.Context, string) (subusage.Account, error) {
	return subusage.Account{Plan: "pro", Windows: []subusage.Window{{ID: "5h", Label: "5 hour", UsedPercent: 25}}}, nil
}

func TestGetSubscriptionUsageGroupsOAuthModelsAndExposesNoCredentials(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{
			"claude-a": {Backend: "anthropic", Model: "claude-a", Auth: "oauth"},
			"claude-b": {Backend: "anthropic", Model: "claude-b", Auth: "oauth"},
			"api":      {Backend: "openai", Model: "gpt", KeyEnv: "VERY_SECRET_KEY"},
		},
		Roles: config.Roles{Coordinator: "claude-a", Implementer: "claude-a", Reviewers: []string{"claude-a"}},
	})
	srv := New(session.NewManager(reg, t.TempDir()))
	srv.subUsage = subusage.NewService(staticSubUsage{})

	resp, err := srv.GetSubscriptionUsage(context.Background(), connect.NewRequest(&v1.GetSubscriptionUsageRequest{Refresh: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Accounts) != 1 {
		t.Fatalf("accounts = %+v", resp.Msg.Accounts)
	}
	got := resp.Msg.Accounts[0]
	if got.Provider != "anthropic" || len(got.Models) != 2 || got.State != "fresh" || len(got.Windows) != 1 {
		t.Fatalf("account = %+v", got)
	}
}
