package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
)

func mustResize(t *testing.T, m *Manager, target int) *WorkLoop {
	t.Helper()
	snap, err := m.ResizeWorkLoop("demo", "", target)
	if err != nil || snap == nil || snap.Parallel != target {
		t.Fatalf("resize to %d: snapshot %+v, error %v", target, snap, err)
	}
	return snap
}

func TestWorkLoopResizeGlobalAdmission(t *testing.T) {
	f := newParallelFake(t, 1, 6)
	defer f.m.ReclaimAll()
	original := f.m.newRunSession
	f.m.newRunSession = func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		runner := original(wl)
		wl.waitingRecheck = time.Hour // new capacity must signal the scheduler
		return runner
	}
	initial, err := f.m.StartWorkLoop("demo")
	if err != nil {
		t.Fatal(err)
	}
	f.letClaim(1)
	mustResize(t, f.m, 3)
	awaitCond(t, "second worker", func() bool { return f.startedCount() == 2 })
	f.stays(2, "third worker must wait for second worker's claim")
	f.letClaim(2)
	f.letClaim(3)
	snap := mustResize(t, f.m, 2)
	if snap.LoopID != initial.LoopID || len(snap.Slots) != 3 {
		t.Fatalf("after shrink: %+v", snap)
	}
	if p, ok := readPersistedWorkLoop(f.ws); !ok || p.Parallel != 2 || len(p.Slots) != 3 {
		t.Fatalf("persisted shrink: %+v", p)
	}
	// Free the lowest-numbered lane first, leaving a 'retired' higher lane
	// occupied. The available lane alone must not allow a replacement.
	f.finish(1, parallelEnd{done: true})
	awaitCond(t, "first finish collected", func() bool { s, _ := f.m.GetWorkLoop("demo"); return s.SessionsRun == 1 })
	f.stays(3, "global working count still at target")
	f.finish(2, parallelEnd{done: true})
	f.letClaim(4)
	f.stays(4, "replacement fills the target alongside the high-numbered worker")
	if _, err := f.m.StopWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	f.finish(3, parallelEnd{done: true})
	f.finish(4, parallelEnd{done: true})
	final := waitLoopFinished(t, f.m, "demo")
	if final.Parallel != 2 || final.SessionsRun != 4 {
		t.Fatalf("final: %+v", final)
	}
	next, err := f.m.StartWorkLoop("demo")
	if err != nil || next.Parallel != 1 {
		t.Fatalf("next loop didn't use project default: %+v, %v", next, err)
	}
	if _, err := f.m.ResizeWorkLoop("demo", initial.LoopID, 16); !errors.Is(err, ErrLoopNotLive) {
		t.Fatalf("resize from a stale client: %v", err)
	}
	if s, _ := f.m.GetWorkLoop("demo"); s.LoopID != next.LoopID || s.Parallel != 1 {
		t.Fatalf("stale resize changed the replacement loop: %+v", s)
	}
}

func TestWorkLoopResizeValidation(t *testing.T) {
	f := newParallelFake(t, 1, 1)
	defer f.m.ReclaimAll()
	refused := func(n int, want error) {
		t.Helper()
		if snap, err := f.m.ResizeWorkLoop("demo", "", n); snap != nil || !errors.Is(err, want) {
			t.Fatalf("resize to %d = %+v, %v; want %v", n, snap, err, want)
		}
	}
	refused(1, ErrLoopNotLive)
	if _, err := f.m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	f.letClaim(1)
	for _, n := range []int{-1, 0, 17} {
		refused(n, ErrInvalidLoopParallel)
	}
	if p, _ := readPersistedWorkLoop(f.ws); p.Parallel != 1 {
		t.Fatalf("invalid resize changed target: %d", p.Parallel)
	}
	mustResize(t, f.m, 16)
	mustResize(t, f.m, 16)
	if _, err := f.m.StopWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	refused(1, ErrLoopStopping)
	f.finish(1, parallelEnd{done: true})
	waitLoopFinished(t, f.m, "demo")
	refused(1, ErrLoopNotLive)
}

func TestWorkLoopResizeDuringStallWait(t *testing.T) {
	f := newParallelFake(t, 1, 2)
	defer f.m.ReclaimAll()
	if _, err := f.m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	f.letClaim(1)
	f.finish(1, parallelEnd{})
	awaitCond(t, "stall backoff", func() bool { s, _ := f.m.GetWorkLoop("demo"); return s.WaitKind == waitKindNoProgress })
	mustResize(t, f.m, 2)
	f.letClaim(2)
	f.stays(2, "only new slot can start during stall")
	if s, _ := f.m.GetWorkLoop("demo"); len(s.Slots) != 1 || s.Slots[0].Slot != 1 {
		t.Fatalf("stalled lane's delay was bypassed: %+v", s)
	}
}

func TestWorkLoopResizeDuringProviderWait(t *testing.T) {
	var starts atomic.Int32
	m, _, ws := loopTestManager(t, func(*workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			starts.Add(1)
			return loopSessRec{id: "outage", errKind: "network", errRetryable: true}, false, nil
		}
	})
	defer m.ReclaimAll()
	if _, err := docs.NewStore(ws).Create("work", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "provider wait", func() bool { s, _ := m.GetWorkLoop("demo"); return s.WaitKind == "network" })
	before, _ := m.GetWorkLoop("demo")
	for _, n := range []int{4, 1} {
		mustResize(t, m, n)
		time.Sleep(80 * time.Millisecond)
		s, _ := m.GetWorkLoop("demo")
		if starts.Load() != 1 || s.WaitKind != "network" || !s.ResumeAt.Equal(before.ResumeAt) {
			t.Fatalf("resize to %d disturbed provider backoff: before %+v, after %+v", n, before, s)
		}
	}
}

// Lane 0 has exhausted its stall patience; lane 1 owes provider backoff.
// Retiring lane 1 must let the scheduler stop without waiting out the outage.
func TestWorkLoopResizeRetiresLastUsableLaneDuringProviderWait(t *testing.T) {
	var starts atomic.Int32
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.lanes[0].gaveUp = true
		wl.lanes[0].pace.stallSessions = 17
		return func(context.Context) (loopSessRec, bool, error) {
			starts.Add(1)
			return loopSessRec{id: "outage", errKind: "network", errRetryable: true}, false, nil
		}
	})
	defer m.ReclaimAll()
	task, err := docs.NewStore(ws).Create("work", "", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoopWith("demo", WorkLoopOptions{Parallel: 2}); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "provider wait", func() bool { s, _ := m.GetWorkLoop("demo"); return s.WaitKind == "network" })
	mustResize(t, m, 1)
	final := waitLoopFinished(t, m, "demo")
	want := "loop stalled: 17 consecutive sessions made no progress (ready: " + task.ID + "); gave up after 12h"
	if starts.Load() != 1 || final.Outcome != want {
		t.Fatalf("starts = %d, outcome = %q; want 1, %q", starts.Load(), final.Outcome, want)
	}
}

func TestWorkLoopResizeRearmsWaiters(t *testing.T) {
	m, _, ws := loopTestManager(t, nil)
	defer m.ReclaimAll()
	wl := &workLoop{m: m, workspace: ws, loopID: "resize", parallel: 1, state: "waiting", holders: map[string]struct{}{"busy": {}}}
	var woke bool
	wl.waiters = []*loopWaiter{{id: "waiter", nudge: func() { woke = wl.admitWake("waiter") }}}
	m.workLoops[ws] = wl
	if wl.admitWake("waiter") {
		t.Fatal("wake admitted at capacity")
	}
	mustResize(t, m, 2)
	if !woke {
		t.Fatal("increased capacity did not re-arm denied wake")
	}
	mustResize(t, m, 1)
	if !wl.admitWake("waiter") {
		t.Fatal("shrink evicted woken waiter")
	}
	if wl.admitWake("other") {
		t.Fatal("shrink allowed extra wake")
	}
}

func TestWorkLoopResizeConcurrent(t *testing.T) {
	f := newParallelFake(t, 1, 2)
	defer f.m.ReclaimAll()
	if _, err := f.m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	// Keep the first claim outstanding while resize, Get and Stop contend.
	awaitCond(t, "first runner", func() bool { return f.startedCount() == 1 })
	var wg sync.WaitGroup
	start := make(chan struct{})
	resized := make(chan struct{}, 16)
	for n := 1; n <= 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			snap, err := f.m.ResizeWorkLoop("demo", "", n)
			if err != nil {
				if !errors.Is(err, ErrLoopStopping) {
					t.Errorf("resize %d: %v", n, err)
				}
			} else if snap.Parallel != n {
				t.Errorf("resize %d reported %d", n, snap.Parallel)
			}
			resized <- struct{}{}
			if s, err := f.m.GetWorkLoop("demo"); err != nil || s == nil {
				t.Errorf("Get: %+v, %v", s, err)
			}
		}()
	}
	close(start)
	// Exercise concurrent successful writes as well as the race with stop.
	for i := 0; i < 4; i++ {
		<-resized
	}
	stop, err := f.m.StopWorkLoop("demo")
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if p, ok := readPersistedWorkLoop(f.ws); !ok || p.Parallel != stop.Parallel || p.State != "stopping" {
		t.Fatalf("stale persisted state after stop: %+v (stop %+v)", p, stop)
	}
	f.letClaim(1)
	f.finish(1, parallelEnd{done: true})
	if final := waitLoopFinished(t, f.m, "demo"); final.Parallel != stop.Parallel || final.SessionsRun != 1 {
		t.Fatalf("final: %+v", final)
	}
}
