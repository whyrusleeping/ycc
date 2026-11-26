package server

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// A user notice's expiry is range-checked before it becomes a duration, so an
// overflowing value is rejected rather than wrapped into an accepted one.
func TestPostProjectNoticeExpiryBounds(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ws := t.TempDir()
	if out, err := exec.Command("git", "-C", ws, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	mgr := session.NewManager(config.NewRegistry(nil), ws)
	defer mgr.ReclaimAll()
	mgr.SetProjects(project.NewMemory())
	if _, err := mgr.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetSessionCoordination("proj", true); err != nil {
		t.Fatal(err)
	}
	srv := New(mgr)
	post := func(secs int64) (*connect.Response[v1.PostProjectNoticeResponse], error) {
		return srv.PostProjectNotice(context.Background(), connect.NewRequest(&v1.PostProjectNoticeRequest{
			Project: "proj", Text: "Deploying to staging", ExpiresInSeconds: secs}))
	}
	for _, secs := range []int64{(1 << 55) + 60, -1, 30, 86401} {
		if _, err := post(secs); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("expires_in_seconds %d: %v", secs, err)
		}
	}
	resp, err := post(3600)
	if err != nil || resp.Msg.Notice.GetActor() != "user" || resp.Msg.Notice.GetSessionId() != "" {
		t.Fatalf("valid post: %+v %v", resp, err)
	}
}

// A live session's claims reach clients in order, none ambiguous.
func TestClaimedPathsProtoKeepsOrder(t *testing.T) {
	since := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	got := claimedPathsProto([]session.PathClaim{
		{Path: "gen.go", Since: since},
		{Path: "mine.go"},
	})
	if len(got) != 2 {
		t.Fatalf("claims = %v, want 2", got)
	}
	if c := got[0]; c.GetPath() != "gen.go" || c.GetSince() != "2026-11-24T10:00:00Z" || c.GetAmbiguous() {
		t.Fatalf("first claim = %v, want gen.go since 10:00", c)
	}
	if c := got[1]; c.GetPath() != "mine.go" || c.GetSince() != "" || c.GetAmbiguous() {
		t.Fatalf("second claim = %v, want mine.go of unknown age", c)
	}
}
