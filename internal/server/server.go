// Package server implements the daemon's Connect-RPC surface (spec §12): start
// sessions, list them, subscribe to a session's event stream (with replay from
// an offset), and send input. A bearer-token interceptor guards every RPC.
package server

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/session"
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

// StartSession creates and launches a new session.
func (s *Server) StartSession(_ context.Context, req *connect.Request[v1.StartSessionRequest]) (*connect.Response[v1.StartSessionResponse], error) {
	m := req.Msg
	if m.Prompt == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errEmptyPrompt)
	}
	sess, err := s.mgr.Start(session.Config{
		Workspace:        m.Workspace,
		Mode:             m.Mode,
		InteractionLevel: m.InteractionLevel,
		Prompt:           m.Prompt,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.StartSessionResponse{SessionId: sess.ID}), nil
}

// ListSessions returns all live sessions and their current status.
func (s *Server) ListSessions(_ context.Context, _ *connect.Request[v1.ListSessionsRequest]) (*connect.Response[v1.ListSessionsResponse], error) {
	var infos []*v1.SessionInfo
	for _, sess := range s.mgr.List() {
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

// SendInput queues a follow-up instruction for the session's agent.
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
