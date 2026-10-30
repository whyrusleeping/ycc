// Package health periodically checks the daemon's external dependencies —
// model providers, the Exa web API, and free disk space — and keeps the latest
// result of each check so clients can alert the user when one starts failing.
package health

import (
	"context"
	"sync"
	"time"
)

// Status is a check outcome. Clients alert on StatusWarn and StatusFail.
type Status string

const (
	StatusPending Status = "pending" // not checked yet
	StatusOK      Status = "ok"
	StatusWarn    Status = "warn"
	StatusFail    Status = "fail"
	StatusOff     Status = "off" // intentionally not configured; not an alert
)

// Check is the latest result of one health check.
type Check struct {
	ID        string
	Label     string
	Status    Status
	Detail    string
	Remedy    string
	CheckedAt time.Time // zero while pending
}

// Target is one check the monitor runs. Run fills Status/Detail/Remedy; the
// monitor stamps ID, Label, and CheckedAt.
type Target struct {
	ID       string
	Label    string
	Interval time.Duration
	Run      func(ctx context.Context) Check
}

const (
	// rescanInterval bounds how long a newly configured target waits for its
	// first check, since the target set is recomputed on every wake.
	rescanInterval = 5 * time.Minute
	// recheckAfter debounces observation-triggered re-checks of one target.
	recheckAfter = 2 * time.Minute
	// suspectDelay lets a burst of failing real requests coalesce into one probe.
	suspectDelay = 15 * time.Second
	checkTimeout = 45 * time.Second
	startupDelay = 30 * time.Second
)

// Monitor schedules checks and holds their latest results.
type Monitor struct {
	targets func() []Target
	now     func() time.Time
	wake    chan struct{}

	mu      sync.Mutex
	results map[string]Check
	last    map[string]time.Time // last probe or observed real success
	suspect map[string]time.Time // first observed real failure since last
	running map[string]chan struct{}
}

// New returns a monitor over the targets produced by targets, which is called
// on every scheduling pass so configuration changes are picked up.
func New(targets func() []Target) *Monitor {
	return &Monitor{
		targets: targets,
		now:     time.Now,
		wake:    make(chan struct{}, 1),
		results: map[string]Check{},
		last:    map[string]time.Time{},
		suspect: map[string]time.Time{},
		running: map[string]chan struct{}{},
	}
}

// Run schedules checks until ctx is done.
func (m *Monitor) Run(ctx context.Context) {
	timer := time.NewTimer(startupDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-m.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		timer.Reset(m.runDue(ctx))
	}
}

// runDue runs every due target, waits for them, and returns the delay until
// the next one is due.
func (m *Monitor) runDue(ctx context.Context) time.Duration {
	targets := m.targets()
	now := m.now()
	next := rescanInterval
	var due []Target
	m.mu.Lock()
	for _, t := range targets {
		at := m.dueAtLocked(t)
		if !at.After(now) {
			due = append(due, t)
			at = now.Add(t.Interval)
		}
		if d := at.Sub(now); d < next {
			next = d
		}
	}
	m.mu.Unlock()
	m.runAll(ctx, due)
	if next < time.Second {
		next = time.Second
	}
	return next
}

func (m *Monitor) dueAtLocked(t Target) time.Time {
	last, ok := m.last[t.ID]
	if !ok {
		return time.Time{}
	}
	at := last.Add(t.Interval)
	if s, ok := m.suspect[t.ID]; ok {
		re := s.Add(suspectDelay)
		if min := last.Add(recheckAfter); re.Before(min) {
			re = min
		}
		if re.Before(at) {
			at = re
		}
	}
	return at
}

func (m *Monitor) runAll(ctx context.Context, targets []Target) {
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.runOne(ctx, t)
		}()
	}
	wg.Wait()
}

// runOne starts t unless a probe of it is already running, then waits for that
// probe or for ctx. The probe itself is monitor-owned and bounded only by
// checkTimeout, so one caller giving up never discards a result another
// caller is waiting for.
func (m *Monitor) runOne(ctx context.Context, t Target) {
	m.mu.Lock()
	ch, ok := m.running[t.ID]
	if !ok {
		ch = make(chan struct{})
		m.running[t.ID] = ch
		// The probe answers any failure observed so far; failures observed
		// while it runs leave a fresh mark and get their own re-check.
		delete(m.suspect, t.ID)
		go m.probe(t, ch)
	}
	m.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

func (m *Monitor) probe(t Target, done chan struct{}) {
	started := m.now()
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	res := t.Run(ctx)
	cancel()

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.running, t.ID)
	defer close(done)
	// A real request that succeeded while the probe ran is better evidence
	// than a probe that failed (e.g. timed out) meanwhile.
	if res.Status != StatusOK && m.last[t.ID].After(started) {
		return
	}
	res.ID, res.Label, res.CheckedAt = t.ID, t.Label, m.now()
	m.results[t.ID] = res
	m.last[t.ID] = res.CheckedAt
}

// Refresh runs every check now and returns the resulting snapshot.
func (m *Monitor) Refresh(ctx context.Context) []Check {
	m.runAll(ctx, m.targets())
	return m.Snapshot()
}

// Snapshot returns the latest result of every currently configured check,
// without running anything.
func (m *Monitor) Snapshot() []Check {
	targets := m.targets()
	out := make([]Check, 0, len(targets))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range targets {
		c, ok := m.results[t.ID]
		if !ok {
			c = Check{ID: t.ID, Status: StatusPending}
		}
		c.Label = t.Label
		out = append(out, c)
	}
	return out
}

// Observe records the outcome of a real (non-probe) request for check id. A
// success marks the check healthy and postpones its next probe; a failure
// schedules a confirming probe soon, since one failed request may be specific
// to that request rather than the dependency.
func (m *Monitor) Observe(id string, err error) {
	if m == nil || id == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if err == nil {
		m.results[id] = Check{ID: id, Status: StatusOK, Detail: "A recent real request succeeded.", CheckedAt: now}
		m.last[id] = now
		delete(m.suspect, id)
		return
	}
	if _, ok := m.suspect[id]; ok {
		return
	}
	m.suspect[id] = now
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
