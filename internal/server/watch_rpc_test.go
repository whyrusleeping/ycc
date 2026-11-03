package server

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestWatchRPCLifecycle(t *testing.T) {
	ctx := context.Background()
	mgr := session.NewManager(testRegistry(), t.TempDir())
	t.Cleanup(mgr.ReclaimAll)
	srv := New(mgr)

	def := &v1.WatchDefinition{
		Name:           "deploy",
		Brief:          "check the deploy is healthy",
		IntervalSecs:   3600,
		Model:          "c",
		Probe:          "true",
		ProbeAlways:    true,
		Expires:        "2099-01-02T03:04:05Z",
		AllowedActions: []string{"restart the worker"},
	}
	created, err := srv.CreateWatch(ctx, connect.NewRequest(&v1.CreateWatchRequest{Definition: def}))
	if err != nil {
		t.Fatalf("CreateWatch: %v", err)
	}
	if created.Msg.Watch.NextDue == "" || created.Msg.Watch.Paused {
		t.Fatalf("created watch = %+v", created.Msg.Watch)
	}

	list, err := srv.ListWatches(ctx, connect.NewRequest(&v1.ListWatchesRequest{}))
	if err != nil {
		t.Fatalf("ListWatches: %v", err)
	}
	if len(list.Msg.Watches) != 1 {
		t.Fatalf("watches = %d, want 1", len(list.Msg.Watches))
	}
	got, err := srv.GetWatch(ctx, connect.NewRequest(&v1.GetWatchRequest{Name: "deploy"}))
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	for _, w := range []*v1.Watch{list.Msg.Watches[0], got.Msg.Watch} {
		d := w.Definition
		if d.Name != def.Name || d.Brief != def.Brief || d.IntervalSecs != def.IntervalSecs || d.Model != def.Model ||
			d.Probe != def.Probe || d.ProbeAlways != def.ProbeAlways || d.Expires != "2099-01-02T03:04:05.000Z" ||
			!slices.Equal(d.AllowedActions, def.AllowedActions) {
			t.Fatalf("definition round-trip = %+v", d)
		}
	}
	if len(got.Msg.Reports) != 0 {
		t.Fatalf("reports = %+v, want none", got.Msg.Reports)
	}

	_, err = srv.CreateWatch(ctx, connect.NewRequest(&v1.CreateWatchRequest{Definition: def}))
	if code := connect.CodeOf(err); code != connect.CodeAlreadyExists {
		t.Fatalf("duplicate create code = %v (%v), want AlreadyExists", code, err)
	}
	_, err = srv.GetWatch(ctx, connect.NewRequest(&v1.GetWatchRequest{Name: "nope"}))
	if code := connect.CodeOf(err); code != connect.CodeNotFound {
		t.Fatalf("unknown watch code = %v (%v), want NotFound", code, err)
	}
	_, err = srv.CreateWatch(ctx, connect.NewRequest(&v1.CreateWatchRequest{Definition: &v1.WatchDefinition{Name: "fast", Brief: "x", IntervalSecs: 1}}))
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Fatalf("too-short interval code = %v (%v), want InvalidArgument", code, err)
	}

	paused, err := srv.SetWatchPaused(ctx, connect.NewRequest(&v1.SetWatchPausedRequest{Name: "deploy", Paused: true}))
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if !paused.Msg.Watch.Paused || paused.Msg.Watch.PausedReason != "user" {
		t.Fatalf("paused watch = %+v", paused.Msg.Watch)
	}
	got, err = srv.GetWatch(ctx, connect.NewRequest(&v1.GetWatchRequest{Name: "deploy"}))
	if err != nil || !got.Msg.Watch.Paused {
		t.Fatalf("GetWatch after pause = %+v, %v", got, err)
	}
	resumed, err := srv.SetWatchPaused(ctx, connect.NewRequest(&v1.SetWatchPausedRequest{Name: "deploy"}))
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Msg.Watch.Paused || resumed.Msg.Watch.PausedReason != "" {
		t.Fatalf("resumed watch = %+v", resumed.Msg.Watch)
	}

	if _, err := srv.DeleteWatch(ctx, connect.NewRequest(&v1.DeleteWatchRequest{Name: "deploy"})); err != nil {
		t.Fatalf("DeleteWatch: %v", err)
	}
	list, err = srv.ListWatches(ctx, connect.NewRequest(&v1.ListWatchesRequest{}))
	if err != nil || len(list.Msg.Watches) != 0 {
		t.Fatalf("ListWatches after delete = %+v, %v", list, err)
	}
	_, err = srv.DeleteWatch(ctx, connect.NewRequest(&v1.DeleteWatchRequest{Name: "deploy"}))
	if code := connect.CodeOf(err); code != connect.CodeNotFound {
		t.Fatalf("second delete code = %v (%v), want NotFound", code, err)
	}
}
