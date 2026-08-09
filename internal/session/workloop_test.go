package session

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/notify"
)

// --- pure decision logic (task 0179, spec §9/§20.6) ---

func TestDecideLoopStopsWhenEmpty(t *testing.T) {
	d := decideLoop(loopDecideInput{next: "", loopStarted: true, fp: "x", prevFP: "y"})
	if !d.stop || d.outcome != "loop complete: no ready tasks remain" {
		t.Fatalf("expected empty-backlog completion, got %+v", d)
	}
}

func TestDecideLoopStopsOnNoProgress(t *testing.T) {
	d := decideLoop(loopDecideInput{next: "0001", loopStarted: true, fp: "same", prevFP: "same"})
	if !d.stop || d.outcome != "loop stopped: session made no progress" {
		t.Fatalf("expected no-progress stop, got %+v", d)
	}
}

func TestDecideLoopErroredSessionSkipsNoProgress(t *testing.T) {
	d := decideLoop(loopDecideInput{
		next: "0001", loopStarted: true, fp: "same", prevFP: "same", prevErrored: true,
	})
	if d.stop {
		t.Fatalf("expected errored session to skip no-progress guard, got %+v", d)
	}

	// Empty backlog and loop budgets remain authoritative even after an error.
	if d := decideLoop(loopDecideInput{loopStarted: true, prevErrored: true}); !d.stop {
		t.Fatalf("expected empty backlog to stop, got %+v", d)
	}
	if d := decideLoop(loopDecideInput{
		next: "0001", loopStarted: true, prevErrored: true,
		cumTokens: 10, loopTokens: 10,
	}); !d.stop || !strings.Contains(d.outcome, "budget reached") {
		t.Fatalf("expected budget cap to stop, got %+v", d)
	}
}

func TestDecideLoopContinuesWhenBacklogChanged(t *testing.T) {
	d := decideLoop(loopDecideInput{next: "0002", loopStarted: true, fp: "new", prevFP: "old"})
	if d.stop {
		t.Fatalf("expected loop to continue, got %+v", d)
	}
}

func TestDecideLoopStopsOnSessionBreach(t *testing.T) {
	d := decideLoop(loopDecideInput{next: "0002", loopStarted: true, fp: "new", prevFP: "old", prevBreach: true})
	if !d.stop || d.outcome != "loop stopped: session budget reached" {
		t.Fatalf("expected session-breach stop, got %+v", d)
	}
}

func TestDecideLoopStopsAtTokenCap(t *testing.T) {
	d := decideLoop(loopDecideInput{
		next: "0002", loopStarted: true, fp: "new", prevFP: "old",
		cumTokens: 12000, loopTokens: 10000,
	})
	if !d.stop {
		t.Fatalf("expected token-cap stop, got %+v", d)
	}
	if want := "loop stopped: budget reached (12.0k tokens, cap 10.0k)"; d.outcome != want {
		t.Fatalf("outcome = %q, want %q", d.outcome, want)
	}
}

func TestDecideLoopStopsAtCostCap(t *testing.T) {
	d := decideLoop(loopDecideInput{
		next: "0002", loopStarted: true, fp: "new", prevFP: "old",
		cumCost: 5.5, loopCost: 5.0,
	})
	if !d.stop {
		t.Fatalf("expected cost-cap stop, got %+v", d)
	}
	if want := "loop stopped: budget reached ($5.50, cap $5.00)"; d.outcome != want {
		t.Fatalf("outcome = %q, want %q", d.outcome, want)
	}
}

func TestDecideLoopNoCapContinues(t *testing.T) {
	d := decideLoop(loopDecideInput{
		next: "0002", loopStarted: true, fp: "new", prevFP: "old",
		cumTokens: 999999, cumCost: 999, // caps are 0 (unlimited)
	})
	if d.stop {
		t.Fatalf("expected unlimited caps to continue, got %+v", d)
	}
}

func TestDecideLoopFirstIterationSkipsGuards(t *testing.T) {
	// loopStarted=false: no prior session, so the no-progress / breach / cap guards
	// must not trip even when fp==prevFP.
	d := decideLoop(loopDecideInput{next: "0001", loopStarted: false, fp: "same", prevFP: "same", prevBreach: true, cumTokens: 1e6, loopTokens: 1})
	if d.stop {
		t.Fatalf("expected first iteration to continue, got %+v", d)
	}
}

// --- pure helpers ---

func TestTopReadyTaskDaemon(t *testing.T) {
	tasks := []*docs.Task{
		{ID: "0001", Status: docs.StatusDone, Priority: 1},
		{ID: "0002", Status: docs.StatusTodo, Priority: 3},
		{ID: "0003", Status: docs.StatusTodo, Priority: 2},
		{ID: "0004", Status: docs.StatusInProgress, Priority: 2},
		{ID: "0005", Status: docs.StatusBlocked, Priority: 1},
		{ID: "0006", Status: docs.StatusTodo, Priority: 2, DependsOn: []string{"0007"}},
		{ID: "0007", Status: docs.StatusTodo, Priority: 5},
	}
	// Highest priority ready task is 0003 (prio 2, ties break by id vs 0004).
	if got := topReadyTask(tasks); got != "0003" {
		t.Fatalf("topReadyTask = %q, want 0003", got)
	}
}

func TestLoopSessionFailureUsesLastStructuredError(t *testing.T) {
	events := []event.Event{
		{Type: event.SessionError, Data: map[string]any{"kind": "auth", "retryable": false}},
		{Type: event.ModelTurn},
		{Type: event.SessionError, Data: map[string]any{"kind": "rate_limit", "retryable": true}},
	}
	kind, retryable := loopSessionFailure(events)
	if kind != "rate_limit" || !retryable {
		t.Fatalf("failure = %q/%v", kind, retryable)
	}
	kind, retryable = loopSessionFailure([]event.Event{{Type: event.SessionError}})
	if kind != "unknown" || retryable {
		t.Fatalf("missing fields failure = %q/%v", kind, retryable)
	}
}

func TestBacklogFingerprintStable(t *testing.T) {
	a := backlogFingerprint([]*docs.Task{{ID: "0002", Status: docs.StatusTodo}, {ID: "0001", Status: docs.StatusDone}})
	b := backlogFingerprint([]*docs.Task{{ID: "0001", Status: docs.StatusDone}, {ID: "0002", Status: docs.StatusTodo}})
	if a != b {
		t.Fatalf("fingerprint not order-independent: %q vs %q", a, b)
	}
}

// --- digest roll-up + classification + pricing + blocked reason ---

func TestBuildLoopDigest(t *testing.T) {
	wl := &workLoop{
		baseline: map[string]docs.Status{"0001": docs.StatusTodo, "0002": docs.StatusTodo},
		sessions: []loopSessRec{
			{
				id: "s1", focus: "0001", tokens: 1000, cost: 0.10, priceStatus: "priced",
				commits:  []loopCommit{{task: "0001", sha: "abcdef1234567", message: "do it"}},
				verdicts: []string{"approve", "approve"},
			},
			{id: "s2", focus: "0002", tokens: 500, cost: 0, priceStatus: "unpriced"},
		},
	}
	final := []*docs.Task{
		{ID: "0001", Title: "First", Status: docs.StatusDone},
		{ID: "0002", Title: "Second", Status: docs.StatusBlocked, Body: "## Work log\n- started\n- blocked: needs the staging DB password"},
		{ID: "0003", Title: "Follow-up", Status: docs.StatusTodo},
		{ID: "0004", Title: "Review me", Status: docs.StatusInReview},
	}
	// 0004 must be in baseline (not created) to classify as in_review.
	wl.baseline["0004"] = docs.StatusInProgress

	wl.buildDigestLocked(final)

	if len(wl.completed) != 1 || wl.completed[0].ID != "0001" {
		t.Fatalf("completed = %+v", wl.completed)
	}
	c := wl.completed[0]
	if c.SHA != "abcdef1234567" || c.VerdictTally != "approve×2" || c.Tokens != 1000 || c.Cost != 0.10 || c.PriceStatus != "priced" {
		t.Fatalf("completed row wrong: %+v", c)
	}
	if len(wl.blocked) != 1 || wl.blocked[0].ID != "0002" {
		t.Fatalf("blocked = %+v", wl.blocked)
	}
	if wl.blocked[0].Reason != "blocked: needs the staging DB password" {
		t.Fatalf("blocked reason = %q", wl.blocked[0].Reason)
	}
	if len(wl.created) != 1 || wl.created[0].ID != "0003" {
		t.Fatalf("created = %+v", wl.created)
	}
	if len(wl.inReview) != 1 || wl.inReview[0].ID != "0004" {
		t.Fatalf("in_review = %+v", wl.inReview)
	}
}

func TestBuildLoopDigestExcludesUntouchedBaselineStates(t *testing.T) {
	wl := &workLoop{
		baseline: map[string]docs.Status{
			"0001": docs.StatusBlocked,
			"0002": docs.StatusInReview,
			"0003": docs.StatusInReview,
			"0004": docs.StatusTodo,
			"0005": docs.StatusTodo,
			"0006": docs.StatusBlocked,
		},
		sessions: []loopSessRec{{
			id: "s1", focus: "0003", priceStatus: "unpriced",
			commits: []loopCommit{{task: "0006", sha: "abcdef"}},
		}},
	}
	final := []*docs.Task{
		{ID: "0001", Status: docs.StatusBlocked},
		{ID: "0002", Status: docs.StatusInReview},
		{ID: "0003", Status: docs.StatusInReview},
		{ID: "0004", Status: docs.StatusBlocked},
		{ID: "0005", Status: docs.StatusInReview},
		{ID: "0006", Status: docs.StatusBlocked},
	}

	wl.buildDigestLocked(final)

	if len(wl.blocked) != 2 || wl.blocked[0].ID != "0004" || wl.blocked[1].ID != "0006" {
		t.Fatalf("blocked = %+v, want changed 0004 and commit-touched 0006", wl.blocked)
	}
	if len(wl.inReview) != 2 || wl.inReview[0].ID != "0003" || wl.inReview[1].ID != "0005" {
		t.Fatalf("in review = %+v, want focus-touched 0003 and changed 0005", wl.inReview)
	}
}

func TestMergeCostStatus(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "priced", "priced"},
		{"priced", "", "priced"},
		{"priced", "priced", "priced"},
		{"priced", "unpriced", "partial"},
	}
	for _, c := range cases {
		if got := mergeCostStatus(c.a, c.b); got != c.want {
			t.Fatalf("mergeCostStatus(%q,%q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// --- end-to-end drive via the injectable runSession seam ---

// loopTestManager builds a manager with a real backlog dir and a notifier sink,
// plus a runSession seam factory that mutates the backlog to advance the loop.
func loopTestManager(t *testing.T, runSession func(wl *workLoop) func(context.Context) (loopSessRec, bool, error)) (*Manager, *notifySink, string) {
	t.Helper()
	sink := newNotifySink(t)
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"c": {Backend: "ollama", Model: "m"}},
		Roles:  config.Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
	})
	m := NewManager(reg, "")
	m.SetNotifier(notify.New(config.Notify{URL: sink.URL}))
	m.newRunSession = runSession

	ws := t.TempDir()
	if _, err := m.AddProject(ws, "demo"); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	return m, sink, ws
}

func waitLoopFinished(t *testing.T, m *Manager, project string) *WorkLoop {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		wl, err := m.GetWorkLoop(project)
		if err != nil {
			t.Fatalf("GetWorkLoop: %v", err)
		}
		if wl != nil && wl.State == "finished" {
			return wl
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("loop did not finish within deadline")
	return nil
}

// TestWorkLoopSoleProjectOmitted covers project=="" as an unambiguous
// convenience when the manager has exactly one named project.
func TestWorkLoopSoleProjectOmitted(t *testing.T) {
	sink := newNotifySink(t)
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"c": {Backend: "ollama", Model: "m"}},
		Roles:  config.Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
	})
	// NewManager registers its startup workspace as a normal named project.
	ws := t.TempDir()
	m := NewManager(reg, ws)
	m.SetNotifier(notify.New(config.Notify{URL: sink.URL}))
	m.newRunSession = func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(ctx context.Context) (loopSessRec, bool, error) {
			store := docs.NewStore(ws)
			tasks, _ := store.List()
			id := topReadyTask(tasks)
			if id == "" {
				return loopSessRec{}, false, nil
			}
			_, _ = store.Update(id, func(tk *docs.Task) { tk.Status = docs.StatusDone })
			return loopSessRec{id: "sess_" + id, focus: id, tokens: 100, cost: 0.01, priceStatus: "priced"}, false, nil
		}
	}

	store := docs.NewStore(ws)
	for _, title := range []string{"alpha", "beta"} {
		if _, err := store.Create(title, "", 3, nil, nil); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	if _, err := m.StartWorkLoop(""); err != nil {
		t.Fatalf("StartWorkLoop(\"\"): %v", err)
	}
	final := waitLoopFinished(t, m, "")
	if final.Outcome != "loop complete: no ready tasks remain" {
		t.Fatalf("outcome = %q (loop likely died on unknown-project resolution)", final.Outcome)
	}
	if final.SessionsRun != 2 {
		t.Fatalf("sessions run = %d, want 2 (backlog should have drained)", final.SessionsRun)
	}
	if len(final.Completed) != 2 {
		t.Fatalf("completed = %d, want 2", len(final.Completed))
	}
}

func TestWorkLoopDrainsBacklogAndPushesDigest(t *testing.T) {
	// The fake session runner marks the top-ready task done on each call and
	// returns a canned per-session record, so the loop drains the backlog and then
	// stops with "no ready tasks remain".
	var m *Manager
	var ws string
	factory := func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(ctx context.Context) (loopSessRec, bool, error) {
			store := docs.NewStore(ws)
			tasks, _ := store.List()
			id := topReadyTask(tasks)
			if id == "" {
				return loopSessRec{}, false, nil
			}
			_, _ = store.Update(id, func(tk *docs.Task) { tk.Status = docs.StatusDone })
			return loopSessRec{id: "sess_" + id, focus: id, tokens: 100, cost: 0.01, priceStatus: "priced"}, false, nil
		}
	}
	var sink *notifySink
	m, sink, ws = loopTestManager(t, factory)

	store := docs.NewStore(ws)
	for _, title := range []string{"one", "two"} {
		if _, err := store.Create(title, "", 3, nil, nil); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	wl, err := m.StartWorkLoop("demo")
	if err != nil {
		t.Fatalf("StartWorkLoop: %v", err)
	}
	if wl.State != "running" {
		t.Fatalf("initial state = %q, want running", wl.State)
	}

	final := waitLoopFinished(t, m, "demo")
	if final.Outcome != "loop complete: no ready tasks remain" {
		t.Fatalf("outcome = %q", final.Outcome)
	}
	if final.SessionsRun != 2 {
		t.Fatalf("sessions run = %d, want 2", final.SessionsRun)
	}
	if len(final.Completed) != 2 {
		t.Fatalf("completed = %d, want 2", len(final.Completed))
	}
	if final.TotalTokens != 200 {
		t.Fatalf("total tokens = %d, want 200", final.TotalTokens)
	}

	// The completion digest is pushed via the daemon notifier (digest kind).
	m.notifier.Flush()
	var digestPush *notifyRec
	for _, r := range sink.all() {
		if r.tags == notify.KindDigest {
			rr := r
			digestPush = &rr
		}
	}
	if digestPush == nil {
		t.Fatalf("expected a digest notification, got %+v", sink.all())
	}
	if want := "work loop finished: 2 completed, 0 blocked, 0 in review"; !strings.Contains(digestPush.body, want) {
		t.Fatalf("digest body = %q, want to contain %q", digestPush.body, want)
	}
}

func TestWorkLoopPublishesIncrementalDigest(t *testing.T) {
	type observation struct {
		loop *WorkLoop
		err  error
	}
	observed := make(chan observation, 1)
	var m *Manager
	var ws string
	calls := 0
	factory := func(*workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			calls++
			if calls == 2 {
				loop, err := m.GetWorkLoop("demo")
				observed <- observation{loop: loop, err: err}
			}
			store := docs.NewStore(ws)
			tasks, err := store.List()
			if err != nil {
				return loopSessRec{}, false, err
			}
			id := topReadyTask(tasks)
			_, err = store.Update(id, func(task *docs.Task) { task.Status = docs.StatusDone })
			return loopSessRec{id: fmt.Sprintf("session-%d", calls), focus: id, priceStatus: "unpriced"}, false, err
		}
	}
	m, _, ws = loopTestManager(t, factory)
	store := docs.NewStore(ws)
	first, err := store.Create("first task", "", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("second task", "", 2, nil, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	final := waitLoopFinished(t, m, "demo")
	if final.SessionsRun != 2 {
		t.Fatalf("sessions run = %d, want 2", final.SessionsRun)
	}

	select {
	case got := <-observed:
		if got.err != nil {
			t.Fatalf("mid-loop GetWorkLoop: %v", got.err)
		}
		if got.loop == nil || got.loop.State != "running" {
			t.Fatalf("mid-loop snapshot = %+v, want running", got.loop)
		}
		if len(got.loop.Completed) != 1 || got.loop.Completed[0].ID != first.ID || got.loop.Completed[0].Title != first.Title {
			t.Fatalf("mid-loop completed = %+v, want first task %+v", got.loop.Completed, first)
		}
	case <-time.After(time.Second):
		t.Fatal("second session did not observe a mid-loop snapshot")
	}
}

func TestWorkLoopNoProgressStops(t *testing.T) {
	// A runner that never changes the backlog: the loop runs one session, sees an
	// unchanged fingerprint, and stops with the no-progress outcome.
	var ws string
	factory := func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(ctx context.Context) (loopSessRec, bool, error) {
			return loopSessRec{id: "sX", focus: "0001", tokens: 50, priceStatus: "unpriced"}, false, nil
		}
	}
	m, _, w := loopTestManager(t, factory)
	ws = w

	store := docs.NewStore(ws)
	if _, err := store.Create("stuck", "", 3, nil, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatalf("StartWorkLoop: %v", err)
	}
	final := waitLoopFinished(t, m, "demo")
	if final.Outcome != "loop stopped: session made no progress" {
		t.Fatalf("outcome = %q", final.Outcome)
	}
	if final.SessionsRun != 1 {
		t.Fatalf("sessions run = %d, want 1", final.SessionsRun)
	}
}

func TestWorkLoopRetryableFailureWaitsThenResumes(t *testing.T) {
	var m *Manager
	var ws string
	calls := 0
	factory := func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			calls++
			if calls == 1 {
				return loopSessRec{id: "failed", errKind: "rate_limit", errRetryable: true}, false, nil
			}
			store := docs.NewStore(ws)
			tasks, _ := store.List()
			id := topReadyTask(tasks)
			_, err := store.Update(id, func(task *docs.Task) { task.Status = docs.StatusDone })
			return loopSessRec{id: "recovered", focus: id, priceStatus: "unpriced"}, false, err
		}
	}
	m, _, ws = loopTestManager(t, factory)
	var waits []time.Duration
	m.newLoopWait = func(*workLoop) func(time.Duration) bool {
		return func(delay time.Duration) bool {
			waits = append(waits, delay)
			return true
		}
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("recover after limit reset", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	final := waitLoopFinished(t, m, "demo")
	if final.Outcome != "loop complete: no ready tasks remain" || final.SessionsRun != 2 {
		t.Fatalf("final loop = %+v", final)
	}
	if len(waits) != 1 || waits[0] != time.Minute {
		t.Fatalf("waits = %v, want [1m]", waits)
	}
}

func TestWorkLoopNonRetryableFailureStopsTruthfully(t *testing.T) {
	var store *docs.Store
	factory := func(*workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			tasks, _ := store.List()
			id := topReadyTask(tasks)
			_, _ = store.Update(id, func(task *docs.Task) { task.Status = docs.StatusBlocked })
			return loopSessRec{id: "failed", focus: id, errKind: "auth"}, false, nil
		}
	}
	m, _, ws := loopTestManager(t, factory)
	store = docs.NewStore(ws)
	if _, err := store.Create("requires provider", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	final := waitLoopFinished(t, m, "demo")
	if want := "loop stopped: session failed (auth)"; final.Outcome != want {
		t.Fatalf("outcome = %q, want %q", final.Outcome, want)
	}
	if len(final.Blocked) != 1 {
		t.Fatalf("blocked digest = %+v, want session's pre-failure backlog update", final.Blocked)
	}
}

func TestWorkLoopProviderPatienceExhaustion(t *testing.T) {
	calls := 0
	factory := func(*workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			calls++
			return loopSessRec{id: fmt.Sprintf("failed-%d", calls), errKind: "overloaded", errRetryable: true}, false, nil
		}
	}
	m, _, ws := loopTestManager(t, factory)
	var waited time.Duration
	m.newLoopWait = func(*workLoop) func(time.Duration) bool {
		return func(delay time.Duration) bool { waited += delay; return true }
	}
	if _, err := docs.NewStore(ws).Create("provider outage", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	final := waitLoopFinished(t, m, "demo")
	if want := "loop stopped: provider unavailable (overloaded), gave up after 8h"; final.Outcome != want {
		t.Fatalf("outcome = %q, want %q", final.Outcome, want)
	}
	if waited != workLoopProviderPatience {
		t.Fatalf("waited = %v, want exactly %v", waited, workLoopProviderPatience)
	}
}

func TestStopWorkLoopDuringProviderWait(t *testing.T) {
	factory := func(*workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			return loopSessRec{id: "failed", errKind: "network", errRetryable: true}, false, nil
		}
	}
	m, _, ws := loopTestManager(t, factory)
	m.newLoopWait = func(wl *workLoop) func(time.Duration) bool {
		return func(time.Duration) bool {
			<-wl.stopCh
			return false
		}
	}
	if _, err := docs.NewStore(ws).Create("provider outage", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, err := m.GetWorkLoop("demo")
		if err != nil {
			t.Fatal(err)
		}
		if current != nil && current.State == "waiting" {
			if current.WaitKind != "network" || current.ResumeAt.IsZero() {
				t.Fatalf("waiting snapshot = %+v", current)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("loop did not enter waiting")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := m.StartWorkLoop("demo"); err == nil {
		t.Fatal("waiting loop did not reject duplicate start")
	}
	stopped, err := m.StopWorkLoop("demo")
	if err != nil || stopped.State != "stopping" {
		t.Fatalf("StopWorkLoop = %+v, %v", stopped, err)
	}
	final := waitLoopFinished(t, m, "demo")
	if final.Outcome != "loop stopped: requested" {
		t.Fatalf("outcome = %q", final.Outcome)
	}
}

func TestStartWorkLoopRejectsDuplicate(t *testing.T) {
	// A runner that blocks until released so the loop stays running.
	release := make(chan struct{})
	factory := func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(ctx context.Context) (loopSessRec, bool, error) {
			<-release
			return loopSessRec{}, false, nil
		}
	}
	m, _, ws := loopTestManager(t, factory)
	store := docs.NewStore(ws)
	if _, err := store.Create("task", "", 3, nil, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatalf("first StartWorkLoop: %v", err)
	}
	// The loop is now running its (blocked) session.
	if _, err := m.StartWorkLoop("demo"); err == nil {
		t.Fatal("expected duplicate StartWorkLoop to error")
	}

	// StopWorkLoop flips it to stopping; Get reflects it.
	stopped, err := m.StopWorkLoop("demo")
	if err != nil {
		t.Fatalf("StopWorkLoop: %v", err)
	}
	if stopped == nil || stopped.State != "stopping" {
		t.Fatalf("stopped state = %+v, want stopping", stopped)
	}
	close(release)
	waitLoopFinished(t, m, "demo")
}

func TestGetWorkLoopNoneReturnsNil(t *testing.T) {
	m, _, _ := loopTestManager(t, nil)
	wl, err := m.GetWorkLoop("demo")
	if err != nil {
		t.Fatalf("GetWorkLoop: %v", err)
	}
	if wl != nil {
		t.Fatalf("expected nil loop, got %+v", wl)
	}
}

func TestWorkLoopUnknownProject(t *testing.T) {
	m, _, _ := loopTestManager(t, nil)
	if _, err := m.StartWorkLoop("nope"); err == nil {
		t.Fatal("expected unknown-project error")
	}
	// Empty selection resolves the sole named project even with no loop.
	if _, err := m.GetWorkLoop(""); err != nil {
		t.Fatalf("GetWorkLoop(\"\"): %v", err)
	}
}
