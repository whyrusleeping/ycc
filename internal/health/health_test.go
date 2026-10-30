package health

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestObservedFailureSchedulesDebouncedRecheck(t *testing.T) {
	runs := 0
	m := New(func() []Target {
		return []Target{{ID: "x", Label: "X", Interval: time.Hour, Run: func(context.Context) Check {
			runs++
			return Check{Status: StatusFail}
		}}}
	})
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) { m.now = func() time.Time { return t0.Add(d) } }
	ctx := context.Background()

	at(0)
	m.runDue(ctx)
	if runs != 1 {
		t.Fatalf("first pass ran %d checks, want 1", runs)
	}
	at(time.Minute)
	m.runDue(ctx)
	if runs != 1 {
		t.Fatalf("check re-ran before its interval")
	}

	m.Observe("x", errors.New("boom"))
	at(time.Minute + 30*time.Second)
	if next := m.runDue(ctx); runs != 1 || next != 30*time.Second {
		t.Fatalf("recheck not debounced: runs=%d next=%v", runs, next)
	}
	at(2 * time.Minute)
	m.runDue(ctx)
	if runs != 2 {
		t.Fatalf("observed failure did not trigger a recheck")
	}

	at(3 * time.Minute)
	m.Observe("x", nil)
	if got := m.Snapshot()[0]; got.Status != StatusOK || got.Label != "X" {
		t.Fatalf("observed success = %+v, want ok", got)
	}
	at(10 * time.Minute)
	m.runDue(ctx)
	if runs != 2 {
		t.Fatalf("observed success did not postpone the probe")
	}
}

// A refresh whose client disconnects must not void the probe another caller
// (or the scheduler) joined.
func TestCancelledCallerDoesNotDiscardSharedProbe(t *testing.T) {
	release := make(chan struct{})
	m := New(func() []Target {
		return []Target{{ID: "x", Label: "X", Interval: time.Hour, Run: func(context.Context) Check {
			<-release
			return Check{Status: StatusOK}
		}}}
	})
	cancelled, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		m.Refresh(cancelled)
	}()
	<-started
	cancel()
	done := make(chan []Check)
	go func() { done <- m.Refresh(context.Background()) }()
	close(release)
	if got := (<-done)[0]; got.Status != StatusOK {
		t.Fatalf("surviving refresh got %+v, want the shared probe's ok result", got)
	}
}
