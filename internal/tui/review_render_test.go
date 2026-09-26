package tui

import (
	"strings"
	"testing"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestReviewHeadingSnapshotAndLegacy(t *testing.T) {
	ev := &v1.Event{DataJson: `{"reviewer":"sol","logical_model":"gpt","round":2,"findings":2,"findings_by_severity":{"high":1,"low":1},"reviewed_snapshot_id":"abcdef1234567890"}`}
	got := reviewHeading(ev, "revise")
	for _, part := range []string{"sol", "gpt", "round 2", "REVISE", "high:1", "low:1", "abcdef123456", "only"} {
		if !strings.Contains(got, part) {
			t.Errorf("missing %q: %q", part, got)
		}
	}
	legacy := reviewHeading(&v1.Event{DataJson: `{"model":"claude"}`}, "unknown")
	if !strings.Contains(legacy, "UNKNOWN") || strings.Contains(legacy, "reviewed snapshot") {
		t.Errorf("legacy heading %q", legacy)
	}
}
