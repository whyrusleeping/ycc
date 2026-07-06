// Package server implements the daemon's Connect-RPC surface (spec §12): start
// sessions, list them, subscribe to a session's event stream (with replay from
// an offset), and send input. A bearer-token interceptor guards every RPC.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"google.golang.org/protobuf/proto"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/session"
	"github.com/whyrusleeping/ycc/internal/usage"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// Server adapts a session.Manager to the generated SessionServiceHandler.
type Server struct {
	yccv1connect.UnimplementedSessionServiceHandler
	mgr *session.Manager
}

// New returns a Server backed by mgr.
func New(mgr *session.Manager) *Server { return &Server{mgr: mgr} }

// ListModes returns the selectable session modes and opening-prompt presets for
// the home menu.
func (s *Server) ListModes(_ context.Context, _ *connect.Request[v1.ListModesRequest]) (*connect.Response[v1.ListModesResponse], error) {
	var modes []*v1.Mode
	for _, m := range orchestrator.Modes() {
		modes = append(modes, &v1.Mode{Name: m.Name, Title: m.Title, Description: m.Description})
	}
	var presets []*v1.Preset
	for _, p := range orchestrator.Presets() {
		presets = append(presets, &v1.Preset{Name: p.Name, Title: p.Title, Description: p.Description, Mode: p.Mode, OpeningPrompt: p.Prompt})
	}
	return connect.NewResponse(&v1.ListModesResponse{Modes: modes, Presets: presets}), nil
}

// StartSession creates and launches a new session.
func (s *Server) StartSession(_ context.Context, req *connect.Request[v1.StartSessionRequest]) (*connect.Response[v1.StartSessionResponse], error) {
	m := req.Msg
	sess, err := s.mgr.Start(session.Config{
		Workspace:        m.Workspace,
		Mode:             m.Mode,
		InteractionLevel: m.InteractionLevel,
		Prompt:           m.Prompt,
		Project:          m.Project,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.StartSessionResponse{SessionId: sess.ID}), nil
}

// ListProjects returns the registered projects (name + path) for the picker
// (spec §3.1).
func (s *Server) ListProjects(_ context.Context, _ *connect.Request[v1.ListProjectsRequest]) (*connect.Response[v1.ListProjectsResponse], error) {
	var projs []*v1.ProjectInfo
	for _, p := range s.mgr.Projects() {
		projs = append(projs, &v1.ProjectInfo{Name: p.Name, Path: p.Path})
	}
	return connect.NewResponse(&v1.ListProjectsResponse{Projects: projs}), nil
}

// AddProject registers a workspace under an optional name (spec §3.1).
func (s *Server) AddProject(_ context.Context, req *connect.Request[v1.AddProjectRequest]) (*connect.Response[v1.AddProjectResponse], error) {
	if req.Msg.Path == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errNoPath)
	}
	p, err := s.mgr.AddProject(req.Msg.Path, req.Msg.Name)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.AddProjectResponse{Project: &v1.ProjectInfo{Name: p.Name, Path: p.Path}}), nil
}

// RemoveProject deregisters a project by name (spec §3.1).
func (s *Server) RemoveProject(_ context.Context, req *connect.Request[v1.RemoveProjectRequest]) (*connect.Response[v1.RemoveProjectResponse], error) {
	if err := s.mgr.RemoveProject(req.Msg.Name); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.RemoveProjectResponse{}), nil
}

// ListSessions returns all live sessions and their current status, optionally
// filtered to a single project (spec §3.1).
func (s *Server) ListSessions(_ context.Context, req *connect.Request[v1.ListSessionsRequest]) (*connect.Response[v1.ListSessionsResponse], error) {
	var infos []*v1.SessionInfo
	for _, sess := range s.mgr.ListByProject(req.Msg.Project) {
		infos = append(infos, &v1.SessionInfo{
			SessionId: sess.ID,
			Mode:      sess.Mode,
			Status:    string(sess.Status()),
			Workspace: sess.Workspace,
		})
	}
	return connect.NewResponse(&v1.ListSessionsResponse{Sessions: infos}), nil
}

// ListSessionHistory enumerates all sessions for a project (live + persisted
// on-disk logs), most-recent first (spec §18.6). Unlike ListSessions it includes
// sessions that are no longer live in memory.
func (s *Server) ListSessionHistory(_ context.Context, req *connect.Request[v1.ListSessionHistoryRequest]) (*connect.Response[v1.ListSessionHistoryResponse], error) {
	sums, err := s.mgr.ListSessionHistory(req.Msg.Project)
	if err != nil {
		if errors.Is(err, session.ErrUnknownProject) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var out []*v1.SessionSummary
	for _, su := range sums {
		out = append(out, &v1.SessionSummary{
			SessionId:    su.ID,
			Mode:         su.Mode,
			Status:       string(su.Status),
			Workspace:    su.Workspace,
			Title:        su.Title,
			StartedAt:    rfc3339(su.StartedAt),
			LastActivity: rfc3339(su.LastActivity),
			FocusTasks:   su.FocusTasks,
			Turns:        int64(su.Turns),
			ToolCalls:    int64(su.ToolCalls),
			Live:         su.Live,
			WaitingInput: su.Waiting,
		})
	}
	return connect.NewResponse(&v1.ListSessionHistoryResponse{Sessions: out}), nil
}

// GetSessionTranscript returns a session's full event log (live or persisted on
// disk) so the session browser can render a read-only replayed transcript with
// the same event components as the live view (spec §18.6).
func (s *Server) GetSessionTranscript(_ context.Context, req *connect.Request[v1.GetSessionTranscriptRequest]) (*connect.Response[v1.GetSessionTranscriptResponse], error) {
	evs, err := s.mgr.SessionTranscript(req.Msg.Project, req.Msg.SessionId)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrUnknownProject):
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		case errors.Is(err, session.ErrUnknownSession):
			return nil, connect.NewError(connect.CodeNotFound, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	out := make([]*v1.Event, 0, len(evs))
	for _, ev := range evs {
		out = append(out, toProto(ev))
	}
	return connect.NewResponse(&v1.GetSessionTranscriptResponse{Events: out}), nil
}

// GetCommitDiff returns a commit's `git show` diff (stat + patch) so the
// transcript can drill into what an agent committed from a commit_made row (task
// 0140, spec §18.6). The diff is capped at maxCommitDiffBytes (truncated at a
// line boundary) to bound the wire payload and the client render; the client
// renders a truncation notice when truncated is set.
func (s *Server) GetCommitDiff(_ context.Context, req *connect.Request[v1.GetCommitDiffRequest]) (*connect.Response[v1.GetCommitDiffResponse], error) {
	if strings.TrimSpace(req.Msg.Sha) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("sha is required"))
	}
	diff, err := s.mgr.CommitDiff(req.Msg.Project, req.Msg.Sha)
	if err != nil {
		// An unknown project or a bad/unknown sha (git can't resolve it) is a
		// not-found from the caller's perspective, not a server fault.
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	truncated := false
	if len(diff) > maxCommitDiffBytes {
		cut := maxCommitDiffBytes
		// Truncate at a line boundary so the client never renders a half-line.
		if nl := strings.LastIndexByte(diff[:cut], '\n'); nl > 0 {
			cut = nl + 1
		}
		diff = diff[:cut]
		truncated = true
	}
	return connect.NewResponse(&v1.GetCommitDiffResponse{Diff: diff, Truncated: truncated}), nil
}

// maxCommitDiffBytes caps the diff returned by GetCommitDiff (~1 MiB) so a huge
// commit can never blow up the wire payload or the client's render.
const maxCommitDiffBytes = 1 << 20

// rfc3339 formats a timestamp using the same precision as toProto, returning ""
// for a zero time so absent timestamps serialize as empty rather than a sentinel.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02T15:04:05.000Z07:00")
}

// Subscribe streams a session's events, replaying those with seq > from_seq and
// then delivering live ones until the client disconnects.
func (s *Server) Subscribe(ctx context.Context, req *connect.Request[v1.SubscribeRequest], stream *connect.ServerStream[v1.Event]) error {
	sess, ok := s.mgr.Get(req.Msg.SessionId)
	if !ok {
		return connect.NewError(connect.CodeNotFound, errNoSession)
	}
	ch, cancel := sess.Log().Subscribe(int(req.Msg.FromSeq))
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, open := <-ch:
			if !open {
				return nil // log closed
			}
			if err := stream.Send(toProto(ev)); err != nil {
				return err
			}
		}
	}
}

// SendInput delivers user text: it answers a pending question if one is open,
// otherwise queues a follow-up instruction for the session's agent.
func (s *Server) SendInput(_ context.Context, req *connect.Request[v1.SendInputRequest]) (*connect.Response[v1.SendInputResponse], error) {
	sess, ok := s.mgr.Get(req.Msg.SessionId)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoSession)
	}
	if err := sess.SendInput(req.Msg.Text); err != nil {
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	}
	return connect.NewResponse(&v1.SendInputResponse{}), nil
}

// Interrupt requests a graceful pause-to-steer of a running session (spec
// §18.7): it pauses at the next safe checkpoint without aborting a tool.
func (s *Server) Interrupt(_ context.Context, req *connect.Request[v1.InterruptRequest]) (*connect.Response[v1.InterruptResponse], error) {
	sess, ok := s.mgr.Get(req.Msg.SessionId)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoSession)
	}
	if err := sess.Interrupt(); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&v1.InterruptResponse{}), nil
}

// Resume continues a paused session (optionally after SendInput corrections),
// continuing the same loop/conversation (spec §18.7).
func (s *Server) Resume(_ context.Context, req *connect.Request[v1.ResumeRequest]) (*connect.Response[v1.ResumeResponse], error) {
	sess, ok := s.mgr.Get(req.Msg.SessionId)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoSession)
	}
	if err := sess.Resume(); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&v1.ResumeResponse{}), nil
}

// StopSession hard-terminates a running session (spec §12): it cancels the
// agent loop, closes the log, and removes the session from the daemon. Distinct
// from Interrupt's graceful pause (spec §18.7) — there is no resume.
func (s *Server) StopSession(_ context.Context, req *connect.Request[v1.StopSessionRequest]) (*connect.Response[v1.StopSessionResponse], error) {
	if err := s.mgr.Stop(req.Msg.SessionId); err != nil {
		if errors.Is(err, session.ErrUnknownSession) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.StopSessionResponse{}), nil
}

// ResumeSession re-opens a persisted session on its existing event log
// ("resume = replay", spec §4.5/§18.6): the coordinator is re-instantiated with
// history reconstructed from the log and new activity appends to the same
// continuous events.jsonl. Idempotent if the session is already live.
func (s *Server) ResumeSession(_ context.Context, req *connect.Request[v1.ResumeSessionRequest]) (*connect.Response[v1.ResumeSessionResponse], error) {
	sess, err := s.mgr.Reopen(req.Msg.Project, req.Msg.SessionId)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrUnknownProject):
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		case errors.Is(err, session.ErrUnknownSession):
			return nil, connect.NewError(connect.CodeNotFound, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&v1.ResumeSessionResponse{
		SessionId: sess.ID,
		Mode:      sess.Mode,
		Status:    string(sess.Status()),
		Workspace: sess.Workspace,
	}), nil
}

// AnswerQuestion responds to a question the coordinator asked via ask_user.
func (s *Server) AnswerQuestion(_ context.Context, req *connect.Request[v1.AnswerQuestionRequest]) (*connect.Response[v1.AnswerQuestionResponse], error) {
	sess, ok := s.mgr.Get(req.Msg.SessionId)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoSession)
	}
	if err := sess.AnswerOption(int(req.Msg.OptionIndex), req.Msg.Text); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&v1.AnswerQuestionResponse{}), nil
}

// AnswerQuestions responds to a batch of questions the coordinator asked via a
// single ask_user call. Answers are positional: the i-th answer answers the
// i-th question.
func (s *Server) AnswerQuestions(_ context.Context, req *connect.Request[v1.AnswerQuestionsRequest]) (*connect.Response[v1.AnswerQuestionsResponse], error) {
	sess, ok := s.mgr.Get(req.Msg.SessionId)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoSession)
	}
	idxs := make([]int, len(req.Msg.Answers))
	texts := make([]string, len(req.Msg.Answers))
	for i, a := range req.Msg.Answers {
		idxs[i] = int(a.OptionIndex)
		texts[i] = a.Text
	}
	if err := sess.AnswerBatch(idxs, texts); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&v1.AnswerQuestionsResponse{}), nil
}

// ListModels enumerates the configured logical models for the settings overlay
// pickers (spec §13, §18.2).
func (s *Server) ListModels(_ context.Context, _ *connect.Request[v1.ListModelsRequest]) (*connect.Response[v1.ListModelsResponse], error) {
	var models []*v1.ModelInfo
	for _, m := range s.mgr.Models() {
		mi := &v1.ModelInfo{Name: m.Name, Backend: m.Backend, Model: m.Model, Priced: m.Pricing.Configured}
		// Only attach the optional price_* fields when pricing is configured so an
		// unset rate stays nil on the wire (spec §20.4): the TUI must never invent a
		// cost for an unpriced model.
		if m.Pricing.Configured {
			mi.PriceInput = proto.Float64(m.Pricing.Input)
			mi.PriceOutput = proto.Float64(m.Pricing.Output)
			mi.PriceCacheRead = proto.Float64(m.Pricing.CacheRead)
			mi.PriceCacheWrite = proto.Float64(m.Pricing.CacheWrite)
		}
		models = append(models, mi)
	}
	coord, impl, revs := s.mgr.Roles()
	ct, it, rt := s.mgr.ThinkingLevels()
	return connect.NewResponse(&v1.ListModelsResponse{
		Models: models, Coordinator: coord, Implementer: impl, Reviewers: revs,
		CoordinatorThinking: ct, ImplementerThinking: it, ReviewersThinking: rt,
	}), nil
}

// modelConfigToConfig translates a proto ModelConfig into a config.Model. Only
// key_env references move through — never secret values.
func modelConfigToConfig(mc *v1.ModelConfig) config.Model {
	return config.Model{
		Backend:         mc.Backend,
		BaseURL:         mc.BaseUrl,
		Model:           mc.Model,
		KeyEnv:          mc.KeyEnv,
		Thinking:        mc.Thinking,
		Effort:          mc.Effort,
		ThinkingDisplay: mc.ThinkingDisplay,
		PriceInput:      mc.PriceInput,
		PriceOutput:     mc.PriceOutput,
		PriceCacheRead:  mc.PriceCacheRead,
		PriceCacheWrite: mc.PriceCacheWrite,
	}
}

// configToModelConfig translates a config.Model (under logical name) into a
// proto ModelConfig for editing in the settings overlay.
func configToModelConfig(name string, m config.Model) *v1.ModelConfig {
	return &v1.ModelConfig{
		Name:            name,
		Backend:         m.Backend,
		BaseUrl:         m.BaseURL,
		Model:           m.Model,
		KeyEnv:          m.KeyEnv,
		Thinking:        m.Thinking,
		Effort:          m.Effort,
		ThinkingDisplay: m.ThinkingDisplay,
		PriceInput:      m.PriceInput,
		PriceOutput:     m.PriceOutput,
		PriceCacheRead:  m.PriceCacheRead,
		PriceCacheWrite: m.PriceCacheWrite,
	}
}

// UpsertModel adds or replaces a logical model backend at runtime (spec §18.2).
// The change takes effect on the next turn/spawn and is always written back to
// ycc.toml so settings edits survive a restart. The request's persist flag is
// retained for wire compatibility but ignored — persistence is unconditional.
func (s *Server) UpsertModel(_ context.Context, req *connect.Request[v1.UpsertModelRequest]) (*connect.Response[v1.UpsertModelResponse], error) {
	mc := req.Msg.Model
	if mc == nil || mc.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("model name is required"))
	}
	if err := s.mgr.UpsertModel(mc.Name, modelConfigToConfig(mc), true); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&v1.UpsertModelResponse{}), nil
}

// RemoveModel deletes a logical model backend (spec §18.2). It is rejected if a
// role still references it. Like UpsertModel the change is always written back
// to ycc.toml; the request's persist flag is ignored.
func (s *Server) RemoveModel(_ context.Context, req *connect.Request[v1.RemoveModelRequest]) (*connect.Response[v1.RemoveModelResponse], error) {
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("model name is required"))
	}
	if err := s.mgr.RemoveModel(req.Msg.Name, true); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&v1.RemoveModelResponse{}), nil
}

// GetModelConfig returns a model backend's full record for editing (spec §18.2).
func (s *Server) GetModelConfig(_ context.Context, req *connect.Request[v1.GetModelConfigRequest]) (*connect.Response[v1.GetModelConfigResponse], error) {
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("model name is required"))
	}
	m, ok := s.mgr.GetModel(req.Msg.Name)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("unknown model %q", req.Msg.Name))
	}
	return connect.NewResponse(&v1.GetModelConfigResponse{Model: configToModelConfig(req.Msg.Name, m)}), nil
}

// DiscoverModels lists the model ids available from a backend connection (spec
// §13, §18.2) so the connection form can offer them for selection. On a
// discovery failure it degrades to curated defaults rather than erroring, so the
// form is always usable offline / without a key.
func (s *Server) DiscoverModels(ctx context.Context, req *connect.Request[v1.DiscoverModelsRequest]) (*connect.Response[v1.DiscoverModelsResponse], error) {
	backend := req.Msg.Backend
	if backend == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("backend is required"))
	}
	ids, err := s.mgr.DiscoverModels(ctx, backend, req.Msg.BaseUrl, req.Msg.KeyEnv)
	if err != nil || len(ids) == 0 {
		curated := config.CuratedModelIDs(backend)
		note := "using curated defaults"
		if err != nil {
			note = "discovery failed (" + err.Error() + "); using curated defaults"
		} else if len(ids) == 0 {
			note = "backend returned no models; using curated defaults"
		}
		return connect.NewResponse(&v1.DiscoverModelsResponse{
			ModelIds: curated, FromNetwork: false, Note: note,
		}), nil
	}
	return connect.NewResponse(&v1.DiscoverModelsResponse{
		ModelIds: ids, FromNetwork: true, Note: fmt.Sprintf("%d models from %s", len(ids), backend),
	}), nil
}

// SetInteractionLevel changes a session's interaction level mid-flight (spec §11).
func (s *Server) SetInteractionLevel(_ context.Context, req *connect.Request[v1.SetInteractionLevelRequest]) (*connect.Response[v1.SetInteractionLevelResponse], error) {
	sess, ok := s.mgr.Get(req.Msg.SessionId)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoSession)
	}
	if err := sess.SetInteractionLevel(req.Msg.Level); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&v1.SetInteractionLevelResponse{}), nil
}

// SetRoleConfig reassigns per-role logical models (spec §13, §18.2). When
// session_id names a live session the change applies to it immediately and is
// persisted; with an empty/unknown session_id (e.g. changed from the home menu
// before any session exists) it just updates the persisted default in ycc.toml.
func (s *Server) SetRoleConfig(_ context.Context, req *connect.Request[v1.SetRoleConfigRequest]) (*connect.Response[v1.SetRoleConfigResponse], error) {
	if sess, ok := s.mgr.Get(req.Msg.SessionId); ok {
		if err := sess.SetRoleConfig(req.Msg.Coordinator, req.Msg.Implementer, req.Msg.Reviewers); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return connect.NewResponse(&v1.SetRoleConfigResponse{}), nil
	}
	// No live session to apply to — persist the default assignment so it takes
	// effect for the next session (and survives a restart).
	if err := s.mgr.SetRoles(req.Msg.Coordinator, req.Msg.Implementer, req.Msg.Reviewers); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&v1.SetRoleConfigResponse{}), nil
}

// SetThinking changes a thinking/effort level per role (empty role = all roles)
// (spec §7.4, §18.2). When session_id names a live session the change applies to
// it immediately and is persisted; with an empty/unknown session_id it just
// updates the persisted default (roles.thinking.*) so it survives a restart.
func (s *Server) SetThinking(_ context.Context, req *connect.Request[v1.SetThinkingRequest]) (*connect.Response[v1.SetThinkingResponse], error) {
	if sess, ok := s.mgr.Get(req.Msg.SessionId); ok {
		if err := sess.SetThinking(req.Msg.Role, req.Msg.Level); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return connect.NewResponse(&v1.SetThinkingResponse{}), nil
	}
	if err := s.mgr.SetRoleThinking(req.Msg.Role, req.Msg.Level); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&v1.SetThinkingResponse{}), nil
}

// ListBacklog returns summary rows for the backlog, with per-task readiness
// derived from dependency status (spec §18.5). Read-only.
func (s *Server) ListBacklog(_ context.Context, req *connect.Request[v1.ListBacklogRequest]) (*connect.Response[v1.ListBacklogResponse], error) {
	store, err := s.mgr.Backlog(req.Msg.Project)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	tasks, err := store.List()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	byID := docs.StatusByID(tasks)
	var out []*v1.BacklogTaskSummary
	for _, t := range tasks {
		blocking := docs.BlockingDeps(t, byID)
		out = append(out, &v1.BacklogTaskSummary{
			Id: t.ID, Title: t.Title, Status: string(t.Status), Priority: int32(t.Priority),
			DependsOn: t.DependsOn, Ready: len(blocking) == 0, BlockedBy: blocking,
		})
	}
	return connect.NewResponse(&v1.ListBacklogResponse{Tasks: out}), nil
}

// GetTask returns one task's full detail for the backlog browser (spec §18.5).
func (s *Server) GetTask(_ context.Context, req *connect.Request[v1.GetTaskRequest]) (*connect.Response[v1.GetTaskResponse], error) {
	store, err := s.mgr.Backlog(req.Msg.Project)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	t, err := store.Get(req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	tasks, err := store.List()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	blocking := docs.BlockingDeps(t, docs.StatusByID(tasks))
	return connect.NewResponse(&v1.GetTaskResponse{Task: &v1.TaskDetail{
		Id: t.ID, Title: t.Title, Status: string(t.Status), Priority: int32(t.Priority),
		DependsOn: t.DependsOn, SpecRefs: t.SpecRefs, Created: t.Created, Updated: t.Updated,
		Body: t.Body, Ready: len(blocking) == 0, BlockedBy: blocking, Path: t.Path,
	}}), nil
}

// UpdateTask grooms a backlog task in place from the browser (spec §18.5, task
// 0099): change status/priority/title. Unset optional fields are left untouched;
// a request with NO mutation fields set is a valid "refresh" that re-reads the
// task file (used after hand-edits in $EDITOR). The
// docs Store serializes writes per backlog dir, so this shares the same locking
// path as work sessions and the capture agent.
func (s *Server) UpdateTask(_ context.Context, req *connect.Request[v1.UpdateTaskRequest]) (*connect.Response[v1.UpdateTaskResponse], error) {
	store, err := s.mgr.Backlog(req.Msg.Project)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	m := req.Msg
	if m.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id required"))
	}
	// Validate mutations up front so a bad request never touches the store.
	if m.Status != nil {
		switch docs.Status(m.GetStatus()) {
		case docs.StatusProposed, docs.StatusTodo, docs.StatusInProgress, docs.StatusInReview, docs.StatusDone, docs.StatusBlocked:
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid status %q", m.GetStatus()))
		}
	}
	if m.Priority != nil {
		if p := m.GetPriority(); p < 1 || p > 5 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("priority %d out of range (1..5)", p))
		}
	}
	if m.Title != nil && strings.TrimSpace(m.GetTitle()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("title must not be blank"))
	}
	t, err := store.Update(m.Id, func(t *docs.Task) {
		if m.Status != nil {
			t.Status = docs.Status(m.GetStatus())
		}
		if m.Priority != nil {
			t.Priority = int(m.GetPriority())
		}
		if m.Title != nil {
			t.Title = strings.TrimSpace(m.GetTitle())
		}
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	tasks, err := store.List()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	blocking := docs.BlockingDeps(t, docs.StatusByID(tasks))
	return connect.NewResponse(&v1.UpdateTaskResponse{Task: &v1.TaskDetail{
		Id: t.ID, Title: t.Title, Status: string(t.Status), Priority: int32(t.Priority),
		DependsOn: t.DependsOn, SpecRefs: t.SpecRefs, Created: t.Created, Updated: t.Updated,
		Body: t.Body, Ready: len(blocking) == 0, BlockedBy: blocking, Path: t.Path,
	}}), nil
}

// CreateTask adds a new task to the backlog (task 0143). It composes the same
// canonical scaffold as the capture agent (docs.TaskBody) and assigns the next
// id via the docs Store, sharing the per-directory write lock with work sessions
// and the capture agent. Used by `ycc task add` when a daemon is available.
func (s *Server) CreateTask(_ context.Context, req *connect.Request[v1.CreateTaskRequest]) (*connect.Response[v1.CreateTaskResponse], error) {
	store, err := s.mgr.Backlog(req.Msg.Project)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	m := req.Msg
	if strings.TrimSpace(m.Title) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("title must not be blank"))
	}
	prio := int(m.Priority)
	if prio == 0 {
		prio = 3
	} else if prio < 1 || prio > 5 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("priority %d out of range (1..5)", prio))
	}
	t, err := store.Create(strings.TrimSpace(m.Title), docs.TaskBody(m.Body), prio, m.DependsOn, m.SpecRefs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	tasks, err := store.List()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	blocking := docs.BlockingDeps(t, docs.StatusByID(tasks))
	return connect.NewResponse(&v1.CreateTaskResponse{Task: &v1.TaskDetail{
		Id: t.ID, Title: t.Title, Status: string(t.Status), Priority: int32(t.Priority),
		DependsOn: t.DependsOn, SpecRefs: t.SpecRefs, Created: t.Created, Updated: t.Updated,
		Body: t.Body, Ready: len(blocking) == 0, BlockedBy: blocking, Path: t.Path,
	}}), nil
}

// ListPlans returns the in-repo plan library (plans/*.md) so clients can browse
// saved runbooks (task 0020/0077). Read-only.
func (s *Server) ListPlans(_ context.Context, req *connect.Request[v1.ListPlansRequest]) (*connect.Response[v1.ListPlansResponse], error) {
	store, err := s.mgr.Backlog(req.Msg.Project)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	plans, err := store.ListPlans()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var out []*v1.PlanSummary
	for _, p := range plans {
		out = append(out, &v1.PlanSummary{Name: p.Name, Title: p.Title, Path: p.Path})
	}
	return connect.NewResponse(&v1.ListPlansResponse{Plans: out}), nil
}

// GetPlan returns one saved plan's markdown content for viewing (task 0077).
func (s *Server) GetPlan(_ context.Context, req *connect.Request[v1.GetPlanRequest]) (*connect.Response[v1.GetPlanResponse], error) {
	store, err := s.mgr.Backlog(req.Msg.Project)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	content, err := store.ReadPlan(req.Msg.Name)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	// Derive title from the listing (first heading) when available, else the name.
	title := req.Msg.Name
	if plans, lerr := store.ListPlans(); lerr == nil {
		for _, p := range plans {
			if p.Name == req.Msg.Name || p.Name == strings.TrimSuffix(req.Msg.Name, ".md") {
				title = p.Title
				break
			}
		}
	}
	return connect.NewResponse(&v1.GetPlanResponse{Name: req.Msg.Name, Title: title, Content: content}), nil
}

// CaptureBacklogItem runs the lightweight, off-stream quick-add capture agent to
// turn a natural-language description into a backlog task without disturbing any
// running session (spec §18.2, task 0016). It streams the capture agent's action
// log live (the same Event stream as Subscribe), ending with a terminal
// `capture_result` event whose data carries {task_id,title,question} on success
// (or {error} on failure / a clarifying question via `question`).
func (s *Server) CaptureBacklogItem(ctx context.Context, req *connect.Request[v1.CaptureBacklogItemRequest], stream *connect.ServerStream[v1.Event]) error {
	if strings.TrimSpace(req.Msg.Description) == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("description is required"))
	}
	ch := make(chan event.Event, 64)
	go func() {
		res, err := s.mgr.CaptureBacklogItem(ctx, req.Msg.Project, req.Msg.Description, req.Msg.PriorQuestion, req.Msg.PriorAnswer, func(ev event.Event) {
			select {
			case ch <- ev:
			case <-ctx.Done():
			}
		})
		// Terminal event: the stream has already started, so a mid-stream gRPC
		// error code is no longer possible — surface failures (including an
		// unknown project) via the capture_result error field instead.
		data := map[string]any{}
		if err != nil {
			data["error"] = err.Error()
		} else {
			data["task_id"] = res.TaskID
			data["title"] = res.Title
			data["question"] = res.Question
		}
		select {
		case ch <- event.Event{Actor: "capture", Type: "capture_result", Data: data}:
		case <-ctx.Done():
		}
		close(ch)
	}()
	for ev := range ch {
		if err := stream.Send(toProto(ev)); err != nil {
			return err
		}
	}
	return nil
}

// GetUsage returns the aggregated, priced usage/cost breakdown for a project's
// workspace (spec §20.3, §20.5) so non-CLI clients can render it.
func (s *Server) GetUsage(_ context.Context, req *connect.Request[v1.GetUsageRequest]) (*connect.Response[v1.GetUsageResponse], error) {
	var groupBy []usage.Dim
	for _, g := range req.Msg.GroupBy {
		d, err := usage.ParseDim(g)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		groupBy = append(groupBy, d)
	}
	opts := usage.Options{GroupBy: groupBy}
	if req.Msg.Since != "" {
		t, err := time.Parse("2006-01-02", req.Msg.Since)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		opts.Since = t
	}
	if req.Msg.Until != "" {
		t, err := time.Parse("2006-01-02", req.Msg.Until)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		opts.Until = t
	}
	res, err := s.mgr.UsageReport(req.Msg.Project, opts)
	if err != nil {
		// An unknown project is client input; a scan/IO failure is internal.
		if errors.Is(err, session.ErrUnknownProject) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := &v1.GetUsageResponse{Workspace: res.Workspace, Total: usageRowToProto(res.Total)}
	for _, r := range res.Rows {
		out.Rows = append(out.Rows, usageRowToProto(r))
	}
	return connect.NewResponse(out), nil
}

// GetBudget returns the configured spend-guard caps (task 0137, spec §20.6) so
// the TUI work-loop driver can enforce the per-loop-run cap client-side. Session
// caps are enforced daemon-side; this only exposes the configured values.
func (s *Server) GetBudget(_ context.Context, _ *connect.Request[v1.GetBudgetRequest]) (*connect.Response[v1.GetBudgetResponse], error) {
	b := s.mgr.Budget()
	return connect.NewResponse(&v1.GetBudgetResponse{
		SessionCost:   b.SessionCost,
		SessionTokens: b.SessionTokens,
		LoopCost:      b.LoopCost,
		LoopTokens:    b.LoopTokens,
	}), nil
}

func usageRowToProto(r usage.Row) *v1.UsageRow {
	return &v1.UsageRow{
		Task:        r.Task,
		Model:       r.Model,
		Session:     r.Session,
		Agent:       r.Agent,
		Day:         r.Day,
		Input:       int64(r.Tokens.Input),
		Output:      int64(r.Tokens.Output),
		CacheRead:   int64(r.Tokens.CacheRead),
		CacheWrite:  int64(r.Tokens.CacheWrite),
		Total:       int64(r.Tokens.Total),
		Cost:        r.Cost,
		PriceStatus: string(r.Status),
	}
}

func toProto(ev event.Event) *v1.Event {
	var dataJSON string
	if len(ev.Data) > 0 {
		if b, err := json.Marshal(ev.Data); err == nil {
			dataJSON = string(b)
		}
	}
	return &v1.Event{
		Seq:       int64(ev.Seq),
		Ts:        ev.TS.Format("2006-01-02T15:04:05.000Z07:00"),
		Actor:     ev.Actor,
		Type:      string(ev.Type),
		DataJson:  dataJSON,
		Transient: ev.Transient,
	}
}
