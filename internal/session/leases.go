package session

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
)

// Exclusive named leases (spec §7.3): a coordinator holds a project-wide lease
// on a target such as a deploy or a shared service while it changes it. Leases
// live only in daemon memory, so a daemon restart releases them all; acquiring
// a held lease fails at once, naming the holder, and never waits.

// Lease bounds.
const (
	leaseMaxName   = 80 // runes
	leaseMaxReason = 200
	leaseMinTTL    = time.Minute
	leaseMaxTTL    = 24 * time.Hour
	leaseMaxHeld   = 8 // leases one session may hold at once
)

// Release reasons recorded as lease_released's why.
const (
	leaseReleased     = "released"
	leaseExpired      = "expired"
	leaseJobExited    = "job_exited"
	leaseSessionEnded = "session_ended"
)

// Lease is one held lease as listings report it.
type Lease struct {
	Name       string
	Reason     string
	SessionID  string
	Tasks      []string // holder's focused tasks when it acquired the lease
	JobID      string   // the background job the lease ends with; empty when none
	AcquiredAt time.Time
	ExpiresAt  time.Time
	holder     *Session
}

// leaseEntry is a held lease with its release machinery. Entries are replaced,
// never mutated, so a stale timer or job watcher can tell it lost the lease.
type leaseEntry struct {
	Lease
	holder *Session
	job    *jobs.Job
	timer  *time.Timer
	stop   chan struct{} // closed once the entry is released or replaced
}

// live reports whether the entry still holds its lease: unexpired, and its
// job still executing or its holder session not ended. A timer or hook that
// has not run yet never keeps a dead lease held, and a stopping session's
// lease stays held while its job is still running.
func (e *leaseEntry) live(now time.Time) bool {
	return now.Before(e.ExpiresAt) && (e.jobRunning() || e.holder.ctx.Err() == nil)
}

func (e *leaseEntry) jobRunning() bool { return e.job != nil && e.job.ExecutionRunning() }

// normalizeLeaseName makes a lease name one printable lowercase line, so
// "Prod-DB" and "prod-db" name the same target.
func normalizeLeaseName(name string) string {
	return strings.ToLower(sanitizeLine(name))
}

// acquireLease takes, renews or re-attaches the lease name in project primary
// for s. Another live holder makes it fail at once. Renewing (s already holds
// it) replaces the reason and expiry and, when jobID is set, the job; without
// jobID a renewal keeps the job it had.
func (m *Manager) acquireLease(s *Session, primary, name, reason string, ttl time.Duration, jobID string, now time.Time) (Lease, bool, error) {
	name = normalizeLeaseName(name)
	reason = sanitizeLine(reason)
	jobID = strings.TrimSpace(jobID)
	switch {
	case name == "":
		return Lease{}, false, errors.New("name is empty")
	case utf8.RuneCountInString(name) > leaseMaxName:
		return Lease{}, false, fmt.Errorf("name is %d characters; the limit is %d", utf8.RuneCountInString(name), leaseMaxName)
	case reason == "":
		return Lease{}, false, errors.New("reason is required")
	case utf8.RuneCountInString(reason) > leaseMaxReason:
		return Lease{}, false, fmt.Errorf("reason is %d characters; the limit is %d", utf8.RuneCountInString(reason), leaseMaxReason)
	case ttl < leaseMinTTL || ttl > leaseMaxTTL:
		return Lease{}, false, fmt.Errorf("ttl %s is outside %s to %s", ttl, leaseMinTTL, leaseMaxTTL)
	case s.ctx.Err() != nil:
		return Lease{}, false, errors.New("this session has ended")
	}
	var job *jobs.Job
	if jobID != "" {
		var ok bool
		if s.deps != nil && s.deps.Jobs != nil {
			job, ok = s.deps.Jobs.Get(jobID)
		}
		if !ok || job.Status() != jobs.Running {
			return Lease{}, false, fmt.Errorf("%s is not a running background job of this session", jobID)
		}
	}
	tasks := focusTasks(s.Log().Snapshot())
	if len(tasks) > 3 {
		tasks = tasks[len(tasks)-3:]
	}
	key := canonicalPath(primary)

	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()
	m.pruneLeasesLocked(key, now)
	held := m.leases[key]
	cur := held[name]
	if cur != nil && cur.holder != s {
		return Lease{}, false, fmt.Errorf("lease %q is held by %s; leases never wait: work on something else, and look again later "+
			"(the list_backlog footer shows held leases)", name, cur.Lease.describe(now))
	}
	if cur == nil {
		n := 0
		for _, e := range held {
			if e.holder == s {
				n++
			}
		}
		if n >= leaseMaxHeld {
			return Lease{}, false, fmt.Errorf("this session already holds %d leases (the limit); release one first", n)
		}
	}
	e := &leaseEntry{
		Lease:  Lease{Name: name, Reason: reason, SessionID: s.ID, Tasks: tasks, AcquiredAt: now.UTC(), ExpiresAt: now.Add(ttl).UTC(), holder: s},
		holder: s, job: job, stop: make(chan struct{}),
	}
	renewed := cur != nil
	if renewed {
		e.AcquiredAt = cur.AcquiredAt
		if job == nil {
			e.job = cur.job
		}
		cur.end()
	}
	if e.job != nil {
		e.JobID = e.job.ID()
	}
	if held == nil {
		held = map[string]*leaseEntry{}
		if m.leases == nil {
			m.leases = map[string]map[string]*leaseEntry{}
		}
		m.leases[key] = held
	}
	held[name] = e
	e.timer = time.AfterFunc(time.Until(e.ExpiresAt), func() { m.endLease(key, e, leaseExpired) })
	if e.job != nil {
		go func(j *jobs.Job) {
			select {
			case <-j.Done():
				j.WaitExecution()
				m.endLease(key, e, leaseJobExited)
			case <-e.stop:
			}
		}(e.job)
	}
	// Recorded under leaseMu, so a release can never be logged before it.
	data := map[string]any{"name": name, "reason": reason, "expires_at": e.ExpiresAt.Format(time.RFC3339)}
	if e.JobID != "" {
		data["job"] = e.JobID
	}
	if renewed {
		data["renewed"] = true
	}
	s.emitter.Emit(event.LeaseAcquired, data)
	return e.Lease, renewed, nil
}

// end stops the entry's timer and job watcher. The caller holds leaseMu and
// has removed or replaced the entry.
func (e *leaseEntry) end() {
	close(e.stop)
	if e.timer != nil {
		e.timer.Stop()
	}
}

// releaseLocked removes e from held and records why in its holder's log. The
// caller holds leaseMu.
func releaseLocked(held map[string]*leaseEntry, e *leaseEntry, why string) {
	delete(held, e.Name)
	e.end()
	e.holder.emitter.Emit(event.LeaseReleased, map[string]any{"name": e.Name, "why": why})
}

// pruneLeasesLocked releases key's leases that expired or whose holder ended
// before their timer or hook ran. The caller holds leaseMu.
func (m *Manager) pruneLeasesLocked(key string, now time.Time) {
	held := m.leases[key]
	for _, e := range held {
		if e.live(now) {
			continue
		}
		why := leaseExpired
		if now.Before(e.ExpiresAt) {
			why = leaseSessionEnded
		}
		releaseLocked(held, e, why)
	}
}

// endLease releases e for why if it still holds its lease (a timer or job
// watcher of a replaced or released entry does nothing).
func (m *Manager) endLease(key string, e *leaseEntry, why string) {
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()
	if held := m.leases[key]; held[e.Name] == e {
		releaseLocked(held, e, why)
	}
}

// releaseLease gives up s's lease name in project primary.
func (m *Manager) releaseLease(s *Session, primary, name string, now time.Time) (Lease, error) {
	name = normalizeLeaseName(name)
	key := canonicalPath(primary)
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()
	m.pruneLeasesLocked(key, now)
	held := m.leases[key]
	e := held[name]
	switch {
	case e == nil:
		return Lease{}, fmt.Errorf("no lease %q is held (it may have expired or ended with its job)", name)
	case e.holder != s:
		return Lease{}, fmt.Errorf("lease %q is held by %s, not this session", name, e.Lease.describe(now))
	}
	releaseLocked(held, e, leaseReleased)
	return e.Lease, nil
}

// releaseSessionLeases releases the leases s holds, at session end. Its jobs
// have been killed and joined by then; a lease whose job is somehow still
// executing stays held until its job watcher releases it.
func (m *Manager) releaseSessionLeases(s *Session) {
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()
	for _, held := range m.leases {
		for _, e := range held {
			if e.holder == s && !e.jobRunning() {
				releaseLocked(held, e, leaseSessionEnded)
			}
		}
	}
}

// projectLeases returns primary's held leases ordered by name.
func (m *Manager) projectLeases(primary string, now time.Time) []Lease {
	key := canonicalPath(primary)
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()
	m.pruneLeasesLocked(key, now)
	var out []Lease
	for _, e := range m.leases[key] {
		out = append(out, e.Lease)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// leasesBySession groups primary's held leases by holder session.
func (m *Manager) leasesBySession(primary string, now time.Time) map[string][]Lease {
	out := map[string][]Lease{}
	for _, l := range m.projectLeases(primary, now) {
		out[l.SessionID] = append(out[l.SessionID], l)
	}
	return out
}

// describe names a lease's holder, reason and expiry for a refusal.
func (l Lease) describe(now time.Time) string {
	s := "session " + l.SessionID
	if len(l.Tasks) > 0 {
		s += " (task " + strings.Join(l.Tasks, ", ") + ")"
	}
	return fmt.Sprintf("%s for %q since %s, expiring %s (%s)%s", s, l.Reason, l.AcquiredAt.UTC().Format("15:04 UTC"),
		l.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"), untilText(l.ExpiresAt, now), l.jobNote())
}

// line formats a held lease under its session in session listings.
func (l Lease) line(now time.Time) string {
	return fmt.Sprintf("lease %q: %q, expires %s (%s)%s", l.Name, l.Reason, l.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"),
		untilText(l.ExpiresAt, now), l.jobNote())
}

func (l Lease) jobNote() string {
	if l.JobID == "" {
		return ""
	}
	return ", or when " + l.JobID + " exits"
}

// sessionLeaseTools is a coordinator's orchestrator.Leases.
type sessionLeaseTools struct {
	m       *Manager
	s       *Session
	primary string
}

func (t *sessionLeaseTools) Acquire(name, reason string, ttl time.Duration, jobID string) (string, error) {
	l, renewed, err := t.m.acquireLease(t.s, t.primary, name, reason, ttl, jobID, time.Now())
	if err != nil {
		return "", err
	}
	verb := "Acquired"
	if renewed {
		verb = "Renewed"
	}
	end := "on release_lease, at expiry, or when this session ends"
	if l.JobID != "" {
		end = "on release_lease, at expiry, when " + l.JobID + " exits, or when this session ends"
	}
	return fmt.Sprintf("%s lease %q until %s. Other sessions cannot acquire it; it is released %s. "+
		"Release it as soon as the work on the target is done.", verb, l.Name, l.ExpiresAt.Format("2006-01-02 15:04 UTC"), end), nil
}

func (t *sessionLeaseTools) Release(name string) (string, error) {
	l, err := t.m.releaseLease(t.s, t.primary, name, time.Now())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Released lease %q.", l.Name), nil
}

// releaseLeases releases the session's leases at session end. Nil-safe for
// minimally-constructed sessions.
func (s *Session) releaseLeases() {
	if s.leases != nil {
		s.leases.m.releaseSessionLeases(s)
	}
}
