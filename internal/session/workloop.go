package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/notify"
	"github.com/whyrusleeping/ycc/internal/usage"
)

// Daemon-side work loop (task 0179, spec §9/§20.6). The unattended "work (loop)"
// backlog-drain driver used to live in the TUI; it now runs in the daemon so a
// loop survives client disconnects and any client (including a phone that
// suspends in the background) can start/observe/stop it via plain Connect RPCs.
//
// The loop starts fresh autonomous `work` sessions one after another, re-reading
// the LIVE backlog before each pick (so tasks added mid-loop are considered),
// enforcing the no-progress guard and the per-loop budget caps daemon-side, and
// rolling every session up into an end-of-batch digest pushed via the notifier.

// WorkLoop is a snapshot of a work loop's state for the RPC layer. It carries no
// mutex (the live loop keeps that internally) so it is safe to hand to callers.
type WorkLoop struct {
	LoopID           string
	Project          string // human project label
	Workspace        string // resolved absolute workspace
	State            string // running | stopping | finished
	CurrentSessionID string // session being driven now (Subscribe target); empty between sessions
	Outcome          string // human outcome line once finished
	StartedAt        time.Time
	SessionsRun      int
	Sessions         []WorkLoopSession
	Completed        []WorkLoopDigestTask
	Blocked          []WorkLoopDigestTask
	InReview         []WorkLoopDigestTask
	Created          []WorkLoopDigestTask
	TotalTokens      int64
	TotalCost        float64
	CostStatus       string // priced | unpriced | partial
}

// WorkLoopSession is a per-session record captured as each loop session finishes.
type WorkLoopSession struct {
	SessionID   string
	Focus       string
	Tokens      int64
	Cost        float64
	PriceStatus string
}

// WorkLoopDigestTask is one task row in a finished loop's batch digest.
type WorkLoopDigestTask struct {
	ID           string
	Title        string
	Status       string
	SHA          string
	VerdictTally string
	Tokens       int64
	Cost         float64
	PriceStatus  string
	Reason       string // blocked reason (from the task work log), blocked tasks only
}

// loopCommit is one commit made during a loop session.
type loopCommit struct{ task, sha, message string }

// loopSessRec is a per-session snapshot captured when a loop session finishes: its
// id, focus task, summed tokens, commits, review verdicts, and (folded in as the
// session completes) its priced cost/status.
type loopSessRec struct {
	id          string
	focus       string
	tokens      int64
	commits     []loopCommit
	verdicts    []string
	cost        float64
	priceStatus string
}

// workLoop is the live, mutating loop the daemon drives in a goroutine.
type workLoop struct {
	m *Manager

	loopID     string
	project    string // human display label (for the notifier line / WorkLoop.Project)
	projectArg string // caller's ORIGINAL project argument (empty => default workspace)
	workspace  string // resolved absolute workspace

	mu               sync.Mutex
	state            string
	currentSessionID string
	outcome          string
	startedAt        time.Time
	loopStarted      bool
	prevFP           string
	prevBreach       bool
	stopReq          bool

	// caps captured once at loop start (task 0137, spec §20.6).
	loopCost   float64
	loopTokens int64

	// baseline is the backlog status per id at loop start so the finished digest
	// can classify each task by how it changed. Missing id => task created mid-loop.
	baseline map[string]docs.Status

	// run accumulator.
	sessions   []loopSessRec
	cumTokens  int64
	cumCost    float64
	costStatus string

	// digest fields, populated at finish.
	completed []WorkLoopDigestTask
	blocked   []WorkLoopDigestTask
	inReview  []WorkLoopDigestTask
	created   []WorkLoopDigestTask

	// runSession is the injectable seam: the default runs a real autonomous work
	// session; tests substitute a fake returning canned records.
	runSession func(ctx context.Context) (loopSessRec, bool, error)
}

// snapshot copies the live loop's public state into a mutex-free WorkLoop.
func (wl *workLoop) snapshot() *WorkLoop {
	wl.mu.Lock()
	defer wl.mu.Unlock()
	out := &WorkLoop{
		LoopID:           wl.loopID,
		Project:          wl.project,
		Workspace:        wl.workspace,
		State:            wl.state,
		CurrentSessionID: wl.currentSessionID,
		Outcome:          wl.outcome,
		StartedAt:        wl.startedAt,
		SessionsRun:      len(wl.sessions),
		TotalTokens:      wl.cumTokens,
		TotalCost:        wl.cumCost,
		CostStatus:       wl.costStatus,
	}
	if out.CostStatus == "" {
		out.CostStatus = string(usage.StatusUnpriced)
	}
	for _, s := range wl.sessions {
		out.Sessions = append(out.Sessions, WorkLoopSession{
			SessionID: s.id, Focus: s.focus, Tokens: s.tokens,
			Cost: s.cost, PriceStatus: s.priceStatus,
		})
	}
	out.Completed = append(out.Completed, wl.completed...)
	out.Blocked = append(out.Blocked, wl.blocked...)
	out.InReview = append(out.InReview, wl.inReview...)
	out.Created = append(out.Created, wl.created...)
	return out
}

func (wl *workLoop) stopRequested() bool {
	wl.mu.Lock()
	defer wl.mu.Unlock()
	return wl.stopReq
}

// --- Manager plumbing ---

// StartWorkLoop starts an unattended work loop for a project (spec §9). It errors
// if a loop is already running/stopping for that workspace. The loop runs in its
// own goroutine and survives client disconnects; the returned snapshot lets the
// caller observe state and Subscribe to the current session.
func (m *Manager) StartWorkLoop(project string) (*WorkLoop, error) {
	absWS, label, err := m.resolveWorkspace(project)
	if err != nil {
		return nil, err
	}
	m.loopMu.Lock()
	if existing := m.workLoops[absWS]; existing != nil {
		st := existing.snapshot().State
		if st == "running" || st == "stopping" {
			m.loopMu.Unlock()
			return nil, fmt.Errorf("%w: a work loop is already %s for %s", ErrLoopRunning, st, label)
		}
	}
	b := m.Budget()
	wl := &workLoop{
		m:          m,
		loopID:     newLoopID(),
		project:    label,
		projectArg: project,
		workspace:  absWS,
		state:      "running",
		startedAt:  time.Now(),
		loopCost:   b.LoopCost,
		loopTokens: b.LoopTokens,
		baseline:   map[string]docs.Status{},
	}
	wl.runSession = wl.realRunSession
	if m.newRunSession != nil {
		wl.runSession = m.newRunSession(wl)
	}
	m.workLoops[absWS] = wl
	m.loopMu.Unlock()

	go wl.run()
	return wl.snapshot(), nil
}

// StopWorkLoop gracefully stops a running loop for a project: the current session
// finishes and no next session is picked. Returns the loop snapshot, or a nil loop
// when none is running.
func (m *Manager) StopWorkLoop(project string) (*WorkLoop, error) {
	absWS, _, err := m.resolveWorkspace(project)
	if err != nil {
		return nil, err
	}
	m.loopMu.Lock()
	wl := m.workLoops[absWS]
	m.loopMu.Unlock()
	if wl == nil {
		return nil, nil
	}
	wl.mu.Lock()
	if wl.state == "running" {
		wl.stopReq = true
		wl.state = "stopping"
	}
	wl.mu.Unlock()
	return wl.snapshot(), nil
}

// GetWorkLoop returns the current loop snapshot for a project (nil when none has
// run for its workspace) so a reconnecting client can observe state and Subscribe.
func (m *Manager) GetWorkLoop(project string) (*WorkLoop, error) {
	absWS, _, err := m.resolveWorkspace(project)
	if err != nil {
		return nil, err
	}
	m.loopMu.Lock()
	wl := m.workLoops[absWS]
	m.loopMu.Unlock()
	if wl == nil {
		return nil, nil
	}
	return wl.snapshot(), nil
}

// resolveWorkspace resolves a project label to its absolute workspace and human
// label, matching the resolution used by Backlog/start (spec §3.1).
func (m *Manager) resolveWorkspace(project string) (absWS, label string, err error) {
	ws := m.defaultWorkspace
	if project != "" {
		p, ok := m.projects.Resolve(project)
		if !ok {
			return "", "", fmt.Errorf("%w %q", ErrUnknownProject, project)
		}
		ws = p
	}
	absWS, err = filepath.Abs(ws)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace: %w", err)
	}
	return absWS, m.projectLabel(absWS), nil
}

func newLoopID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "loop_" + hex.EncodeToString(b[:])
}

// --- control loop ---

// run drives the loop: re-read the backlog, decide, run one session, repeat until
// a stop condition trips, then build + publish the digest.
func (wl *workLoop) run() {
	store, err := wl.m.Backlog(wl.projectArg)
	if err != nil {
		wl.finish("loop stopped: "+err.Error(), nil)
		return
	}
	tasks, err := store.List()
	if err != nil {
		wl.finish("loop stopped: "+err.Error(), nil)
		return
	}
	wl.mu.Lock()
	for _, t := range tasks {
		wl.baseline[t.ID] = t.Status
	}
	wl.mu.Unlock()

	for {
		if wl.stopRequested() {
			wl.finish("loop stopped: requested", tasks)
			return
		}
		tasks, err = store.List()
		if err != nil {
			wl.finish("loop stopped: "+err.Error(), tasks)
			return
		}
		next := topReadyTask(tasks)
		fp := backlogFingerprint(tasks)

		wl.mu.Lock()
		in := loopDecideInput{
			next: next, fp: fp,
			loopStarted: wl.loopStarted, prevFP: wl.prevFP, prevBreach: wl.prevBreach,
			cumTokens: wl.cumTokens, cumCost: wl.cumCost,
			loopTokens: wl.loopTokens, loopCost: wl.loopCost,
		}
		wl.mu.Unlock()
		if dec := decideLoop(in); dec.stop {
			wl.finish(dec.outcome, tasks)
			return
		}

		wl.mu.Lock()
		wl.loopStarted = true
		wl.prevFP = fp
		wl.mu.Unlock()

		rec, breach, err := wl.runSession(context.Background())
		wl.accumulate(rec, breach)
		if err != nil {
			// Re-read the backlog so the digest reflects any progress the session
			// made before failing.
			if final, lerr := store.List(); lerr == nil {
				tasks = final
			}
			wl.finish("loop stopped: "+err.Error(), tasks)
			return
		}
	}
}

// loopDecideInput is the pure input to a single loop decision.
type loopDecideInput struct {
	next        string
	fp          string
	loopStarted bool
	prevFP      string
	prevBreach  bool
	cumTokens   int64
	cumCost     float64
	loopTokens  int64
	loopCost    float64
}

// loopDecision is the outcome of a decision: whether to stop and why.
type loopDecision struct {
	stop    bool
	outcome string
}

// decideLoop decides whether to start another work session or stop the loop. It
// mirrors the client driver's applyLoopDecision ordering (spec §9, §20.6) and is
// pure so the control logic is unit-testable without a live model. Graceful stop
// and session errors are handled by the caller, before/after this.
func decideLoop(in loopDecideInput) loopDecision {
	switch {
	case in.next == "":
		return loopDecision{stop: true, outcome: "loop complete: no ready tasks remain"}
	case in.loopStarted && in.fp == in.prevFP:
		// A session ran but the backlog is byte-for-byte unchanged: it advanced
		// nothing, so starting another would loop forever on the same state.
		return loopDecision{stop: true, outcome: "loop stopped: session made no progress"}
	case in.loopStarted && in.prevBreach:
		// The previous loop session breached its own budget daemon-side (task 0137):
		// halt at this safe decision point (the session already completed).
		return loopDecision{stop: true, outcome: "loop stopped: session budget reached"}
	}
	// Per-loop-run spend cap (task 0137, spec §20.6): once cumulative tokens or
	// priced cost crosses a configured cap, stop before starting the next session.
	// Unpriced runs contribute no dollars so a cost cap never breaches on them.
	if in.loopStarted {
		if in.loopTokens > 0 && in.cumTokens >= in.loopTokens {
			return loopDecision{stop: true, outcome: fmt.Sprintf(
				"loop stopped: budget reached (%s tokens, cap %s)",
				fmtTokens(in.cumTokens), fmtTokens(in.loopTokens))}
		}
		if in.loopCost > 0 && in.cumCost >= in.loopCost {
			return loopDecision{stop: true, outcome: fmt.Sprintf(
				"loop stopped: budget reached ($%.2f, cap $%.2f)", in.cumCost, in.loopCost)}
		}
	}
	return loopDecision{stop: false}
}

// accumulate folds a finished session's record into the run accumulator. Cost is
// the priced estimate (0 for unpriced models, never invented dollars, §20.4).
func (wl *workLoop) accumulate(rec loopSessRec, breach bool) {
	if rec.id == "" {
		return
	}
	wl.mu.Lock()
	wl.sessions = append(wl.sessions, rec)
	wl.cumTokens += rec.tokens
	wl.cumCost += rec.cost
	wl.costStatus = mergeCostStatus(wl.costStatus, rec.priceStatus)
	wl.prevBreach = breach
	wl.mu.Unlock()
}

// finish stops the loop: it builds the batch digest against the baseline snapshot
// and the final backlog, marks the loop finished, and — when at least one session
// ran — pushes the completion digest via the daemon notifier (`digest` kind).
func (wl *workLoop) finish(outcome string, final []*docs.Task) {
	wl.mu.Lock()
	wl.buildDigestLocked(final)
	wl.state = "finished"
	wl.outcome = outcome
	nComplete, nBlocked, nReview := len(wl.completed), len(wl.blocked), len(wl.inReview)
	pushed := len(wl.sessions) > 0
	label := wl.project
	wl.mu.Unlock()

	if pushed {
		line := fmt.Sprintf("work loop finished: %d completed, %d blocked, %d in review",
			nComplete, nBlocked, nReview)
		wl.m.Notify(notify.KindDigest, label, "", line)
	}
}

// buildDigestLocked rolls the run's session records up against the baseline and
// the final backlog into the digest fields. Caller holds wl.mu.
func (wl *workLoop) buildDigestLocked(final []*docs.Task) {
	wl.completed, wl.blocked, wl.inReview, wl.created = nil, nil, nil, nil

	shaByTask := map[string]string{}
	verdictsByTask := map[string][]string{}
	tokensByTask := map[string]int64{}
	costByTask := map[string]float64{}
	statusByTask := map[string]string{}
	for _, s := range wl.sessions {
		for _, c := range s.commits {
			if c.task != "" {
				shaByTask[c.task] = c.sha
			}
		}
		if s.focus != "" {
			verdictsByTask[s.focus] = append(verdictsByTask[s.focus], s.verdicts...)
			tokensByTask[s.focus] += s.tokens
			costByTask[s.focus] += s.cost
			statusByTask[s.focus] = mergeCostStatus(statusByTask[s.focus], s.priceStatus)
		}
	}

	byID := map[string]*docs.Task{}
	sorted := append([]*docs.Task(nil), final...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, t := range sorted {
		byID[t.ID] = t
		dt := WorkLoopDigestTask{
			ID: t.ID, Title: t.Title, Status: string(t.Status),
			SHA:          shaByTask[t.ID],
			VerdictTally: tallyVerdicts(verdictsByTask[t.ID]),
			Tokens:       tokensByTask[t.ID],
			PriceStatus:  string(usage.StatusUnpriced),
		}
		if st, ok := statusByTask[t.ID]; ok {
			dt.Cost = costByTask[t.ID]
			dt.PriceStatus = st
		}
		base, existed := wl.baseline[t.ID]
		if !existed {
			wl.created = append(wl.created, dt)
			continue
		}
		switch t.Status {
		case docs.StatusDone:
			if base != docs.StatusDone {
				wl.completed = append(wl.completed, dt)
			}
		case docs.StatusBlocked:
			dt.Reason = blockedReasonFromBody(t.Body)
			wl.blocked = append(wl.blocked, dt)
		case docs.StatusInReview:
			wl.inReview = append(wl.inReview, dt)
		}
	}
}

// --- session runner (the injectable seam's default) ---

// realRunSession starts one autonomous `work` session, waits for it to finish,
// snapshots + prices it, then reclaims it. Graceful stop still lets the current
// session complete (checked before the NEXT pick, not here).
func (wl *workLoop) realRunSession(ctx context.Context) (loopSessRec, bool, error) {
	sess, err := wl.m.Start(Config{Project: wl.projectArg, Mode: "work", InteractionLevel: "autonomous"})
	if err != nil {
		return loopSessRec{}, false, err
	}
	wl.mu.Lock()
	wl.currentSessionID = sess.ID
	wl.mu.Unlock()

	// An autonomous work session reaches Idle only after `finish` (it then blocks
	// on input), so Idle == done here; Error is also terminal.
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
waitLoop:
	for {
		switch sess.Status() {
		case event.StatusIdle, event.StatusError:
			break waitLoop
		}
		select {
		case <-ctx.Done():
			break waitLoop
		case <-ticker.C:
		}
	}

	events := sess.Log().Snapshot()
	rec := loopSessRec{id: sess.ID, priceStatus: string(usage.StatusUnpriced)}
	for _, ev := range events {
		switch ev.Type {
		case event.TaskFocus:
			if t := strField(ev, "task"); t != "" {
				rec.focus = t
			}
		case event.CommitMade:
			rec.commits = append(rec.commits, loopCommit{
				task: strField(ev, "task"), sha: strField(ev, "sha"), message: strField(ev, "message"),
			})
		case event.ReviewSubmitted:
			if v := strField(ev, "verdict"); v != "" {
				rec.verdicts = append(rec.verdicts, v)
			}
		}
	}
	res := usage.Aggregate(usage.ReduceEvents(sess.ID, events), wl.m.reg, usage.Options{})
	rec.tokens = int64(res.Total.Tokens.Total)
	rec.cost = res.Total.Cost
	rec.priceStatus = string(res.Total.Status)
	if rec.priceStatus == "" {
		rec.priceStatus = string(usage.StatusUnpriced)
	}
	breach := sess.BudgetBreached()

	wl.mu.Lock()
	wl.currentSessionID = ""
	wl.mu.Unlock()
	wl.m.reclaim(sess.ID)
	return rec, breach, nil
}

// --- pure helpers (ported from the client driver) ---

// topReadyTask returns the id of the task a work session would pick next: the
// highest-priority (lowest priority number) actionable task that is ready and not
// yet done/blocked/in-review. Ties break by id. "" when nothing is ready.
func topReadyTask(tasks []*docs.Task) string {
	byID := docs.StatusByID(tasks)
	best := ""
	bestPrio := 0
	for _, t := range tasks {
		if t.Status != docs.StatusTodo && t.Status != docs.StatusInProgress {
			continue
		}
		if len(docs.BlockingDeps(t, byID)) != 0 {
			continue
		}
		if best == "" || t.Priority < bestPrio || (t.Priority == bestPrio && t.ID < best) {
			best, bestPrio = t.ID, t.Priority
		}
	}
	return best
}

// backlogFingerprint is a stable, order-independent summary of the backlog's
// actionable state (id:status of every task). Equal fingerprints across a
// finished session mean nothing moved — a genuine stall.
func backlogFingerprint(tasks []*docs.Task) string {
	parts := make([]string, 0, len(tasks))
	for _, t := range tasks {
		parts = append(parts, t.ID+":"+string(t.Status))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// tallyVerdicts summarises a task's review verdicts as "approve×2 reject×1"
// (insertion order preserved).
func tallyVerdicts(verdicts []string) string {
	if len(verdicts) == 0 {
		return ""
	}
	counts := map[string]int{}
	var order []string
	for _, v := range verdicts {
		if _, ok := counts[v]; !ok {
			order = append(order, v)
		}
		counts[v]++
	}
	parts := make([]string, 0, len(order))
	for _, v := range order {
		parts = append(parts, fmt.Sprintf("%s×%d", v, counts[v]))
	}
	return strings.Join(parts, " ")
}

// mergeCostStatus combines two price statuses: unset ("") adopts the other; a
// disagreement is "partial" (some priced, some not).
func mergeCostStatus(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" || a == b {
		return a
	}
	return string(usage.StatusPartial)
}

// blockedReasonFromBody extracts a one-line reason a task is blocked from its
// markdown body: the last "## Work log" bullet mentioning "blocked", else the last
// bullet (spec §18.7). Empty when there is no work log.
func blockedReasonFromBody(body string) string {
	lines := strings.Split(body, "\n")
	inLog := false
	var bullets []string
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") {
			inLog = strings.Contains(strings.ToLower(t), "work log")
			continue
		}
		if inLog && (strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")) {
			bullets = append(bullets, strings.TrimSpace(t[2:]))
		}
	}
	if len(bullets) == 0 {
		return ""
	}
	for i := len(bullets) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(bullets[i]), "blocked") {
			return bullets[i]
		}
	}
	return bullets[len(bullets)-1]
}

// fmtTokens renders a token count compactly (1.2k, 3.4M) for cap messages.
func fmtTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// strField reads a string data field from an event, tolerating absent data.
func strField(ev event.Event, key string) string {
	if ev.Data == nil {
		return ""
	}
	if s, ok := ev.Data[key].(string); ok {
		return s
	}
	return ""
}
