package server

import (
	"context"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestQuickRepliesRPC(t *testing.T) {
	ws := t.TempDir()
	globalFile := filepath.Join(t.TempDir(), "quick-replies.json")
	newServer := func() *Server {
		mgr := session.NewManager(config.NewRegistry(nil), ws)
		t.Cleanup(mgr.ReclaimAll)
		mgr.SetQuickRepliesFile(globalFile)
		return New(mgr)
	}
	srv := newServer()
	ctx := context.Background()
	get := func(s *Server) *v1.GetQuickRepliesResponse {
		t.Helper()
		got, err := s.GetQuickReplies(ctx, connect.NewRequest(&v1.GetQuickRepliesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return got.Msg
	}

	// Until edited, the daemon-wide list is the defaults.
	if got := get(srv); len(got.Global) != len(session.DefaultQuickReplies) || got.Global[0].Id != "go_ahead" || len(got.Project) != 0 {
		t.Fatalf("initial: %+v", got)
	}

	// Editing the daemon-wide list keeps a default's id (even when renamed),
	// drops invented ids, and can remove defaults.
	set, err := srv.SetQuickReplies(ctx, connect.NewRequest(&v1.SetQuickRepliesRequest{Global: true, Replies: []*v1.QuickReply{
		{Id: "continue", Label: "Keep going", Text: "Keep going"},
		{Id: "made_up", Label: "Ship it", Text: "Ship it", Icon: "rocket"},
	}}))
	if err != nil || len(set.Msg.Replies) != 2 || set.Msg.Replies[0].Id != "continue" || set.Msg.Replies[1].Id != "" || set.Msg.Replies[1].Icon != "rocket" {
		t.Fatalf("set global: %+v %v", set, err)
	}

	set, err = srv.SetQuickReplies(ctx, connect.NewRequest(&v1.SetQuickRepliesRequest{Replies: []*v1.QuickReply{
		{Id: "go_ahead", Label: " Deploy ", Text: " Deploy to prod following the runbook\n", Icon: "upload"},
	}}))
	if err != nil || len(set.Msg.Replies) != 1 || set.Msg.Replies[0].Label != "Deploy" || set.Msg.Replies[0].Id != "" {
		t.Fatalf("set project: %+v %v", set, err)
	}

	// A fresh manager reads both lists back; an emptied daemon-wide list
	// stays empty rather than reverting to the defaults.
	got := get(newServer())
	if len(got.Global) != 2 || got.Global[0].Label != "Keep going" || len(got.Project) != 1 || got.Project[0].Text != "Deploy to prod following the runbook" ||
		got.Global[1].Icon != "rocket" || got.Project[0].Icon != "upload" {
		t.Fatalf("reload: %+v", got)
	}
	if _, err := srv.SetQuickReplies(ctx, connect.NewRequest(&v1.SetQuickRepliesRequest{Global: true})); err != nil {
		t.Fatal(err)
	}
	if got := get(newServer()); len(got.Global) != 0 {
		t.Fatalf("emptied global list: %+v", got.Global)
	}

	for name, replies := range map[string][]*v1.QuickReply{
		"empty text": {{Label: "Deploy", Text: "  "}},
		"long label": {{Label: "This label is far too long for a quick reply chip", Text: "x"}},
		"too many":   make([]*v1.QuickReply, 13),
		"bad icon":   {{Label: "Deploy", Text: "Deploy", Icon: "Rocket Ship"}},
	} {
		for _, global := range []bool{false, true} {
			_, err := srv.SetQuickReplies(ctx, connect.NewRequest(&v1.SetQuickRepliesRequest{Global: global, Replies: replies}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("%s (global=%v): got %v, want invalid_argument", name, global, err)
			}
		}
	}
	if got := get(srv); len(got.Project) != 1 || len(got.Global) != 0 {
		t.Fatalf("rejected sets must not change the stored lists: %+v", got)
	}
}
