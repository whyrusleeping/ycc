package server

import (
	"context"
	"errors"
	"log"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/uianalytics"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// SetAnalytics enables durable client usage analytics. Without a store (the
// one-shot in-process daemon) RecordUiEvents validates and discards.
func (s *Server) SetAnalytics(store *uianalytics.Store) { s.analytics = store }

// RecordUiEvents stores a best-effort batch of client usage events. Invalid
// events are dropped and counted rather than failing the batch; only an
// unattributable (bad client name) batch is rejected.
func (s *Server) RecordUiEvents(_ context.Context, req *connect.Request[v1.RecordUiEventsRequest]) (*connect.Response[v1.RecordUiEventsResponse], error) {
	if s.analytics == nil {
		recs, dropped, err := uianalytics.Validate(req.Msg, time.Now())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return connect.NewResponse(&v1.RecordUiEventsResponse{Accepted: int32(len(recs)), Dropped: int32(dropped)}), nil
	}
	accepted, dropped, err := s.analytics.Record(req.Msg)
	if err != nil {
		if errors.Is(err, uianalytics.ErrInvalidClient) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		log.Printf("analytics: %v", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.RecordUiEventsResponse{Accepted: int32(accepted), Dropped: int32(dropped), Stored: true}), nil
}

// GetUiAnalytics summarises stored usage events over a trailing window.
func (s *Server) GetUiAnalytics(_ context.Context, req *connect.Request[v1.GetUiAnalyticsRequest]) (*connect.Response[v1.GetUiAnalyticsResponse], error) {
	if s.analytics == nil {
		return connect.NewResponse(&v1.GetUiAnalyticsResponse{}), nil
	}
	days := int(req.Msg.GetDays())
	if days < 0 || days > 3660 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("days out of range"))
	}
	resp, err := s.analytics.Report(days, req.Msg.GetClient())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(resp), nil
}
