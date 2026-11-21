package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/workstream"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func writeSearchLog(t *testing.T, root, id, text string) {
	t.Helper()
	absRoot, _ := filepath.Abs(root)
	lg, err := event.OpenLog(filepath.Join(root, ".ycc", "sessions", id, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lg.Record("coordinator", event.SessionStarted, map[string]any{"mode": "chat", "workspace": absRoot})
	lg.Record("user", event.UserInput, map[string]any{"text": text})
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
}

// searchSettled queries until the refresh the request started has finished.
func searchSettled(t *testing.T, srv *Server, project, query string) *v1.SearchSessionsResponse {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := srv.SearchSessions(context.Background(), connect.NewRequest(&v1.SearchSessionsRequest{Project: project, Query: query}))
		if err != nil {
			t.Fatalf("SearchSessions(%q): %v", query, err)
		}
		if !resp.Msg.Indexing {
			return resp.Msg
		}
		if time.Now().After(deadline) {
			t.Fatalf("SearchSessions(%q) still indexing", query)
		}
	}
}

// Deleting the database while the daemon runs rebuilds it from the logs.
func TestSearchSessionsRebuildsDeletedDatabase(t *testing.T) {
	srv, workspace := newImageTestServer(t)
	writeSearchLog(t, workspace, "s_old", "pineapple")
	if hits := searchSettled(t, srv, "", "pineapple").Hits; len(hits) != 1 {
		t.Fatalf("before deletion: %+v", hits)
	}
	if err := os.Remove(filepath.Join(workspace, ".ycc", "session-search.sqlite")); err != nil {
		t.Fatal(err)
	}
	writeSearchLog(t, workspace, "s_new", "passionfruit")
	got := searchSettled(t, srv, "", "pineapple passionfruit")
	if len(got.Hits) != 0 || got.IndexedSessions != 2 || got.TotalSessions != 2 {
		t.Fatalf("after deletion: %+v", got)
	}
	for _, q := range []string{"pineapple", "passionfruit"} {
		if hits := searchSettled(t, srv, "", q).Hits; len(hits) != 1 {
			t.Fatalf("%q after deletion: %+v", q, hits)
		}
	}
}

// A live session in another project never stands in for this project's log
// of the same id, even when a workstream of this project records that id.
func TestSearchSessionsStaysInProject(t *testing.T) {
	srv, a := newImageTestServer(t)
	defer srv.mgr.ReclaimAll()
	ctx := context.Background()
	other, err := srv.mgr.AddProject(t.TempDir(), "other")
	if err != nil {
		t.Fatal(err)
	}
	workstreams := workstream.NewMemory()
	srv.mgr.SetWorkstreams(workstreams, t.TempDir())
	started, err := srv.StartSession(ctx, connect.NewRequest(&v1.StartSessionRequest{Workspace: a, Mode: "chat", Prompt: "appleorigin"}))
	if err != nil {
		t.Fatal(err)
	}
	id := started.Msg.SessionId
	defer srv.StopSession(ctx, connect.NewRequest(&v1.StopSessionRequest{SessionId: id})) //nolint:errcheck
	writeSearchLog(t, other.Path, id, "bananacopy")
	if err := workstreams.Add(workstream.Workstream{ID: "ws_merged", Status: workstream.StatusMerged, Project: other.Name, SessionID: id}); err != nil {
		t.Fatal(err)
	}
	if hits := searchSettled(t, srv, other.Name, "appleorigin").Hits; len(hits) != 0 {
		t.Fatalf("other project's live input matched: %+v", hits)
	}
	hits := searchSettled(t, srv, other.Name, "bananacopy").Hits
	if len(hits) != 1 || hits[0].Session.GetSessionId() != id {
		t.Fatalf("this project's own log of the id: %+v", hits)
	}
	// Opening the hit in its project serves the matched row.
	view, err := srv.GetSessionView(ctx, connect.NewRequest(&v1.GetSessionViewRequest{Project: other.Name, SessionId: id}))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range view.Msg.Rows {
		if row.Id == hits[0].RowId {
			if data := row.Events[0].GetDataJson(); !strings.Contains(data, "bananacopy") {
				t.Fatalf("hit row %s opened as %s", row.Id, data)
			}
			return
		}
	}
	t.Fatalf("GetSessionView did not serve hit row %s: %+v", hits[0].RowId, view.Msg.Rows)
}

// SearchSessions backfills sessions nobody has opened and follows a live
// session's appends.
func TestSearchSessionsBackfillsAndFollowsLiveSessions(t *testing.T) {
	srv, workspace := newImageTestServer(t)
	defer srv.mgr.ReclaimAll()
	ctx := context.Background()
	absWS, _ := filepath.Abs(workspace)
	lg, err := event.OpenLog(filepath.Join(workspace, ".ycc", "sessions", "s_old", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lg.Record("coordinator", event.SessionStarted, map[string]any{"mode": "chat", "workspace": absWS})
	lg.Record("user", event.UserInput, map[string]any{"text": "where does the walrus live"})
	lg.Record("coordinator", event.ToolResult, map[string]any{"id": "c1", "result": "walrus.txt"})
	lg.Record("coordinator", event.ModelTurn, map[string]any{"text": "In the arctic."})
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}

	search := func(query string) *v1.SearchSessionsResponse { return searchSettled(t, srv, "", query) }
	got := search("walrus")
	if len(got.Hits) != 1 {
		t.Fatalf("hits = %+v, want one", got.Hits)
	}
	hit := got.Hits[0]
	if hit.Session.GetSessionId() != "s_old" || hit.Session.GetTitle() != "where does the walrus live" || hit.RowId != "seq-2" || hit.Kind != "user" || hit.MatchCount != 1 {
		t.Fatalf("hit = %+v", hit)
	}
	if got.TotalSessions != 1 || got.IndexedSessions != 1 {
		t.Fatalf("progress %d/%d, want 1/1", got.IndexedSessions, got.TotalSessions)
	}

	started, err := srv.StartSession(ctx, connect.NewRequest(&v1.StartSessionRequest{Workspace: workspace, Mode: "chat"}))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	id := started.Msg.SessionId
	defer srv.StopSession(ctx, connect.NewRequest(&v1.StopSessionRequest{SessionId: id})) //nolint:errcheck
	if _, err := srv.SendInput(ctx, connect.NewRequest(&v1.SendInputRequest{SessionId: id, Text: "zebra crossing"})); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		hits := search("zebra").Hits
		if len(hits) == 1 && hits[0].Session.GetSessionId() == id {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live input never matched: %+v", hits)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
