package orchestrator

import (
	"encoding/json"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

const (
	maxReviewDiffBytes  = 64 * 1024
	reviewDiffNudge     = "Orient yourself: the change under review is preloaded below."
	reviewDiffCommand   = "git diff HEAD"
	reviewDiffCallID    = "preload_diff"
	reviewDiffTruncated = "\n…[review diff truncated: run 'git diff HEAD' yourself to inspect the rest]"
)

type reviewDiffBuild struct {
	History  []gollama.Message
	Call     gollama.ToolCall
	Result   string
	Duration int64
}

// buildReviewDiffHistory captures the same staged snapshot used by the rest of
// the review flow and presents it as a genuine-looking Bash exchange. A missing
// repo, an empty tree, or a capture failure simply leaves reviewers to inspect
// the workspace themselves as directed by their ordinary prompt.
func buildReviewDiffHistory(repo *git.Repo) reviewDiffBuild {
	if repo == nil {
		return reviewDiffBuild{}
	}
	start := time.Now()
	diff, err := repo.Diff()
	duration := time.Since(start).Milliseconds()
	if err != nil || diff == "" {
		return reviewDiffBuild{}
	}
	if len(diff) > maxReviewDiffBytes {
		budget := maxReviewDiffBytes - len(reviewDiffTruncated)
		if budget < 0 {
			budget = 0
		}
		diff = validUTF8Prefix(diff, budget) + reviewDiffTruncated
	}
	args, _ := json.Marshal(map[string]string{"command": reviewDiffCommand})
	call := gollama.ToolCall{
		ID:   reviewDiffCallID,
		Type: "function",
		Function: gollama.ToolCallFunction{
			Name:      "Bash",
			Arguments: string(args),
		},
	}
	return reviewDiffBuild{
		History: []gollama.Message{
			{Role: "user", Content: reviewDiffNudge},
			{Role: "assistant", ToolCalls: []gollama.ToolCall{call}},
			{Role: "tool", ToolCallID: call.ID, Content: diff},
		},
		Call:     call,
		Result:   diff,
		Duration: duration,
	}
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
	})
	em.Emit(event.ToolCall, map[string]any{
		"name": "Bash", "args": built.Call.Function.Arguments,
		"id": built.Call.ID, "synthetic": true,
	})
	em.Emit(event.ToolResult, map[string]any{
		"name": "Bash", "result": built.Result, "error": false,
		"id": built.Call.ID, "duration_ms": built.Duration, "synthetic": true,
	})
}
