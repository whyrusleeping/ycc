package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

const (
	watchReportsDefault = 20
	watchReportsMax     = 500
)

// ListWatches returns a project's watches.
func (s *Server) ListWatches(_ context.Context, req *connect.Request[v1.ListWatchesRequest]) (*connect.Response[v1.ListWatchesResponse], error) {
	infos, err := s.mgr.ListWatches(req.Msg.Project)
	if err != nil {
		return nil, watchError(err)
	}
	resp := &v1.ListWatchesResponse{}
	for _, info := range infos {
		resp.Watches = append(resp.Watches, watchToProto(info))
	}
	return connect.NewResponse(resp), nil
}

// GetWatch returns one watch with its most recent reports, newest first.
func (s *Server) GetWatch(_ context.Context, req *connect.Request[v1.GetWatchRequest]) (*connect.Response[v1.GetWatchResponse], error) {
	m := req.Msg
	if err := requireWatchName(m.Name); err != nil {
		return nil, err
	}
	info, err := s.mgr.GetWatch(m.Project, m.Name)
	if err != nil {
		return nil, watchError(err)
	}
	limit := int(m.ReportLimit)
	if limit <= 0 {
		limit = watchReportsDefault
	}
	limit = min(limit, watchReportsMax)
	reports, err := s.mgr.WatchReports(m.Project, m.Name, limit)
	if err != nil {
		return nil, watchError(err)
	}
	resp := &v1.GetWatchResponse{Watch: watchToProto(info)}
	for _, r := range reports {
		resp.Reports = append(resp.Reports, watchReportToProto(r))
	}
	return connect.NewResponse(resp), nil
}

// CreateWatch persists a new watch; its first run is due immediately.
func (s *Server) CreateWatch(_ context.Context, req *connect.Request[v1.CreateWatchRequest]) (*connect.Response[v1.CreateWatchResponse], error) {
	def, err := watchDefinitionFromProto(req.Msg.Definition)
	if err != nil {
		return nil, err
	}
	info, err := s.mgr.CreateWatch(req.Msg.Project, def)
	if err != nil {
		return nil, watchError(err)
	}
	return connect.NewResponse(&v1.CreateWatchResponse{Watch: watchToProto(info)}), nil
}

// UpdateWatch replaces the definition of the watch named by definition.name.
func (s *Server) UpdateWatch(_ context.Context, req *connect.Request[v1.UpdateWatchRequest]) (*connect.Response[v1.UpdateWatchResponse], error) {
	def, err := watchDefinitionFromProto(req.Msg.Definition)
	if err != nil {
		return nil, err
	}
	info, err := s.mgr.UpdateWatch(req.Msg.Project, def)
	if err != nil {
		return nil, watchError(err)
	}
	return connect.NewResponse(&v1.UpdateWatchResponse{Watch: watchToProto(info)}), nil
}

// DeleteWatch removes a watch and its report history.
func (s *Server) DeleteWatch(_ context.Context, req *connect.Request[v1.DeleteWatchRequest]) (*connect.Response[v1.DeleteWatchResponse], error) {
	if err := requireWatchName(req.Msg.Name); err != nil {
		return nil, err
	}
	if err := s.mgr.DeleteWatch(req.Msg.Project, req.Msg.Name); err != nil {
		return nil, watchError(err)
	}
	return connect.NewResponse(&v1.DeleteWatchResponse{}), nil
}

// SetWatchPaused pauses or resumes a watch.
func (s *Server) SetWatchPaused(_ context.Context, req *connect.Request[v1.SetWatchPausedRequest]) (*connect.Response[v1.SetWatchPausedResponse], error) {
	if err := requireWatchName(req.Msg.Name); err != nil {
		return nil, err
	}
	info, err := s.mgr.SetWatchPaused(req.Msg.Project, req.Msg.Name, req.Msg.Paused)
	if err != nil {
		return nil, watchError(err)
	}
	return connect.NewResponse(&v1.SetWatchPausedResponse{Watch: watchToProto(info)}), nil
}

// RunWatchNow starts a run immediately without changing the schedule.
func (s *Server) RunWatchNow(_ context.Context, req *connect.Request[v1.RunWatchNowRequest]) (*connect.Response[v1.RunWatchNowResponse], error) {
	if err := requireWatchName(req.Msg.Name); err != nil {
		return nil, err
	}
	if err := s.mgr.RunWatchNow(req.Msg.Project, req.Msg.Name); err != nil {
		return nil, watchError(err)
	}
	return connect.NewResponse(&v1.RunWatchNowResponse{}), nil
}

func requireWatchName(name string) error {
	if strings.TrimSpace(name) == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("watch name is required"))
	}
	return nil
}

// watchError maps a manager watch error to a Connect code.
func watchError(err error) *connect.Error {
	switch {
	case errors.Is(err, session.ErrUnknownWatch):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, session.ErrWatchExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, session.ErrInvalidWatch), errors.Is(err, session.ErrUnknownProject), errors.Is(err, session.ErrUnknownModel):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, session.ErrWatchBusy), errors.Is(err, session.ErrDisabledModel):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

func watchDefinitionFromProto(d *v1.WatchDefinition) (session.WatchDefinition, error) {
	if d == nil {
		return session.WatchDefinition{}, connect.NewError(connect.CodeInvalidArgument, errors.New("watch definition is required"))
	}
	if err := requireWatchName(d.Name); err != nil {
		return session.WatchDefinition{}, err
	}
	// Reject values whose nanosecond Duration would overflow into a valid-looking interval.
	if d.IntervalSecs < 0 || d.IntervalSecs > math.MaxInt64/int64(time.Second) {
		return session.WatchDefinition{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("interval_secs %d is out of range", d.IntervalSecs))
	}
	def := session.WatchDefinition{
		Name:           d.Name,
		Brief:          d.Brief,
		Interval:       session.Duration(time.Duration(d.IntervalSecs) * time.Second),
		Model:          d.Model,
		Probe:          d.Probe,
		ProbeAlways:    d.ProbeAlways,
		AllowedActions: d.AllowedActions,
	}
	if e := strings.TrimSpace(d.Expires); e != "" {
		t, err := time.Parse(time.RFC3339, e)
		if err != nil {
			return session.WatchDefinition{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("expires must be RFC 3339: %w", err))
		}
		def.Expires = t
	}
	return def, nil
}

func watchDefinitionToProto(d session.WatchDefinition) *v1.WatchDefinition {
	return &v1.WatchDefinition{
		Name:           d.Name,
		Brief:          d.Brief,
		IntervalSecs:   int64(time.Duration(d.Interval) / time.Second),
		Model:          d.Model,
		Probe:          d.Probe,
		ProbeAlways:    d.ProbeAlways,
		Expires:        rfc3339(d.Expires),
		AllowedActions: d.AllowedActions,
	}
}

func watchToProto(info session.WatchInfo) *v1.Watch {
	w := &v1.Watch{
		Project:          info.Project,
		Definition:       watchDefinitionToProto(info.Definition),
		CreatedAt:        rfc3339(info.Created),
		Paused:           info.Paused,
		PausedReason:     info.PausedReason,
		NextDue:          rfc3339(info.NextDue),
		Notes:            info.Notes,
		Running:          info.Running,
		ParkedSessionIds: info.ParkedSessions,
	}
	for _, d := range info.Decisions {
		w.Decisions = append(w.Decisions, &v1.WatchDecision{
			At: rfc3339(d.At), SessionId: d.SessionID, Seq: int64(d.Seq), Question: d.Question, Answer: d.Answer,
		})
	}
	if info.Last != nil {
		w.Last = watchReportToProto(*info.Last)
	}
	return w
}

func watchReportToProto(r session.WatchReportRecord) *v1.WatchReport {
	out := &v1.WatchReport{
		At:           rfc3339(r.At),
		Kind:         r.Kind,
		SessionId:    r.SessionID,
		Status:       r.Status,
		Summary:      r.Summary,
		ScheduledFor: rfc3339(r.ScheduledFor),
	}
	for _, a := range r.Actions {
		out.Actions = append(out.Actions, &v1.WatchAction{Action: a.Action, Undo: a.Undo})
	}
	return out
}
