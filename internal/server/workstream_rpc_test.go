package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/server"
	"github.com/whyrusleeping/ycc/internal/session"
	"github.com/whyrusleeping/ycc/internal/workstream"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// newWorkstreamServer stands up a real Connect handler over an h2c httptest
// server backed by a manager with a registered temp git project (design §8). It
// returns a client, the manager, the project's primary tree path, and the server
// (for the JSON-path test that talks raw HTTP to srv.URL).
func newWorkstreamServer(t *testing.T) (yccv1connect.SessionServiceClient, *session.Manager, string, *httptest.Server) {
	t.Helper()
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	mgr := session.NewManager(reg, t.TempDir())
	mgr.SetProjects(project.NewMemory())

	proj := t.TempDir()
	if _, err := git.Open(proj); err != nil { // auto-inits with an initial commit
		t.Fatalf("git.Open: %v", err)
	}
	if _, err := mgr.AddProject(proj, "demo"); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	mgr.SetWorkstreams(workstream.NewMemory(), filepath.Join(t.TempDir(), "worktrees"))

	path, handler := yccv1connect.NewSessionServiceHandler(server.New(mgr))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)

	httpClient := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	client := yccv1connect.NewSessionServiceClient(httpClient, srv.URL)
	return client, mgr, proj, srv
}

// commitInto opens the git tree at dir, writes name=content, and commits it,
// returning the new short sha (mirrors the session package test idiom).
func commitInto(t *testing.T, dir, name, content, msg string) string {
	t.Helper()
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatalf("git.Open(%s): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	sha, err := repo.Commit(msg)
	if err != nil {
		t.Fatalf("commit %s: %v", name, err)
	}
	return sha
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatalf("git.Open(%s): %v", dir, err)
	}
	sha, err := repo.RevParse("HEAD")
	if err != nil {
		t.Fatalf("RevParse HEAD: %v", err)
	}
	return sha
}

// awaitEvent subscribes to a session's stream and reads until it sees an event of
// the given type, returning true on success or false on timeout / stream end.
func awaitEvent(t *testing.T, client yccv1connect.SessionServiceClient, sessionID string, typ event.Type) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Subscribe(ctx, connect.NewRequest(&v1.SubscribeRequest{SessionId: sessionID, FromSeq: 0}))
	if err != nil {
		t.Fatalf("Subscribe(%s): %v", sessionID, err)
	}
	defer stream.Close()
	for stream.Receive() {
		if stream.Msg().GetType() == string(typ) {
			return true
		}
	}
	return false
}

// TestWorkstreamRPCEndToEnd is the scripted-client scenario over real HTTP: spawn
// two workstreams, observe both via Subscribe, then merge both back — one clean,
// one conflicting — with the conflict surfaced (design §6, §8; task 0084 AC).
func TestWorkstreamRPCEndToEnd(t *testing.T) {
	client, mgr, proj, _ := newWorkstreamServer(t)
	ctx := context.Background()

	// Spawn two autonomous workstreams so a clean merge integrates without a
	// separate accept round-trip.
	spawn := func() *v1.WorkstreamInfo {
		resp, err := client.SpawnWorkstream(ctx, connect.NewRequest(&v1.SpawnWorkstreamRequest{
			Project: "demo", InteractionLevel: "autonomous",
		}))
		if err != nil {
			t.Fatalf("SpawnWorkstream: %v", err)
		}
		return resp.Msg.GetWorkstream()
	}
	ws1 := spawn()
	ws2 := spawn()
	if ws1.GetSessionId() == "" || ws2.GetSessionId() == "" {
		t.Fatalf("spawned workstreams missing session ids: %+v %+v", ws1, ws2)
	}
	defer mgr.Stop(ws1.GetSessionId())
	defer mgr.Stop(ws2.GetSessionId())

	// ListWorkstreams reports both, active.
	list, err := client.ListWorkstreams(ctx, connect.NewRequest(&v1.ListWorkstreamsRequest{Project: "demo"}))
	if err != nil {
		t.Fatalf("ListWorkstreams: %v", err)
	}
	if len(list.Msg.GetWorkstreams()) != 2 {
		t.Fatalf("ListWorkstreams = %d, want 2", len(list.Msg.GetWorkstreams()))
	}
	for _, w := range list.Msg.GetWorkstreams() {
		if w.GetStatus() != string(workstream.StatusActive) {
			t.Fatalf("workstream %s status = %s, want active", w.GetId(), w.GetStatus())
		}
	}

	// Both workstreams stream their creation event via the reused Subscribe RPC.
	if !awaitEvent(t, client, ws1.GetSessionId(), event.WorkstreamCreated) {
		t.Fatal("ws1: never observed workstream_created via Subscribe")
	}
	if !awaitEvent(t, client, ws2.GetSessionId(), event.WorkstreamCreated) {
		t.Fatal("ws2: never observed workstream_created via Subscribe")
	}

	// ws1 makes a clean change (a unique new file). ws2 conflicts with base on a
	// shared file.
	commitInto(t, ws1.GetWorktreePath(), "feature1.txt", "hello from ws1\n", "ws1 feature")
	commitInto(t, ws2.GetWorktreePath(), "shared.txt", "ws2 content\n", "ws2 shared")
	commitInto(t, proj, "shared.txt", "base content\n", "base shared")

	baseBeforePreview := headOf(t, proj)

	// ListWorkstreams enriches non-terminal rows: ws1 now has one commit on its
	// branch and a live session status (design §8).
	listEnriched, err := client.ListWorkstreams(ctx, connect.NewRequest(&v1.ListWorkstreamsRequest{Project: "demo"}))
	if err != nil {
		t.Fatalf("ListWorkstreams (enriched): %v", err)
	}
	for _, w := range listEnriched.Msg.GetWorkstreams() {
		if w.GetId() == ws1.GetId() {
			if w.GetCommitCount() != 1 {
				t.Fatalf("ws1 commit_count = %d, want 1", w.GetCommitCount())
			}
			if w.GetSessionStatus() == "" {
				t.Fatal("ws1 session_status empty, want a live status")
			}
		}
	}

	// PreviewMerge ws1: clean with a non-empty diff.
	pv1, err := client.PreviewMerge(ctx, connect.NewRequest(&v1.PreviewMergeRequest{WorkstreamId: ws1.GetId()}))
	if err != nil {
		t.Fatalf("PreviewMerge ws1: %v", err)
	}
	if !pv1.Msg.GetClean() || pv1.Msg.GetDiff() == "" {
		t.Fatalf("PreviewMerge ws1 = %+v, want clean with diff", pv1.Msg)
	}

	// PreviewMerge ws2: conflicted, listing the shared path.
	pv2, err := client.PreviewMerge(ctx, connect.NewRequest(&v1.PreviewMergeRequest{WorkstreamId: ws2.GetId()}))
	if err != nil {
		t.Fatalf("PreviewMerge ws2: %v", err)
	}
	if pv2.Msg.GetClean() {
		t.Fatalf("PreviewMerge ws2 reported clean, want conflict")
	}
	if !containsStr(pv2.Msg.GetConflicts(), "shared.txt") {
		t.Fatalf("PreviewMerge ws2 conflicts = %v, want shared.txt", pv2.Msg.GetConflicts())
	}
	// Preview mutated nothing: base HEAD unchanged.
	if got := headOf(t, proj); got != baseBeforePreview {
		t.Fatalf("base HEAD moved during preview: %s -> %s", baseBeforePreview, got)
	}

	// MergeWorkstream ws1: clean autonomous merge integrates with a commit sha.
	mg1, err := client.MergeWorkstream(ctx, connect.NewRequest(&v1.MergeWorkstreamRequest{WorkstreamId: ws1.GetId(), Accept: true}))
	if err != nil {
		t.Fatalf("MergeWorkstream ws1: %v", err)
	}
	if !mg1.Msg.GetMerged() || mg1.Msg.GetCommit() == "" {
		t.Fatalf("MergeWorkstream ws1 = %+v, want merged with commit", mg1.Msg)
	}
	if _, err := os.Stat(filepath.Join(proj, "feature1.txt")); err != nil {
		t.Fatalf("ws1 change not integrated into base tree: %v", err)
	}
	baseAfterWs1 := headOf(t, proj)
	if baseAfterWs1 == baseBeforePreview {
		t.Fatal("base HEAD did not advance after ws1 merge")
	}

	// MergeWorkstream ws2: conflict surfaced, base untouched, worktree kept.
	mg2, err := client.MergeWorkstream(ctx, connect.NewRequest(&v1.MergeWorkstreamRequest{WorkstreamId: ws2.GetId(), Accept: true}))
	if err != nil {
		t.Fatalf("MergeWorkstream ws2: %v", err)
	}
	if mg2.Msg.GetMerged() {
		t.Fatalf("MergeWorkstream ws2 reported merged, want conflict")
	}
	if !containsStr(mg2.Msg.GetConflicts(), "shared.txt") {
		t.Fatalf("MergeWorkstream ws2 conflicts = %v, want shared.txt", mg2.Msg.GetConflicts())
	}
	if got := headOf(t, proj); got != baseAfterWs1 {
		t.Fatalf("base HEAD moved during conflicting ws2 merge: %s -> %s", baseAfterWs1, got)
	}
	if _, err := os.Stat(ws2.GetWorktreePath()); err != nil {
		t.Fatalf("ws2 worktree removed after conflict: %v", err)
	}

	// Registry: ws1 merged, ws2 still active.
	list2, err := client.ListWorkstreams(ctx, connect.NewRequest(&v1.ListWorkstreamsRequest{Project: "demo"}))
	if err != nil {
		t.Fatalf("ListWorkstreams after merges: %v", err)
	}
	statuses := map[string]string{}
	for _, w := range list2.Msg.GetWorkstreams() {
		statuses[w.GetId()] = w.GetStatus()
	}
	if statuses[ws1.GetId()] != string(workstream.StatusMerged) {
		t.Fatalf("ws1 status = %s, want merged", statuses[ws1.GetId()])
	}
	if statuses[ws2.GetId()] != string(workstream.StatusActive) {
		t.Fatalf("ws2 status = %s, want active", statuses[ws2.GetId()])
	}

	// DiscardWorkstream ws2 cleans it up.
	if _, err := client.DiscardWorkstream(ctx, connect.NewRequest(&v1.DiscardWorkstreamRequest{WorkstreamId: ws2.GetId()})); err != nil {
		t.Fatalf("DiscardWorkstream ws2: %v", err)
	}
	list3, _ := client.ListWorkstreams(ctx, connect.NewRequest(&v1.ListWorkstreamsRequest{Project: "demo"}))
	for _, w := range list3.Msg.GetWorkstreams() {
		if w.GetId() == ws2.GetId() && w.GetStatus() != string(workstream.StatusDiscarded) {
			t.Fatalf("ws2 status after discard = %s, want discarded", w.GetStatus())
		}
	}
	if _, err := os.Stat(ws2.GetWorktreePath()); !os.IsNotExist(err) {
		t.Fatalf("ws2 worktree still present after discard: %v", err)
	}
}

// TestWorkstreamJSONPath proves the Connect JSON codec works for the new RPCs by
// POSTing application/json directly to the httptest server (task 0084 AC).
func TestWorkstreamJSONPath(t *testing.T) {
	client, mgr, _, srv := newWorkstreamServer(t)
	ctx := context.Background()

	resp, err := client.SpawnWorkstream(ctx, connect.NewRequest(&v1.SpawnWorkstreamRequest{Project: "demo", InteractionLevel: "autonomous"}))
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	defer mgr.Stop(resp.Msg.GetWorkstream().GetSessionId())

	body := bytes.NewBufferString(`{"project":"demo"}`)
	httpResp, err := http.Post(srv.URL+"/ycc.v1.SessionService/ListWorkstreams", "application/json", body)
	if err != nil {
		t.Fatalf("POST ListWorkstreams: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", httpResp.StatusCode)
	}
	var decoded struct {
		Workstreams []struct {
			Id      string `json:"id"`
			Project string `json:"project"`
			Status  string `json:"status"`
		} `json:"workstreams"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
	if len(decoded.Workstreams) != 1 || decoded.Workstreams[0].Project != "demo" {
		t.Fatalf("JSON response = %+v, want one demo workstream", decoded.Workstreams)
	}
}

// TestWorkstreamRPCErrorMapping covers the error-code mapping: unknown workstream
// ids are NotFound; a missing project on Spawn is InvalidArgument.
func TestWorkstreamRPCErrorMapping(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	mgr := session.NewManager(reg, t.TempDir())
	mgr.SetProjects(project.NewMemory())
	mgr.SetWorkstreams(workstream.NewMemory(), filepath.Join(t.TempDir(), "worktrees"))
	srv := server.New(mgr)
	ctx := context.Background()

	if _, err := srv.PreviewMerge(ctx, connect.NewRequest(&v1.PreviewMergeRequest{WorkstreamId: "ws_nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("PreviewMerge unknown id code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.MergeWorkstream(ctx, connect.NewRequest(&v1.MergeWorkstreamRequest{WorkstreamId: "ws_nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("MergeWorkstream unknown id code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.DiscardWorkstream(ctx, connect.NewRequest(&v1.DiscardWorkstreamRequest{WorkstreamId: "ws_nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("DiscardWorkstream unknown id code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.SpawnWorkstream(ctx, connect.NewRequest(&v1.SpawnWorkstreamRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("SpawnWorkstream empty project code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	// A missing workstream_id is likewise InvalidArgument.
	if _, err := srv.PreviewMerge(ctx, connect.NewRequest(&v1.PreviewMergeRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("PreviewMerge empty id code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
