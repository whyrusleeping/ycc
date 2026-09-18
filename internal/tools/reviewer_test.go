package tools

import (
	"encoding/json"
	"testing"
)

func TestSubmitReviewBindsVerificationToTrustedSnapshot(t *testing.T) {
	ws := &Workspace{ReviewSnapshot: "snapshot-current", ReviewTree: "tree-current"}
	ws.recordReviewExecution(reviewExecutionReceipt{
		ID: "rv_test", Snapshot: "snapshot-current", Tree: "tree-current",
		Command: "cargo test", Result: "exit 0",
	})
	reg := New()
	reg.Add(submitReview(ws))
	res := dispatch(t, reg, "submit_review", `{
		"verdict":"accept",
		"summary":"ok",
		"verification":[{"classification":"independently_rebuilt_and_executed","evidence":"untrusted prose","receipt_id":"rv_test"}],
		"snapshot_id":"model-supplied-stale"
	}`)
	if res.IsError {
		t.Fatal(res.Content)
	}
	control, ok := res.Structured.(*Control)
	if !ok {
		t.Fatalf("structured result = %T", res.Structured)
	}
	var report struct {
		SnapshotID   string `json:"snapshot_id"`
		Verification []struct {
			Classification string `json:"classification"`
			Evidence       string `json:"evidence"`
			ReceiptID      string `json:"receipt_id"`
		} `json:"verification"`
	}
	if err := json.Unmarshal([]byte(control.Report), &report); err != nil {
		t.Fatal(err)
	}
	if report.SnapshotID != "snapshot-current" {
		t.Fatalf("snapshot_id = %q, want trusted current snapshot", report.SnapshotID)
	}
	if len(report.Verification) != 1 || report.Verification[0].Classification != "independently_rebuilt_and_executed" {
		t.Fatalf("verification = %+v", report.Verification)
	}
	if report.Verification[0].ReceiptID != "rv_test" || report.Verification[0].Evidence != `source-bound command "cargo test" on Git tree tree-current: exit 0` {
		t.Fatalf("trusted receipt was not substituted: %+v", report.Verification[0])
	}
}

func TestSubmitReviewRejectsIndependentClaimWithoutSourceBoundExecution(t *testing.T) {
	ws := &Workspace{ReviewSnapshot: "snapshot-current", ReviewTree: "tree-current"}
	// A different successful source-bound command must not authorize a stale
	// unbound binary claim that has no matching receipt.
	ws.recordReviewExecution(reviewExecutionReceipt{
		ID: "rv_trivial", Snapshot: "snapshot-current", Tree: "tree-current",
		Command: "true", Result: "exit 0",
	})
	reg := New()
	reg.Add(submitReview(ws))
	res := dispatch(t, reg, "submit_review", `{
		"verdict":"accept",
		"summary":"ran a stale binary",
		"verification":[{"classification":"independently_rebuilt_and_executed","evidence":"./target/debug/check passed"}]
	}`)
	control := res.Structured.(*Control)
	var report struct {
		Verification []struct {
			Classification string `json:"classification"`
			Evidence       string `json:"evidence"`
		} `json:"verification"`
	}
	if err := json.Unmarshal([]byte(control.Report), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Verification) != 1 || report.Verification[0].Classification != "unavailable" {
		t.Fatalf("verification = %+v", report.Verification)
	}
}

func TestSubmitReviewAcceptsMatchingNonzeroExecutionReceipt(t *testing.T) {
	ws := &Workspace{ReviewSnapshot: "snapshot-current", ReviewTree: "tree-current"}
	ws.recordReviewExecution(reviewExecutionReceipt{
		ID: "rv_failed_test", Snapshot: "snapshot-current", Tree: "tree-current",
		Command: "cargo test", Result: "exit 101",
	})
	reg := New()
	reg.Add(submitReview(ws))
	res := dispatch(t, reg, "submit_review", `{
		"verdict":"revise",
		"summary":"test failed",
		"verification":[{"classification":"independently_rebuilt_and_executed","evidence":"failed","receipt_id":"rv_failed_test"}]
	}`)
	control := res.Structured.(*Control)
	var report struct {
		Verification []struct {
			Classification string `json:"classification"`
			Evidence       string `json:"evidence"`
		} `json:"verification"`
	}
	if err := json.Unmarshal([]byte(control.Report), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Verification) != 1 || report.Verification[0].Classification != "independently_rebuilt_and_executed" || report.Verification[0].Evidence != `source-bound command "cargo test" on Git tree tree-current: exit 101` {
		t.Fatalf("failing execution was not retained as executed: %+v", report.Verification)
	}
}

func TestSubmitReviewDefaultsMissingProvenanceToUnavailable(t *testing.T) {
	reg := New()
	reg.Add(submitReview(&Workspace{ReviewSnapshot: "snapshot-current"}))
	res := dispatch(t, reg, "submit_review", `{"verdict":"accept","summary":"inspected only"}`)
	if res.IsError {
		t.Fatal(res.Content)
	}
	control := res.Structured.(*Control)
	var report struct {
		Verification []struct {
			Classification string `json:"classification"`
		} `json:"verification"`
	}
	if err := json.Unmarshal([]byte(control.Report), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Verification) != 1 || report.Verification[0].Classification != "unavailable" {
		t.Fatalf("verification = %+v", report.Verification)
	}
}
