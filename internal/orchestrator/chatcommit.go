package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// pushTimeout bounds the push that follows a chat commit under commit+push.
const pushTimeout = 2 * time.Minute

// chatFinishGuidance tells a chat agent what to do with finished work under the
// project's [chat] finished_work policy (spec §9).
func chatFinishGuidance(policy string) string {
	const scope = `The commit tool commits only the files this session changed, whole (paths adds more); pass
task_id and outcome when the work completes a backlog task so it is marked done. Do not commit
partial, exploratory, or unverified work, or work the user asked you to hold.`
	switch policy {
	case docs.ChatAsk:
		return `Finished work: this project asks before committing. When a change is complete and verified, ask
with ask_user whether to commit it (options such as "Commit" and "Leave uncommitted") unless the
user already said; never end on a prose "should I commit?". Commit only after a yes.
` + scope
	case docs.ChatCommitPush:
		return `Finished work: commit and push it. When a change the user asked for is complete and verified,
call commit before your final reply; it also pushes the current branch. Do not ask whether to
commit. Your reply names what you committed (short sha) and whether the push succeeded.
` + scope
	default:
		return `Finished work: commit it. When a change the user asked for is complete and verified, call commit
before your final reply. Do not ask whether to commit, and push only when the user asks. Your
reply names what you committed (short sha).
` + scope
	}
}

// chatCommitTool commits a chat session's finished work. With a task_id it
// finalizes that task like the work-mode commit tool; otherwise it commits the
// session's attributed changes. Under commit+push it then pushes.
func chatCommitTool(d *Deps, policy string) *gollama.Tool {
	desc := "Commit this session's finished, verified work to git: the files this session changed, whole, plus any named paths. " +
		"Pass task_id and outcome when the work completes a backlog task; that task is compacted and marked done in the same commit (do not mark it done first)."
	if policy == docs.ChatCommitPush {
		desc += " The project's policy then pushes the current branch to its upstream."
	}
	return &gollama.Tool{
		Name:        "commit",
		Description: desc,
		Params: tools.Obj(map[string]any{
			"message": tools.StrProp("concise commit message"),
			"task_id": tools.StrProp("optional backlog task this work completes"),
			"outcome": tools.StrProp("with task_id: concise accepted outcome retained with the completed task"),
			"paths":   tools.StrArrProp("optional extra files to commit whole (workspace-relative)"),
		}, "message"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			msg, _ := tools.GetString(params, "message")
			id, _ := tools.GetString(params, "task_id")
			outcome, _ := tools.GetString(params, "outcome")
			paths := tools.GetStringSlice(params, "paths")
			if strings.TrimSpace(msg) == "" {
				return tools.ErrResult("commit: a message is required"), nil
			}
			var sha string
			var notes []string
			var err error
			if strings.TrimSpace(id) != "" {
				if strings.TrimSpace(outcome) == "" {
					return tools.ErrResult("commit: an outcome is required with task_id"), nil
				}
				sha, notes, err = finalizeTask(ctx, d, id, msg, outcome, paths)
			} else {
				sha, notes, err = commitSessionChanges(ctx, d, msg, paths)
			}
			if err != nil {
				return tools.ErrResult("commit: %v", err), nil
			}
			out := "committed " + sha
			for _, note := range notes {
				out += "\n- " + note
			}
			if policy == docs.ChatCommitPush {
				if pushed, err := pushCurrentBranch(ctx, d); err != nil {
					out += "\n- not pushed: " + err.Error()
				} else {
					out += "\n- pushed: " + pushed
				}
			}
			return tools.OkResult(out), nil
		},
	}
}

// commitSessionChanges commits the session's attributed changes (plus paths)
// with no task bookkeeping and records commit_made.
func commitSessionChanges(ctx context.Context, d *Deps, message string, paths []string) (string, []string, error) {
	if err := finalizationCanProceed(ctx, d); err != nil {
		return "", nil, err
	}
	release, err := d.gitSection(ctx)
	if err != nil {
		return "", nil, err
	}
	changes, err := d.sessionChangeset(normalizePaths(paths)...)
	if err != nil {
		release()
		return "", nil, fmt.Errorf("unsafe changeset: %w", err)
	}
	if strings.TrimSpace(changes.Diff) == "" {
		release()
		return "", nil, fmt.Errorf("nothing to commit: this session has no uncommitted changes")
	}
	sha, err := d.Repo.Commit(changes, message)
	if err == nil {
		d.releaseCommittedClaims(changes.Recovery())
	}
	release()
	if err != nil {
		return "", nil, err
	}
	notes := changesetNotes(changes)
	// No reviewer saw this changeset, so name what attribution only inferred.
	if len(changes.Unclaimed) > 0 {
		notes = append(notes, fmt.Sprintf("included without a file-tool write from this session (shell output, backlog writes, or another writer; tell the user if any are not this session's work) (%d): %s",
			len(changes.Unclaimed), boundedPathList(changes.Unclaimed, 1024)))
	}
	if err := emitDurable(ctx, d.Emitter, event.CommitMade, map[string]any{
		"sha": sha, "message": message, "finalization_id": changes.ID,
	}); err != nil {
		notes = append(notes, "the commit succeeded but recording it in the session log failed: "+err.Error())
	}
	return sha, notes, nil
}

// pushCurrentBranch pushes only the current branch to its upstream and returns
// git's summary.
func pushCurrentBranch(ctx context.Context, d *Deps) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	out, err := d.Repo.PushUpstream(ctx)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git push timed out after %s", pushTimeout)
		}
		return "", fmt.Errorf("%s", truncate(err.Error(), 600))
	}
	if out == "" {
		out = "up to date"
	}
	return truncate(out, 600), nil
}
