package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestQuickRepliesRPC(t *testing.T) {
	ws := t.TempDir()
	mgr := session.NewManager(config.NewRegistry(nil), ws)
	defer mgr.ReclaimAll()
	srv := New(mgr)
	ctx := context.Background()

	got, err := srv.GetQuickReplies(ctx, connect.NewRequest(&v1.GetQuickRepliesRequest{}))
	if err != nil || len(got.Msg.Builtin) != len(session.BuiltinQuickReplies) || got.Msg.Builtin[0].Id == "" || len(got.Msg.Project) != 0 {
		t.Fatalf("initial: %+v %v", got, err)
	}

	set, err := srv.SetQuickReplies(ctx, connect.NewRequest(&v1.SetQuickRepliesRequest{Replies: []*v1.QuickReply{
		{Id: "ignored", Label: " Deploy ", Text: " Deploy to prod following the runbook\n"},
	}}))
	if err != nil || len(set.Msg.Project) != 1 || set.Msg.Project[0].Label != "Deploy" || set.Msg.Project[0].Id != "" {
		t.Fatalf("set: %+v %v", set, err)
	}

	// A fresh manager reads the persisted list back.
	mgr2 := session.NewManager(config.NewRegistry(nil), ws)
	defer mgr2.ReclaimAll()
	got, err = New(mgr2).GetQuickReplies(ctx, connect.NewRequest(&v1.GetQuickRepliesRequest{}))
	if err != nil || len(got.Msg.Project) != 1 || got.Msg.Project[0].Text != "Deploy to prod following the runbook" {
		t.Fatalf("reload: %+v %v", got, err)
	}

	for name, replies := range map[string][]*v1.QuickReply{
		"empty text": {{Label: "Deploy", Text: "  "}},
		"long label": {{Label: "This label is far too long for a quick reply chip", Text: "x"}},
		"too many":   make([]*v1.QuickReply, 13),
	} {
		if _, err := srv.SetQuickReplies(ctx, connect.NewRequest(&v1.SetQuickRepliesRequest{Replies: replies})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: got %v, want invalid_argument", name, err)
		}
	}
	got, _ = srv.GetQuickReplies(ctx, connect.NewRequest(&v1.GetQuickRepliesRequest{}))
	if len(got.Msg.Project) != 1 {
		t.Fatalf("rejected sets must not change the stored list: %+v", got.Msg.Project)
	}
}
