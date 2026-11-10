package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// Clients place presets and pick the default mode from ListModes metadata
// alone, so every preset must carry a known placement and the default mode
// must be one of the listed modes.
func TestListModesPlacementMetadata(t *testing.T) {
	resp, err := (&Server{}).ListModes(context.Background(), connect.NewRequest(&v1.ListModesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Msg
	if m.DefaultMode != "chat" {
		t.Fatalf("default mode = %q, want chat", m.DefaultMode)
	}
	listed := map[string]bool{}
	for _, mode := range m.Modes {
		listed[mode.Name] = true
	}
	if !listed[m.DefaultMode] {
		t.Fatalf("default mode %q not among listed modes", m.DefaultMode)
	}
	known := map[string]bool{"onboarding": true, "project": true, "backlog": true, "memory": true}
	placed := map[string]string{}
	for _, p := range m.Presets {
		if !known[p.Placement] {
			t.Errorf("preset %q has placement %q", p.Name, p.Placement)
		}
		if !listed[p.Mode] {
			t.Errorf("preset %q starts unlisted mode %q", p.Name, p.Mode)
		}
		placed[p.Name] = p.Placement
	}
	// Clients that start presets by name (Memory page, automatic grooms) and
	// the onboarding flow depend on these staying where they are.
	for name, want := range map[string]string{"onboard": "onboarding", "spec-doctor": "project", "memory-groom": "memory", "backlog-groom": "backlog"} {
		if placed[name] != want {
			t.Errorf("preset %q placement = %q, want %q", name, placed[name], want)
		}
	}
}
