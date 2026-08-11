package main

import (
	"testing"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestResolveWorkstreamID(t *testing.T) {
	workstreams := []*v1.WorkstreamInfo{
		{Id: "ws_3f9abc01"},
		{Id: "ws_3f9ade02"},
		{Id: "ws_aabbccdd"},
	}

	tests := []struct {
		name string
		arg  string
		want string
	}{
		{name: "exact wins over longer prefix matches", arg: "ws_3f9abc01", want: "ws_3f9abc01"},
		{name: "bare prefix", arg: "aabb", want: "ws_aabbccdd"},
		{name: "ws prefix", arg: "ws_aabb", want: "ws_aabbccdd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWorkstreamID(workstreams, tc.arg)
			if err != nil {
				t.Fatalf("resolveWorkstreamID(%q): %v", tc.arg, err)
			}
			if got.GetId() != tc.want {
				t.Fatalf("resolveWorkstreamID(%q) id = %q, want %q", tc.arg, got.GetId(), tc.want)
			}
		})
	}
}

func TestResolveWorkstreamIDUnknown(t *testing.T) {
	_, err := resolveWorkstreamID([]*v1.WorkstreamInfo{{Id: "ws_aabbccdd"}}, "dead")
	if err == nil || err.Error() != `unknown workstream "dead"` {
		t.Fatalf("error = %v, want unknown-workstream error", err)
	}
}

func TestResolveWorkstreamIDAmbiguous(t *testing.T) {
	workstreams := []*v1.WorkstreamInfo{
		{Id: "ws_3f9ade02"},
		{Id: "ws_3f9abc01"},
	}
	_, err := resolveWorkstreamID(workstreams, "3f9a")
	want := `ambiguous workstream "3f9a" (matches: ws_3f9abc01, ws_3f9ade02)`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestWorkstreamStatus(t *testing.T) {
	tests := []struct {
		name string
		info *v1.WorkstreamInfo
		want string
	}{
		{name: "active session status", info: &v1.WorkstreamInfo{Status: "active", SessionStatus: "running"}, want: "running"},
		{name: "terminal registry status wins", info: &v1.WorkstreamInfo{Status: "merged", SessionStatus: "stopped"}, want: "merged"},
		{name: "ready wins", info: &v1.WorkstreamInfo{Status: "ready", SessionStatus: "stopped"}, want: "ready"},
		{name: "needs attention", info: &v1.WorkstreamInfo{Status: "needs_attention", SessionStatus: "error"}, want: "⚠ needs attention"},
		{name: "registry fallback", info: &v1.WorkstreamInfo{Status: "active"}, want: "active"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := workstreamStatus(tc.info); got != tc.want {
				t.Fatalf("workstreamStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}
