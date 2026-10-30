package server

import (
	"context"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"

	"github.com/whyrusleeping/ycc/internal/health"
)

// SetHealth attaches the daemon's dependency health monitor.
func (s *Server) SetHealth(m *health.Monitor) { s.health = m }

// GetHealth returns the latest health checks, re-running them first on refresh.
func (s *Server) GetHealth(ctx context.Context, req *connect.Request[v1.GetHealthRequest]) (*connect.Response[v1.GetHealthResponse], error) {
	resp := &v1.GetHealthResponse{}
	if s.health == nil {
		return connect.NewResponse(resp), nil
	}
	checks := s.health.Snapshot()
	if req.Msg.Refresh {
		checks = s.health.Refresh(ctx)
	}
	for _, c := range checks {
		hc := &v1.HealthCheck{
			Id: c.ID, Label: c.Label, Status: string(c.Status), Detail: c.Detail, Remedy: c.Remedy,
		}
		if !c.CheckedAt.IsZero() {
			hc.CheckedAt = c.CheckedAt.UTC().Format(time.RFC3339)
		}
		resp.Checks = append(resp.Checks, hc)
	}
	return connect.NewResponse(resp), nil
}
