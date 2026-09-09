package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

const (
	maxReviewDiffBytes  = 64 * 1024
	reviewDiffNudge     = "Orient yourself: the scoped change under review is preloaded below."
	reviewDiffCallID    = "preload_diff"
	reviewDiffTruncated = "\n…[review diff truncated: inspect the listed paths in the workspace for the rest]"
)

type reviewDiffBuild struct {
	History    []gollama.Message
	Call       gollama.ToolCall
	Result     string
	Duration   int64
	SnapshotID string
	BaselineID string
	Paths      []string
	Command    string
	Err        error
}

// buildReviewDiffHistory captures a non-mutating, explicitly scoped snapshot
// and presents it as a genuine-looking Bash exchange. The snapshot identifier
// lets reports and reviewer evidence name exactly what was inspected.
func buildReviewDiffHistory(repo *git.Repo, baseline *git.Baseline) reviewDiffBuild {
	if repo == nil {
		return reviewDiffBuild{Err: fmt.Errorf("git repository is not available")}
	}
	if baseline == nil {
		return reviewDiffBuild{Err: fmt.Errorf("session git baseline is unavailable; start a new session before review")}
	}
	start := time.Now()
	changes, err := repo.Changes(baseline)
	duration := time.Since(start).Milliseconds()
	if err != nil {
		return reviewDiffBuild{Err: err}
	}
	scope := strings.Join(changes.Paths, ", ")
	if scope == "" {
		scope = "(none)"
	}
	header := fmt.Sprintf("CHANGESET SNAPSHOT %s (baseline %s)\nSCOPE: %s\n\n",
		changes.ID, changes.BaselineID, scope)
	command := "git diff --binary --no-color --no-ext-diff " + changes.BaseCommit + " " + changes.Tree + " --"
	if len(changes.Paths) > 0 {
		command += " " + strings.Join(shellQuoteTopLiteralPaths(changes.Paths), " ")
	}
	built := reviewDiffBuild{
		Duration: duration, SnapshotID: changes.ID, BaselineID: changes.BaselineID,
		Paths: append([]string(nil), changes.Paths...), Command: command,
	}
	if changes.Diff == "" {
		return built
	}
	diff := changes.Diff
	if len(header)+len(diff) > maxReviewDiffBytes {
		budget := maxReviewDiffBytes - len(header) - len(reviewDiffTruncated)
		if budget < 0 {
			budget = 0
		}
		diff = validUTF8Prefix(diff, budget) + reviewDiffTruncated
	}
	args, _ := json.Marshal(map[string]string{"command": command})
	call := gollama.ToolCall{
		ID:   reviewDiffCallID,
		Type: "function",
		Function: gollama.ToolCallFunction{
			Name:      "Bash",
			Arguments: string(args),
		},
	}
	result := header + diff
	built.History = []gollama.Message{
		{Role: "user", Content: reviewDiffNudge},
		{Role: "assistant", ToolCalls: []gollama.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: result},
	}
	built.Call = call
	built.Result = result
	return built
}

func compactReviewDiffEvidence(built reviewDiffBuild) string {
	scope := strings.Join(built.Paths, ", ")
	if scope == "" {
		scope = "(none)"
	}
	return fmt.Sprintf("CURRENT SCOPED CHANGESET %s (baseline %s)\nScope: %s\nExact retrieval: %s",
		built.SnapshotID, built.BaselineID, scope, built.Command)
}

func shellQuoteTopLiteralPaths(paths []string) []string {
	quoted := make([]string, len(paths))
	for i, path := range paths {
		path = ":(top,literal)" + path
		quoted[i] = "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
	}
	return quoted
}

// emitSyntheticReviewDiff records the exchange exactly as installed in a
// reviewer's history. In particular, it emits no user_input event: replay treats
// every such event as coordinator-authored conversation.
func emitSyntheticReviewDiff(em *event.Emitter, spec AgentSpec, actor string, built reviewDiffBuild) {
	if len(built.History) == 0 || em == nil {
		return
	}
	em = em.With(actor)
	em.Emit(event.ModelTurn, map[string]any{
		"text": "", "tool_calls": 1, "model_name": spec.Name,
		"backend": spec.Backend, "model_id": spec.Model, "synthetic": true,
		"changeset_id": built.SnapshotID,
	})
	em.Emit(event.ToolCall, map[string]any{
		"name": "Bash", "args": built.Call.Function.Arguments,
		"id": built.Call.ID, "synthetic": true, "changeset_id": built.SnapshotID,
	})
	em.Emit(event.ToolResult, map[string]any{
		"name": "Bash", "result": built.Result, "error": false,
		"id": built.Call.ID, "duration_ms": built.Duration, "synthetic": true,
		"changeset_id": built.SnapshotID,
	})
}
