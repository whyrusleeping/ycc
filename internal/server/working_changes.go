package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// GetWorkingChanges exposes a bounded view of the session's current scoped tree.
func (s *Server) GetWorkingChanges(_ context.Context, req *connect.Request[v1.GetWorkingChangesRequest]) (*connect.Response[v1.GetWorkingChangesResponse], error) {
	if req.Msg.SessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}
	c, scope, err := s.mgr.WorkingChanges(req.Msg.Project, req.Msg.SessionId, req.Msg.TaskId)
	if err != nil {
		code := connect.CodeFailedPrecondition
		if errors.Is(err, session.ErrUnknownProject) {
			code = connect.CodeInvalidArgument
		}
		if errors.Is(err, session.ErrUnknownSession) {
			code = connect.CodeNotFound
		}
		return nil, connect.NewError(code, err)
	}
	diff := c.Diff
	size := len(diff)
	sum := sha256.Sum256([]byte(diff))
	adds, dels := int64(0), int64(0)
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			adds++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			dels++
		}
	}
	paths := c.Paths
	truncated := false
	if len(paths) > 200 {
		paths = paths[:200]
		truncated = true
	}
	if size > maxCommitDiffBytes {
		cut := maxCommitDiffBytes
		if nl := strings.LastIndexByte(diff[:cut], '\n'); nl >= 0 {
			cut = nl + 1
		} else {
			cut = 0 // no complete line fits the response budget
		}
		diff = diff[:cut]
		truncated = true
	}
	return connect.NewResponse(&v1.GetWorkingChangesResponse{
		SnapshotId: c.ID, BaselineId: c.BaselineID, BaseCommit: c.BaseCommit, Tree: c.Tree,
		Paths: paths, PathsTotal: int32(len(c.Paths)), ExcludedDirtyPaths: int32(c.ExcludedDirtyPaths),
		Additions: adds, Deletions: dels, Diff: diff, DiffBytes: int64(size), DiffSha256: hex.EncodeToString(sum[:]),
		Truncated: truncated, Scope: scope, ChangedSinceKnown: req.Msg.KnownSnapshotId != "" && req.Msg.KnownSnapshotId != c.ID,
	}), nil
}
