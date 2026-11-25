package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
)

// parallelFake is a fake slot runner whose sessions are live in the manager,
// so the tasks they claim are held like a real session's. Each session waits
// for the test to let it claim, claims the top ready task nobody holds, then
// waits for the test to end it.
type parallelFake struct {
	t     *testing.T
	m     *Manager
	ws    string
	store *docs.Store

	mu       sync.Mutex
	started  []string
	claimed  map[string]string
	claimGo  map[string]chan struct{}
	end      map[string]chan parallelEnd
	inFlight map[string]int64
}

// parallelEnd is how the test ends a fake session.
type parallelEnd struct {
	done   bool // mark the claimed task done (progress)
	tokens int64
}

func newParallelFake(t *testing.T, parallel, tasks int) *parallelFake {
	t.Helper()
	f := &parallelFake{t: t, claimed: map[string]string{}, claimGo: map[string]chan struct{}{},
		end: map[string]chan parallelEnd{}, inFlight: map[string]int64{}}
	f.m, _, f.ws = loopTestManager(t, f.runner)
	f.store = docs.NewStore(f.ws)
	writeLoopConfig(t, f.ws, fmt.Sprintf("[work_loop]\nparallel = %d\n", parallel))
	for i := 0; i < tasks; i++ {
		if _, err := f.store.Create(fmt.Sprintf("task %d", i+1), "", 3, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func writeLoopConfig(t *testing.T, ws, config string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(ws, ".ycc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".ycc", "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *parallelFake) runner(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
	wl.waitingRecheck = 10 * time.Millisecond
	return func(ctx context.Context) (loopSessRec, bool, error) {
		run := loopRunFrom(ctx)
		s := newStopSession(f.t)
		f.mu.Lock()
		s.ID = fmt.Sprintf("p%d", len(f.started)+1)
		f.started = append(f.started, s.ID)
		claimGo, end := make(chan struct{}), make(chan parallelEnd, 1)
		f.claimGo[s.ID], f.end[s.ID] = claimGo, end
		f.mu.Unlock()
		s.Workspace = f.ws
		f.m.mu.Lock()
		f.m.sessions[s.ID] = s
		f.m.mu.Unlock()
		defer func() {
			f.m.mu.Lock()
			delete(f.m.sessions, s.ID)
			f.m.mu.Unlock()
		}()
		wl.nameRun(run, s.ID, func() (int64, float64) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.inFlight[s.ID], 0
		})

		select {
		case <-claimGo:
		case <-ctx.Done():
			return loopSessRec{id: s.ID}, false, ctx.Err()
		}
		tasks, err := f.store.ListMetadata()
		if err != nil {
			return loopSessRec{id: s.ID}, false, err
		}
		task, _ := pickReadyTask(tasks, f.m.heldTasks(f.ws, s.ID))
		if task != "" {
			s.emitter.Emit(event.TaskFocus, map[string]any{"task": task})
			wl.claimRun(run, task)
		}
		f.mu.Lock()
		f.claimed[s.ID] = task
		f.mu.Unlock()

		var how parallelEnd
		select {
		case how = <-end:
		case <-ctx.Done():
			return loopSessRec{id: s.ID, focus: task}, false, ctx.Err()
		}
		rec := loopSessRec{id: s.ID, focus: task, tokens: how.tokens, priceStatus: "unpriced"}
		if how.done && task != "" {
			if _, err := f.store.Update(task, func(tk *docs.Task) { tk.Status = docs.StatusDone }); err != nil {
				return rec, false, err
			}
			rec.updated = []string{task}
		}
		return rec, false, nil
	}
}

func (f *parallelFake) startedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

// letClaim lets the n-th started session (1-based) claim and waits for it.
func (f *parallelFake) letClaim(n int) string {
	f.t.Helper()
	id := fmt.Sprintf("p%d", n)
	awaitCond(f.t, id+" to start", func() bool { return f.startedCount() >= n })
	f.mu.Lock()
	ch := f.claimGo[id]
	f.mu.Unlock()
	close(ch)
	awaitCond(f.t, id+" to claim", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, ok := f.claimed[id]
		return ok
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claimed[id]
}

func (f *parallelFake) finish(n int, how parallelEnd) {
	f.mu.Lock()
	ch := f.end[fmt.Sprintf("p%d", n)]
	f.mu.Unlock()
	ch <- how
}

// stays asserts that the loop has started exactly n sessions for a while.
func (f *parallelFake) stays(n int, why string) {
	f.t.Helper()
	time.Sleep(80 * time.Millisecond)
	if got := f.startedCount(); got != n {
		f.t.Fatalf("%s: started %d sessions, want %d", why, got, n)
	}
}

// At three slots, each new session starts only once the previous one claimed
// a task, the sessions claim distinct tasks, and no fourth starts while three
// work; a freed slot starts the next.
func TestWorkLoopParallelSlotsStartOnClaimAndClaimDistinctTasks(t *testing.T) {
	f := newParallelFake(t, 3, 4)
	defer f.m.ReclaimAll()
	if _, err := f.m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	f.stays(1, "before the first claim")
	claims := map[string]bool{f.letClaim(1): true}
	f.stays(2, "before the second claim")
	claims[f.letClaim(2)] = true
	claims[f.letClaim(3)] = true
	f.stays(3, "with every slot working")
	if len(claims) != 3 || claims[""] {
		t.Fatalf("claimed tasks = %v, want three distinct tasks", claims)
	}
	snap, _ := f.m.GetWorkLoop("demo")
	if snap.Parallel != 3 || len(snap.Slots) != 3 || snap.CurrentSessionID != "p3" {
		t.Fatalf("loop with three working slots = %+v", snap)
	}
	for _, slot := range snap.Slots {
		if slot.State != "working" || !claims[slot.Task] {
			t.Fatalf("slots = %+v", snap.Slots)
		}
	}

	f.finish(1, parallelEnd{done: true})
	if task := f.letClaim(4); task == "" || claims[task] {
		t.Fatalf("fourth session claimed %q beside %v", task, claims)
	}
	for n := 2; n <= 4; n++ {
		f.finish(n, parallelEnd{done: true})
	}
	final := waitLoopFinished(t, f.m, "demo")
	if final.Outcome != "loop complete: no ready tasks remain" || final.SessionsRun != 4 || len(final.Completed) != 4 || len(final.Slots) != 0 {
		t.Fatalf("final loop = %+v", final)
	}
}

// A session that never claims holds the next start back only until the claim
// timeout.
func TestWorkLoopClaimGateTimesOut(t *testing.T) {
	f := newParallelFake(t, 2, 2)
	defer f.m.ReclaimAll()
	original := f.m.newRunSession
	f.m.newRunSession = func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.claimTimeout = 150 * time.Millisecond
		return original(wl)
	}
	if _, err := f.m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	f.stays(1, "within the claim timeout")
	awaitCond(t, "the second slot after the timeout", func() bool { return f.startedCount() == 2 })
}

// A graceful stop starts nothing more and lets every working slot finish.
func TestWorkLoopStopDrainsEverySlot(t *testing.T) {
	f := newParallelFake(t, 3, 5)
	defer f.m.ReclaimAll()
	if _, err := f.m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		f.letClaim(n)
	}
	if snap, err := f.m.StopWorkLoop("demo"); err != nil || snap.State != "stopping" {
		t.Fatalf("StopWorkLoop = %+v, %v", snap, err)
	}
	f.finish(1, parallelEnd{done: true})
	f.finish(2, parallelEnd{done: true})
	f.stays(3, "while draining")
	if snap, _ := f.m.GetWorkLoop("demo"); snap.State != "stopping" || snap.SessionsRun != 2 || len(snap.Slots) != 1 {
		t.Fatalf("draining loop = %+v", snap)
	}
	f.finish(3, parallelEnd{done: true})
	final := waitLoopFinished(t, f.m, "demo")
	if final.Outcome != "loop stopped: requested" || final.SessionsRun != 3 || len(final.Completed) != 3 {
		t.Fatalf("final loop = %+v", final)
	}
}

// A slot whose session made no progress backs off on its own; another slot
// that frees meanwhile starts at once.
func TestWorkLoopStallBacksOffOnlyItsSlot(t *testing.T) {
	f := newParallelFake(t, 2, 3)
	defer f.m.ReclaimAll()
	if _, err := f.m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	f.letClaim(1)
	f.letClaim(2)
	f.finish(1, parallelEnd{}) // no progress
	awaitCond(t, "the stalled slot's backoff", func() bool {
		snap, _ := f.m.GetWorkLoop("demo")
		return snap.State == "waiting" && snap.WaitKind == waitKindNoProgress
	})
	f.stays(2, "while the stalled slot backs off")
	f.finish(2, parallelEnd{done: true})
	f.letClaim(3)
	snap, _ := f.m.GetWorkLoop("demo")
	if len(snap.Slots) != 1 || snap.Slots[0].SessionID != "p3" || snap.Slots[0].Slot != 1 {
		t.Fatalf("slots after the other slot freed = %+v", snap.Slots)
	}
}

// The loop stalls only once every slot has stalled past patience.
func TestWorkLoopStallsWhenEverySlotStalled(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			return loopSessRec{id: fmt.Sprintf("idle-%d", calls)}, false, nil
		}
	})
	defer m.ReclaimAll()
	m.newLoopWait = func(*workLoop) func(time.Duration) bool {
		return func(time.Duration) bool { return true }
	}
	writeLoopConfig(t, ws, "[work_loop]\nparallel = 2\n")
	task, err := docs.NewStore(ws).Create("waits for a download", "", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	final := waitLoopFinished(t, m, "demo")
	// Each slot gives up after 17 sessions without progress (see
	// TestWorkLoopStallPatienceStops).
	want := "loop stalled: all 2 slots made no progress for 12h (ready: " + task.ID + ")"
	if final.Outcome != want || final.SessionsRun != 34 {
		t.Fatalf("final = %q after %d sessions, want %q after 34", final.Outcome, final.SessionsRun, want)
	}
}

// Loop caps count working sessions' usage so far: once it reaches the cap no
// further session starts, and the working ones finish.
func TestWorkLoopBudgetCountsInFlightUsage(t *testing.T) {
	f := newParallelFake(t, 3, 6)
	defer f.m.ReclaimAll()
	original := f.m.newRunSession
	f.m.newRunSession = func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.loopTokens = 100
		return original(wl)
	}
	if _, err := f.m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		f.letClaim(n)
	}
	f.mu.Lock()
	f.inFlight["p1"] = 120
	f.mu.Unlock()
	f.finish(2, parallelEnd{done: true, tokens: 10})
	awaitCond(t, "the loop to stop starting sessions", func() bool {
		snap, _ := f.m.GetWorkLoop("demo")
		return snap.State == "stopping"
	})
	f.stays(3, "over the cap")
	f.finish(1, parallelEnd{done: true, tokens: 120})
	f.finish(3, parallelEnd{done: true, tokens: 5})
	final := waitLoopFinished(t, f.m, "demo")
	// The cap is reached by p1's usage so far, before or after p2's record.
	if !strings.HasPrefix(final.Outcome, "loop stopped: budget reached (1") || final.SessionsRun != 3 || final.TotalTokens != 135 {
		t.Fatalf("final loop = %+v", final)
	}
}

// Commits made during the loop that no loop session recorded are manual
// landings in the digest.
func TestWorkLoopDigestCountsManualLandings(t *testing.T) {
	var ws string
	gitIn := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		cmd.Dir = ws
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(name string) string {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn("add", name)
		gitIn("commit", "-q", "-m", "add "+name)
		return gitIn("rev-parse", "HEAD")
	}
	var store *docs.Store
	m, sink, dir := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			tasks, _ := store.List()
			id := topReadyTask(tasks)
			_, err := store.Update(id, func(tk *docs.Task) { tk.Status = docs.StatusDone })
			own := commit("own.txt")
			commit("manual.txt")
			return loopSessRec{id: "s1", focus: id, updated: []string{id}, commits: []loopCommit{{task: id, sha: own[:12]}}}, false, err
		}
	})
	ws = dir
	gitIn("init", "-q")
	commit("base.txt")
	store = docs.NewStore(ws)
	if _, err := store.Create("one", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	final := waitLoopFinished(t, m, "demo")
	if len(final.ManualLandings) != 1 || final.ManualLandings[0].Subject != "add manual.txt" {
		t.Fatalf("manual landings = %+v", final.ManualLandings)
	}
	m.loopWG.Wait()
	m.notifier.Flush()
	var digest string
	for _, r := range sink.all() {
		digest = r.body
	}
	if !strings.Contains(digest, "; 1 other commit") {
		t.Fatalf("digest notification = %q", digest)
	}
}

// A slot session's record is published together with freeing its slot: until
// then the next slot's start stays gated, here after a non-retryable failure.
func TestWorkLoopNoStartBeforeSlotRecordIsPublished(t *testing.T) {
	s := newStopSession(t)
	s.setStatus(event.StatusError)
	s.emitter.Emit(event.SessionError, map[string]any{"kind": "auth", "retryable": false})
	returned, unblock := make(chan struct{}), make(chan struct{})
	secondStarted := make(chan struct{}, 1)
	var mu sync.Mutex
	starts := 0
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.waitingRecheck = 5 * time.Millisecond
		wl.startSession = func(Config) (*Session, error) {
			wl.m.mu.Lock()
			wl.m.sessions[s.ID] = s
			wl.m.mu.Unlock()
			return s, nil
		}
		return func(ctx context.Context) (loopSessRec, bool, error) {
			mu.Lock()
			starts++
			n := starts
			mu.Unlock()
			if n != 1 {
				select {
				case secondStarted <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return loopSessRec{}, false, ctx.Err()
			}
			rec, breach, err := wl.realRunSession(ctx)
			close(returned)
			<-unblock // between the real runner's cleanup and publication
			return rec, breach, err
		}
	})
	defer m.ReclaimAll()
	var once sync.Once
	release := func() { once.Do(func() { close(unblock) }) }
	defer release()
	s.Workspace = ws
	if _, err := docs.NewStore(ws).Create("ready", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoopWith("demo", WorkLoopOptions{Parallel: 2}); err != nil {
		t.Fatal(err)
	}
	<-returned
	select {
	case <-secondStarted:
		t.Fatal("started another slot before the failed session's record was published")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	if final := waitLoopFinished(t, m, "demo"); final.Outcome != "loop stopped: session failed (auth)" {
		t.Fatalf("final loop = %+v", final)
	}
}

// Slots that stall together back off together rather than one after another.
func TestWorkLoopIdleSlotsBackOffTogether(t *testing.T) {
	old := workLoopStallSchedule[0]
	workLoopStallSchedule[0] = time.Second
	defer func() { workLoopStallSchedule[0] = old }()
	f := newParallelFake(t, 2, 4)
	defer f.m.ReclaimAll()
	if _, err := f.m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	f.letClaim(1)
	f.letClaim(2)
	f.finish(1, parallelEnd{})
	f.finish(2, parallelEnd{})
	f.letClaim(3)
	// Served one after another, the second slot would still owe a second.
	deadline := time.Now().Add(500 * time.Millisecond)
	for f.startedCount() < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.startedCount() != 4 {
		t.Fatal("the second stalled slot served its backoff after the first")
	}
}

// A waiting session's switch to working is recorded at once.
func TestWaiterWakePersists(t *testing.T) {
	ws := t.TempDir()
	s := newStopSession(t) // running: woken
	wl := &workLoop{m: NewManager(workLoopPersistTestRegistry(), ws), loopID: "loop_wake", workspace: ws,
		state: "running", parallel: 1, waiters: []*loopWaiter{{id: s.ID, focus: "0007"}}}
	wl.persist()
	wl.syncSlot(s)
	if p, ok := readPersistedWorkLoop(ws); !ok || fmt.Sprint(persistedSlots(p)) != "[test working 0007]" {
		t.Fatalf("persisted loop = %+v", p)
	}
}

// A slot session that claimed its task and then failed no longer holds it,
// but nothing starts until its record is published (and here stops the loop).
func TestWorkLoopClaimedFailureStartsNoReplacement(t *testing.T) {
	s := newStopSession(t)
	returned, publish := make(chan struct{}), make(chan struct{})
	second := make(chan struct{}, 1)
	var mu sync.Mutex
	starts := 0
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.waitingRecheck = 5 * time.Millisecond
		wl.startSession = func(Config) (*Session, error) {
			wl.m.mu.Lock()
			wl.m.sessions[s.ID] = s
			wl.m.mu.Unlock()
			return s, nil
		}
		return func(ctx context.Context) (loopSessRec, bool, error) {
			mu.Lock()
			starts++
			n := starts
			mu.Unlock()
			if n != 1 {
				select {
				case second <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return loopSessRec{}, false, ctx.Err()
			}
			rec, breach, err := wl.realRunSession(ctx)
			close(returned)
			<-publish
			return rec, breach, err
		}
	})
	defer m.ReclaimAll()
	var once sync.Once
	release := func() { once.Do(func() { close(publish) }) }
	defer release()
	task, err := docs.NewStore(ws).Create("ready", "", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Workspace = ws
	s.emitter.Emit(event.TaskFocus, map[string]any{"task": task.ID})
	if _, err := m.StartWorkLoopWith("demo", WorkLoopOptions{Parallel: 2}); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "the first session's claim", func() bool {
		snap, _ := m.GetWorkLoop("demo")
		return len(snap.Slots) == 1 && snap.Slots[0].Task == task.ID
	})
	s.emitter.Emit(event.SessionError, map[string]any{"kind": "auth", "retryable": false})
	s.setStatus(event.StatusError)
	<-returned
	select {
	case <-second:
		t.Fatal("another slot started while the failed session's record was unpublished")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	if final := waitLoopFinished(t, m, "demo"); final.Outcome != "loop stopped: session failed (auth)" {
		t.Fatalf("final loop = %+v", final)
	}
}

// A job wake admitted while a slot session is being let wait keeps its
// working slot through the handoff.
func TestWakeDuringDetachKeepsTheSlot(t *testing.T) {
	ws := t.TempDir()
	m := NewManager(testRegistry(), "")
	defer m.ReclaimAll()
	s, jr, job := jobSession(t, "detaching", ws, "0001", "bash")
	run := &loopRun{placeholder: "starting", started: time.Now()}
	wl := &workLoop{m: m, workspace: ws, state: "running", parallel: 1, maxWaiting: 2,
		holders: map[string]struct{}{run.placeholder: {}}, lanes: []*loopLane{{run: run}}, lastRun: run,
		endedCh: make(chan struct{}, 1), slotCh: make(chan struct{}, 1)}
	wl.startSession = func(Config) (*Session, error) {
		m.mu.Lock()
		m.sessions[s.ID] = s
		m.mu.Unlock()
		return s, nil
	}
	returned, publish := make(chan struct{}), make(chan struct{})
	wl.runSession = func(ctx context.Context) (loopSessRec, bool, error) {
		rec, breach, err := wl.realRunSession(ctx)
		if rec.waiter == nil || err != nil {
			t.Errorf("not let wait: %+v, %v", rec, err)
		}
		close(returned)
		<-publish
		return rec, breach, err
	}
	wl.launch(run)
	<-returned
	job.Finish(jobs.Done, "done")
	if !wl.admitWake(s.ID) {
		close(publish)
		t.Fatal("the session lost its slot while being let wait")
	}
	jr.DrainFinished("coordinator")
	s.setStatus(event.StatusRunning)
	close(publish)
	m.loopWG.Wait()
	if wl.admitWake("another-waiter") {
		t.Fatal("another session took the only slot while the woken session works")
	}
}

// No session is admitted while a slot session has ended and its record is on
// its way, even after the scheduler's earlier checks passed.
func TestAdmitRefusesWhileASlotSessionIsEnding(t *testing.T) {
	ended := &loopRun{id: "failed", ending: true}
	free := &loopLane{index: 1}
	wl := &workLoop{parallel: 2, lanes: []*loopLane{{run: ended}, free}, holders: map[string]struct{}{"failed": {}}}
	if run := wl.admit(free, nil); run != nil {
		t.Fatal("admitted a session while another slot's session was ending")
	}
	ended.done = true
	if run := wl.admit(free, nil); run == nil || free.run != run {
		t.Fatal("did not admit once the ended session's record was published")
	}
}

// An observation that a session waits only on its shell jobs does not free its
// slot when a wake was admitted after the observation was made.
func TestStaleShellOnlyObservationKeepsAWokenSessionsSlot(t *testing.T) {
	wl := &workLoop{workspace: t.TempDir(), parallel: 1, holders: map[string]struct{}{"s": {}}, waiters: []*loopWaiter{{id: "s"}}}
	wakes := wl.wakeSeq
	if !wl.admitWake("s") {
		t.Fatal("the slot holder's wake was refused")
	}
	wl.applySlotState("s", false, true, wakes)
	if wl.admitWake("other") {
		t.Fatal("a stale observation freed the woken session's slot")
	}
	wl.applySlotState("s", false, true, wl.wakeSeq)
	if !wl.admitWake("other") {
		t.Fatal("a current observation did not free the slot")
	}
}

// A waiting session that a job woke and that then failed holds admission back
// until its record is published, like a slot session.
func TestEndingWokenWaiterHoldsAdmissionBack(t *testing.T) {
	var sess *Session
	var wl *workLoop
	settled, publish := make(chan struct{}), make(chan struct{})
	second := make(chan struct{}, 1)
	var mu sync.Mutex
	starts := 0
	m, _, ws := loopTestManager(t, func(loop *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl = loop
		wl.waitGrace = 0
		wl.waitingRecheck = 5 * time.Millisecond
		wl.startSession = func(Config) (*Session, error) {
			wl.m.mu.Lock()
			wl.m.sessions[sess.ID] = sess
			wl.m.mu.Unlock()
			return sess, nil
		}
		return func(ctx context.Context) (loopSessRec, bool, error) {
			mu.Lock()
			starts++
			n := starts
			mu.Unlock()
			if n != 1 {
				select {
				case second <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return loopSessRec{}, false, ctx.Err()
			}
			rec, breach, err := wl.realRunSession(ctx)
			if rec.waiter == nil {
				return rec, breach, err
			}
			resume := rec.waiter.resume
			rec.waiter.resume = func(release <-chan struct{}) loopWaitResult {
				res := resume(release)
				close(settled)
				<-publish
				return res
			}
			return rec, breach, err
		}
	})
	defer m.ReclaimAll()
	var once sync.Once
	release := func() { once.Do(func() { close(publish) }) }
	defer release()
	task, err := docs.NewStore(ws).Create("only task", "", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, jr, job := jobSession(t, "waiter", ws, task.ID, "bash")
	sess = s
	if _, err := m.StartWorkLoopWith("demo", WorkLoopOptions{Parallel: 2}); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "the session to wait", func() bool {
		snap, _ := m.GetWorkLoop("demo")
		return len(snap.Slots) == 1 && snap.Slots[0].State == "waiting"
	})
	job.Finish(jobs.Done, "finished")
	s.setJobWakePosted(true)
	if !wl.admitWake(s.ID) {
		t.Fatal("the waiting session could not wake")
	}
	jr.DrainFinished("coordinator")
	s.setStatus(event.StatusRunning)
	s.emitter.Emit(event.SessionError, map[string]any{"kind": "auth", "retryable": false})
	s.setStatus(event.StatusError)
	<-settled
	select {
	case <-second:
		t.Fatal("another slot started while the failed waiter's record was unpublished")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	if final := waitLoopFinished(t, m, "demo"); final.Outcome != "loop stopped: session failed (auth)" {
		t.Fatalf("final loop = %+v", final)
	}
}

// A waiting slot's Since is when it entered its shown state: a wake that
// takes a working slot restarts it, and so does going back to waiting.
func TestWaitingSlotSinceTracksItsState(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	w := &loopWaiter{id: "s", since: t0, shown: "waiting", shownAt: t0}
	wl := &workLoop{workspace: t.TempDir(), parallel: 1, waiters: []*loopWaiter{w}}
	slot := func() WorkLoopSlot {
		t.Helper()
		snap := wl.snapshot()
		if len(snap.Slots) != 1 {
			t.Fatalf("slots = %+v", snap.Slots)
		}
		return snap.Slots[0]
	}
	if s := slot(); s.State != "waiting" || !s.Since.Equal(t0) || !s.Detached {
		t.Fatalf("waiting slot = %+v, want since %v", s, t0)
	}
	if !wl.admitWake("s") {
		t.Fatal("wake refused")
	}
	woke := slot()
	if woke.State != "working" || !woke.Since.After(t0) || !woke.Detached {
		t.Fatalf("woken slot = %+v, want working since the wake", woke)
	}
	time.Sleep(2 * time.Millisecond)
	wl.applySlotState("s", false, true, wl.wakeSeq)
	if s := slot(); s.State != "waiting" || !s.Since.After(woke.Since) {
		t.Fatalf("slot after the wake = %+v, want waiting since after %v", s, woke.Since)
	}
}

// AnnotateWorkLoopSlots names each slot's task and gives its live session's
// intent and latest event time.
func TestAnnotateWorkLoopSlots(t *testing.T) {
	ws := t.TempDir()
	m := NewManager(testRegistry(), "")
	defer m.ReclaimAll()
	task, err := docs.NewStore(ws).Create("Slot cards", "", 2, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := jobSession(t, "live", ws, task.ID, "bash")
	s.intentText = "editing the loop page"
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	last, _ := s.Log().Last()
	wl := &WorkLoop{Workspace: ws, Slots: []WorkLoopSlot{
		{SessionID: "live", Task: task.ID, State: "waiting"},
		{SessionID: "gone", Task: "9999", State: "working"},
		{State: "working"},
	}}
	m.AnnotateWorkLoopSlots(wl)
	got := wl.Slots[0]
	if got.TaskTitle != "Slot cards" || got.Intent != "editing the loop page" || last.TS.IsZero() || !got.LastActivity.Equal(last.TS) {
		t.Fatalf("live slot = %+v, want title, intent and last activity %v", got, last.TS)
	}
	if gone := wl.Slots[1]; gone.TaskTitle != "" || gone.Intent != "" || !gone.LastActivity.IsZero() {
		t.Fatalf("slot of an unknown task and ended session = %+v", gone)
	}
}
