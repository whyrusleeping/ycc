package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/projectfs"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// ListFiles lists a directory inside a project (or a session's live worktree)
// for read-only remote browsing. Confinement, dotfile hiding and gitignore
// marking live in internal/projectfs. The bearer token already permits
// StartSession — arbitrary shell in any workspace — so project-confined reads
// do not expand the trust surface.
func (s *Server) ListFiles(ctx context.Context, req *connect.Request[v1.ListFilesRequest]) (*connect.Response[v1.ListFilesResponse], error) {
	root, fellBack, err := s.mgr.FileRoot(req.Msg.Project, req.Msg.SessionId)
	if err != nil {
		return nil, fileError(err)
	}
	l, err := projectfs.List(ctx, root, req.Msg.Path, 0)
	if err != nil {
		return nil, fileError(err)
	}
	entries := make([]*v1.FileEntry, 0, len(l.Entries))
	for _, e := range l.Entries {
		entries = append(entries, &v1.FileEntry{
			Name:      e.Name,
			IsDir:     e.IsDir,
			Size:      e.Size,
			Mtime:     rfc3339(e.ModTime),
			IsSymlink: e.IsSymlink,
			Ignored:   e.Ignored,
		})
	}
	return connect.NewResponse(&v1.ListFilesResponse{
		Root:         root,
		Path:         l.Path,
		Entries:      entries,
		Truncated:    l.Truncated,
		RootFallback: fellBack,
	}), nil
}

// ReadFile returns one file inside a project (or a session's live worktree),
// read-only. Text beyond the cap is truncated at a line boundary; non-image
// binaries carry metadata only.
func (s *Server) ReadFile(_ context.Context, req *connect.Request[v1.ReadFileRequest]) (*connect.Response[v1.ReadFileResponse], error) {
	root, fellBack, err := s.mgr.FileRoot(req.Msg.Project, req.Msg.SessionId)
	if err != nil {
		return nil, fileError(err)
	}
	f, err := projectfs.Read(root, req.Msg.Path, req.Msg.MaxBytes)
	if err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&v1.ReadFileResponse{
		Root:         root,
		Path:         f.Path,
		Data:         f.Data,
		Size:         f.Size,
		MediaType:    f.MediaType,
		IsBinary:     f.IsBinary,
		Truncated:    f.Truncated,
		Mtime:        rfc3339(f.ModTime),
		RootFallback: fellBack,
	}), nil
}

// fileError maps project resolution and projectfs errors onto RPC codes.
func fileError(err error) error {
	switch {
	case errors.Is(err, session.ErrUnknownProject),
		errors.Is(err, projectfs.ErrInvalidPath),
		errors.Is(err, projectfs.ErrNotDir),
		errors.Is(err, projectfs.ErrNotFile):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, projectfs.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, projectfs.ErrDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
