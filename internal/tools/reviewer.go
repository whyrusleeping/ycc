package tools

import (
	"context"
	"encoding/json"

	"github.com/whyrusleeping/gollama"
)

// Inspect returns the read/inspect tools: Read and Bash. Bash covers searching
// (ripgrep), listing, and running builds/tests, so reviewers and the authoring
// modes (spec/backlog/feature/bug) can understand a workspace without an explicit
// write/edit tool.
func Inspect(ws *Workspace) []*gollama.Tool {
	return []*gollama.Tool{readFile(ws), bash(ws)}
}

// Reviewer returns the tool set for a review subagent: the inspect tools plus
// submit_review, a control tool that ends the review with a structured verdict.
// Reviewers are prompted not to modify the workspace; hard enforcement is
// deferred (task 0008).
func Reviewer(ws *Workspace) []*gollama.Tool {
	return append(Inspect(ws), submitReview())
}

// submitReview is a control tool. It serializes the reviewer's structured verdict
// (verdict + summary + findings) into Control.Report as JSON for the coordinator
// to parse, and stops the review loop.
func submitReview() *gollama.Tool {
	return &gollama.Tool{
		Name: "submit_review",
		Description: "Submit your review verdict for the change. Call exactly once when done. " +
			"verdict is 'accept' if the change satisfies the task and is correct, or 'revise' if it needs work.",
		Params: obj(map[string]any{
			"verdict": map[string]any{"type": "string", "enum": []string{"accept", "revise"}, "description": "accept or revise"},
			"summary": strProp("one-paragraph overall assessment"),
			"findings": map[string]any{
				"type":        "array",
				"description": "specific issues found (empty if none)",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"severity": map[string]any{"type": "string", "enum": []string{"blocker", "major", "minor", "nit"}},
						"message":  map[string]any{"type": "string"},
					},
					"required": []string{"severity", "message"},
				},
			},
		}, "verdict", "summary"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			raw, err := json.Marshal(params)
			if err != nil {
				return errResult("submit_review: %v", err), nil
			}
			return &gollama.ToolResult{Content: "review submitted", Structured: &Control{Stop: true, Report: string(raw)}}, nil
		},
	}
}
