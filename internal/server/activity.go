package server

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/sessionview"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

const (
	defaultJobOutputBytes = 64 << 10
	maxJobOutputBytes     = 256 << 10
)

// GetSessionActivity projects the session's coordinator, subagent, and
// background-job graph from the indexed lifecycle and progress events,
// reconciling open work with the live registry when the session runs here.
func (s *Server) GetSessionActivity(ctx context.Context, req *connect.Request[v1.GetSessionActivityRequest]) (*connect.Response[v1.GetSessionActivityResponse], error) {
	store, logPath, durableSeq, durableOffset, err := s.sessionViewStore(req.Msg.Project, req.Msg.SessionId)
	if err != nil {
		return nil, sessionViewError(err)
	}
	st, events, progress, err := store.Activity(ctx, req.Msg.SessionId, logPath, durableSeq, durableOffset)
	if err != nil {
		return nil, sessionViewError(err)
	}
	nodes := sessionview.BuildActivity(events, progress)
	var infos map[string]jobs.Info
	live := false
	if sess, ok := s.mgr.Get(req.Msg.SessionId); ok {
		live = true
		infos = make(map[string]jobs.Info)
		for _, info := range sess.JobInfos() {
			infos[info.ID] = info
		}
	}
	// Work left open by a session that is no longer live ended, at the latest,
	// with its last durable event (the lost end time clients draw).
	endedAt, _ := time.Parse("2006-01-02T15:04:05.000Z07:00", st.LastTimestamp)
	out := &v1.GetSessionActivityResponse{Live: live, IndexedThroughSeq: st.IndexedThrough}
	for i := range nodes {
		n := &nodes[i]
		if n.Kind == "coordinator" {
			n.Status = st.Phase
			n.Model = st.Coordinator
			n.LastActivity = endedAt
		} else {
			overlayActivity(n, infos, live, endedAt)
		}
		pn := activityNodeProto(*n)
		if info, ok := infos[n.JobID]; ok && n.Actor == "" {
			pn.OutputAvailable = !info.Restored
		}
		out.Nodes = append(out.Nodes, pn)
	}
	return connect.NewResponse(out), nil
}

// overlayActivity resolves a node's open rounds/jobs against the live registry,
// or marks them lost at endedAt when the session is not live (nothing is
// running them).
func overlayActivity(n *sessionview.ActivityNode, infos map[string]jobs.Info, live bool, endedAt time.Time) {
	resolve := func(jobID string) (string, time.Time) {
		if !live {
			return string(jobs.Lost), endedAt
		}
		info, ok := infos[jobID]
		if !ok || jobID == "" || info.Status == jobs.Running {
			return "running", time.Time{} // running, or a foreground round still in progress
		}
		return string(info.Status), info.Finished
	}
	if n.Actor == "" {
		if n.Status == "running" {
			n.Status, n.Finished = resolve(n.JobID)
		}
		return
	}
	for i := range n.Spans {
		if span := &n.Spans[i]; span.Status == "running" {
			span.Status, span.Finished = resolve(span.JobID)
		}
	}
	if k := len(n.Spans); k > 0 {
		n.Status, n.Finished = n.Spans[k-1].Status, n.Spans[k-1].Finished
	}
	if n.Status != "running" {
		n.CurrentTool = ""
	}
}

func activityNodeProto(n sessionview.ActivityNode) *v1.ActivityNode {
	out := &v1.ActivityNode{Id: n.ID, ParentId: n.ParentID, Kind: n.Kind, Label: truncatePresentationString(n.Label, 512),
		Actor: n.Actor, Model: n.Model, Status: n.Status, Mutates: n.Mutates, JobId: n.JobID, Result: n.Result,
		StartedAt: rfc3339(n.Started), FinishedAt: rfc3339(n.Finished),
		CurrentTool: n.CurrentTool, Turns: int32(n.Turns), TotalTokens: n.TotalTokens, LastActivityAt: rfc3339(n.LastActivity)}
	for _, sp := range n.Spans {
		out.Spans = append(out.Spans, &v1.ActivitySpan{JobId: sp.JobID, Round: int32(sp.Round), Status: sp.Status,
			ContextMode: sp.ContextMode, StartedAt: rfc3339(sp.Started), FinishedAt: rfc3339(sp.Finished)})
	}
	return out
}

// GetJobOutput reads a live session's retained job output by absolute byte
// cursor or retained tail. Output does not survive the session, so anything
// else is reported unavailable rather than as an error.
func (s *Server) GetJobOutput(ctx context.Context, req *connect.Request[v1.GetJobOutputRequest]) (*connect.Response[v1.GetJobOutputResponse], error) {
	if _, _, _, _, err := s.mgr.SessionViewLogPath(req.Msg.Project, req.Msg.SessionId); err != nil {
		return nil, sessionViewError(err)
	}
	sess, ok := s.mgr.Get(req.Msg.SessionId)
	if !ok {
		return connect.NewResponse(&v1.GetJobOutputResponse{UnavailableReason: "session is not live; job output does not survive the session"}), nil
	}
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = defaultJobOutputBytes
	}
	limit = min(limit, maxJobOutputBytes)
	offset := max(req.Msg.Offset, 0)
	output, info, ok := sess.JobOutput(strings.TrimSpace(req.Msg.JobId), offset, limit, max(int(req.Msg.TailLines), 0))
	if !ok {
		return connect.NewResponse(&v1.GetJobOutputResponse{UnavailableReason: "no such job in this session"}), nil
	}
	if info.Restored {
		return connect.NewResponse(&v1.GetJobOutputResponse{Status: string(info.Status),
			UnavailableReason: "job output did not survive the daemon restart"}), nil
	}
	return connect.NewResponse(&v1.GetJobOutputResponse{Available: true, Data: output.Data, Start: output.Start,
		End: output.End, RetainedStart: output.RetainedStart, RetainedEnd: output.RetainedEnd, Status: string(info.Status)}), nil
}
