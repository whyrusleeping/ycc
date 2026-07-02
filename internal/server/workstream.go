package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/session"
	"github.com/whyrusleeping/ycc/internal/workstream"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// toWorkstreamInfo converts a registry Workstream into its proto shape. The
// session_id rides inside so a client can Subscribe(session_id) for the
// workstream's live event stream (design §8).
func toWorkstreamInfo(w workstream.Workstream) *v1.WorkstreamInfo {
	return &v1.WorkstreamInfo{
		Id:           w.ID,
		Project:      w.Project,
		BaseCommit:   w.BaseCommit,
		Branch:       w.Branch,
		WorktreePath: w.WorktreePath,
		SessionId:    w.SessionID,
		TaskId:       w.TaskID,
		Status:       string(w.Status),
		CreatedAt:    rfc3339(w.CreatedAt),
	}
}

// workstreamError maps a manager workstream error to a Connect code. The manager
// returns fmt.Errorf sentinels-by-string for the unknown-id / not-active cases
// (no exported error values), plus the workstream.ErrWorktreeInUse sentinel; map
// each to the closest client/precondition code and fall back to Internal.
func workstreamError(err error) *connect.Error {
	switch {
	case errors.Is(err, workstream.ErrWorktreeInUse):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, session.ErrUnknownProject):
		return connect.NewError(connect.CodeNotFound, err)
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unknown workstream"), strings.Contains(msg, "unknown project"):
		return connect.NewError(connect.CodeNotFound, err)
	case strings.Contains(msg, "is not active"):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// SpawnWorkstream creates a linked worktree + branch off the project's base and
// starts a `work` session inside it (design §5, §8). The session_id for
// Subscribe rides inside the returned WorkstreamInfo.
func (s *Server) SpawnWorkstream(_ context.Context, req *connect.Request[v1.SpawnWorkstreamRequest]) (*connect.Response[v1.SpawnWorkstreamResponse], error) {
	m := req.Msg
	if strings.TrimSpace(m.Project) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project is required"))
	}
	ws, _, err := s.mgr.SpawnWorkstream(session.SpawnWorkstreamConfig{
		Project:          m.Project,
		BaseRef:          m.BaseRef,
		TaskID:           m.TaskId,
		Prompt:           m.Prompt,
		InteractionLevel: m.InteractionLevel,
	})
	if err != nil {
		return nil, workstreamError(err)
	}
	return connect.NewResponse(&v1.SpawnWorkstreamResponse{Workstream: toWorkstreamInfo(ws)}), nil
}

// ListWorkstreams returns the workstreams for a project (empty project => all)
// for the Workstreams panel (design §8). Non-terminal entries are enriched with
// a live commit count and session status so the panel can render per-workstream
// progress; enrichment is best-effort (0 / empty on error).
func (s *Server) ListWorkstreams(_ context.Context, req *connect.Request[v1.ListWorkstreamsRequest]) (*connect.Response[v1.ListWorkstreamsResponse], error) {
	var out []*v1.WorkstreamInfo
	all := s.mgr.Workstreams(req.Msg.Project)
	// Compute commit counts for the non-terminal workstreams in one batch so
	// each project's primary repo is opened at most once per call (the TUI
	// polls this ~every 3s).
	var nonTerminal []workstream.Workstream
	for _, w := range all {
		if !w.Status.Terminal() {
			nonTerminal = append(nonTerminal, w)
		}
	}
	counts := s.mgr.WorkstreamCommitCounts(nonTerminal)
	for _, w := range all {
		info := toWorkstreamInfo(w)
		if !w.Status.Terminal() {
			info.CommitCount = int64(counts[w.ID])
			info.SessionStatus = s.mgr.WorkstreamSessionStatus(w)
		}
		out = append(out, info)
	}
	return connect.NewResponse(&v1.ListWorkstreamsResponse{Workstreams: out}), nil
}

// PreviewMerge trial-merges a workstream's branch into its project's current base
// WITHOUT mutating anything (design §6 step 1): clean/conflicts + the integrated
// diff when clean.
func (s *Server) PreviewMerge(_ context.Context, req *connect.Request[v1.PreviewMergeRequest]) (*connect.Response[v1.PreviewMergeResponse], error) {
	if strings.TrimSpace(req.Msg.WorkstreamId) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workstream_id is required"))
	}
	prev, err := s.mgr.PreviewWorkstreamMerge(req.Msg.WorkstreamId)
	if err != nil {
		return nil, workstreamError(err)
	}
	return connect.NewResponse(&v1.PreviewMergeResponse{
		Clean:     prev.Clean,
		Conflicts: prev.Conflicts,
		Diff:      prev.Diff,
	}), nil
}

// MergeWorkstream integrates a workstream's branch back to base with the
// conflict-aware, review-gated flow (design §6). accept accepts a clean but gated
// merge under interactive/judgement; a conflict surfaces the conflicted paths.
func (s *Server) MergeWorkstream(_ context.Context, req *connect.Request[v1.MergeWorkstreamRequest]) (*connect.Response[v1.MergeWorkstreamResponse], error) {
	if strings.TrimSpace(req.Msg.WorkstreamId) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workstream_id is required"))
	}
	out, err := s.mgr.MergeWorkstream(req.Msg.WorkstreamId, req.Msg.Accept)
	if err != nil {
		return nil, workstreamError(err)
	}
	return connect.NewResponse(&v1.MergeWorkstreamResponse{
		Merged:      out.Merged,
		Commit:      out.Commit,
		NeedsAccept: out.NeedsAccept,
		Diff:        out.Diff,
		Conflicts:   out.Conflicts,
	}), nil
}

// DiscardWorkstream abandons a workstream without merging: stop the session,
// clean up the worktree + branch, mark the entry discarded (design §6).
func (s *Server) DiscardWorkstream(_ context.Context, req *connect.Request[v1.DiscardWorkstreamRequest]) (*connect.Response[v1.DiscardWorkstreamResponse], error) {
	if strings.TrimSpace(req.Msg.WorkstreamId) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workstream_id is required"))
	}
	if err := s.mgr.DiscardWorkstream(req.Msg.WorkstreamId); err != nil {
		return nil, workstreamError(err)
	}
	return connect.NewResponse(&v1.DiscardWorkstreamResponse{}), nil
}
