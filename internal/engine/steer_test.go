package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
)

// fakeSteer returns a scripted set of corrections, one slice per Checkpoint call.
type fakeSteer struct {
	msgs  [][]string
	calls int
}

func (f *fakeSteer) Checkpoint(ctx context.Context) ([]string, error) {
	defer func() { f.calls++ }()
	if f.calls < len(f.msgs) {
		return f.msgs[f.calls], nil
	}
	return nil, nil
}

// A correction returned at the first checkpoint is appended to the conversation
// the model sees on a later turn (spec §18.7).
func TestLoopSteerInjectsCorrection(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantText("done"),
	}}
	loop := newLoop(t, turner)
	loop.Seed("do the thing")
	loop.Steer = &fakeSteer{msgs: [][]string{{"actually do the other thing"}}}

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The first turn's request should include both the seed and the steered
	// correction as user messages, in order.
	var users []string
	for _, mm := range turner.lastMsgs {
		if mm.Role == "user" {
			users = append(users, mm.Content)
		}
	}
	if len(users) < 2 {
		t.Fatalf("want >=2 user messages, got %v", users)
	}
	joined := strings.Join(users, "|")
	if !strings.Contains(joined, "actually do the other thing") {
		t.Fatalf("correction not seen by model: %v", users)
	}
	if users[0] != "do the thing" || users[1] != "actually do the other thing" {
		t.Fatalf("messages out of order: %v", users)
	}
}

// With Steer == nil the checkpoint is a no-op: behavior is unchanged.
func TestLoopSteerNilNoop(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantText("ok"),
	}}
	loop := newLoop(t, turner)
	loop.Seed("hi")
	if loop.Steer != nil {
		t.Fatal("Steer should be nil")
	}
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Report != "ok" {
		t.Fatalf("report = %q", res.Report)
	}
}
