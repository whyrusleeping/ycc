package session

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
)

// fakeClock is a controllable clock for deterministic reaper tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// idleSession registers a minimal idle session in the manager and returns it.
func idleSession(t *testing.T, m *Manager, id string) *Session {
	t.Helper()
	s := newStopSession(t)
	s.ID = id
	s.setStatus(event.StatusIdle)
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return s
}

// An idle session is reaped only AFTER IdleTimeout elapses.
func TestReaperReapsAfterTimeout(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	clk := newFakeClock()
	r := newReaper(m, GCConfig{IdleTimeout: 10 * time.Minute, Now: clk.Now})
	idleSession(t, m, "s_idle")

	// First tick records idleSince; not yet past threshold.
	r.tick()
	if _, ok := m.Get("s_idle"); !ok {
		t.Fatal("session reaped on first observation")
	}
	// Advance short of the threshold: still alive.
	clk.Advance(9 * time.Minute)
	r.tick()
	if _, ok := m.Get("s_idle"); !ok {
		t.Fatal("session reaped before idle timeout elapsed")
	}
	// Advance past the threshold: reaped and removed.
	clk.Advance(2 * time.Minute)
	r.tick()
	if _, ok := m.Get("s_idle"); ok {
		t.Fatal("session not reaped after idle timeout elapsed")
	}
}

// A session blocked on a pending ask_user question is NOT reaped.
func TestReaperSkipsPendingQuestion(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	clk := newFakeClock()
	r := newReaper(m, GCConfig{IdleTimeout: time.Minute, Now: clk.Now})
	s := idleSession(t, m, "s_pending")

	// Simulate a blocked ask_user: a pending waiting channel makes reapable()
	// false even though status is idle.
	s.inter.mu.Lock()
	s.inter.waiting = make(chan string, 1)
	s.inter.mu.Unlock()

	if s.reapable() {
		t.Fatal("session with pending question reported reapable")
	}
	r.tick()
	clk.Advance(time.Hour)
	r.tick()
	if _, ok := m.Get("s_pending"); !ok {
		t.Fatal("session with pending question was reaped")
	}
}

// A paused (or pausing) session is NOT reaped.
func TestReaperSkipsPaused(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	clk := newFakeClock()
	r := newReaper(m, GCConfig{IdleTimeout: time.Minute, Now: clk.Now})
	s := idleSession(t, m, "s_paused")
	s.steerMu.Lock()
	s.paused = true
	s.steerMu.Unlock()

	if s.reapable() {
		t.Fatal("paused session reported reapable")
	}
	r.tick()
	clk.Advance(time.Hour)
	r.tick()
	if _, ok := m.Get("s_paused"); !ok {
		t.Fatal("paused session was reaped")
	}

	// pauseReq (pause requested but not yet effective) also blocks reaping.
	s.steerMu.Lock()
	s.paused = false
	s.pauseReq = true
	s.steerMu.Unlock()
	if s.reapable() {
		t.Fatal("pausing session reported reapable")
	}
}

// The idle timer resets when a session leaves and re-enters the idle state.
func TestReaperTimerResetsOnLeavingIdle(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	clk := newFakeClock()
	r := newReaper(m, GCConfig{IdleTimeout: 10 * time.Minute, Now: clk.Now})
	s := idleSession(t, m, "s_reset")

	r.tick() // records idleSince
	clk.Advance(9 * time.Minute)

	// Session goes busy then idle again before the threshold.
	s.setStatus(event.StatusRunning)
	r.tick() // drops idleSince
	if _, ok := r.idleSince["s_reset"]; ok {
		t.Fatal("idleSince not cleared when session left idle")
	}
	s.setStatus(event.StatusIdle)
	r.tick() // records a fresh idleSince

	// Advancing past the ORIGINAL window but short of the new one keeps it alive.
	clk.Advance(2 * time.Minute)
	r.tick()
	if _, ok := m.Get("s_reset"); !ok {
		t.Fatal("session reaped despite idle timer reset")
	}
}

// Reaping an idle session frees memory WITHOUT terminating it: no
// session_stopped marker is recorded, so the durable log stays reopenable
// (spec §18.6 — a GC'd idle session can still be resumed / re-entered). This is
// the regression guard for the bug where reaping made a session un-reopenable.
func TestReaperDoesNotHardStop(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	clk := newFakeClock()
	r := newReaper(m, GCConfig{IdleTimeout: time.Minute, Now: clk.Now})
	s := idleSession(t, m, "s_reap")

	r.tick()
	clk.Advance(time.Hour)
	r.tick()

	if _, ok := m.Get("s_reap"); ok {
		t.Fatal("idle session was not reaped after timeout")
	}
	if n := countType(s.log.Snapshot(), event.SessionStopped); n != 0 {
		t.Fatalf("reaped session recorded %d session_stopped events, want 0", n)
	}
	if proj := event.Reduce(s.log.Snapshot()); proj.Status == event.StatusStopped {
		t.Fatal("reaped session reduced to StatusStopped; it must stay reopenable")
	}
}

// A disabled reaper (no idle timeout, no retention) launches no goroutine and
// reaps nothing.
func TestStartReaperDisabled(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	idleSession(t, m, "s_keep")
	m.StartReaper(context.Background(), GCConfig{})
	// Give any (erroneously launched) goroutine a chance to run.
	time.Sleep(20 * time.Millisecond)
	if _, ok := m.Get("s_keep"); !ok {
		t.Fatal("disabled reaper reaped a session")
	}
}

// pruneLogs removes only stale, non-live session dirs and leaves recent ones and
// live sessions' dirs intact.
func TestReaperPruneLogs(t *testing.T) {
	ws := t.TempDir()
	m := NewManager(nil, ws)
	clk := newFakeClock()
	r := newReaper(m, GCConfig{LogRetention: 7 * 24 * time.Hour, Now: clk.Now})

	sessionsDir := filepath.Join(ws, ".ycc", "sessions")
	mk := func(id string, age time.Duration) {
		dir := filepath.Join(sessionsDir, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "events.jsonl")
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mod := clk.Now().Add(-age)
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}

	mk("s_stale", 30*24*time.Hour) // older than retention => pruned
	mk("s_recent", 1*24*time.Hour) // within retention => kept
	mk("s_live", 30*24*time.Hour)  // stale on disk but currently live => kept

	// Mark s_live as a live session.
	live := idleSession(t, m, "s_live")
	_ = live

	r.pruneLogs(clk.Now())

	if _, err := os.Stat(filepath.Join(sessionsDir, "s_stale")); !os.IsNotExist(err) {
		t.Fatal("stale session dir was not pruned")
	}
	if _, err := os.Stat(filepath.Join(sessionsDir, "s_recent")); err != nil {
		t.Fatal("recent session dir was wrongly pruned")
	}
	if _, err := os.Stat(filepath.Join(sessionsDir, "s_live")); err != nil {
		t.Fatal("live session dir was wrongly pruned")
	}
}
