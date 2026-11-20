package orchestrator

import (
	"context"
	"errors"

	"github.com/whyrusleeping/gollama"

	"github.com/whyrusleeping/ycc/internal/tools"
)

// ErrCoordinationDisabled is returned by Deps.ListSessions once the project's
// experimental session coordination has been turned off.
var ErrCoordinationDisabled = errors.New("session coordination is turned off for this project")

// listSessions is the experimental session-awareness tool. It is registered
// only when Deps.ListSessions is set (the project enabled the setting when the
// session was built).
func listSessions(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "list_sessions",
		Description: "List this project's other sessions (main tree and workstreams): mode, state, held or earlier task focus, " +
			"title, worktree, running jobs, and the paths each has claimed with claim age. With path, list the sessions that " +
			"claim that file, including ended ones. A claim records an uncommitted write; it is evidence of past work, not a " +
			"reservation or lock.",
		Params: tools.Obj(map[string]any{
			"path": tools.StrProp("optional file path (workspace-relative or absolute) whose claimants to list"),
		}),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			path, _ := tools.GetString(params, "path")
			out, err := d.ListSessions(path)
			if errors.Is(err, ErrCoordinationDisabled) {
				return tools.OkResult("list_sessions: " + err.Error() + "; nothing to report."), nil
			}
			if err != nil {
				return tools.ErrResult("list_sessions: %v", err), nil
			}
			return tools.OkResult(out), nil
		},
	}
}

// coordinationTools returns the session-awareness tools for a coordinator or
// mutating subagent, or none when the session was built without them.
func coordinationTools(d *Deps) []*gollama.Tool {
	if d.ListSessions == nil {
		return nil
	}
	return []*gollama.Tool{listSessions(d)}
}
