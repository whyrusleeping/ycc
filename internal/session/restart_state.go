package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
)

// Planned-restart manifest (docs/design/daemon-restart.md §5). Graceful
// shutdown writes <state-dir>/restart-state.json once every session is
// suspended: the sessions the next daemon reopens by itself (those waiting on
// background jobs, and those whose turn the shutdown cut short) and the leases
// without a job that it holds again. Persistent startup consumes it while
// execution is still held. Each entry is authorized by the manifest's
// restart_id and the session's last_seq, so a startup that crashes and runs
// again never reopens a session twice or nudges it twice. An unplanned crash
// writes no manifest, so after one no model turn starts by itself.

const (
	restartStateVersion = 1
	// restartMaxAttempts bounds the startups that may try to prepare one
	// entry: one that keeps startup from finishing is quarantined after that.
	restartMaxAttempts = 2

	// Kinds of waiting_sessions entries.
	restartKindWait = "wait" // blocked in wait
	restartKindIdle = "idle" // idle, awaiting a background-job continuation

	// Other dispositions of a suspended session (restartDisposition).
	restartKindInterrupted = "interrupted" // its turn was cut short
	restartKindQuestion    = "question"    // parked on ask_user
	restartKindEnded       = "ended"       // its turn finished, or ended on an error or a pause
)

// restartContextText is the synthetic context a reopened interrupted session
// whose history owes no model response continues from.
const restartContextText = "[ycc] Not a message from the user: the ycc daemon was restarted while your previous turn " +
	"was still in progress, and that turn was cut short. Background jobs that were still running kept running " +
	"(their reports arrive automatically, and wait with no job ids waits on them). Check where your work stood, and continue it."

// restartEntry is one session the next daemon reopens.
type restartEntry struct {
	// Project is the registered project holding the session's log when the
	// manifest was written (informational: startup locates the log again).
	Project   string `json:"project,omitempty"`
	SessionID string `json:"session_id"`
	// Kind is restartKindWait or restartKindIdle for waiting_sessions.
	Kind string `json:"kind,omitempty"`
	// LastSeq is the session log's last sequence number at shutdown.
	LastSeq int `json:"last_seq"`
	// Attempts counts the startups that began preparing this entry.
	Attempts int `json:"attempts,omitempty"`
}

// restartState is restart-state.json.
type restartState struct {
	Version       int            `json:"version"`
	RestartID     string         `json:"restart_id"`
	WrittenAt     time.Time      `json:"written_at"`
	Waiting       []restartEntry `json:"waiting_sessions"`
	Interrupted   []restartEntry `json:"interrupted_sessions"`
	UnboundLeases []leaseRecord  `json:"unbound_leases"`
	// SuspendedLoops lists the work loops whose restart checkpoint
	// (.ycc/workloop.json) carries this restart_id (workloop_restart.go).
	SuspendedLoops []restartLoopEntry `json:"suspended_loops,omitempty"`
}

// restartLoopEntry is one work loop the next daemon resumes.
type restartLoopEntry struct {
	Project   string `json:"project,omitempty"`
	Workspace string `json:"workspace"`
}

// SetRestartStateFile makes graceful shutdown write the restart manifest at
// path, and RecoverRestart consume it.
func (m *Manager) SetRestartStateFile(path string) { m.restartStateFile = path }

// restartCandidate is a live session graceful shutdown may list in the
// manifest: one running, or idle awaiting its jobs, as suspension began.
type restartCandidate struct {
	s *Session
}

// restartCandidates picks, before suspension, the live sessions the next
// daemon may reopen by itself: running ones and idle ones awaiting their
// background jobs. Sessions a daemon worker runs (work loops, watches,
// grooms, workstream integration) are left to that worker; a work loop's
// sessions come back with the loop (workloop_restart.go).
func (m *Manager) restartCandidates() []restartCandidate {
	if m.restartStateFile == "" {
		return nil
	}
	var out []restartCandidate
	for _, s := range m.List() {
		if !s.restartReopenable() {
			continue
		}
		st, awaiting := s.StatusWithJobContinuation()
		if awaiting || st == event.StatusRunning {
			out = append(out, restartCandidate{s: s})
		}
	}
	return out
}

// restartReopenable reports whether a planned restart may reopen the session
// by itself: not one a daemon worker started and follows (its worker, which
// would handle its outcome, does not come back with it).
func (s *Session) restartReopenable() bool {
	switch s.origin {
	case OriginWorkLoop, OriginWatch, OriginMemoryGroom:
		return false
	}
	return s.Mode != watchMode && s.Mode != integrateMode && s.preset != backlogGroomPreset
}

// restartEntryFor classifies a suspended candidate from its closed log
// (restartDisposition): waiting (blocked in wait, or idle awaiting its jobs),
// interrupted (its turn was cut short, including a turn a job's completion had
// just started), or neither (parked on ask_user, which stays closed until
// answered; finished; or held after an error or a pause).
func (m *Manager) restartEntryFor(c restartCandidate) (e restartEntry, interrupted, ok bool) {
	s := c.s
	if s.log == nil || s.logFailure() != nil {
		return e, false, false
	}
	e = restartEntry{SessionID: s.ID, LastSeq: s.log.LastSeq()}
	e.Project, _ = m.persistedSessionProject(s.ID)
	switch kind := restartDisposition(s.log.Snapshot()); kind {
	case restartKindWait, restartKindIdle:
		e.Kind = kind
		return e, false, true
	case restartKindInterrupted:
		return e, true, true
	}
	return e, false, false
}

// restartDisposition classifies a suspended session from its log: parked on
// ask_user (restartKindQuestion), blocked in wait (restartKindWait), idle
// awaiting its background jobs (restartKindIdle), ended (restartKindEnded: its
// turn finished, or ended in an error or a pause), or cut short mid-turn
// (restartKindInterrupted).
func restartDisposition(events []event.Event) string {
	switch {
	case findResumableQuestion(events) != nil:
		return restartKindQuestion
	case findResumableWait(events) != nil:
		return restartKindWait
	}
	idle, awaiting := reopenBoundary(events)
	switch {
	case awaiting:
		return restartKindIdle
	case idle, endedOnHold(events):
		return restartKindEnded
	}
	return restartKindInterrupted
}

// endedOnHold reports whether the coordinator's latest turn ended in an error
// (a refusal among them) or a confirmed pause, with nothing moving the
// conversation on since: a human decides how it goes on, so a restart does
// not.
func endedOnHold(events []event.Event) bool {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Transient {
			continue
		}
		switch ev.Type {
		case event.SessionError, event.Interrupted:
			// A subagent's failure is recorded under its own actor; it holds
			// only that subagent.
			if ev.Actor == "" || ev.Actor == "coordinator" {
				return true
			}
		case event.UserInput:
			if !boolVal(ev.Data, "queued") {
				return false
			}
		case event.ModelTurn, event.ToolCall, event.ToolResult:
			if ev.Actor == "coordinator" {
				return false
			}
		case event.UserInputDelivered, event.JobNotified, event.RestartContext, event.CoordinationNotice,
			event.PlanOnlyDelivered, event.QuestionAnswered, event.Resumed, event.SessionIdle, event.SessionStarted:
			return false
		}
	}
	return false
}

// writeRestartManifest writes restart-state.json after restart suspension
// (once): the candidates' entries and the leases takeRestartLeases set aside.
// A failure is logged; the sessions then stay closed after the restart, and
// their detached jobs keep running. A manifest this daemon never consumed (a
// shutdown during startup) is kept as it is.
func (m *Manager) writeRestartManifest(candidates []restartCandidate) {
	path := m.restartStateFile
	if path == "" {
		return
	}
	m.restartWrite.Do(func() {
		m.leaseMu.Lock()
		leases := m.restartLeases
		m.restartLeases = nil
		m.leaseMu.Unlock()
		restartID := newRestartID()
		// Each suspended loop's checkpoint is written before the manifest
		// naming it: a crash in between leaves a checkpoint no manifest
		// matches, which restores as interrupted.
		loops := m.checkpointSuspendedLoops(restartID)
		if !m.restartRead.Load() {
			if _, err := os.Stat(path); err == nil {
				log.Printf("restart manifest: keeping %s, which this daemon never consumed", path)
				m.endUncarriedLeases(leases)
				return
			}
		}
		st := restartState{Version: restartStateVersion, RestartID: restartID, WrittenAt: time.Now().UTC(),
			Waiting: []restartEntry{}, Interrupted: []restartEntry{}, UnboundLeases: leases, SuspendedLoops: loops}
		if st.UnboundLeases == nil {
			st.UnboundLeases = []leaseRecord{}
		}
		for _, c := range candidates {
			e, interrupted, ok := m.restartEntryFor(c)
			switch {
			case !ok:
			case interrupted:
				st.Interrupted = append(st.Interrupted, e)
			default:
				st.Waiting = append(st.Waiting, e)
			}
		}
		for _, l := range [][]restartEntry{st.Waiting, st.Interrupted} {
			sort.Slice(l, func(i, j int) bool { return l[i].SessionID < l[j].SessionID })
		}
		if len(st.Waiting)+len(st.Interrupted)+len(st.UnboundLeases)+len(st.SuspendedLoops) == 0 {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("restart manifest: removing stale %s: %v", path, err)
			}
			return
		}
		if err := writeRestartState(path, &st); err != nil {
			log.Printf("restart manifest: %v; its sessions stay closed after the restart, its leases end, and its work loops restore as interrupted", err)
			m.endUncarriedLeases(st.UnboundLeases)
			return
		}
		log.Printf("restart manifest %s (%s): %d waiting and %d interrupted session(s), %d lease(s), %d work loop(s)",
			path, st.RestartID, len(st.Waiting), len(st.Interrupted), len(st.UnboundLeases), len(st.SuspendedLoops))
	})
}

// endUncarriedLeases records, in each holder's closed log, the end of the
// leases suspension kept for a manifest that was not written, so no log is
// left showing a lease still held.
func (m *Manager) endUncarriedLeases(leases []leaseRecord) {
	for _, r := range leases {
		n := &leaseNote{id: r.SessionID, dir: sessionDir(r.Workspace, r.SessionID),
			data: map[string]any{"name": r.Name, "why": leaseSessionEnded}}
		unlock := m.lockSessionID(n.id)
		m.recordLeaseNote(n)
		unlock()
	}
}

func newRestartID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

// writeRestartState replaces path with st (0600, atomic rename).
func writeRestartState(path string, st *restartState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'))
}

// RecoverRestart consumes the restart manifest at persistent startup (phase
// 3), after SupervisePassiveJobs and RestoreLeases and while execution is
// held. It restores the manifest's leases, then reopens each authorized
// session, recording its session_reopened (with restart_id) before execution
// is released: a waiting session resumes its wait or its wake-eligible idle,
// and an interrupted one goes on with the turn its history owes, or else
// from one synthetic restart_context note. An entry is skipped once its log
// recorded session_reopened for this restart_id or moved on past last_seq.
// Each attempt is counted in the file first, and an entry that keeps
// startup from finishing is quarantined (restart-state.json.quarantined).
// The manifest is removed once every entry is prepared; an unreadable one is
// moved aside (restart-state.json.unreadable). The work loops it lists are
// resumed from their checkpoints (resumeSuspendedLoop).
func (m *Manager) RecoverRestart() {
	defer m.restartRead.Store(true)
	path := m.restartStateFile
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var st restartState
	var bad []json.RawMessage
	if err == nil {
		st, bad, err = decodeRestartState(data)
	}
	if err != nil {
		aside := path + ".unreadable"
		log.Printf("restart recovery: %s: %v; reopening nothing (moved to %s)", path, err, aside)
		_ = os.Rename(path, aside)
		return
	}
	for _, raw := range bad {
		quarantineRestartRecord(path, st.RestartID, "malformed entry", raw)
	}
	m.restoreUnboundLeases(st.UnboundLeases, time.Now())
	retry := restartState{Version: st.Version, RestartID: st.RestartID, WrittenAt: st.WrittenAt,
		Waiting: []restartEntry{}, Interrupted: []restartEntry{}, UnboundLeases: []leaseRecord{}}
	seen := map[string]bool{}
	for _, list := range []struct {
		entries     []restartEntry
		interrupted bool
	}{{st.Waiting, false}, {st.Interrupted, true}} {
		for i := range list.entries {
			e := &list.entries[i]
			if seen[e.SessionID] {
				continue
			}
			seen[e.SessionID] = true
			project, ok := m.authorizedRestartEntry(st.RestartID, *e)
			if !ok {
				continue
			}
			if e.Attempts >= restartMaxAttempts {
				raw, _ := json.Marshal(e)
				quarantineRestartRecord(path, st.RestartID, fmt.Sprintf("%d startups did not finish preparing it", e.Attempts), raw)
				continue
			}
			e.Attempts++
			if err := writeRestartState(path, &st); err != nil {
				log.Printf("restart recovery: session %s: recording the attempt: %v; not reopening it", e.SessionID, err)
				continue
			}
			if !m.recoverRestartEntry(project, st.RestartID, *e, list.interrupted) {
				if list.interrupted {
					retry.Interrupted = append(retry.Interrupted, *e)
				} else {
					retry.Waiting = append(retry.Waiting, *e)
				}
			}
		}
	}
	// Work loops come back with their slots' sessions, unattended, before
	// execution is released (workloop_restart.go).
	for _, l := range st.SuspendedLoops {
		m.resumeSuspendedLoop(st.RestartID, l)
	}
	// Admission opens once startup finishes, so the generation's leases must
	// not be replayed by a later startup: they may be released meanwhile. A
	// session whose preparation could not be logged stays listed alone, for
	// a startup after a crash (a graceful shutdown writes a new manifest).
	if len(retry.Waiting)+len(retry.Interrupted) > 0 {
		if err := writeRestartState(path, &retry); err == nil {
			log.Printf("restart recovery: keeping %s for %d session(s) not prepared", path, len(retry.Waiting)+len(retry.Interrupted))
			return
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("restart recovery: removing %s: %v", path, err)
	}
}

// decodeRestartState parses a manifest, entry by entry: an entry that does
// not decode or is not valid is returned in bad (for quarantine) without
// costing the others. Only a manifest whose envelope is unreadable fails.
func decodeRestartState(data []byte) (restartState, []json.RawMessage, error) {
	var raw struct {
		Version        int               `json:"version"`
		RestartID      string            `json:"restart_id"`
		WrittenAt      time.Time         `json:"written_at"`
		Waiting        []json.RawMessage `json:"waiting_sessions"`
		Interrupted    []json.RawMessage `json:"interrupted_sessions"`
		UnboundLeases  []json.RawMessage `json:"unbound_leases"`
		SuspendedLoops []json.RawMessage `json:"suspended_loops"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return restartState{}, nil, err
	}
	if raw.Version != restartStateVersion || raw.RestartID == "" {
		return restartState{}, nil, fmt.Errorf("unsupported version %d or no restart_id", raw.Version)
	}
	st := restartState{Version: raw.Version, RestartID: raw.RestartID, WrittenAt: raw.WrittenAt,
		Waiting: []restartEntry{}, Interrupted: []restartEntry{}, UnboundLeases: []leaseRecord{}}
	var bad []json.RawMessage
	entries := func(in []json.RawMessage, waiting bool) []restartEntry {
		var out []restartEntry
		for _, r := range in {
			var e restartEntry
			id := ""
			if json.Unmarshal(r, &e) == nil {
				id = e.SessionID
			}
			valid := id != "" && id != "." && filepath.Base(id) == id && e.LastSeq > 0 && e.Attempts >= 0 &&
				(!waiting || e.Kind == restartKindWait || e.Kind == restartKindIdle)
			if !valid {
				bad = append(bad, r)
				continue
			}
			out = append(out, e)
		}
		return out
	}
	st.Waiting = append(st.Waiting, entries(raw.Waiting, true)...)
	st.Interrupted = append(st.Interrupted, entries(raw.Interrupted, false)...)
	for _, r := range raw.UnboundLeases {
		var l leaseRecord
		if json.Unmarshal(r, &l) != nil {
			bad = append(bad, r)
			continue
		}
		st.UnboundLeases = append(st.UnboundLeases, l)
	}
	for _, r := range raw.SuspendedLoops {
		var l restartLoopEntry
		if json.Unmarshal(r, &l) != nil || !filepath.IsAbs(l.Workspace) {
			bad = append(bad, r)
			continue
		}
		st.SuspendedLoops = append(st.SuspendedLoops, l)
	}
	return st, bad, nil
}

// quarantineRestartRecord sets a manifest record aside for the operator in
// restart-state.json.quarantined, instead of acting on it: one that does not
// decode, or that earlier startups failed to prepare.
func quarantineRestartRecord(path, restartID, why string, raw json.RawMessage) {
	log.Printf("restart recovery: quarantined %s (%s), not acted on", raw, why)
	line, err := json.Marshal(map[string]any{"restart_id": restartID, "why": why, "record": raw, "at": time.Now().UTC()})
	if err != nil {
		return
	}
	f, err := os.OpenFile(path+".quarantined", os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		log.Printf("restart recovery: recording a quarantined record: %v", err)
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

// authorizedRestartEntry locates a manifest session's log and reports
// whether the manifest still authorizes reopening it (restartEntryConsumed),
// with the project holding it.
func (m *Manager) authorizedRestartEntry(restartID string, e restartEntry) (string, bool) {
	id := e.SessionID
	project, ok := m.persistedSessionProject(id)
	if !ok {
		log.Printf("restart recovery: session %s: no registered project or worktree holds its log; not reopening it", id)
		return "", false
	}
	events, err := m.SessionTranscript(project, id)
	if err != nil {
		log.Printf("restart recovery: session %s: %v; not reopening it", id, err)
		return "", false
	}
	if why := restartEntryConsumed(events, restartID, e.LastSeq); why != "" {
		log.Printf("restart recovery: session %s: %s; not reopening it", id, why)
		return "", false
	}
	return project, true
}

// recoverRestartEntry reopens an authorized manifest session in project and
// prepares it for this restart. It reports false when the preparation could
// not be recorded (the session's log failed).
func (m *Manager) recoverRestartEntry(project, restartID string, e restartEntry, interrupted bool) bool {
	id := e.SessionID
	s, err := m.Reopen(project, id)
	if err != nil {
		log.Printf("restart recovery: reopening session %s: %v", id, err)
		return true
	}
	if err := s.prepareRestartReopen(restartID, interrupted); err != nil {
		log.Printf("restart recovery: session %s: %v", id, err)
		return false
	}
	return true
}

// restartEntryConsumed says why a manifest entry no longer authorizes a
// reopen, or "": the log already records a reopen for this restart, moved on
// past lastSeq, or has no event at lastSeq (the entry is not this log's).
// Records of job outcomes and lease releases a closed session's log may gain
// meanwhile do not count as moving on, and neither does this restart's own
// restart_context, which an earlier startup recorded before it could record
// the reopen (the reopen then goes on from it).
func restartEntryConsumed(events []event.Event, restartID string, lastSeq int) string {
	found := false
	for _, ev := range events {
		if ev.Type == event.SessionReopened && str(ev.Data, "restart_id") == restartID {
			return "already reopened for this restart"
		}
		if ev.Seq == lastSeq {
			found = true
		}
		if ev.Seq <= lastSeq {
			continue
		}
		switch {
		case ev.Type == event.JobFinished, ev.Type == event.JobClaimed, ev.Type == event.LeaseReleased:
		case ev.Type == event.RestartContext && str(ev.Data, "restart_id") == restartID:
		default:
			return fmt.Sprintf("its log moved on since the restart (%s at seq %d)", ev.Type, ev.Seq)
		}
	}
	if !found {
		return fmt.Sprintf("its log has no event at seq %d", lastSeq)
	}
	return ""
}

// prepareRestartReopen readies a session restart recovery reopened, before
// execution is released: it records session_reopened with restart_id now. An
// interrupted session whose history owes the model no response, and that is
// not parked on purpose (its turn finished, a fork awaits input, a question
// or a context gate holds it, or its conversation never began), first gets
// one restart_context note and goes on from it, with its cut-off run's
// undelivered queued messages put back first. The note precedes the marker,
// so a startup cut off between them reopens the session again (the note does
// not consume the entry) and goes on from the note without a second one. A
// session whose run has started already keeps the marker its run wrote. It
// fails when the log could not record the preparation.
func (s *Session) prepareRestartReopen(restartID string, interrupted bool) error {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	if s.launched || s.retired {
		return nil
	}
	// A conversation the shutdown cut off before it began (no opening input
	// recorded) has nothing to go on from.
	nudge := interrupted && s.reopenParked && !s.reopenIdle && !s.forkAwaitsInput &&
		s.resumeQuestion == nil && s.resumeWait == nil && !s.reopenAwaitingJobs && !s.contextModelSwitchRequired() &&
		len(s.currentLoop().History()) > 0
	if nudge {
		pending := pendingQueuedInputs(s.log.Snapshot())
		s.reopenParked = false
		s.steerMu.Lock()
		s.running = true
		s.corrections = append(append([]correction(nil), pending...), s.corrections...)
		s.steerMu.Unlock()
		if ev := s.emitter.Emit(event.RestartContext, map[string]any{"text": restartContextText, "restart_id": restartID}); ev.Seq == 0 {
			return fmt.Errorf("recording the restart note: %v", s.logFailure())
		}
		s.currentLoop().Post(restartContextText)
	}
	s.recordReopened(restartID)
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("recording the reopen: %w", err)
	}
	return nil
}

// recordReopened records the reopened session's session_reopened marker, at
// most once: from restart recovery (restartID set) ahead of the run, or else
// from the run as it starts.
func (s *Session) recordReopened(restartID string) {
	s.reopenMarked.Do(func() {
		impl := (config.Work{}).ResolvedImplementation()
		if s.deps != nil {
			impl = (config.Work{Implementation: s.deps.WorkImplementation}).ResolvedImplementation()
		}
		reopened := map[string]any{"work_implementation": impl}
		if restartID != "" {
			reopened["restart_id"] = restartID
		}
		if s.resumeQuestion != nil {
			// Tell clients the question replayed just before this marker is still
			// live (restored), not a stale gate to drop.
			reopened["question_restored"] = true
		}
		if s.resumeWait != nil {
			reopened["wait_restored"] = true
		}
		// Recovery metadata (docs/design/daemon-restart.md §3); omitted fields
		// keep the legacy meaning (nothing survived the reopen).
		if len(s.reopenSurviving) > 0 {
			reopened["surviving_jobs"] = s.reopenSurviving
		}
		if s.reopenAwaitingJobs {
			reopened["awaiting_jobs"] = true
		}
		// Every message on the correction queue precedes the marker, whether the
		// reopen put it back or it arrived since publication: list them all so
		// a later reopen keeps them if the log is cut before their delivery.
		// steerMu orders the marker against a correction being accepted.
		s.steerMu.Lock()
		var requeued []int
		for _, c := range s.corrections {
			requeued = append(requeued, c.seq)
		}
		if len(requeued) > 0 {
			reopened["requeued_inputs"] = requeued
		}
		s.emitter.Emit(event.SessionReopened, reopened)
		s.steerMu.Unlock()
		s.recovery.ReleaseMarker()
	})
}
