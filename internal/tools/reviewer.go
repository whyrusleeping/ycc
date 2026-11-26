package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/whyrusleeping/gollama"
)

// Inspect returns the read/inspect tools: Read, Search, and Bash. Bash covers
// listing, builds/tests, and searches outside Search's textual contract, so
// reviewers and authoring modes can understand a workspace without write/edit
// tools.
func Inspect(ws *Workspace) []*gollama.Tool {
	return []*gollama.Tool{readFile(ws), search(ws), bash(ws)}
}

// ReadOnly returns a minimal read-only tool set: just the file Read tool (no
// shell, no edits). Used by the quick-add backlog capture agent,
// which should be able to ground a new task in existing files without the power
// to run commands or mutate the workspace.
func ReadOnly(ws *Workspace) []*gollama.Tool {
	return []*gollama.Tool{readFile(ws)}
}

// ReadOnlyInspect returns Read and Search plus a sandboxed shell and the web
// tools for general-purpose inspection agents, whose main use is research.
// Unlike Inspect, shell commands cannot mutate the workspace; on hosts without
// byte-and-mode confinement the shell fails closed while Read and Search remain
// available. The web tools report an error when EXA_API_KEY is unset.
func ReadOnlyInspect(ws *Workspace) []*gollama.Tool {
	return append([]*gollama.Tool{readFile(ws), search(ws), sandboxedBash(ws)}, Web()...)
}

// Reviewer returns the tool set for a review subagent: Read, Search, a sandboxed
// Bash (see internal/sandbox), and submit_review, a control tool that
// ends the review with a structured verdict. Reviewers must not modify the change
// under review: the sandboxed Bash mounts the workspace read-only so mutation is
// hard-enforced, and fails closed where no qualifying mechanism is available.
// Reviewers deliberately lack the web tools: verdicts rest on the assigned
// snapshot and its source-bound evidence, and the coordinator supplies any
// external facts in the review handoff.
func Reviewer(ws *Workspace) []*gollama.Tool {
	return []*gollama.Tool{readFile(ws), search(ws), sandboxedBash(ws), submitReview(ws)}
}

// submitReview is a control tool. It serializes the reviewer's structured verdict
// and verification provenance into Control.Report as JSON for the coordinator to
// parse, and stops the review loop. The trusted snapshot identifier and execution
// details come from the reviewer workspace rather than model-supplied arguments.
func bindReviewVerification(ws *Workspace, snapshot, tree string, value any) any {
	items, ok := value.([]any)
	if !ok {
		return value
	}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok || entry["classification"] != "independently_rebuilt_and_executed" {
			continue
		}
		id, _ := entry["receipt_id"].(string)
		receipt, found := ws.reviewExecution(id, snapshot, tree)
		if !found {
			entry["classification"] = "unavailable"
			entry["evidence"] = "no matching source-bound execution receipt exists for the assigned snapshot; claimed evidence: " + fmt.Sprint(entry["evidence"])
			delete(entry, "receipt_id")
			continue
		}
		// Replace free-form evidence with the trusted command receipt. This keeps a
		// stale or unbound binary claim from borrowing an unrelated successful run;
		// the coordinator sees exactly what command actually ran and how it exited.
		entry["receipt_id"] = receipt.ID
		entry["evidence"] = fmt.Sprintf("source-bound command %q on Git tree %s: %s", receipt.Command, receipt.Tree, receipt.Result)
	}
	return items
}

func submitReview(workspaces ...*Workspace) *gollama.Tool {
	ws := &Workspace{}
	if len(workspaces) > 0 && workspaces[0] != nil {
		ws = workspaces[0]
	}
	return &gollama.Tool{
		Name: "submit_review",
		Description: "Submit one verdict per review: accept when correct/complete, revise for substantive defects, " +
			"ready_to_deploy when the code is correct but criteria needing operational evidence (deploy, run, measure, report) remain; name them in pending_criteria. " +
			"Independent execution requires the receipt_id from that exact source-bound Bash call, including failed checks. " +
			"Classify prior logs as inspected_prior_evidence and checks that could not run as unavailable.",
		Params: obj(map[string]any{
			"verdict": map[string]any{"type": "string", "enum": []string{"accept", "ready_to_deploy", "revise"}, "description": "accept, ready_to_deploy, or revise"},
			"summary": strProp("one-paragraph overall assessment"),
			"pending_criteria": map[string]any{
				"type":        "array",
				"description": "ready_to_deploy only: the acceptance criteria that still need operational evidence after deployment or a run",
				"items":       map[string]any{"type": "string"},
			},
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
			"verification": map[string]any{
				"type":        "array",
				"description": "checks/evidence considered, each with truthful provenance and result or unavailability reason",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"classification": map[string]any{"type": "string", "enum": []string{"independently_rebuilt_and_executed", "inspected_prior_evidence", "unavailable"}},
						"evidence":       map[string]any{"type": "string", "description": "command and result, inspected artifact, or reason unavailable"},
						"receipt_id":     map[string]any{"type": "string", "description": "source-bound Bash receipt for independently executed evidence"},
					},
					"required": []string{"classification", "evidence"},
				},
			},
		}, "verdict", "summary"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			report, ok := params.(map[string]any)
			if !ok {
				return errResult("submit_review: invalid arguments"), nil
			}
			if report["verdict"] == "ready_to_deploy" && !hasPendingCriterion(report["pending_criteria"]) {
				return errResult("submit_review: ready_to_deploy needs pending_criteria naming the acceptance criteria that still need operational evidence; otherwise submit accept or revise"), nil
			}
			copy := make(map[string]any, len(report)+1)
			for key, value := range report {
				copy[key] = value
			}
			if _, ok := copy["verification"]; !ok {
				copy["verification"] = []map[string]any{{
					"classification": "unavailable",
					"evidence":       "reviewer did not report verification provenance",
				}}
			}
			snapshot, tree := ws.reviewIdentity()
			copy["verification"] = bindReviewVerification(ws, snapshot, tree, copy["verification"])
			copy["snapshot_id"] = snapshot
			raw, err := json.Marshal(copy)
			if err != nil {
				return errResult("submit_review: %v", err), nil
			}
			ws.reviewMu.Lock()
			ws.submittedReview = string(raw)
			ws.reviewMu.Unlock()
			return &gollama.ToolResult{Content: "review submitted", Structured: &Control{Stop: true, Report: string(raw)}}, nil
		},
	}
}

// TakeSubmittedReview returns and clears the report of the latest successful
// submit_review call. A reviewer's run report that equals it came from the
// tool, with its validation and trusted snapshot and receipt binding, rather
// than from a plain-text reply.
func (w *Workspace) TakeSubmittedReview() string {
	w.reviewMu.Lock()
	defer w.reviewMu.Unlock()
	report := w.submittedReview
	w.submittedReview = ""
	return report
}

// hasPendingCriterion reports whether value is a list with a non-blank string.
func hasPendingCriterion(value any) bool {
	items, _ := value.([]any)
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}
