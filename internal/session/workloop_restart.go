package session

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"sort"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
)

// Work-loop continuity across a graceful restart (docs/design/daemon-restart.md
// §6). A daemon that keeps a restart manifest does not end its work loops at
// shutdown: from BeginShutdown on they start no session, and their slot
// runners and waiting-session watchers let go of their sessions without
// reclaiming them; once the drain is over suspendLoopsForRestart stops and
// joins the schedulers.
// After the sessions are suspended, each loop's scheduler checkpoint is written
// to .ycc/workloop.json (state workLoopSuspendedState, stamped with the
// restart id), sessions that ended meanwhile settled into it first, and the
// manifest names the loop. At startup RecoverRestart resumes a loop only from a
// checkpoint carrying the manifest's restart id, re-adopting every slot's
// session unattended before execution is released. Any other checkpoint, and a
// loop a crash cut short, restores as interrupted (restoreWorkLoopLocked).

// workLoopSuspendedState is the persisted state of a loop suspended for a
// restart.
const workLoopSuspendedState = "suspended_restart"

// Kinds of a checkpoint slot.
const (
	loopSlotWorking = "working" // working in a slot
	loopSlotWaiting = "waiting" // let wait on its own background jobs
)

// persistedLoopCheckpoint is the scheduler state a resumed loop goes on from.
type persistedLoopCheckpoint struct {
	RestartID     string `json:"restart_id"`
	State         string `json:"state"` // running | waiting | stopping
	StopRequested bool   `json:"stop_requested,omitempty"`
	// Baseline is the backlog's status per task when the loop started; a
	// resumed loop keeps it.
	Baseline    map[string]docs.Status `json:"baseline"`
	StartHead   string                 `json:"start_head,omitempty"`
	LoopStarted bool                   `json:"loop_started,omitempty"`
	PrevBreach  bool                   `json:"prev_breach,omitempty"`
	MaxWaiting  int                    `json:"max_waiting"`
	// StopOutcome is the reason the loop already stopped starting sessions,
	// if any; it finishes once its working sessions end.
	StopOutcome string              `json:"stop_outcome,omitempty"`
	StopHard    bool                `json:"stop_hard,omitempty"`
	Pacing      persistedPacing     `json:"pacing"`
	Lanes       []persistedLane     `json:"lanes"`
	Slots       []persistedLoopSlot `json:"slots"`
	SuspendedAt time.Time           `json:"suspended_at"`
}

// persistedPacing is a loopPacing; backoff still owed is kept as it was at
// suspension (the downtime does not count as served).
type persistedPacing struct {
	OutageFailures int           `json:"outage_failures,omitempty"`
	OutageWait     time.Duration `json:"outage_wait_ns,omitempty"`
	StallSessions  int           `json:"stall_sessions,omitempty"`
	StallWait      time.Duration `json:"stall_wait_ns,omitempty"`
	StallDelay     time.Duration `json:"stall_delay_ns,omitempty"`
	RetryDelay     time.Duration `json:"retry_delay_ns,omitempty"`
	RetryKind      string        `json:"retry_kind,omitempty"`
}

type persistedLane struct {
	Index  int             `json:"index"`
	Pacing persistedPacing `json:"pacing"`
	GaveUp bool            `json:"gave_up,omitempty"`
}

// persistedLoopSlot is one loop session the resumed loop re-adopts.
type persistedLoopSlot struct {
	Lane      int    `json:"lane"`
	SessionID string `json:"session_id"`
	Focus     string `json:"focus,omitempty"`
	// Attempt counts the loop's sessions on Focus, this one included.
	Attempt int    `json:"attempt,omitempty"`
	Kind    string `json:"kind"` // loopSlotWorking | loopSlotWaiting
	// Claimed records that a working session has focused a task (which
	// lets the next slot's session start).
	Claimed bool      `json:"claimed,omitempty"`
	Started time.Time `json:"started"`
	Since   time.Time `json:"since"`
	// Before hashes the backlog's task files when the session started, for
	// its progress check.
	Before map[string]string `json:"before,omitempty"`
	// LastSeq is the session log's last sequence number at shutdown: the
	// resume re-adopts the session only while its log has not moved on.
	LastSeq int `json:"last_seq"`
}

func pacingRecord(p loopPacing) persistedPacing {
	return persistedPacing{OutageFailures: p.outageFailures, OutageWait: p.outageWait, StallSessions: p.stallSessions,
		StallWait: p.stallWait, StallDelay: p.stallDelay, RetryDelay: p.retryDelay, RetryKind: p.retryKind}
}

func (p persistedPacing) pacing() loopPacing {
	return loopPacing{outageFailures: p.OutageFailures, outageWait: p.OutageWait, stallSessions: p.StallSessions,
		stallWait: p.StallWait, stallDelay: p.StallDelay, retryDelay: p.RetryDelay, retryKind: p.RetryKind}
}

// loopSuspension is a live loop a restart suspended, with the sessions it
// was following.
type loopSuspension struct {
	wl    *workLoop
	slots []loopSuspendedSlot
}

// loopSuspendedSlot is a working slot's run or a waiting session.
type loopSuspendedSlot struct {
	run *loopRun
	w   *loopWaiter
}

func (s loopSuspendedSlot) session() *Session {
	if s.run != nil {
		return s.run.sess
	}
	return s.w.sess
}

// loopSuspended reports whether suspendLoopsForRestart is stopping the loops.
func (m *Manager) loopSuspended() bool {
	select {
	case <-m.loopSuspend:
		return true
	default:
		return false
	}
}

// suspending reports whether a graceful restart is suspending the loop: it
// starts no session from then on.
func (wl *workLoop) suspending() bool {
	return wl.m != nil && wl.m.loopRestart.Load()
}

// claimFinish decides, against a graceful restart beginning, that the
// scheduler finishes the loop: it reports false once the restart is
// suspending the loop, which then leaves a checkpoint instead. Decided under
// the manager's mu, where BeginShutdown starts the restart, so a loop either
// finishes before the restart began or is suspended by it.
func (wl *workLoop) claimFinish() bool {
	if wl.m == nil {
		return true
	}
	wl.m.mu.Lock()
	defer wl.m.mu.Unlock()
	return !wl.m.loopRestart.Load()
}

// awaitSuspend waits, while a restart is suspending the loop, until a record
// is published or suspendLoopsForRestart stops the loop.
func (wl *workLoop) awaitSuspend() {
	select {
	case <-wl.m.loopSuspend:
	case <-wl.endedCh:
	}
}

// suspendLoopsForRestart stops every work loop for a graceful restart, before
// the sessions are suspended: schedulers, slot runners and waiting-session
// watchers (which let go of their sessions as the shutdown began, without
// reclaiming or replacing any) are joined. It captures each live loop for its checkpoint
// (checkpointSuspendedLoops). It runs once, and only for a daemon that keeps
// a restart manifest.
func (m *Manager) suspendLoopsForRestart() {
	if !m.loopRestart.Load() {
		return
	}
	m.loopSuspendOnce.Do(func() {
		// No loop starts, or goroutine joins the wait group, from here on.
		m.loopMu.Lock()
		m.loopStop = true
		loops := make([]*workLoop, 0, len(m.workLoops))
		for _, wl := range m.workLoops {
			loops = append(loops, wl)
		}
		m.loopMu.Unlock()
		close(m.loopSuspend)
		m.loopWG.Wait()
		sort.Slice(loops, func(i, j int) bool { return loops[i].workspace < loops[j].workspace })
		for _, wl := range loops {
			if susp := wl.captureSuspension(); susp != nil {
				m.suspendedLoops = append(m.suspendedLoops, susp)
			}
		}
	})
}

// captureSuspension sorts a joined loop's sessions into the slots its
// checkpoint keeps: runs their runners let go (working), and waiting
// sessions, including runs let wait as the suspension came. A run whose
// session never started is dropped; records already published stay queued
// for settling. It returns nil for a loop that is not live.
func (wl *workLoop) captureSuspension() *loopSuspension {
	wl.mu.Lock()
	defer wl.mu.Unlock()
	switch wl.state {
	case "running", "waiting", "stopping":
	default:
		return nil
	}
	susp := &loopSuspension{wl: wl}
	var queued []loopWaitResult
	endedWaiters := map[*loopWaiter]bool{}
	for _, res := range wl.ended {
		switch {
		case res.run != nil && errors.Is(res.err, errLoopSuspended):
			res.run.done = false
			susp.slots = append(susp.slots, loopSuspendedSlot{run: res.run})
		case res.run != nil && res.err == nil && res.rec.waiter != nil:
			w := res.rec.waiter
			w.before, w.lane = res.run.before, res.run.lane
			wl.clearLaneLocked(res.run)
			wl.waiters = append(wl.waiters, w)
		case res.run != nil && res.rec.id == "":
			wl.clearLaneLocked(res.run)
		default:
			if res.waiter != nil {
				endedWaiters[res.waiter] = true
			}
			queued = append(queued, res)
		}
	}
	wl.ended = queued
	for _, w := range wl.waiters {
		if !endedWaiters[w] {
			susp.slots = append(susp.slots, loopSuspendedSlot{w: w})
		}
	}
	// Slot holds are rebuilt when the loop resumes.
	wl.holders = nil
	wl.waitReserved = 0
	return susp
}

// clearLaneLocked frees the lane run held. Caller holds wl.mu.
func (wl *workLoop) clearLaneLocked(run *loopRun) {
	for _, lane := range wl.lanes {
		if lane.run == run {
			lane.run = nil
		}
	}
}

// checkpointSuspendedLoops writes each suspended loop's checkpoint stamped
// with restartID, once its sessions are suspended, and returns the loops the
// manifest names. A slot session whose closed log shows it ended (its turn
// finished, or it ended on an error or a pause) is settled into the loop's
// records first, like any session that ended; the others are kept with their
// log's last sequence number.
func (m *Manager) checkpointSuspendedLoops(restartID string) []restartLoopEntry {
	var out []restartLoopEntry
	for _, susp := range m.suspendedLoops {
		if err := susp.checkpoint(restartID); err != nil {
			log.Printf("restart manifest: work loop %s in %s: %v; it restores as interrupted", susp.wl.loopID, susp.wl.workspace, err)
			continue
		}
		out = append(out, restartLoopEntry{Project: susp.wl.project, Workspace: susp.wl.workspace})
	}
	m.suspendedLoops = nil
	return out
}

func (susp *loopSuspension) checkpoint(restartID string) error {
	wl := susp.wl
	store := wl.m.backlogStore(wl.workspace)
	var kept []persistedLoopSlot
	var settle []loopWaitResult
	for _, slot := range susp.slots {
		sess := slot.session()
		start := time.Time{}
		lane := 0
		if slot.run != nil {
			start, lane = slot.run.started, slot.run.lane
		} else {
			start, lane = slot.w.started, slot.w.lane
		}
		lastSeq := 0
		kind := restartKindEnded
		var events []event.Event
		if sess != nil && sess.log != nil && sess.logFailure() == nil {
			events = sess.log.Snapshot()
			lastSeq = sess.log.LastSeq()
			kind = restartDisposition(events)
		}
		if kind == restartKindEnded || lastSeq == 0 {
			res := loopWaitResult{run: slot.run, waiter: slot.w}
			if sess != nil {
				// The closed log has the whole ending: a failure it recorded
				// counts even if the cancellation came before the run marked
				// the session errored.
				if events == nil {
					events = sess.Log().Snapshot()
				}
				failed := sess.Status() == event.StatusError || loopEndedWithError(events)
				res.rec = wl.recordFromEvents(sess.ID, events, failed, time.Since(start))
				res.breach = sess.BudgetBreached() || budgetBreachedInLog(events)
			}
			settle = append(settle, res)
			continue
		}
		ps := persistedLoopSlot{Lane: lane, SessionID: sess.ID, Started: start, LastSeq: lastSeq}
		if slot.run != nil {
			ps.Kind, ps.Focus, ps.Claimed, ps.Since, ps.Before = loopSlotWorking, slot.run.focus, slot.run.claimed, slot.run.started, slot.run.before
		} else {
			ps.Kind, ps.Focus, ps.Since, ps.Before = loopSlotWaiting, slot.w.focus, slot.w.since, slot.w.before
		}
		// The closed log, not what the follower last saw, has the session's
		// focus: it may have claimed a task since the loop let go of it.
		var seen loopSessRec
		seen.addEvents(events)
		if seen.focus != "" {
			ps.Focus, ps.Claimed = seen.focus, true
		}
		kept = append(kept, ps)
	}
	// Sessions that ended are accounted once, here, as the scheduler would
	// have: records, budgets, progress and pacing.
	wl.mu.Lock()
	wl.ended = append(wl.ended, settle...)
	wl.mu.Unlock()
	if ends := wl.collectEnded(store); len(ends) > 0 {
		tasks, _ := store.ListMetadata()
		wl.applyEnds(ends, tasks)
	}

	wl.persistMu.Lock()
	defer wl.persistMu.Unlock()
	wl.mu.Lock()
	p := persistedWorkLoopFromSnapshot(wl.snapshotLocked())
	for i := range p.Sessions {
		if i >= len(wl.sessions) {
			break
		}
		for _, c := range wl.sessions[i].commits {
			p.Sessions[i].Commits = append(p.Sessions[i].Commits, persistedCommit{Task: c.task, SHA: c.sha, Message: c.message})
		}
		p.Sessions[i].Verdicts = append([]string(nil), wl.sessions[i].verdicts...)
	}
	attempts := map[string]int{}
	for _, s := range wl.sessions {
		if s.focus != "" {
			attempts[s.focus]++
		}
	}
	p.Slots = nil
	for i := range kept {
		if kept[i].Focus != "" {
			kept[i].Attempt = attempts[kept[i].Focus] + 1
		}
		p.Slots = append(p.Slots, persistedWorkLoopSlot{Slot: kept[i].Lane, SessionID: kept[i].SessionID,
			Task: kept[i].Focus, State: kept[i].Kind, Since: kept[i].Since})
	}
	cp := &persistedLoopCheckpoint{
		RestartID: restartID, State: wl.state, StopRequested: wl.stopReq,
		Baseline: map[string]docs.Status{}, StartHead: wl.startHead,
		LoopStarted: wl.loopStarted, PrevBreach: wl.prevBreach, MaxWaiting: wl.maxWaiting,
		StopOutcome: wl.stop.outcome, StopHard: wl.stop.hard, Pacing: pacingRecord(wl.pace),
		Slots: kept, SuspendedAt: time.Now().UTC(),
	}
	for id, st := range wl.baseline {
		cp.Baseline[id] = st
	}
	for _, lane := range wl.lanes {
		cp.Lanes = append(cp.Lanes, persistedLane{Index: lane.index, Pacing: pacingRecord(lane.pace), GaveUp: lane.gaveUp})
	}
	p.State, p.Restart = workLoopSuspendedState, cp
	wl.mu.Unlock()
	return writePersistedWorkLoop(p)
}

// resumeSuspendedLoop resumes the loop the restart manifest named in
// workspace l.Workspace, at persistent startup (RecoverRestart, execution
// held): only from a checkpoint stamped with restartID, which it consumes
// first, so the loop is never resumed twice. Each slot's session is reopened
// unattended, prepared for the restart as a manifest session would be
// (session_reopened with restartID, and a restart note for a turn cut short
// that owes no response), and followed by the loop again with its slot gate;
// a slot whose log moved on, or that cannot be reopened, is accounted from
// its log instead. Any other checkpoint restores as interrupted.
func (m *Manager) resumeSuspendedLoop(restartID string, l restartLoopEntry) {
	ws := l.Workspace
	project := ""
	for _, p := range m.projects.List() {
		if abs, err := filepath.Abs(p.Path); err == nil && abs == ws {
			project = p.Name
			break
		}
	}
	if project == "" {
		log.Printf("restart recovery: work loop in %s: not a registered project; not resuming it", ws)
		return
	}
	m.loopMu.Lock()
	if m.loopStop || m.workLoops[ws] != nil {
		m.loopMu.Unlock()
		return
	}
	p, ok := readPersistedWorkLoop(ws)
	if !ok || p.State != workLoopSuspendedState || p.Restart == nil || p.Restart.RestartID != restartID {
		log.Printf("restart recovery: work loop in %s: no checkpoint of restart %s; it restores as interrupted", ws, restartID)
		m.restoreWorkLoopLocked(ws)
		m.loopMu.Unlock()
		return
	}
	wl := m.resumedWorkLoop(p, project)
	// Consumed before the loop is published or any session reopened: a
	// startup cut off from here on finds a live loop's state, which restores
	// as interrupted. A checkpoint that cannot be consumed is not resumed.
	if err := writePersistedWorkLoop(persistedWorkLoopFromSnapshot(wl.snapshot())); err != nil {
		log.Printf("restart recovery: work loop in %s: consuming its checkpoint: %v; it restores as interrupted", ws, err)
		m.restoreWorkLoopLocked(ws)
		m.loopMu.Unlock()
		return
	}
	m.workLoops[ws] = wl
	m.loopWG.Add(1)
	m.loopMu.Unlock()
	for _, slot := range p.Restart.Slots {
		wl.readopt(restartID, project, slot)
	}
	wl.persist()
	log.Printf("restart recovery: resumed work loop %s in %s with %d session(s)", wl.loopID, ws, len(p.Restart.Slots))
	go func() {
		defer m.loopWG.Done()
		wl.run()
	}()
}

// resumedWorkLoop rebuilds a loop from its restart checkpoint p.
func (m *Manager) resumedWorkLoop(p persistedWorkLoop, project string) *workLoop {
	cp := p.Restart
	n := max(p.Parallel, len(cp.Lanes), 1)
	lanes := make([]*loopLane, n)
	for i := range lanes {
		lanes[i] = &loopLane{index: i}
	}
	for _, l := range cp.Lanes {
		if l.Index >= 0 && l.Index < n {
			lanes[l.Index].pace, lanes[l.Index].gaveUp = l.Pacing.pacing(), l.GaveUp
		}
	}
	wl := &workLoop{
		m:                m,
		loopID:           p.LoopID,
		project:          p.Project,
		projectArg:       project,
		workspace:        p.Workspace,
		state:            cp.State,
		stopReq:          cp.StopRequested,
		startedAt:        p.StartedAt,
		waitKind:         p.WaitKind,
		parallel:         max(p.Parallel, 1),
		lanes:            lanes,
		claimTimeout:     workLoopClaimTimeout,
		startHead:        cp.StartHead,
		maxWaiting:       cp.MaxWaiting,
		waitGrace:        workLoopWaitGrace,
		waitingRecheck:   workLoopWaitingRecheck,
		endedCh:          make(chan struct{}, 1),
		slotCh:           make(chan struct{}, 1),
		release:          make(chan struct{}),
		stopCh:           make(chan struct{}),
		loopStarted:      cp.LoopStarted,
		prevBreach:       cp.PrevBreach,
		cumTokens:        p.TotalTokens,
		cumCost:          p.TotalCost,
		costStatus:       p.CostStatus,
		sessionTokens:    p.SessionTokenLimit,
		sessionCost:      p.SessionCostLimit,
		loopTokens:       p.LoopTokenLimit,
		loopCost:         p.LoopCostLimit,
		envelopeCaptured: p.ResourceEnvelopeCaptured,
		baseline:         map[string]docs.Status{},
		completed:        restoredTasks(p.Completed),
		blocked:          restoredTasks(p.Blocked),
		inReview:         restoredTasks(p.InReview),
		created:          restoredTasks(p.Created),
		unfinished:       restoredTasks(p.Unfinished),
		preflight:        WorkLoopPreflight{InReview: restoredTasks(p.UncommittedInReview), Paths: p.UncommittedPaths},
		pace:             cp.Pacing.pacing(),
		stop:             loopStop{outcome: cp.StopOutcome, hard: cp.StopHard},
		resumed:          true,
	}
	if wl.project == "" {
		wl.project = m.projectLabel(p.Workspace)
	}
	if p.ResumeAt != nil {
		wl.resumeAt = *p.ResumeAt
	}
	switch wl.state {
	case "running", "waiting", "stopping":
	default:
		wl.state = "running"
	}
	if wl.stopReq {
		wl.state = "stopping"
		close(wl.stopCh)
	}
	for id, st := range cp.Baseline {
		wl.baseline[id] = st
	}
	for _, s := range p.Sessions {
		rec := loopSessRec{
			id: s.SessionID, focus: s.Focus, report: s.Evidence,
			tokens: s.Tokens, cost: s.Cost, priceStatus: s.PriceStatus,
			duration: time.Duration(s.DurationSecs) * time.Second,
			errKind:  s.ErrorKind, errMessage: s.ErrorMessage, errRetryable: s.ErrorRetryable,
			created: s.CreatedTasks, verdicts: s.Verdicts,
		}
		for _, c := range s.Commits {
			rec.commits = append(rec.commits, loopCommit{task: c.Task, sha: c.SHA, message: c.Message})
		}
		wl.sessions = append(wl.sessions, rec)
	}
	for _, l := range p.ManualLandings {
		wl.manual = append(wl.manual, WorkLoopLanding{SHA: l.SHA, Subject: l.Subject})
	}
	wl.startSession = m.Start
	wl.runSession = wl.realRunSession
	wl.waitFn = wl.waitForRetry
	if m.newRunSession != nil {
		wl.runSession = m.newRunSession(wl)
	}
	if m.newLoopWait != nil {
		wl.waitFn = m.newLoopWait(wl)
	}
	return wl
}

// readopt brings a checkpoint slot's session back into the resumed loop:
// reopened unattended (as the loop started it), prepared for the restart,
// with the loop's wake gate installed before execution is released, and
// followed again in its working slot or as a waiting session. A slot whose
// log moved on since the shutdown, that ended, or that cannot be reopened is
// accounted from its log instead, once, by the scheduler.
func (wl *workLoop) readopt(restartID, project string, slot persistedLoopSlot) {
	m := wl.m
	id := slot.SessionID
	wl.mu.Lock()
	lane := wl.lanes[min(max(slot.Lane, 0), len(wl.lanes)-1)]
	wl.mu.Unlock()
	settle := func(events []event.Event) {
		rec := wl.recordFromEvents(id, events, loopEndedWithError(events), time.Since(slot.Started))
		res := loopWaitResult{rec: rec, breach: budgetBreachedInLog(events)}
		wl.mu.Lock()
		if slot.Kind == loopSlotWaiting {
			w := &loopWaiter{id: id, focus: slot.Focus, since: slot.Since, started: slot.Started, lane: lane.index, before: slot.Before}
			wl.waiters = append(wl.waiters, w)
			res.waiter = w
		} else {
			run := &loopRun{lane: lane.index, id: id, focus: slot.Focus, claimed: true, done: true, started: slot.Started, before: slot.Before}
			lane.run = run
			res.run = run
		}
		wl.ended = append(wl.ended, res)
		wl.mu.Unlock()
	}
	events, err := m.SessionTranscript(project, id)
	if err != nil {
		log.Printf("restart recovery: work loop %s: session %s: %v; accounting it as ended", wl.loopID, id, err)
		settle(nil)
		return
	}
	if why := restartEntryConsumed(events, restartID, slot.LastSeq); why != "" {
		log.Printf("restart recovery: work loop %s: session %s: %s; accounting it as ended", wl.loopID, id, why)
		settle(events)
		return
	}
	kind := restartDisposition(events)
	if kind == restartKindEnded {
		settle(events)
		return
	}
	sess, err := m.reopen(project, id, true)
	if err != nil {
		log.Printf("restart recovery: work loop %s: reopening session %s: %v; accounting it as ended", wl.loopID, id, err)
		settle(events)
		return
	}
	if err := sess.prepareRestartReopen(restartID, kind == restartKindInterrupted || kind == restartKindQuestion); err != nil {
		// Its log failed: the session fail-stops, and its follower
		// accounts for it.
		log.Printf("restart recovery: work loop %s: session %s: %v", wl.loopID, id, err)
	}
	sess.setWakeGate(func() bool { return wl.admitWake(sess.ID) }, func() { wl.cancelWake(sess.ID) })
	if slot.Kind == loopSlotWaiting {
		w := wl.newWaiter(sess, slot.Started, slot.Focus)
		w.since, w.lane, w.before = slot.Since, lane.index, slot.Before
		wl.mu.Lock()
		wl.waiters = append(wl.waiters, w)
		// A waiting session its job had woken goes on working as soon as
		// execution is released: it takes its working slot back now, before
		// a downtime completion can wake another into it.
		if kind != restartKindIdle {
			w.awake = true
			wl.holdLocked(id)
		}
		w.shown, w.shownAt = wl.waiterStateLocked(w), w.since
		wl.mu.Unlock()
		wl.watchWaiter(w)
		return
	}
	run := &loopRun{lane: lane.index, id: id, focus: slot.Focus, claimed: slot.Claimed, started: slot.Started,
		before: slot.Before, usage: wl.usageOf(sess), sess: sess}
	wl.mu.Lock()
	lane.run = run
	wl.holdLocked(id)
	if wl.lastRun == nil || run.started.After(wl.lastRun.started) {
		wl.lastRun = run
	}
	wl.refreshCurrentLocked()
	wl.mu.Unlock()
	wl.launchWith(run, func(ctx context.Context) (loopSessRec, bool, error) {
		return wl.followSession(ctx, run, true, sess, slot.Started)
	})
}

// loopEndedWithError reports whether a loop session's log ends its
// coordinator's turn in an error.
func loopEndedWithError(events []event.Event) bool {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Transient {
			continue
		}
		switch ev.Type {
		case event.SessionError:
			if ev.Actor == "" || ev.Actor == "coordinator" {
				return true
			}
		case event.SessionIdle:
			return false
		case event.ModelTurn, event.ToolCall, event.ToolResult:
			if ev.Actor == "coordinator" {
				return false
			}
		}
	}
	return false
}

// budgetBreachedInLog reports whether a session's log records it crossing a
// spend cap (as seedBudgetFromLog does).
func budgetBreachedInLog(events []event.Event) bool {
	breached := false
	for _, ev := range events {
		switch ev.Type {
		case event.BudgetExceeded:
			breached = true
		case event.SessionForked:
			breached = false
		}
	}
	return breached
}
