package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/jobshim"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// Exclusive named leases (spec §7.3): a coordinator holds a project-wide lease
// on a target such as a deploy target or a shared service while it changes it.
// Acquiring a held lease fails at once, naming the holder, and never waits.
//
// A lease belongs to its holder's session id, not to one in-memory instance
// (docs/design/daemon-restart.md §5). While the holder session is closed (it
// stopped while its job still runs, or the daemon restarted), a job-bound
// lease is held passively until its job's execution has stopped or it expires,
// and its release is recorded in the closed session's log. A reopen of the
// session takes it over. Job-bound leases are also persisted in leases.json
// (SetLeasesFile), so they survive a daemon restart or crash while their job
// runs; other leases live in memory only, and a planned restart's manifest
// carries them to the next daemon (restart_state.go).

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
	// The holder session's mode and workspace, for listing a holder that is
	// no longer live.
	mode, workspace string
	// restored marks a lease held for a session closed since before this
	// daemon started (a closed holder is otherwise still stopping).
	restored bool
	// acquiredSeq is the holder log's lease_acquired event of this
	// acquisition or renewal, which a restart manifest's record names.
	acquiredSeq int
}

// leaseEntry is a held lease with its release machinery. Entries are replaced,
// never mutated, so a stale timer or job watcher can tell it lost the lease.
type leaseEntry struct {
	Lease
	key   string   // canonical primary tree
	dir   string   // the holder's session directory
	owner *Session // the instance that holds it; nil when restored at startup
	// The job's execution, followed in process (job) or, for a job a closed
	// session left running across a daemon restart, through the passive
	// supervisor table (settled and taken, tools.PassiveJobs.Watch).
	job            *jobs.Job
	settled, taken <-chan struct{}
	// unverified marks a restored lease whose job nothing can follow (a
	// direct child of a crashed daemon may still run): it is held until it
	// expires or its holder releases it.
	unverified bool
	timer      *time.Timer
	stop       chan struct{} // closed once the entry is released or replaced
}

// leaseNote is a release to record in a closed holder's log once leaseMu is
// no longer held (emitLeaseNotes).
type leaseNote struct {
	id, dir string
	owner   *Session
	data    map[string]any
}

// leaseRecord is a job-bound lease as leases.json stores it.
type leaseRecord struct {
	Primary    string    `json:"primary"`
	Name       string    `json:"name"`
	Reason     string    `json:"reason"`
	SessionID  string    `json:"session_id"`
	Workspace  string    `json:"workspace"`
	Mode       string    `json:"mode,omitempty"`
	Tasks      []string  `json:"tasks,omitempty"`
	JobID      string    `json:"job_id"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	// AcquiredSeq is the holder log's lease_acquired event (acquire or
	// renewal) the record was taken from.
	AcquiredSeq int `json:"acquired_seq,omitempty"`
}

type leaseFile struct {
	Version int           `json:"version"`
	Leases  []leaseRecord `json:"leases"`
}

// ownerLive reports whether the instance holding e is running.
func (e *leaseEntry) ownerLive() bool { return e.owner != nil && e.owner.ctx.Err() == nil }

// executing reports whether e's job may still be executing. A job whose
// execution is unknown (supervision lost without an exit record) still is.
func (e *leaseEntry) executing() bool {
	switch {
	case e.job != nil:
		return e.job.ExecutionRunning()
	case e.settled != nil:
		select {
		case <-e.settled:
			return false
		default:
			return true
		}
	}
	return e.unverified
}

// live reports whether the entry still holds its lease: unexpired, and its
// job still executing or its holder instance running. A restored lease
// without a job (a planned restart's) is held until it expires, and so is
// one whose holder a planned restart is suspending, which the restart
// manifest may carry (releaseSessionLeases decides). A timer or hook that has
// not run yet never keeps a dead lease held, and a stopping session's lease
// stays held while its job is still running.
func (e *leaseEntry) live(now time.Time) bool {
	if !now.Before(e.ExpiresAt) {
		return false
	}
	if e.executing() {
		return true
	}
	if e.owner != nil {
		if e.owner.ctx.Err() == nil {
			return true
		}
		return e.JobID == "" && errors.Is(context.Cause(e.owner.ctx), jobs.ErrRestartSuspend)
	}
	return e.JobID == ""
}

// normalizeLeaseName makes a lease name one printable lowercase line, so
// "Prod-DB" and "prod-db" name the same target.
func normalizeLeaseName(name string) string {
	return strings.ToLower(sanitizeLine(name))
}

// sessionDir is a session's directory under its workspace.
func sessionDir(workspace, id string) string {
	return filepath.Join(workspace, ".ycc", "sessions", id)
}

// SetLeasesFile persists job-bound leases at path (written on every change),
// so RestoreLeases can hold them again after a daemon restart or crash.
func (m *Manager) SetLeasesFile(path string) {
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()
	m.leasesFile = path
	m.leasesSaved = nil
}

// acquireLease takes, renews or re-attaches the lease name in project primary
// for s. Another session holding it makes it fail at once. Renewing (s
// already holds it) replaces the reason and expiry and, when jobID is set, the
// job; without jobID a renewal keeps the job it had. A job-bound lease is
// acknowledged only once leases.json records it.
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
	notes := m.pruneLeasesLocked(key, now)
	defer func() {
		m.leaseMu.Unlock()
		m.emitLeaseNotes(notes)
	}()
	held := m.leases[key]
	cur := held[name]
	if cur != nil && cur.SessionID != s.ID {
		return Lease{}, false, fmt.Errorf("lease %q is held by %s; leases never wait: work on something else, and look again later "+
			"(the list_backlog footer shows held leases)", name, cur.Lease.describe(now))
	}
	if cur == nil {
		n := 0
		for _, e := range held {
			if e.SessionID == s.ID {
				n++
			}
		}
		if n >= leaseMaxHeld {
			return Lease{}, false, fmt.Errorf("this session already holds %d leases (the limit); release one first", n)
		}
	}
	e := &leaseEntry{
		Lease: Lease{Name: name, Reason: reason, SessionID: s.ID, Tasks: tasks, AcquiredAt: now.UTC(), ExpiresAt: now.Add(ttl).UTC(),
			mode: s.Mode, workspace: s.Workspace},
		key: key, dir: sessionDir(s.Workspace, s.ID), owner: s, job: job,
	}
	renewed := cur != nil
	if renewed {
		e.AcquiredAt = cur.AcquiredAt
		if job == nil {
			e.job, e.settled, e.taken, e.unverified, e.JobID = cur.job, cur.settled, cur.taken, cur.unverified, cur.JobID
		}
	}
	if job != nil {
		e.JobID = job.ID()
	}
	m.placeLocked(e)
	if e.JobID != "" {
		if err := m.saveLeasesLocked(); err != nil {
			if cur != nil {
				m.leases[key][name] = cur
			} else {
				delete(m.leases[key], name)
			}
			return Lease{}, false, fmt.Errorf("the lease could not be saved, so it would not survive a daemon crash: %w", err)
		}
	}
	if cur != nil {
		cur.end()
	}
	m.armLocked(e)
	// Recorded under leaseMu, so a release can never be logged before it.
	data := map[string]any{"name": name, "reason": reason, "expires_at": e.ExpiresAt.Format(time.RFC3339)}
	if e.JobID != "" {
		data["job"] = e.JobID
	}
	if renewed {
		data["renewed"] = true
	}
	e.acquiredSeq = s.emitter.Emit(event.LeaseAcquired, data).Seq
	return e.Lease, renewed, nil
}

// installLocked makes e the held entry for its name, replacing (and stopping)
// any previous one, and starts its expiry timer and job watcher. The caller
// holds leaseMu.
func (m *Manager) installLocked(e *leaseEntry) {
	cur := m.leases[e.key][e.Name]
	m.placeLocked(e)
	if cur != nil && cur != e {
		cur.end()
	}
	m.armLocked(e)
}

// placeLocked makes e the held entry for its name without stopping the one it
// replaces or starting e's timer and watcher (armLocked). The caller holds
// leaseMu.
func (m *Manager) placeLocked(e *leaseEntry) {
	if m.leases == nil {
		m.leases = map[string]map[string]*leaseEntry{}
	}
	held := m.leases[e.key]
	if held == nil {
		held = map[string]*leaseEntry{}
		m.leases[e.key] = held
	}
	held[e.Name] = e
	e.stop = make(chan struct{})
}

// armLocked starts a placed entry's expiry timer and job watcher. The caller
// holds leaseMu.
func (m *Manager) armLocked(e *leaseEntry) {
	e.timer = time.AfterFunc(time.Until(e.ExpiresAt), func() { m.endLease(e, leaseExpired) })
	switch {
	case e.job != nil:
		go func(j *jobs.Job) {
			// Execution, not the report: a killed job holds its lease until
			// its process has actually stopped, and one whose execution is
			// unknown until the lease expires or is released.
			select {
			case <-j.Done():
			case <-e.stop:
				return
			}
			select {
			case <-j.ExecutionDone():
				m.endLease(e, leaseJobExited)
			case <-e.stop:
			}
		}(e.job)
	case e.settled != nil:
		go m.followPassiveLease(e)
	}
}

// followPassiveLease releases a lease whose job a closed session left running
// across a daemon restart once the passive supervisor table confirms the
// job's exit (an operator's cleared marker counts). When a reopen of the
// session takes the job over, the reopen transfers the lease (adoptLeases);
// one that fails holds the job passively again, and the lease follows it
// there.
func (m *Manager) followPassiveLease(e *leaseEntry) {
	select {
	case <-e.settled:
		m.endLease(e, leaseJobExited)
		return
	case <-e.stop:
		return
	case <-e.taken:
	}
	// The reopen holds the session id's lock until it has transferred the
	// lease or given up.
	unlock := m.lockSessionID(e.SessionID)
	defer unlock()
	m.leaseMu.Lock()
	if !m.currentLocked(e) {
		m.leaseMu.Unlock()
		return
	}
	var notes []*leaseNote
	if settled, taken, ok := m.passiveJobs.Watch(sessionKey(e.dir), e.JobID); ok {
		next := *e
		next.settled, next.taken = settled, taken
		m.installLocked(&next)
	} else if e.exitEvidence() {
		if n, _ := m.dropLocked(e, leaseJobExited, nil, false); n != nil {
			notes = append(notes, n)
		}
	} else {
		// Not held again, without evidence that the job stopped.
		next := *e
		next.settled, next.taken, next.unverified = nil, nil, true
		m.installLocked(&next)
	}
	m.leaseMu.Unlock()
	// This goroutine holds the id's lock already.
	for _, n := range notes {
		m.recordLeaseNote(n)
	}
}

// currentLocked reports whether e is still the held entry for its name. The
// caller holds leaseMu.
func (m *Manager) currentLocked(e *leaseEntry) bool {
	return m.leases[e.key][e.Name] == e
}

// end stops the entry's timer and job watcher. The caller holds leaseMu and
// has removed or replaced the entry.
func (e *leaseEntry) end() {
	close(e.stop)
	if e.timer != nil {
		e.timer.Stop()
	}
}

// dropLocked removes e and records why in its holder's log: through via when
// set (a session whose log is still open), or through its owner while that
// runs. Otherwise the holder is closed, and the release is returned for
// emitLeaseNotes, which records it under the session id's lock once leaseMu
// is released. A job-bound removal is committed to leases.json first, so a
// crash never records a release twice. When that fails, an explicit release
// (strict) fails and keeps the lease held; an automatic one (expiry, job
// exit) drops it from memory without recording the release, which the next
// startup records once from the record left on disk. The caller holds
// leaseMu.
func (m *Manager) dropLocked(e *leaseEntry, why string, via *Session, strict bool) (*leaseNote, error) {
	delete(m.leases[e.key], e.Name)
	if e.JobID != "" {
		if err := m.saveLeasesLocked(); err != nil {
			if strict {
				m.leases[e.key][e.Name] = e
				return nil, err
			}
			e.end()
			log.Printf("lease %q of session %s ended (%s), but its release is not recorded: %v", e.Name, e.SessionID, why, err)
			return nil, nil
		}
	}
	e.end()
	data := map[string]any{"name": e.Name, "why": why}
	switch {
	case via != nil:
		via.emitter.Emit(event.LeaseReleased, data)
	case e.ownerLive():
		e.owner.emitter.Emit(event.LeaseReleased, data)
	default:
		return &leaseNote{id: e.SessionID, dir: e.dir, owner: e.owner, data: data}, nil
	}
	return nil, nil
}

// emitLeaseNotes records closed holders' releases, each under its session
// id's lock (so never racing a reopen), in the background: a caller may hold
// another session's id lock. ReclaimAll joins them.
func (m *Manager) emitLeaseNotes(notes []*leaseNote) {
	if len(notes) == 0 {
		return
	}
	// Added under leaseMu, so closeLeases' Wait never races an Add; a
	// release left once the manager shut down goes unrecorded.
	m.leaseMu.Lock()
	closed := m.leasesClosed
	if !closed {
		m.leaseWG.Add(1)
	}
	m.leaseMu.Unlock()
	if closed {
		return
	}
	go func() {
		defer m.leaseWG.Done()
		for _, n := range notes {
			unlock := m.lockSessionID(n.id)
			m.recordLeaseNote(n)
			unlock()
		}
	}()
}

// recordLeaseNote records a closed holder's release: in the session's log
// through its live instance if it has reopened meanwhile, through the
// stopping instance whose log is still open, or else by appending to the
// closed log on disk (never creating one). The caller holds n.id's lock.
func (m *Manager) recordLeaseNote(n *leaseNote) {
	if s, ok := m.Get(n.id); ok {
		if ev := s.emitter.Emit(event.LeaseReleased, n.data); ev.Seq != 0 {
			return
		}
	}
	if n.owner != nil {
		if ev := n.owner.emitter.Emit(event.LeaseReleased, n.data); ev.Seq != 0 {
			return
		}
	}
	path := filepath.Join(n.dir, "events.jsonl")
	if n.dir == "" || nonEmptyLog(path) != nil {
		return
	}
	l, err := event.OpenLog(path)
	if err != nil {
		log.Printf("lease %v: recording its release in session %s: %v", n.data["name"], n.id, err)
		return
	}
	l.Record("coordinator", event.LeaseReleased, n.data)
	l.Close()
}

// pruneLeasesLocked releases key's leases that expired or whose holder ended
// before their timer or hook ran, returning the releases closed holders must
// still record. The caller holds leaseMu.
func (m *Manager) pruneLeasesLocked(key string, now time.Time) []*leaseNote {
	var notes []*leaseNote
	for _, e := range m.leases[key] {
		if e.live(now) {
			continue
		}
		why := leaseExpired
		switch {
		case !now.Before(e.ExpiresAt):
		case e.JobID != "" && !e.ownerLive():
			why = leaseJobExited
		default:
			why = leaseSessionEnded
		}
		if n, _ := m.dropLocked(e, why, nil, false); n != nil {
			notes = append(notes, n)
		}
	}
	return notes
}

// endLease releases e for why if it still holds its lease (a timer or job
// watcher of a replaced or released entry does nothing).
func (m *Manager) endLease(e *leaseEntry, why string) {
	var notes []*leaseNote
	m.leaseMu.Lock()
	if m.currentLocked(e) {
		if n, _ := m.dropLocked(e, why, nil, false); n != nil {
			notes = append(notes, n)
		}
	}
	m.leaseMu.Unlock()
	m.emitLeaseNotes(notes)
}

// releaseLease gives up s's lease name in project primary.
func (m *Manager) releaseLease(s *Session, primary, name string, now time.Time) (Lease, error) {
	name = normalizeLeaseName(name)
	key := canonicalPath(primary)
	m.leaseMu.Lock()
	notes := m.pruneLeasesLocked(key, now)
	defer func() {
		m.leaseMu.Unlock()
		m.emitLeaseNotes(notes)
	}()
	e := m.leases[key][name]
	switch {
	case e == nil:
		return Lease{}, fmt.Errorf("no lease %q is held (it may have expired or ended with its job)", name)
	case e.SessionID != s.ID:
		return Lease{}, fmt.Errorf("lease %q is held by %s, not this session", name, e.Lease.describe(now))
	}
	if _, err := m.dropLocked(e, leaseReleased, s, true); err != nil {
		return Lease{}, fmt.Errorf("lease %q is still held: its release could not be saved: %w", name, err)
	}
	return e.Lease, nil
}

// releaseSessionLeases releases the leases s holds, at session end, recording
// them through s, whose log is still open. Its jobs have been killed and
// joined, or detached for a daemon restart, by then: a lease whose job may
// still execute stays held, passively, until its job's execution stops (or
// past the restart, from leases.json) or it expires. A restart suspension
// (restart) of a daemon that writes a restart manifest also keeps each
// unexpired lease without a job: the manifest carries it to the next daemon
// (takeRestartLeases).
func (m *Manager) releaseSessionLeases(s *Session, restart bool) {
	now := time.Now()
	keepUnbound := restart && m.restartStateFile != ""
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()
	for _, held := range m.leases {
		for _, e := range held {
			if e.owner != s || e.executing() {
				continue
			}
			if keepUnbound && e.JobID == "" && now.Before(e.ExpiresAt) {
				continue
			}
			m.dropLocked(e, leaseSessionEnded, s, false)
		}
	}
}

// takeRestartLeases sets aside, for the restart manifest, every unexpired
// lease without a job still held once restart suspension has finished: those
// of the suspended sessions and those held for closed ones.
func (m *Manager) takeRestartLeases(now time.Time) {
	if m.restartStateFile == "" {
		return
	}
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()
	if m.leasesClosed {
		return
	}
	for _, held := range m.leases {
		for _, e := range held {
			if e.JobID == "" && now.Before(e.ExpiresAt) {
				m.restartLeases = append(m.restartLeases, e.record())
			}
		}
	}
	sortLeaseRecords(m.restartLeases)
}

// restoreUnboundLeases holds again, for their original expiry, the leases
// without a job that a planned restart's manifest carried (RecoverRestart,
// after RestoreLeases and before any session reopens). Each is held for its
// holder's session id like a closed session's lease: a reopen takes it over,
// and it is released at expiry otherwise. One whose acquisition its holder's
// log no longer shows held (released, renewed or acquired anew since) is not
// held again, so a manifest replayed by a later startup cannot resurrect it,
// and one that expired while the daemon was down is
// released, recorded in that log (so a repeated startup records it once). An
// unplanned crash writes no manifest, so it restores none of them.
func (m *Manager) restoreUnboundLeases(records []leaseRecord, now time.Time) {
	var entries []*leaseEntry
	var notes []*leaseNote
	for _, r := range records {
		name := normalizeLeaseName(r.Name)
		id := r.SessionID
		if name == "" || r.JobID != "" || id == "" || id == "." || filepath.Base(id) != id ||
			!filepath.IsAbs(r.Workspace) || !m.knownSessionWorkspace(r.Workspace) {
			log.Printf("restart recovery: dropping lease %q of session %q: not a lease of a known session", r.Name, id)
			continue
		}
		dir := sessionDir(r.Workspace, id)
		// The holder's log is the authority on whether this acquisition is
		// still held: a manifest a startup failed to remove is never replayed
		// over a release, renewal or new acquisition recorded since.
		if !leaseHeldInLog(dir, name, r.AcquiredSeq) {
			log.Printf("restart recovery: lease %q: session %s's log no longer shows this acquisition held; not holding it again", name, id)
			continue
		}
		if !now.Before(r.ExpiresAt) {
			notes = append(notes, &leaseNote{id: id, dir: dir, data: map[string]any{"name": name, "why": leaseExpired}})
			continue
		}
		entries = append(entries, &leaseEntry{
			Lease: Lease{Name: name, Reason: r.Reason, SessionID: id, Tasks: r.Tasks,
				AcquiredAt: r.AcquiredAt, ExpiresAt: r.ExpiresAt, mode: r.Mode, workspace: r.Workspace, restored: true,
				acquiredSeq: r.AcquiredSeq},
			key: canonicalPath(r.Primary), dir: dir,
		})
	}
	m.leaseMu.Lock()
	var placed []*leaseEntry
	for _, e := range entries {
		if cur := m.leases[e.key][e.Name]; cur != nil {
			log.Printf("restart recovery: lease %q is already held by session %s; dropping session %s's", e.Name, cur.SessionID, e.SessionID)
			continue
		}
		m.placeLocked(e)
		m.armLocked(e)
		placed = append(placed, e)
	}
	m.leaseMu.Unlock()
	for _, n := range notes {
		unlock := m.lockSessionID(n.id)
		m.recordLeaseNote(n)
		unlock()
	}
	m.adoptRestoredLeases(placed)
}

// adoptRestoredLeases hands leases restored at startup to their holders that
// are already live, as a reopen would.
func (m *Manager) adoptRestoredLeases(placed []*leaseEntry) {
	seen := map[string]bool{}
	for _, e := range placed {
		if seen[e.SessionID] {
			continue
		}
		seen[e.SessionID] = true
		unlock := m.lockSessionID(e.SessionID)
		if s, ok := m.Get(e.SessionID); ok {
			m.adoptLeases(s)
		}
		unlock()
	}
}

// leaseHeldInLog reports whether the session log in dir still holds the
// acquisition of lease name its event at seq recorded: that lease_acquired
// (an acquire or renewal) is the log's last lease event for the name.
func leaseHeldInLog(dir, name string, seq int) bool {
	if seq <= 0 {
		return false
	}
	events, err := event.ReadLog(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return false
	}
	held := false
	for _, ev := range events {
		if (ev.Type == event.LeaseAcquired || ev.Type == event.LeaseReleased) && normalizeLeaseName(str(ev.Data, "name")) == name {
			held = ev.Type == event.LeaseAcquired && ev.Seq == seq
		}
	}
	return held
}

// adoptLeases transfers the leases a closed instance of s's session holds to
// s, as it reopens (with its job registry restored and reconciled, under the
// session id's lock). A job-bound lease follows the reopened registry's job
// while that executes. A job the reopen took over from the passive table and
// reconciled as finished releases it. Otherwise it keeps following the old
// execution (in process or passively held) while that may run, or stays held
// unverified, and is released when there is evidence that its job stopped.
func (m *Manager) adoptLeases(s *Session) {
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()
	save := false
	for _, held := range m.leases {
		for _, e := range held {
			if e.SessionID != s.ID || e.owner == s {
				continue
			}
			next := *e
			next.owner, next.restored = s, false
			next.mode, next.workspace, next.dir = s.Mode, s.Workspace, sessionDir(s.Workspace, s.ID)
			if e.JobID != "" {
				save = true
				var rj *jobs.Job
				if s.deps != nil && s.deps.Jobs != nil {
					rj, _ = s.deps.Jobs.Get(e.JobID)
				}
				taken := false
				if e.taken != nil {
					select {
					case <-e.taken:
						taken = true
					default:
					}
				}
				switch {
				case rj != nil && rj.ExecutionRunning():
					next.job, next.settled, next.taken, next.unverified = rj, nil, nil, false
				case taken && e.exitEvidence():
					// Reconciliation found the job's exit record or an
					// operator's cleared marker.
					m.dropLocked(e, leaseJobExited, s, false)
					continue
				case taken:
					// The reopen took the job over without following it or
					// finding evidence that it stopped.
					next.settled, next.taken, next.unverified = nil, nil, true
				case e.executing():
				default:
					m.dropLocked(e, leaseJobExited, s, false)
					continue
				}
			}
			m.installLocked(&next)
		}
	}
	if save {
		_ = m.saveLeasesLocked()
	}
}

// RestoreLeases holds the job-bound leases leases.json records again at
// persistent startup, after SupervisePassiveJobs and before any session
// reopens (docs/design/daemon-restart.md §5). A lease whose job a closed
// session left running is held passively until the job's exit is confirmed
// (never while its execution is unknown) or the lease expires. A lease whose
// job left an exit record or an operator's cleared marker, or that expired
// while the daemon was down, is released, recorded in its session's log. A
// lease whose job nothing can follow (a direct child of a crashed daemon may
// still run) stays held until it expires or its holder, reopened, releases
// it. The reconciled set is saved before any release is recorded or any
// watcher starts, so a crash during restore replays the same records and
// never records a release twice. Leases without a job are restored only from
// a planned restart's manifest (restoreUnboundLeases).
func (m *Manager) RestoreLeases() {
	m.leaseMu.Lock()
	path := m.leasesFile
	m.leaseMu.Unlock()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var f leaseFile
	if err == nil {
		err = json.Unmarshal(data, &f)
	}
	if err != nil {
		// Kept for the operator rather than overwritten by the next save.
		aside := path + ".unreadable"
		log.Printf("restore leases: %s: %v; restoring none (moved to %s)", path, err, aside)
		_ = os.Rename(path, aside)
		return
	}
	now := time.Now()
	var restored []*leaseEntry
	var notes []*leaseNote
	for _, r := range f.Leases {
		e, n := m.restoredLease(r, now)
		if e != nil {
			restored = append(restored, e)
		}
		if n != nil {
			notes = append(notes, n)
		}
	}
	m.leaseMu.Lock()
	var placed []*leaseEntry
	for _, e := range restored {
		if cur := m.leases[e.key][e.Name]; cur != nil {
			log.Printf("restore leases: lease %q is already held by session %s; dropping session %s's record", e.Name, cur.SessionID, e.SessionID)
			continue
		}
		m.placeLocked(e)
		placed = append(placed, e)
	}
	m.leasesSaved = nil
	err = m.saveLeasesLocked()
	m.leaseMu.Unlock()
	if err == nil {
		for _, n := range notes {
			unlock := m.lockSessionID(n.id)
			m.recordLeaseNote(n)
			unlock()
		}
	}
	// Otherwise the released records stay on disk, and a later startup
	// records their releases once.
	m.leaseMu.Lock()
	for _, e := range placed {
		if m.currentLocked(e) {
			m.armLocked(e)
		}
	}
	m.leaseMu.Unlock()
	// A holder already live takes its leases over, as a reopen would.
	m.adoptRestoredLeases(placed)
}

// restoredLease turns one persisted record into the entry to hold, or into
// the release to record. An invalid record yields neither.
func (m *Manager) restoredLease(r leaseRecord, now time.Time) (*leaseEntry, *leaseNote) {
	name := normalizeLeaseName(r.Name)
	id := r.SessionID
	if name == "" || r.JobID == "" || id == "" || id == "." || filepath.Base(id) != id ||
		!filepath.IsAbs(r.Workspace) || !m.knownSessionWorkspace(r.Workspace) {
		log.Printf("restore leases: dropping lease %q of session %q: not a job-bound lease of a known session", r.Name, id)
		return nil, nil
	}
	dir := sessionDir(r.Workspace, id)
	released := func(why string) *leaseNote {
		return &leaseNote{id: id, dir: dir, data: map[string]any{"name": name, "why": why}}
	}
	if !now.Before(r.ExpiresAt) {
		return nil, released(leaseExpired)
	}
	e := &leaseEntry{
		Lease: Lease{Name: name, Reason: r.Reason, SessionID: id, Tasks: r.Tasks, JobID: r.JobID,
			AcquiredAt: r.AcquiredAt, ExpiresAt: r.ExpiresAt, mode: r.Mode, workspace: r.Workspace, restored: true},
		key: canonicalPath(r.Primary), dir: dir,
	}
	if settled, taken, ok := m.passiveJobs.Watch(sessionKey(dir), r.JobID); ok {
		e.settled, e.taken = settled, taken
		return e, nil
	}
	if e.exitEvidence() {
		return nil, released(leaseJobExited)
	}
	e.unverified = true
	return e, nil
}

// exitEvidence reports whether e's job left positive evidence on disk that it
// stopped: a supervised job's exit record or an operator's cleared marker.
func (e *leaseEntry) exitEvidence() bool {
	jobDir := filepath.Join(jobshim.JobsDir(sessionKey(e.dir)), e.JobID)
	_, err := jobshim.ReadExit(jobDir)
	return err == nil || jobshim.Cleared(jobDir)
}

// knownSessionWorkspace reports whether ws is a registered project's tree or
// a workstream worktree under the daemon's worktrees root, so a persisted
// lease record never makes the daemon write outside them.
func (m *Manager) knownSessionWorkspace(ws string) bool {
	key := canonicalPath(ws)
	if m.projects != nil {
		for _, p := range m.projects.List() {
			if canonicalPath(p.Path) == key {
				return true
			}
		}
	}
	if m.workstreams != nil {
		for _, w := range m.workstreams.List() {
			if w.WorktreePath != "" && canonicalPath(w.WorktreePath) == key && workstream.VerifyUnderRoot(m.worktreesRoot, w.WorktreePath) == nil {
				return true
			}
		}
	}
	return false
}

// saveLeasesLocked writes the held job-bound leases to leases.json (0600,
// atomically) when they changed. The caller holds leaseMu.
func (m *Manager) saveLeasesLocked() error {
	if m.leasesFile == "" || m.leasesClosed {
		return nil
	}
	f := leaseFile{Version: 1, Leases: []leaseRecord{}}
	for _, held := range m.leases {
		for _, e := range held {
			if e.JobID != "" {
				f.Leases = append(f.Leases, e.record())
			}
		}
	}
	sortLeaseRecords(f.Leases)
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if m.leasesSaved != nil && bytes.Equal(data, m.leasesSaved) {
		return nil
	}
	if err := writePrivateFile(m.leasesFile, append(data, '\n')); err != nil {
		log.Printf("save leases: %v", err)
		return err
	}
	m.leasesSaved = data
	return nil
}

// record is the entry as leases.json (or the restart manifest) stores it.
func (e *leaseEntry) record() leaseRecord {
	return leaseRecord{Primary: e.key, Name: e.Name, Reason: e.Reason, SessionID: e.SessionID,
		Workspace: e.workspace, Mode: e.mode, Tasks: e.Tasks, JobID: e.JobID, AcquiredAt: e.AcquiredAt, ExpiresAt: e.ExpiresAt,
		AcquiredSeq: e.acquiredSeq}
}

func sortLeaseRecords(rs []leaseRecord) {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Primary != b.Primary {
			return a.Primary < b.Primary
		}
		return a.Name < b.Name
	})
}

// writePrivateFile replaces path with data (0600) by an atomic rename.
func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// closeLeases stops every lease's timer and watcher without releasing or
// recording anything, and joins pending release records, as the manager
// shuts down: leases.json keeps the job-bound leases for the next daemon.
func (m *Manager) closeLeases() {
	m.leaseMu.Lock()
	m.leasesClosed = true
	for _, held := range m.leases {
		for _, e := range held {
			e.end()
		}
	}
	m.leases = nil
	m.leaseMu.Unlock()
	m.leaseWG.Wait()
}

// projectLeases returns primary's held leases ordered by name.
func (m *Manager) projectLeases(primary string, now time.Time) []Lease {
	key := canonicalPath(primary)
	m.leaseMu.Lock()
	notes := m.pruneLeasesLocked(key, now)
	var out []Lease
	for _, e := range m.leases[key] {
		out = append(out, e.Lease)
	}
	m.leaseMu.Unlock()
	m.emitLeaseNotes(notes)
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
		end = "on release_lease, at expiry, or when " + l.JobID + " exits (ending this session stops it); " +
			"while the job may still be running it stays held, even across a daemon restart"
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
		s.leases.m.releaseSessionLeases(s, false)
	}
}

// releaseLeasesForRestart releases the session's leases as restart
// suspension ends it (releaseSessionLeases).
func (s *Session) releaseLeasesForRestart() {
	if s.leases != nil {
		s.leases.m.releaseSessionLeases(s, true)
	}
}
