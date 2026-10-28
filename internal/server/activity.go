package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

const (
	defaultActivitySteps = 500
	maxActivitySteps     = 2000
)

// GetActivitySteps lists one actor's timed model turns and tool calls within
// a time range (an agent round, or the whole session for the coordinator),
// with per kind+name totals over the whole range so a truncated list still
// answers what took the time.
func (s *Server) GetActivitySteps(ctx context.Context, req *connect.Request[v1.GetActivityStepsRequest]) (*connect.Response[v1.GetActivityStepsResponse], error) {
	actor := strings.TrimSpace(req.Msg.Actor)
	if actor == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("actor is required"))
	}
	var since, until time.Time
	for _, b := range []struct {
		raw string
		dst *time.Time
	}{{req.Msg.Since, &since}, {req.Msg.Until, &until}} {
		if b.raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, b.raw)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("bad time %q: %w", b.raw, err))
		}
		*b.dst = t
	}
	if !until.IsZero() {
		until = until.Add(time.Millisecond) // timestamps are serialized at millisecond precision
	}
	store, logPath, durableSeq, durableOffset, err := s.sessionViewStore(req.Msg.Project, req.Msg.SessionId)
	if err != nil {
		return nil, sessionViewError(err)
	}
	_, live := s.mgr.Get(req.Msg.SessionId)
	steps, err := store.ActivitySteps(ctx, req.Msg.SessionId, logPath, durableSeq, durableOffset, actor, since, until, live && until.IsZero())
	if err != nil {
		return nil, sessionViewError(err)
	}
	now := time.Now()
	type key struct{ kind, name string }
	totals := make(map[key]*v1.ActivityStepTotal)
	for _, st := range steps {
		k := key{st.Kind, st.Name}
		t := totals[k]
		if t == nil {
			t = &v1.ActivityStepTotal{Kind: st.Kind, Name: st.Name}
			totals[k] = t
		}
		t.Count++
		if st.Status == "running" {
			t.DurationMs += max(0, now.Sub(st.Started).Milliseconds())
		} else {
			t.DurationMs += st.DurationMS
		}
	}
	out := &v1.GetActivityStepsResponse{}
	for _, t := range totals {
		out.Totals = append(out.Totals, t)
	}
	sort.Slice(out.Totals, func(i, j int) bool {
		a, b := out.Totals[i], out.Totals[j]
		if a.DurationMs != b.DurationMs {
			return a.DurationMs > b.DurationMs
		}
		return a.Kind+a.Name < b.Kind+b.Name
	})
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = defaultActivitySteps
	}
	limit = min(limit, maxActivitySteps)
	if len(steps) > limit {
		out.OmittedEarlier = int32(len(steps) - limit)
		steps = steps[len(steps)-limit:]
	}
	for _, st := range steps {
		out.Steps = append(out.Steps, &v1.ActivityStep{Kind: st.Kind, Name: st.Name, Summary: st.Summary, Status: st.Status,
			StartedAt: rfc3339(st.Started), FinishedAt: rfc3339(st.Finished), DurationMs: st.DurationMS,
			OutputTokens: st.OutputTokens, RowId: st.RowID})
	}
	return connect.NewResponse(out), nil
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
