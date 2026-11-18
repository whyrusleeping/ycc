package server

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/projectfs"
	"github.com/whyrusleeping/ycc/internal/publish"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// GetPublications reports the configured share server and the project's (or
// one file's) unexpired publications.
func (s *Server) GetPublications(_ context.Context, req *connect.Request[v1.GetPublicationsRequest]) (*connect.Response[v1.GetPublicationsResponse], error) {
	pubs, err := s.mgr.Publications(req.Msg.Project, req.Msg.SessionId, req.Msg.Path)
	if err != nil {
		return nil, publishError(err)
	}
	resp := &v1.GetPublicationsResponse{}
	if t, ok := s.mgr.PublishTarget(); ok {
		resp.Target = &v1.PublishTarget{Name: t.Name, Url: t.URL}
	}
	for _, p := range pubs {
		resp.Publications = append(resp.Publications, publicationProto(p.Record, p.Current))
	}
	return connect.NewResponse(resp), nil
}

// PublishFile uploads a project file to the share server and returns its link.
func (s *Server) PublishFile(ctx context.Context, req *connect.Request[v1.PublishFileRequest]) (*connect.Response[v1.PublishFileResponse], error) {
	rec, reused, err := s.mgr.PublishFile(ctx, req.Msg.Project, req.Msg.SessionId, req.Msg.Path)
	if err != nil {
		return nil, publishError(err)
	}
	return connect.NewResponse(&v1.PublishFileResponse{Publication: publicationProto(rec, true), Reused: reused}), nil
}

// UnpublishFile deletes a publication from the share server.
func (s *Server) UnpublishFile(ctx context.Context, req *connect.Request[v1.UnpublishFileRequest]) (*connect.Response[v1.UnpublishFileResponse], error) {
	if err := s.mgr.UnpublishFile(ctx, req.Msg.Project, req.Msg.Id); err != nil {
		return nil, publishError(err)
	}
	return connect.NewResponse(&v1.UnpublishFileResponse{}), nil
}

func publicationProto(r publish.Record, current bool) *v1.Publication {
	p := &v1.Publication{
		Id:          r.ID,
		Url:         r.URL,
		Path:        r.Path,
		Sha256:      r.SHA256,
		Size:        r.Size,
		PublishedAt: r.PublishedAt.UTC().Format(time.RFC3339),
		Current:     current,
	}
	if !r.ExpiresAt.IsZero() {
		p.ExpiresAt = r.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return p
}

func publishError(err error) error {
	var unavailable *publish.UnavailableError
	switch {
	case errors.Is(err, session.ErrPublishNotConfigured),
		errors.Is(err, session.ErrPublishNoToken),
		errors.Is(err, projectfs.ErrTooLarge),
		errors.Is(err, publish.ErrUnauthorized),
		errors.Is(err, publish.ErrTooLarge):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, session.ErrUnknownPublication):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.As(err, &unavailable):
		return connect.NewError(connect.CodeUnavailable, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return fileError(err)
}
