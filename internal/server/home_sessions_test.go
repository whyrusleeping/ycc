package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func writeHistoryLogs(t *testing.T, ws string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		lg, err := event.OpenLog(filepath.Join(ws, ".ycc", "sessions", id, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lg.Record("user", event.UserInput, map[string]any{"text": "title " + id})
		if err := lg.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// One ListHomeSessions call returns every project's first page exactly as the
// per-project ListSessionHistory / GetWorkLoop fan-out would.
func TestListHomeSessionsMatchesPerProjectFanOut(t *testing.T) {
	alpha, beta := t.TempDir(), t.TempDir()
	writeHistoryLogs(t, alpha, "s_a1", "s_a2", "s_a3")
	writeHistoryLogs(t, beta, "s_b1", "s_loop")
	loop := fmt.Sprintf(`{"version":1,"loop_id":"wl_1","project":"beta","workspace":%q,"state":"finished",`+
		`"started_at":"2026-01-01T00:00:00Z","sessions":[{"session_id":"s_loop"},{"session_id":""}]}`, beta)
	if err := os.WriteFile(filepath.Join(beta, ".ycc", "workloop.json"), []byte(loop), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := session.NewManager(config.NewRegistry(nil), alpha)
	defer mgr.ReclaimAll()
	if _, err := mgr.AddProject(beta, "beta"); err != nil {
		t.Fatal(err)
	}
	_, handler := yccv1connect.NewSessionServiceHandler(New(mgr))
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	client := yccv1connect.NewSessionServiceClient(httpServer.Client(), httpServer.URL)
	ctx := context.Background()

	home, err := client.ListHomeSessions(ctx, connect.NewRequest(&v1.ListHomeSessionsRequest{Limit: 2}))
	if err != nil {
		t.Fatal(err)
	}
	projects, err := client.ListProjects(ctx, connect.NewRequest(&v1.ListProjectsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range projects.Msg.Projects {
		names = append(names, p.Name)
	}
	var homeNames, pageNames []string
	for _, p := range home.Msg.Projects {
		homeNames = append(homeNames, p.Name)
	}
	for _, p := range home.Msg.Pages {
		pageNames = append(pageNames, p.Project)
	}
	if !slices.Equal(homeNames, names) || !slices.Equal(pageNames, names) || len(names) != 2 {
		t.Fatalf("projects %v, home projects %v, pages %v", names, homeNames, pageNames)
	}
	for _, page := range home.Msg.Pages {
		if page.Error != "" {
			t.Fatalf("%s: unexpected error %q", page.Project, page.Error)
		}
		want, err := client.ListSessionHistory(ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{Project: page.Project, Limit: 2}))
		if err != nil {
			t.Fatal(err)
		}
		ids := func(rows []*v1.SessionSummary) []string {
			var out []string
			for _, r := range rows {
				out = append(out, r.SessionId)
			}
			return out
		}
		if !slices.Equal(ids(page.Sessions), ids(want.Msg.Sessions)) || page.NextCursor != want.Msg.NextCursor ||
			!slices.Equal(ids(page.Pinned), ids(want.Msg.Pinned)) {
			t.Fatalf("%s: home page %v/%v/%q, history %v/%v/%q", page.Project,
				ids(page.Sessions), ids(page.Pinned), page.NextCursor,
				ids(want.Msg.Sessions), ids(want.Msg.Pinned), want.Msg.NextCursor)
		}
		switch page.Project {
		case "beta":
			if !slices.Equal(page.LoopSessionIds, []string{"s_loop"}) {
				t.Fatalf("beta loop ids %v", page.LoopSessionIds)
			}
		default:
			if len(page.LoopSessionIds) != 0 || page.NextCursor == "" {
				t.Fatalf("%s: loop ids %v cursor %q", page.Project, page.LoopSessionIds, page.NextCursor)
			}
		}
	}

	// The cursor continues with ListSessionHistory.
	for _, page := range home.Msg.Pages {
		if page.NextCursor == "" {
			continue
		}
		next, err := client.ListSessionHistory(ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{
			Project: page.Project, Limit: 2, Cursor: page.NextCursor}))
		if err != nil || len(next.Msg.Sessions) != 1 {
			t.Fatalf("%s next page: %+v %v", page.Project, next, err)
		}
	}
}

// No registered project is an empty home, not an error.
func TestListHomeSessionsWithoutProjects(t *testing.T) {
	mgr := session.NewManager(config.NewRegistry(nil), "")
	defer mgr.ReclaimAll()
	srv := New(mgr)
	resp, err := srv.ListHomeSessions(context.Background(), connect.NewRequest(&v1.ListHomeSessionsRequest{}))
	if err != nil || len(resp.Msg.Projects) != 0 || len(resp.Msg.Pages) != 0 {
		t.Fatalf("empty home: %+v %v", resp, err)
	}
}
