// Package server implements the daemon's Connect-RPC surface (spec §12): start
// sessions, list them, subscribe to a session's event stream (with replay from
// an offset), and send input. A bearer-token interceptor guards every RPC.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"connectrpc.com/connect"

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

// ListModels enumerates the configured logical models for the settings overlay
// pickers (spec §13, §18.2).
func (s *Server) ListModels(_ context.Context, _ *connect.Request[v1.ListModelsRequest]) (*connect.Response[v1.ListModelsResponse], error) {
	var models []*v1.ModelInfo
	for _, m := range s.mgr.Models() {
		models = append(models, &v1.ModelInfo{Name: m.Name, Backend: m.Backend, Model: m.Model})
	}
	return connect.NewResponse(&v1.ListModelsResponse{Models: models}), nil
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

// SetRoleConfig reassigns per-role logical models mid-session (spec §13, §18.2).
func (s *Server) SetRoleConfig(_ context.Context, req *connect.Request[v1.SetRoleConfigRequest]) (*connect.Response[v1.SetRoleConfigResponse], error) {
	sess, ok := s.mgr.Get(req.Msg.SessionId)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoSession)
	}
	if err := sess.SetRoleConfig(req.Msg.Coordinator, req.Msg.Implementer, req.Msg.Reviewers); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&v1.SetRoleConfigResponse{}), nil
}

// SetThinking changes a thinking/effort level mid-session per role (empty role =
// all roles) (spec §7.4, §18.2).
func (s *Server) SetThinking(_ context.Context, req *connect.Request[v1.SetThinkingRequest]) (*connect.Response[v1.SetThinkingResponse], error) {
	sess, ok := s.mgr.Get(req.Msg.SessionId)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoSession)
	}
	if err := sess.SetThinking(req.Msg.Role, req.Msg.Level); err != nil {
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
		Body: t.Body, Ready: len(blocking) == 0, BlockedBy: blocking,
	}}), nil
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

func usageRowToProto(r usage.Row) *v1.UsageRow {
	return &v1.UsageRow{
		Task:        r.Task,
		Model:       r.Model,
		Session:     r.Session,
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
		Seq:      int64(ev.Seq),
		Ts:       ev.TS.Format("2006-01-02T15:04:05.000Z07:00"),
		Actor:    ev.Actor,
		Type:     string(ev.Type),
		DataJson: dataJSON,
	}
}
