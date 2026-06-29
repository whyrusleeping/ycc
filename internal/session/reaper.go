package session

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"
)

// GCConfig configures the background session reaper. A zero IdleTimeout disables
// idle-session reaping; a zero LogRetention disables on-disk log pruning; with
// both zero the reaper is a no-op (conservative by default). Now is injectable
// for deterministic tests (defaults to time.Now).
//
// "Idle" for reaping means a session in StatusIdle with no pending ask_user
// question and not paused/pausing for steer (see Session.reapable). A turn
// blocked waiting for a user's ask_user answer stays StatusRunning, so it is
// never reaped — the reaper only reclaims sessions that have genuinely gone
// quiet between turns and been left untouched.
//
// LogRetention prunes on-disk session logs at
// <workspace>/.ycc/sessions/<id>/events.jsonl whose events.jsonl has not been
// modified within the retention window. It is OFF by default because those logs
// back the durable session index / history + reopen view (tasks 0033/0034):
// enabling retention discards logs a user could otherwise reopen or inspect.
// Live sessions' directories are never pruned.
type GCConfig struct {
	Interval     time.Duration
	IdleTimeout  time.Duration
	LogRetention time.Duration
	Now          func() time.Time
}

// reaper performs periodic idle-session GC and on-disk log retention for a
// Manager. Elapsed idle time is measured against the reaper's own clock (via
// idleSince) rather than session-set timestamps, so everything is controllable
// with an injected fake clock in tests.
type reaper struct {
	m   *Manager
	cfg GCConfig
	now func() time.Time
	// idleSince records when each session was FIRST observed reapable; a session
	// that leaves the reapable state has its entry dropped so the timer resets.
	idleSince map[string]time.Time
}

func newReaper(m *Manager, cfg GCConfig) *reaper {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &reaper{m: m, cfg: cfg, now: cfg.Now, idleSince: map[string]time.Time{}}
}

// tick performs one GC pass: it reaps sessions idle past IdleTimeout and prunes
// stale on-disk logs past LogRetention. It is driven directly by tests; the
// StartReaper goroutine calls it on a ticker.
func (r *reaper) tick() {
	now := r.now()

	if r.cfg.IdleTimeout > 0 {
		seen := map[string]bool{}
		for _, s := range r.m.List() {
			seen[s.ID] = true
			if !s.reapable() {
				// Left (or never entered) the idle window: reset its timer.
				delete(r.idleSince, s.ID)
				continue
			}
			first, ok := r.idleSince[s.ID]
			if !ok {
				r.idleSince[s.ID] = now
				continue
			}
			if now.Sub(first) >= r.cfg.IdleTimeout {
				r.m.Stop(s.ID)
				delete(r.idleSince, s.ID)
			}
		}
		// Drop bookkeeping for sessions that are no longer live.
		for id := range r.idleSince {
			if !seen[id] {
				delete(r.idleSince, id)
			}
		}
	}

	if r.cfg.LogRetention > 0 {
		r.pruneLogs(now)
	}
}

// pruneLogs removes on-disk session directories whose events.jsonl is older than
// LogRetention, across the manager's default workspace and every registered
// project workspace. It is best-effort (IO errors are ignored) and never touches
// a directory belonging to a currently-live session.
func (r *reaper) pruneLogs(now time.Time) {
	workspaces := map[string]bool{}
	if r.m.defaultWorkspace != "" {
		if abs, err := filepath.Abs(r.m.defaultWorkspace); err == nil {
			workspaces[abs] = true
		}
	}
	for _, p := range r.m.projects.List() {
		if p.Path != "" {
			workspaces[p.Path] = true
		}
	}

	for ws := range workspaces {
		dir := filepath.Join(ws, ".ycc", "sessions")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // no sessions dir / unreadable: nothing to prune here
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			id := e.Name()
			if _, live := r.m.Get(id); live {
				continue // never prune a live session's log
			}
			logPath := filepath.Join(dir, id, "events.jsonl")
			fi, err := os.Stat(logPath)
			if err != nil {
				continue // no events.jsonl: leave the dir alone
			}
			if now.Sub(fi.ModTime()) >= r.cfg.LogRetention {
				os.RemoveAll(filepath.Join(dir, id)) // best-effort
			}
		}
	}
}

// StartReaper launches the background GC reaper for the manager, governed by cfg.
// It returns immediately without starting a goroutine when the reaper is fully
// disabled (both IdleTimeout and LogRetention zero). The goroutine stops when ctx
// is cancelled. Interval defaults to one minute and Now defaults to time.Now.
func (m *Manager) StartReaper(ctx context.Context, cfg GCConfig) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.IdleTimeout <= 0 && cfg.LogRetention <= 0 {
		return // disabled — nothing to do
	}
	r := newReaper(m, cfg)
	go func() {
		t := time.NewTicker(cfg.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.tick()
			}
		}
	}()
	log.Printf("session GC reaper started: interval=%s idle_timeout=%s log_retention=%s",
		cfg.Interval, cfg.IdleTimeout, cfg.LogRetention)
}
