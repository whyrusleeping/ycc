package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/event"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeClient is an in-memory SessionServiceClient for driving the model-backends
// modal. Embedding the generated interface means unimplemented methods compile
// (and panic if accidentally called), while the four model RPCs are backed by a
// map. RemoveModel rejects names in `referenced` to exercise inline validation.
type fakeClient struct {
	yccv1connect.SessionServiceClient
	models     map[string]*v1.ModelConfig
	order      []string
	referenced map[string]bool

	lastUpsert  *v1.ModelConfig
	upserts     []*v1.ModelConfig // every UpsertModel call, in order
	lastPersist bool
	lastRemove  string
	lastStopped string
	stopCount   int

	workLoop       *v1.WorkLoopInfo
	startLoopErr   error
	startLoopCount int
	stopLoopCount  int
	getLoopCount   int

	discoverIDs  []string // returned by DiscoverModels
	discoverNote string
	lastDiscover *v1.DiscoverModelsRequest

	lastRoleReq *v1.SetRoleConfigRequest // most recent SetRoleConfig call

	// previous-sessions screen (spec §18.6)
	history      []*v1.SessionSummary
	lastReopened string
	transcript   []*v1.Event // returned by GetSessionTranscript
	lastTransID  string

	// commit-diff drill-in (task 0140): canned diff returned by GetCommitDiff and
	// the last sha requested.
	commitDiff      string
	commitDiffTrunc bool
	commitDiffErr   error
	lastCommitSha   string

	// cost view (spec §20.5, task 0039)
	usageRows     []*v1.UsageRow
	usageTotal    *v1.UsageRow
	usageWksp     string
	lastGroupBy   []string
	lastUsageTask string

	// plan library browser (task 0077)
	plans []*v1.PlanSummary

	// batch digest (task 0098): canned per-task detail returned by GetTask so the
	// digest can surface a blocked task's reason, plus the last id requested.
	taskDetails map[string]*v1.TaskDetail
	lastGetTask string
	// backlog grooming (task 0099): records the last UpdateTask request so keypress
	// tests can assert the grooming RPC fired with the expected mutation, plus the
	// canned list a post-update ListBacklog refresh returns.
	lastUpdateTask *v1.UpdateTaskRequest
	backlogList    []*v1.BacklogTaskSummary

	// workstreams panel (task 0085): canned list returned by ListWorkstreams, the
	// SpawnWorkstream requests recorded in order, canned PreviewMerge/MergeWorkstream
	// responses, and the last discard id.
	workstreams   []*v1.WorkstreamInfo
	spawnReqs     []*v1.SpawnWorkstreamRequest
	previewResp   *v1.PreviewMergeResponse
	mergeResp     *v1.MergeWorkstreamResponse
	lastPreviewID string
	lastMergeID   string
	lastDiscardID string
}

func newFakeClient(cfgs ...*v1.ModelConfig) *fakeClient {
	f := &fakeClient{models: map[string]*v1.ModelConfig{}, referenced: map[string]bool{}}
	for _, c := range cfgs {
		f.models[c.Name] = c
		f.order = append(f.order, c.Name)
	}
	return f
}

func (f *fakeClient) ListModels(_ context.Context, _ *connect.Request[v1.ListModelsRequest]) (*connect.Response[v1.ListModelsResponse], error) {
	var out []*v1.ModelInfo
	for _, name := range f.order {
		c := f.models[name]
		out = append(out, &v1.ModelInfo{Name: c.Name, Backend: c.Backend, Model: c.Model})
	}
	return connect.NewResponse(&v1.ListModelsResponse{Models: out}), nil
}

// SetRoleConfig records the most recent role-config request so tests can assert
// that cycling a role picker issues it immediately.
func (f *fakeClient) SetRoleConfig(_ context.Context, req *connect.Request[v1.SetRoleConfigRequest]) (*connect.Response[v1.SetRoleConfigResponse], error) {
	f.lastRoleReq = req.Msg
	return connect.NewResponse(&v1.SetRoleConfigResponse{}), nil
}

// StopSession records the stopped session id; the loop-idle test exercises it
// without dialing a real daemon.
func (f *fakeClient) StopSession(_ context.Context, req *connect.Request[v1.StopSessionRequest]) (*connect.Response[v1.StopSessionResponse], error) {
	f.lastStopped = req.Msg.SessionId
	f.stopCount++
	return connect.NewResponse(&v1.StopSessionResponse{}), nil
}

func (f *fakeClient) StartSession(_ context.Context, _ *connect.Request[v1.StartSessionRequest]) (*connect.Response[v1.StartSessionResponse], error) {
	return connect.NewResponse(&v1.StartSessionResponse{SessionId: "s-new"}), nil
}

func (f *fakeClient) StartWorkLoop(_ context.Context, _ *connect.Request[v1.StartWorkLoopRequest]) (*connect.Response[v1.StartWorkLoopResponse], error) {
	f.startLoopCount++
	if f.startLoopErr != nil {
		return nil, f.startLoopErr
	}
	if f.workLoop == nil {
		f.workLoop = &v1.WorkLoopInfo{State: "running"}
	}
	return connect.NewResponse(&v1.StartWorkLoopResponse{Loop: f.workLoop}), nil
}

func (f *fakeClient) StopWorkLoop(_ context.Context, _ *connect.Request[v1.StopWorkLoopRequest]) (*connect.Response[v1.StopWorkLoopResponse], error) {
	f.stopLoopCount++
	if f.workLoop != nil {
		f.workLoop.State = "stopping"
	}
	return connect.NewResponse(&v1.StopWorkLoopResponse{Loop: f.workLoop}), nil
}

func (f *fakeClient) GetWorkLoop(_ context.Context, _ *connect.Request[v1.GetWorkLoopRequest]) (*connect.Response[v1.GetWorkLoopResponse], error) {
	f.getLoopCount++
	return connect.NewResponse(&v1.GetWorkLoopResponse{Loop: f.workLoop}), nil
}

func (f *fakeClient) GetModelConfig(_ context.Context, req *connect.Request[v1.GetModelConfigRequest]) (*connect.Response[v1.GetModelConfigResponse], error) {
	c, ok := f.models[req.Msg.Name]
	if !ok {
		return nil, fmt.Errorf("no such model %q", req.Msg.Name)
	}
	return connect.NewResponse(&v1.GetModelConfigResponse{Model: c}), nil
}

func (f *fakeClient) UpsertModel(_ context.Context, req *connect.Request[v1.UpsertModelRequest]) (*connect.Response[v1.UpsertModelResponse], error) {
	c := req.Msg.Model
	f.lastUpsert = c
	f.upserts = append(f.upserts, c)
	f.lastPersist = req.Msg.Persist
	if _, ok := f.models[c.Name]; !ok {
		f.order = append(f.order, c.Name)
	}
	f.models[c.Name] = c
	return connect.NewResponse(&v1.UpsertModelResponse{}), nil
}

func (f *fakeClient) DiscoverModels(_ context.Context, req *connect.Request[v1.DiscoverModelsRequest]) (*connect.Response[v1.DiscoverModelsResponse], error) {
	f.lastDiscover = req.Msg
	return connect.NewResponse(&v1.DiscoverModelsResponse{
		ModelIds: f.discoverIDs, Note: f.discoverNote, FromNetwork: len(f.discoverIDs) > 0,
	}), nil
}

func (f *fakeClient) RemoveModel(_ context.Context, req *connect.Request[v1.RemoveModelRequest]) (*connect.Response[v1.RemoveModelResponse], error) {
	name := req.Msg.Name
	f.lastRemove = name
	if f.referenced[name] {
		return nil, fmt.Errorf("model %s is referenced by role coordinator", name)
	}
	if _, ok := f.models[name]; !ok {
		return nil, fmt.Errorf("no such model %q", name)
	}
	delete(f.models, name)
	out := f.order[:0]
	for _, n := range f.order {
		if n != name {
			out = append(out, n)
		}
	}
	f.order = out
	return connect.NewResponse(&v1.RemoveModelResponse{}), nil
}

// ListSessionHistory and ResumeSession back the previous-sessions screen (spec
// §18.6). Subscribe returns an error so the post-reopen subscribe cmd resolves to
// an errMsg instead of panicking on the embedded nil interface.
func (f *fakeClient) ListSessionHistory(_ context.Context, _ *connect.Request[v1.ListSessionHistoryRequest]) (*connect.Response[v1.ListSessionHistoryResponse], error) {
	return connect.NewResponse(&v1.ListSessionHistoryResponse{Sessions: f.history}), nil
}

func (f *fakeClient) ResumeSession(_ context.Context, req *connect.Request[v1.ResumeSessionRequest]) (*connect.Response[v1.ResumeSessionResponse], error) {
	f.lastReopened = req.Msg.SessionId
	mode := "work"
	for _, s := range f.history {
		if s.SessionId == req.Msg.SessionId {
			mode = s.Mode
		}
	}
	return connect.NewResponse(&v1.ResumeSessionResponse{SessionId: req.Msg.SessionId, Mode: mode, Status: "idle"}), nil
}

func (f *fakeClient) Subscribe(_ context.Context, _ *connect.Request[v1.SubscribeRequest]) (*connect.ServerStreamForClient[v1.Event], error) {
	return nil, fmt.Errorf("subscribe not supported in fakeClient")
}

// GetSessionTranscript backs the read-only transcript drill-in (spec §18.6).
func (f *fakeClient) GetSessionTranscript(_ context.Context, req *connect.Request[v1.GetSessionTranscriptRequest]) (*connect.Response[v1.GetSessionTranscriptResponse], error) {
	f.lastTransID = req.Msg.SessionId
	return connect.NewResponse(&v1.GetSessionTranscriptResponse{Events: f.transcript}), nil
}

// GetCommitDiff backs the commit-diff drill-in overlay (task 0140): it records
// the requested sha and returns the canned diff (or a canned error).
func (f *fakeClient) GetCommitDiff(_ context.Context, req *connect.Request[v1.GetCommitDiffRequest]) (*connect.Response[v1.GetCommitDiffResponse], error) {
	f.lastCommitSha = req.Msg.Sha
	if f.commitDiffErr != nil {
		return nil, f.commitDiffErr
	}
	return connect.NewResponse(&v1.GetCommitDiffResponse{Diff: f.commitDiff, Truncated: f.commitDiffTrunc}), nil
}

// ListBacklog backs the backlog browser route from the browse selector; the
// browse tests only need it to not panic, so it returns f.backlogList (empty by
// default; grooming tests set it so a post-update refresh has something to return).
func (f *fakeClient) ListBacklog(_ context.Context, _ *connect.Request[v1.ListBacklogRequest]) (*connect.Response[v1.ListBacklogResponse], error) {
	return connect.NewResponse(&v1.ListBacklogResponse{Tasks: f.backlogList}), nil
}

// GetTask backs the backlog detail drill-in and the batch digest's blocked-reason
// fetch (task 0098). It returns canned detail keyed by id, or an empty detail.
func (f *fakeClient) GetTask(_ context.Context, req *connect.Request[v1.GetTaskRequest]) (*connect.Response[v1.GetTaskResponse], error) {
	f.lastGetTask = req.Msg.Id
	if t, ok := f.taskDetails[req.Msg.Id]; ok {
		return connect.NewResponse(&v1.GetTaskResponse{Task: t}), nil
	}
	return connect.NewResponse(&v1.GetTaskResponse{Task: &v1.TaskDetail{Id: req.Msg.Id}}), nil
}

// UpdateTask backs the backlog grooming keys (task 0099). It records the request
// and returns the (optionally canned) task detail with the mutation applied.
func (f *fakeClient) UpdateTask(_ context.Context, req *connect.Request[v1.UpdateTaskRequest]) (*connect.Response[v1.UpdateTaskResponse], error) {
	f.lastUpdateTask = req.Msg
	t := f.taskDetails[req.Msg.Id]
	if t == nil {
		t = &v1.TaskDetail{Id: req.Msg.Id}
	}
	if req.Msg.Status != nil {
		t.Status = req.Msg.GetStatus()
	}
	if req.Msg.Priority != nil {
		t.Priority = req.Msg.GetPriority()
	}
	if req.Msg.Title != nil {
		t.Title = req.Msg.GetTitle()
	}
	return connect.NewResponse(&v1.UpdateTaskResponse{Task: t}), nil
}

// ListPlans / GetPlan back the plan library browser route (task 0077). They
// return canned data so the browse selector → plans route can be driven.
func (f *fakeClient) ListPlans(_ context.Context, _ *connect.Request[v1.ListPlansRequest]) (*connect.Response[v1.ListPlansResponse], error) {
	return connect.NewResponse(&v1.ListPlansResponse{Plans: f.plans}), nil
}

func (f *fakeClient) GetPlan(_ context.Context, req *connect.Request[v1.GetPlanRequest]) (*connect.Response[v1.GetPlanResponse], error) {
	return connect.NewResponse(&v1.GetPlanResponse{Name: req.Msg.Name, Title: req.Msg.Name, Content: "# " + req.Msg.Name + "\nbody"}), nil
}

func (f *fakeClient) ListWorkstreams(_ context.Context, _ *connect.Request[v1.ListWorkstreamsRequest]) (*connect.Response[v1.ListWorkstreamsResponse], error) {
	return connect.NewResponse(&v1.ListWorkstreamsResponse{Workstreams: f.workstreams}), nil
}

func (f *fakeClient) SpawnWorkstream(_ context.Context, req *connect.Request[v1.SpawnWorkstreamRequest]) (*connect.Response[v1.SpawnWorkstreamResponse], error) {
	f.spawnReqs = append(f.spawnReqs, req.Msg)
	ws := &v1.WorkstreamInfo{
		Id:        fmt.Sprintf("ws_%d", len(f.spawnReqs)),
		Project:   req.Msg.Project,
		TaskId:    req.Msg.TaskId,
		Branch:    "ycc/ws/spawn-" + req.Msg.TaskId,
		SessionId: fmt.Sprintf("s-ws-%d", len(f.spawnReqs)),
		Status:    "active",
	}
	f.workstreams = append(f.workstreams, ws)
	return connect.NewResponse(&v1.SpawnWorkstreamResponse{Workstream: ws}), nil
}

func (f *fakeClient) PreviewMerge(_ context.Context, req *connect.Request[v1.PreviewMergeRequest]) (*connect.Response[v1.PreviewMergeResponse], error) {
	f.lastPreviewID = req.Msg.WorkstreamId
	resp := f.previewResp
	if resp == nil {
		resp = &v1.PreviewMergeResponse{Clean: true, Diff: "diff --git a/x b/x\n+added\n"}
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeClient) MergeWorkstream(_ context.Context, req *connect.Request[v1.MergeWorkstreamRequest]) (*connect.Response[v1.MergeWorkstreamResponse], error) {
	f.lastMergeID = req.Msg.WorkstreamId
	resp := f.mergeResp
	if resp == nil {
		resp = &v1.MergeWorkstreamResponse{Merged: true, Commit: "abc1234"}
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeClient) DiscardWorkstream(_ context.Context, req *connect.Request[v1.DiscardWorkstreamRequest]) (*connect.Response[v1.DiscardWorkstreamResponse], error) {
	f.lastDiscardID = req.Msg.WorkstreamId
	return connect.NewResponse(&v1.DiscardWorkstreamResponse{}), nil
}

// GetUsage backs the cost view route (spec §20.5, tasks 0039/0174). It records
// grouping and task filters, returning a distinct per-agent breakdown when a task
// is selected so drill-down behavior is observable.
func (f *fakeClient) GetUsage(_ context.Context, req *connect.Request[v1.GetUsageRequest]) (*connect.Response[v1.GetUsageResponse], error) {
	f.lastGroupBy = req.Msg.GroupBy
	f.lastUsageTask = req.Msg.Task
	if req.Msg.Task != "" {
		return connect.NewResponse(&v1.GetUsageResponse{
			Rows: []*v1.UsageRow{
				{Task: req.Msg.Task, Agent: "coordinator", Model: "sonnet", Input: 600, Output: 100, Total: 700, Cost: 0.07, PriceStatus: "priced"},
				{Task: req.Msg.Task, Agent: "implementer", Model: "codex", Input: 800, Output: 200, Total: 1000, Cost: 0.1, PriceStatus: "priced"},
				{Task: req.Msg.Task, Agent: "reviewer", Model: "sonnet", Input: 300, Output: 50, Total: 350, Cost: 0.035, PriceStatus: "priced"},
			},
			Total:     &v1.UsageRow{Input: 1700, Output: 350, Total: 2050, Cost: 0.205, PriceStatus: "priced"},
			Workspace: f.usageWksp,
		}), nil
	}
	return connect.NewResponse(&v1.GetUsageResponse{
		Rows:      f.usageRows,
		Total:     f.usageTotal,
		Workspace: f.usageWksp,
	}), nil
}

func (f *fakeClient) GetSubscriptionUsage(_ context.Context, _ *connect.Request[v1.GetSubscriptionUsageRequest]) (*connect.Response[v1.GetSubscriptionUsageResponse], error) {
	return connect.NewResponse(&v1.GetSubscriptionUsageResponse{}), nil
}

// drive feeds a key through Update and, if a command is returned, runs it and
// feeds the resulting message back through Update (recursing until no command).
// It threads the model value through, mirroring the Bubble Tea runtime.
func drive(t *testing.T, m model, key string) model {
	t.Helper()
	updated, cmd := m.Update(keyMsg(key))
	m = updated.(model)
	return runCmds(t, m, cmd)
}

// keyMsg builds a v2 KeyPressMsg from a key name ("enter", "ctrl+n", …) or, for
// anything else, a run of printable runes to type.
func keyMsg(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "ctrl+n":
		return tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+p":
		return tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
	case "ctrl+r":
		return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	case "ctrl+o":
		return tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
	case "ctrl+h":
		return tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl}
	case "ctrl+_":
		return tea.KeyPressMsg{Code: '_', Mod: tea.ModCtrl}
	default:
		if r, ok := strings.CutPrefix(key, "ctrl+"); ok && len([]rune(r)) == 1 {
			return tea.KeyPressMsg{Code: []rune(r)[0], Mod: tea.ModCtrl}
		}
		return tea.KeyPressMsg{Code: []rune(key)[0], Text: key}
	}
}

// runCmds executes a command (and any follow-ups it yields) by feeding each
// returned message back through Update.
func runCmds(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return m
		}
		updated, next := m.Update(msg)
		m = updated.(model)
		cmd = next
	}
	return m
}

// typeText sends each rune of s through Update so the focused text input edits.
// Text editing returns a cursor-blink command that would block if executed
// synchronously, so the returned cmds are intentionally ignored here.
func typeText(t *testing.T, m model, s string) model {
	t.Helper()
	for _, r := range s {
		km := tea.KeyPressMsg{Code: r, Text: string(r)}
		updated, _ := m.Update(km)
		m = updated.(model)
	}
	return m
}

func newBackendsModel(f *fakeClient) model {
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.mbOpen = true
	m.mbView = 0
	return m
}

// t_tempWorkspace is an empty path; the modal tests don't touch the filesystem.
const t_tempWorkspace = ""

// overlayToReviewers opens the settings overlay on a client with the given
// models and moves the cursor to the reviewers row.
func overlayToReviewers(t *testing.T, extra ...*v1.ModelConfig) model {
	t.Helper()
	cfgs := []*v1.ModelConfig{
		{Name: "claude", Backend: "anthropic", Model: "claude-x"},
		{Name: "fable", Backend: "anthropic", Model: "claude-fable-5"},
		{Name: "gpt", Backend: "openai", Model: "gpt-5"},
	}
	cfgs = append(cfgs, extra...)
	f := newFakeClient(cfgs...)
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = runCmds(t, m, m.fetchModels)
	m.openOverlay()
	m = drive(t, m, "down") // coord -> impl
	m = drive(t, m, "down") // impl -> reviewers
	if m.ovCursor != ovReviewers {
		t.Fatalf("cursor = %d, want ovReviewers(%d)", m.ovCursor, ovReviewers)
	}
	return m
}

func reviewerNames(m model) []string { return append([]string(nil), m.roleReviewrs...) }

// lastDiscoverBackend is a tiny helper for the fetch test.
func (m model) lastDiscoverBackend(f *fakeClient) string {
	if f.lastDiscover == nil {
		return ""
	}
	return f.lastDiscover.Backend
}

// newCostFakeClient returns a fakeClient with canned usage rows for the cost view
// tests: one priced row, one unpriced row, a total, and a workspace name.
func newCostFakeClient() *fakeClient {
	f := newFakeClient()
	f.usageWksp = "demo-workspace"
	f.usageRows = []*v1.UsageRow{
		{Task: "", Model: "local", Input: 500, Output: 100, Total: 600, PriceStatus: "unpriced"},
		{Task: "0001", Model: "sonnet", Input: 1000, Output: 200, CacheRead: 50, CacheWrite: 10, Total: 1260, Cost: 0.1234, PriceStatus: "priced"},
	}
	f.usageTotal = &v1.UsageRow{Input: 1500, Output: 300, CacheRead: 50, CacheWrite: 10, Total: 1860, Cost: 0.1234, PriceStatus: "partial"}
	return f
}

// newPickerModel builds a session model with a single pending options question so
// the picker footer (m.picking) is active. Returned ready to feed keys/mouse.
func newPickerModel(t *testing.T, f *fakeClient) model {
	t.Helper()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{
		Seq: 1, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"question":"db?","options":["postgres","sqlite","mysql"]}`,
	})
	if !m.picking || m.wizActive {
		t.Fatalf("expected a single-question picker (picking=%v wizActive=%v)", m.picking, m.wizActive)
	}
	return m
}

// turnEvent builds a model_turn proto Event carrying a usage block for model name.
func turnEvent(seq int, name string, u event.Usage) *v1.Event {
	return &v1.Event{
		Seq: int64(seq), Type: "model_turn", Actor: "coordinator",
		DataJson: fmt.Sprintf(
			`{"model_name":%q,"usage":{"input":%d,"output":%d,"cache_read":%d,"cache_write":%d,"total":%d}}`,
			name, u.Input, u.Output, u.CacheRead, u.CacheWrite, u.Total),
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// newSessionTextareaModel builds a sized session model whose input is a real
// textarea, mirroring how the running TUI is constructed.
func newSessionTextareaModel(t *testing.T) model {
	t.Helper()
	m := model{
		state: stateSession, status: "running", mode: "implement",
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		input: newSessionInput(),
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.input.Focus()
	return m
}

// runBatch executes a command and every sub-command a tea.BatchMsg fans out,
// discarding follow-up messages. It exists so tests can observe RPC side effects
// (e.g. StopSession) issued inside a tea.Batch returned by Update.
func runBatch(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runBatch(c)
		}
	}
}

// driveBacklog feeds a key through the backlog browser handler and runs any
// resulting commands (task 0099 grooming keys).
func driveBacklog(t *testing.T, m model, key string) model {
	t.Helper()
	updated, cmd := m.updateBacklog(keyMsg(key))
	m = updated.(model)
	return runCmds(t, m, cmd)
}

func newBacklogModel(f *fakeClient, tasks []*v1.BacklogTaskSummary) model {
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.backlog = true
	m.backlogTasks = tasks
	m.backlogCursor = 0
	f.backlogList = tasks
	return m
}

// isQuit reports whether cmd (or a batch containing it) yields tea.QuitMsg.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); ok {
		return true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if isQuit(c) {
				return true
			}
		}
	}
	return false
}

// press feeds a key through Update and discards any follow-up command (avoiding
// the textarea's repeating blink tick, which drive would loop on).
func press(m model, key string) model {
	updated, _ := m.Update(keyMsg(key))
	return updated.(model)
}

// searchEvsModel builds a ready session model over the given events (input
// focused), with a rebuilt event pipeline.
func searchEvsModel(t *testing.T, evs []*v1.Event) model {
	t.Helper()
	m := model{
		state: stateSession, status: "running", mode: "implement",
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		follow: true, input: newSessionInput(),
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.input.Focus()
	m.evs = evs
	m.rebuild()
	return m
}

// typeSearch feeds each rune of s through Update while the search bar is active.
func typeSearch(m model, s string) model {
	for _, r := range s {
		m = press(m, string(r))
	}
	return m
}
