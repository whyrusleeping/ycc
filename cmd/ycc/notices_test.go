package main

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// ycc notices lists a session's definite claims, then its ambiguous ones
// marked as such.
func TestRenderNoticesSeparatesAmbiguousClaims(t *testing.T) {
	var b strings.Builder
	renderNotices(&b, &v1.ListProjectNoticesResponse{Sessions: []*v1.ProjectLiveSession{{
		SessionId: "s_a", Mode: "work", Activity: "running",
		Claims: []*v1.ClaimedPath{{Path: "gen.go", Ambiguous: true}, {Path: "mine.go"}},
	}}}, time.Now())
	got := b.String()
	want := "    claims: mine.go\n    ambiguous claims (possibly another session's): gen.go\n"
	if !strings.Contains(got, want) {
		t.Fatalf("renderNotices =\n%s\nwant it to contain\n%s", got, want)
	}
}
