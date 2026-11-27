package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// jobSession is a controlled loop session that has focused task and ended its
// turn while its coordinator job of kind still runs.
func jobSession(t *testing.T, id, ws, task, kind string) (*Session, *jobs.Registry, *jobs.Job) {
	t.Helper()
	s := newStopSession(t)
	s.ID = id
	s.Workspace = ws
	jr := jobs.NewRegistry()
	t.Cleanup(func() { _ = jr.KillAll() })
	s.deps = &orchestrator.Deps{Jobs: jr, Emitter: s.emitter}
	job := jr.Start(kind, "background "+kind, "coordinator")
	if task != "" {
		s.emitter.Emit(event.TaskFocus, map[string]any{"task": task})
	}
	s.emitter.Emit(event.SessionIdle, map[string]any{"report": "waiting on " + kind})
	s.setIdle(true)
	return s, jr, job
}

// persistedSlots lists persisted slots as "session state [task]".
func persistedSlots(p persistedWorkLoop) []string {
	var out []string
	for _, slot := range p.Slots {
		out = append(out, strings.TrimSpace(slot.SessionID+" "+slot.State+" "+slot.Task))
	}
	return out
}

func awaitCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A session idle on its own shell job is handed back as a waiter and stays
// live with its job; one idle on a subagent run still counts as working.
func TestRealRunSessionWaitingOnShellJobVersusSubagent(t *testing.T) {
	for _, kind := range []string{"bash", "agent"} {
		t.Run(kind, func(t *testing.T) {
			m := NewManager(testRegistry(), "")
			defer m.ReclaimAll()
			ws := t.TempDir()
			s, jr, job := jobSession(t, "waits-on-"+kind, ws, "0007", kind)
			wl := &workLoop{
				m: m, loopID: "loop-" + kind, project: "demo", workspace: ws,
				state: "running", startedAt: time.Now(), baseline: map[string]docs.Status{},
				maxWaiting: 2,
				startSession: func(Config) (*Session, error) {
					m.mu.Lock()
					m.sessions[s.ID] = s
					m.mu.Unlock()
					return s, nil
				},
			}
			type result struct {
				rec loopSessRec
				err error
			}
			done := make(chan result, 1)
			go func() {
				rec, _, err := wl.realRunSession(context.Background())
				done <- result{rec, err}
			}()

			if kind == "agent" {
				select {
				case got := <-done:
					t.Fatalf("session waiting on a subagent was let go: %+v, %v", got.rec, got.err)
				case <-time.After(600 * time.Millisecond):
				}
				job.Finish(jobs.Done, "subagent report")
				jr.DrainFinished("coordinator")
				got := <-done
				if got.err != nil || got.rec.waiter != nil || got.rec.id != s.ID {
					t.Fatalf("subagent session record = %+v, %v", got.rec, got.err)
				}
				return
			}

			var got result
			select {
			case got = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("session waiting on a shell job held the runner")
			}
			w := got.rec.waiter
			if got.err != nil || w == nil || w.id != s.ID || w.focus != "0007" {
				t.Fatalf("shell-waiting record = %+v, %v", got.rec, got.err)
			}
			if live, ok := m.Get(s.ID); !ok || live != s || job.Status() != jobs.Running {
				t.Fatal("waiting session or its job did not stay live")
			}
			if w.working() {
				t.Fatal("session idle on a shell job counted as working")
			}
			if current := wl.snapshot().CurrentSessionID; current != "" {
				t.Fatalf("current session = %q after the session was let wait", current)
			}

			// Releasing the watcher leaves the session and its job alone.
			release := make(chan struct{})
			close(release)
			if res := w.resume(release); !res.released {
				t.Fatalf("released watcher returned %+v", res)
			}
			if _, ok := m.Get(s.ID); !ok || job.Status() != jobs.Running {
				t.Fatal("release stopped the waiting session or its job")
			}

			// Its jobs still wake it; the final record arrives once it ends.
			resumed := make(chan loopWaitResult, 1)
			go func() { resumed <- w.resume(make(chan struct{})) }()
			s.setStatus(event.StatusRunning)
			if !w.working() {
				t.Fatal("woken session did not count as working")
			}
			job.Finish(jobs.Done, "run finished")
			jr.DrainFinished("coordinator")
			s.emitter.Emit(event.CommitMade, map[string]any{"task": "0007", "sha": "abc123"})
			s.emitter.Emit(event.SessionIdle, map[string]any{"report": "run checked, committed"})
			s.setIdle(true)
			select {
			case res := <-resumed:
				if res.err != nil || res.rec.id != s.ID || len(res.rec.commits) != 1 || res.rec.report != "run checked, committed" {
					t.Fatalf("final record = %+v, %v", res.rec, res.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("watcher did not report the ended session")
			}
			if _, ok := m.Get(s.ID); ok {
				t.Fatal("ended waiting session was not reclaimed")
			}
		})
	}
}

// The loop moves on from a session waiting on its shell job, keeps its task
// held, waits for it when the only ready work is its own, and counts its
// final record in the digest.
func TestWorkLoopProceedsPastSessionWaitingOnShellJob(t *testing.T) {
	var m *Manager
	var ws string
	var first *Session
	var firstJobs *jobs.Registry
	var firstJob *jobs.Job
	second := newStopSession(t)
	second.ID = "second-session"
	var mu sync.Mutex
	starts := 0
	var taskA, taskB *docs.Task
	factory := func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.waitGrace = 0
		wl.waitingRecheck = 10 * time.Millisecond
		wl.startSession = func(Config) (*Session, error) {
			mu.Lock()
			starts++
			n := starts
			mu.Unlock()
			s := second
			if n == 1 {
				s = first
			}
			m.mu.Lock()
			m.sessions[s.ID] = s
			m.mu.Unlock()
			return s, nil
		}
		return wl.realRunSession
	}
	m, _, ws = loopTestManager(t, factory)
	defer m.ReclaimAll()
	store := docs.NewStore(ws)
	var err error
	if taskA, err = store.Create("long run", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if taskB, err = store.Create("other work", "", 2, nil, nil); err != nil {
		t.Fatal(err)
	}
	first, firstJobs, firstJob = jobSession(t, "first-session", ws, taskA.ID, "bash")
	second.Workspace = ws

	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	wantSlots := fmt.Sprint([]string{"second-session working", "first-session waiting " + taskA.ID})
	awaitCond(t, "the second session to start and be recorded", func() bool {
		p, ok := readPersistedWorkLoop(ws)
		return ok && fmt.Sprint(persistedSlots(p)) == wantSlots
	})
	snap, _ := m.GetWorkLoop("demo")
	if snap.CurrentSessionID != second.ID || len(snap.Waiting) != 1 || snap.Waiting[0].SessionID != first.ID || snap.Waiting[0].Focus != taskA.ID {
		t.Fatalf("loop while the first session waits = %+v", snap)
	}
	if _, ok := m.Get(first.ID); !ok || firstJob.Status() != jobs.Running {
		t.Fatal("waiting session or its job did not stay live")
	}
	if holder := m.heldTasks(ws, "")[taskA.ID]; holder != first.ID {
		t.Fatalf("waiting session lost its task hold: holder %q", holder)
	}

	// The second session finishes its task. The only ready work left is the
	// waiting session's, so the loop waits on it rather than finishing.
	if _, err := store.Update(taskB.ID, func(tk *docs.Task) { tk.Status = docs.StatusDone }); err != nil {
		t.Fatal(err)
	}
	second.emitter.Emit(event.TaskFocus, map[string]any{"task": taskB.ID})
	second.emitter.Emit(event.CommitMade, map[string]any{"task": taskB.ID, "sha": "bbb"})
	second.emitter.Emit(event.SessionIdle, map[string]any{"report": "B done"})
	second.setStatus(event.StatusIdle)
	awaitCond(t, "the loop to wait on its waiting session", func() bool {
		snap, _ := m.GetWorkLoop("demo")
		return snap.State == "waiting" && snap.WaitKind == waitKindSessionsWaiting
	})
	if snap, _ := m.GetWorkLoop("demo"); snap.SessionsRun != 1 || len(snap.Waiting) != 1 {
		t.Fatalf("loop waiting on its session = %+v", snap)
	}

	// The job wakes the first session, which finishes and commits its task.
	// (Its work is recorded before the wake is claimed, which ends it here.)
	if _, err := store.Update(taskA.ID, func(tk *docs.Task) { tk.Status = docs.StatusDone }); err != nil {
		t.Fatal(err)
	}
	first.emitter.Emit(event.CommitMade, map[string]any{"task": taskA.ID, "sha": "aaa"})
	first.emitter.Emit(event.ReviewSubmitted, map[string]any{"verdict": "accept"})
	first.emitter.Emit(event.SessionIdle, map[string]any{"report": "A done"})
	firstJob.Finish(jobs.Done, "run done")
	firstJobs.DrainFinished("coordinator")

	final := waitLoopFinished(t, m, "demo")
	if final.Outcome != "loop complete: no ready tasks remain" || final.SessionsRun != 2 || len(final.Waiting) != 0 {
		t.Fatalf("final loop = %+v", final)
	}
	shas := map[string]string{}
	for _, c := range final.Completed {
		shas[c.ID] = c.SHA + " " + c.VerdictTally
	}
	if shas[taskA.ID] != "aaa accept×1" || shas[taskB.ID] != "bbb " {
		t.Fatalf("completed = %+v", final.Completed)
	}
	if final.Sessions[1].SessionID != first.ID || final.Sessions[1].Evidence != "A done" {
		t.Fatalf("waiting session's record = %+v", final.Sessions)
	}
	if _, ok := m.Get(first.ID); ok {
		t.Fatal("finished waiting session was not reclaimed")
	}
}

// fakeWaiters is a fake session runner whose sessions all wait on shell jobs:
// each puts the top ready task in review and returns a waiter that finishes
// (marking the task done) when the test ends it.
type fakeWaiters struct {
	mu       sync.Mutex
	store    *docs.Store
	started  []string
	end      map[string]chan struct{}
	released map[string]bool
}

func (f *fakeWaiters) runner(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
	wl.waitingRecheck = 10 * time.Millisecond
	return func(context.Context) (loopSessRec, bool, error) {
		tasks, err := f.store.List()
		if err != nil {
			return loopSessRec{}, false, err
		}
		task := topReadyTask(tasks)
		if _, err := f.store.Update(task, func(tk *docs.Task) { tk.Status = docs.StatusInReview }); err != nil {
			return loopSessRec{}, false, err
		}
		id := "sess_" + task
		end := make(chan struct{})
		f.mu.Lock()
		f.started = append(f.started, id)
		f.end[id] = end
		f.mu.Unlock()
		w := &loopWaiter{id: id, focus: task, working: func() bool { return false }}
		w.resume = func(release <-chan struct{}) loopWaitResult {
			select {
			case <-end:
			case <-release:
				f.mu.Lock()
				f.released[id] = true
				f.mu.Unlock()
				return loopWaitResult{released: true}
			}
			if _, err := f.store.Update(task, func(tk *docs.Task) { tk.Status = docs.StatusDone }); err != nil {
				return loopWaitResult{err: err}
			}
			return loopWaitResult{rec: loopSessRec{
				id: id, focus: task, tokens: 100, priceStatus: "unpriced",
				commits: []loopCommit{{task: task, sha: "sha" + task}},
			}}
		}
		return loopSessRec{id: id, focus: task, waiter: w}, false, nil
	}
}

func (f *fakeWaiters) startedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

func (f *fakeWaiters) id(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started[i]
}

// finish ends the i-th started session.
func (f *fakeWaiters) finish(i int) {
	f.mu.Lock()
	end := f.end[f.started[i]]
	f.mu.Unlock()
	close(end)
}

func newFakeWaiterLoop(t *testing.T, tasks int, config string) (*Manager, string, *fakeWaiters) {
	t.Helper()
	f := &fakeWaiters{end: map[string]chan struct{}{}, released: map[string]bool{}}
	m, _, ws := loopTestManager(t, f.runner)
	f.store = docs.NewStore(ws)
	if config != "" {
		if err := os.MkdirAll(filepath.Join(ws, ".ycc"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, ".ycc", "config.toml"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < tasks; i++ {
		if _, err := f.store.Create("task", "", 3, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return m, ws, f
}

// Two foreground sessions can go shell-idle after both have been admitted.
// Only one fits under the detached cap. The other still drains as foreground.
func TestWorkLoopCappedForegroundWait(t *testing.T) {
	starts := make(chan *Session, 2)
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.waitGrace, wl.maxWaiting = 0, 1
		wl.startSession = func(Config) (*Session, error) {
			select {
			case s := <-starts:
				wl.m.mu.Lock()
				wl.m.sessions[s.ID] = s
				wl.m.mu.Unlock()
				return s, nil
			default:
				return nil, fmt.Errorf("unexpected third session")
			}
		}
		return wl.realRunSession
	})
	defer m.ReclaimAll()
	store := docs.NewStore(ws)
	for _, title := range []string{"first", "second"} {
		if _, err := store.Create(title, "", 1, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	first, jr, job := jobSession(t, "first", ws, "0001", "bash")
	first.setStatus(event.StatusRunning)
	second, _, _ := jobSession(t, "second", ws, "0002", "bash")
	starts <- first
	starts <- second
	if _, err := m.StartWorkLoopWith("demo", WorkLoopOptions{Parallel: 2}); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "the second session to detach", func() bool {
		s, _ := m.GetWorkLoop("demo")
		return len(s.Waiting) == 1 && s.Waiting[0].SessionID == second.ID
	})
	first.setIdle(true)
	awaitCond(t, "both the capped foreground slot and the loop to show waiting", func() bool {
		s, _ := m.GetWorkLoop("demo")
		return s.State == "waiting" && s.WaitKind == waitKindSessionsWaiting && len(s.Waiting) == 1 && len(s.Slots) == 2 &&
			s.Slots[0].SessionID == first.ID && !s.Slots[0].Detached && s.Slots[0].State == "waiting" && s.Slots[1].State == "waiting"
	})
	first.setStatus(event.StatusRunning)
	awaitCond(t, "the foreground slot and loop to report work again", func() bool {
		s, _ := m.GetWorkLoop("demo")
		return s.State == "running" && len(s.Slots) == 2 && s.Slots[0].State == "working"
	})
	if _, err := m.StopWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	job.Finish(jobs.Done, "checked")
	jr.DrainFinished("coordinator")
	first.setIdle(false)
	final := waitLoopFinished(t, m, "demo")
	if final.SessionsRun != 1 || final.Sessions[0].SessionID != first.ID || len(final.Waiting) != 1 || final.Waiting[0].SessionID != second.ID {
		t.Fatalf("capped foreground record lost or spuriously detached: %+v", final)
	}
}

func TestWorkLoopWaitingCap(t *testing.T) {
	m, _, f := newFakeWaiterLoop(t, 3, "[work_loop]\nmax_waiting = 2\n")
	defer m.ReclaimAll()
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	atCap := func() bool {
		snap, _ := m.GetWorkLoop("demo")
		return snap.State == "waiting" && snap.WaitKind == waitKindSessionsWaiting && len(snap.Waiting) == 2
	}
	awaitCond(t, "the loop to reach its waiting cap", atCap)
	time.Sleep(100 * time.Millisecond)
	if n := f.startedCount(); n != 2 {
		t.Fatalf("started %d sessions at a waiting cap of 2", n)
	}

	// One waiting session ends: it is counted and the loop starts the next.
	f.finish(0)
	awaitCond(t, "the third session", func() bool { return f.startedCount() == 3 })
	awaitCond(t, "the cap again", atCap)
	snap, _ := m.GetWorkLoop("demo")
	if snap.SessionsRun != 1 || snap.TotalTokens != 100 || len(snap.Completed) != 1 {
		t.Fatalf("loop after one waiting session ended = %+v", snap)
	}

	f.finish(1)
	f.finish(2)
	final := waitLoopFinished(t, m, "demo")
	if final.Outcome != "loop complete: no ready tasks remain" || final.SessionsRun != 3 || final.TotalTokens != 300 || len(final.Completed) != 3 {
		t.Fatalf("final loop = %+v", final)
	}
}

func TestWorkLoopMaxWaitingZeroWaitsForEverySession(t *testing.T) {
	m, _, f := newFakeWaiterLoop(t, 2, "[work_loop]\nmax_waiting = 0\n")
	defer m.ReclaimAll()
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "the first session", func() bool { return f.startedCount() == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := f.startedCount(); n != 1 {
		t.Fatalf("started %d sessions with max_waiting = 0", n)
	}
	f.finish(0)
	awaitCond(t, "the second session", func() bool { return f.startedCount() == 2 })
	f.finish(1)
	if final := waitLoopFinished(t, m, "demo"); final.SessionsRun != 2 {
		t.Fatalf("final loop = %+v", final)
	}
}

func TestStopWorkLoopLeavesWaitingSessionsLive(t *testing.T) {
	m, ws, f := newFakeWaiterLoop(t, 3, "")
	defer m.ReclaimAll()
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "two waiting sessions", func() bool {
		snap, _ := m.GetWorkLoop("demo")
		return len(snap.Waiting) == 2
	})
	if _, err := m.StopWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	final := waitLoopFinished(t, m, "demo")
	names := f.id(0) + " (" + strings.TrimPrefix(f.id(0), "sess_") + "), " +
		f.id(1) + " (" + strings.TrimPrefix(f.id(1), "sess_") + ")"
	if final.Outcome != "loop stopped: requested; still waiting on background jobs: "+names || len(final.Waiting) != 2 {
		t.Fatalf("final loop = %+v", final)
	}
	if f.startedCount() != 2 {
		t.Fatalf("stopped loop started %d sessions", f.startedCount())
	}
	awaitCond(t, "the watchers to let go", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.released[f.started[0]] && f.released[f.started[1]]
	})
	if p, ok := readPersistedWorkLoop(ws); !ok || p.State != "finished" || len(persistedSlots(p)) != 2 || p.Slots[0].State != "waiting" {
		t.Fatalf("persisted loop = %+v", p)
	}
}

// A stop requested while the loop checks its waiting sessions starts nothing.
func TestStopDuringWaiterCheckStartsNoSession(t *testing.T) {
	checked, resume := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := 0
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n != 1 {
				return loopSessRec{id: "started-after-stop"}, false, nil
			}
			var once sync.Once
			return loopSessRec{waiter: &loopWaiter{id: "first",
				working: func() bool { once.Do(func() { close(checked); <-resume }); return false },
				resume:  func(release <-chan struct{}) loopWaitResult { <-release; return loopWaitResult{released: true} },
			}}, false, nil
		}
	})
	defer m.ReclaimAll()
	if _, err := docs.NewStore(ws).Create("ready", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	<-checked
	_, err := m.StopWorkLoop("demo")
	close(resume)
	if err != nil {
		t.Fatal(err)
	}
	final := waitLoopFinished(t, m, "demo")
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 || !strings.HasPrefix(final.Outcome, "loop stopped: requested; still waiting on background jobs: first") {
		t.Fatalf("calls %d, final loop = %+v", calls, final)
	}
}

// A reached budget stops the loop even while the only ready work belongs to
// a session still waiting; that session is left live.
func TestWorkLoopBudgetStopsWhileWaitingOnHeldWork(t *testing.T) {
	m, _, f := newFakeWaiterLoop(t, 2, "")
	original := m.newRunSession
	m.newRunSession = func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.loopTokens = 50
		return original(wl)
	}
	defer m.ReclaimAll()
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "both waiters", func() bool {
		s, _ := m.GetWorkLoop("demo")
		return s.State == "waiting" && len(s.Waiting) == 2
	})
	f.finish(0)
	final := waitLoopFinished(t, m, "demo")
	if !strings.HasPrefix(final.Outcome, "loop stopped: budget reached") || !strings.Contains(final.Outcome, "still waiting on background jobs: "+f.id(1)) || final.TotalTokens != 100 {
		t.Fatalf("final loop = %+v", final)
	}
}

// A waiting session that ends without progress counts toward the stall
// backoff like any other session.
func TestWaiterWithoutProgressBacksOff(t *testing.T) {
	ended, next := make(chan struct{}), make(chan struct{})
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.maxWaiting = 1
		wl.waitingRecheck = 10 * time.Millisecond
		starts := 0
		return func(ctx context.Context) (loopSessRec, bool, error) {
			starts++
			if starts != 1 {
				close(next)
				<-ctx.Done()
				return loopSessRec{}, false, ctx.Err()
			}
			return loopSessRec{waiter: &loopWaiter{id: "first", resume: func(release <-chan struct{}) loopWaitResult {
				select {
				case <-ended:
					return loopWaitResult{rec: loopSessRec{id: "first", tokens: 25}}
				case <-release:
					return loopWaitResult{released: true}
				}
			}}}, false, nil
		}
	})
	defer m.ReclaimAll()
	if _, err := docs.NewStore(ws).Create("ready", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "waiting at the cap", func() bool {
		s, _ := m.GetWorkLoop("demo")
		return s.WaitKind == waitKindSessionsWaiting
	})
	close(ended)
	awaitCond(t, "the no-progress backoff", func() bool {
		s, _ := m.GetWorkLoop("demo")
		return s.WaitKind == waitKindNoProgress && s.SessionsRun == 1
	})
	select {
	case <-next:
		t.Fatal("started another session right after a waiting session made no progress")
	default:
	}
}

// A daemon shutdown names the sessions that were waiting, and restoring the
// loop keeps that outcome.
func TestShutdownNamesWaitingSessions(t *testing.T) {
	m, ws, f := newFakeWaiterLoop(t, 2, "")
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "two waiting sessions", func() bool {
		s, _ := m.GetWorkLoop("demo")
		return s.State == "waiting" && len(s.Waiting) == 2
	})
	m.ReclaimAll()
	restarted := NewManager(workLoopPersistTestRegistry(), ws)
	defer restarted.ReclaimAll()
	got, err := restarted.GetWorkLoop("")
	if err != nil {
		t.Fatal(err)
	}
	want := "loop interrupted: daemon shutting down; sessions were waiting on background jobs: " +
		f.id(0) + " (" + strings.TrimPrefix(f.id(0), "sess_") + "), " + f.id(1) + " (" + strings.TrimPrefix(f.id(1), "sess_") + ")"
	if got == nil || got.Outcome != want {
		t.Fatalf("restored loop = %+v", got)
	}
}

// A waiting session whose end races a graceful stop is counted, not named as
// still waiting: finish joins the watchers before it reports.
func TestStopCountsWaiterThatEndsAtRelease(t *testing.T) {
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.maxWaiting = 1
		wl.waitingRecheck = time.Hour
		return func(context.Context) (loopSessRec, bool, error) {
			return loopSessRec{waiter: &loopWaiter{id: "first", resume: func(release <-chan struct{}) loopWaitResult {
				<-release
				time.Sleep(50 * time.Millisecond) // still recording its end
				return loopWaitResult{rec: loopSessRec{id: "first", tokens: 10}}
			}}}, false, nil
		}
	})
	defer m.ReclaimAll()
	if _, err := docs.NewStore(ws).Create("ready", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "the loop to wait", func() bool {
		s, _ := m.GetWorkLoop("demo")
		return s.WaitKind == waitKindSessionsWaiting
	})
	if _, err := m.StopWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	final := waitLoopFinished(t, m, "demo")
	if final.SessionsRun != 1 || final.TotalTokens != 10 || len(final.Waiting) != 0 || final.Outcome != "loop stopped: requested" {
		t.Fatalf("final loop = %+v", final)
	}
}

// A waiting session's record published just before the next start is
// accounted first: here it reaches the loop budget, so nothing starts.
func TestWaiterResultBeforeStartIsAccounted(t *testing.T) {
	end, checked := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	starts := 0
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.loopTokens = 50
		return func(ctx context.Context) (loopSessRec, bool, error) {
			mu.Lock()
			starts++
			n := starts
			mu.Unlock()
			if n != 1 {
				return loopSessRec{id: "started-over-cap"}, false, nil
			}
			var once sync.Once
			return loopSessRec{waiter: &loopWaiter{id: "first",
				// The record is published while the loop checks this waiter.
				working: func() bool {
					once.Do(func() {
						close(end)
						awaitCond(t, "the record", wl.hasEnded)
						close(checked)
					})
					return false
				},
				resume: func(release <-chan struct{}) loopWaitResult {
					select {
					case <-end:
						return loopWaitResult{rec: loopSessRec{id: "first", tokens: 100}}
					case <-release:
						return loopWaitResult{released: true}
					}
				}}}, false, nil
		}
	})
	defer m.ReclaimAll()
	if _, err := docs.NewStore(ws).Create("ready", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	<-checked
	final := waitLoopFinished(t, m, "demo")
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 || final.SessionsRun != 1 || !strings.HasPrefix(final.Outcome, "loop stopped: budget reached") {
		t.Fatalf("starts %d, final loop = %+v", starts, final)
	}
}

// A waiting session that ends during a provider backoff interrupts it, so its
// record is accounted (here reaching the budget) without serving the rest.
func TestWaiterEndInterruptsProviderBackoff(t *testing.T) {
	end := make(chan struct{})
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.loopTokens = 50
		starts := 0
		return func(ctx context.Context) (loopSessRec, bool, error) {
			starts++
			if starts != 1 {
				return loopSessRec{id: "foreground", errKind: "rate_limit", errRetryable: true}, false, nil
			}
			return loopSessRec{waiter: &loopWaiter{id: "first", resume: func(release <-chan struct{}) loopWaitResult {
				select {
				case <-end:
					return loopWaitResult{rec: loopSessRec{id: "first", tokens: 100}}
				case <-release:
					return loopWaitResult{released: true}
				}
			}}}, false, nil
		}
	})
	defer m.ReclaimAll()
	if _, err := docs.NewStore(ws).Create("ready", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "provider backoff", func() bool {
		s, _ := m.GetWorkLoop("demo")
		return s.WaitKind == "rate_limit"
	})
	close(end)
	final := waitLoopFinished(t, m, "demo")
	if final.TotalTokens != 100 || !strings.HasPrefix(final.Outcome, "loop stopped: budget reached") {
		t.Fatalf("final loop = %+v", final)
	}
}

// A session its job has woken is named as working, not as waiting.
func TestStopNamesWokenWaiterAsWorking(t *testing.T) {
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.waitingRecheck = 10 * time.Millisecond
		return func(context.Context) (loopSessRec, bool, error) {
			return loopSessRec{waiter: &loopWaiter{id: "woken", awake: true, working: func() bool { return true },
				resume: func(release <-chan struct{}) loopWaitResult { <-release; return loopWaitResult{released: true} }}}, false, nil
		}
	})
	defer m.ReclaimAll()
	if _, err := docs.NewStore(ws).Create("ready", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "the loop to show its working session", func() bool {
		s, _ := m.GetWorkLoop("demo")
		return s.State == "running" && len(s.Slots) == 1 && s.Slots[0].Detached && s.Slots[0].State == "working"
	})
	if _, err := m.StopWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	if final := waitLoopFinished(t, m, "demo"); final.Outcome != "loop stopped: requested; still working after their jobs woke them: woken" {
		t.Fatalf("final loop = %+v", final)
	}
}

// Patience counts only backoff served: results settled together schedule
// backoff but do not exhaust patience before it is waited.
func TestLoopPacingChargesOnlyServedBackoff(t *testing.T) {
	stall := loopPacing{stallSessions: 10, stallWait: workLoopStallPatience - time.Hour}
	retry := loopPacing{outageFailures: 10, outageWait: workLoopProviderPatience - time.Hour}
	for i := 0; i < 2; i++ {
		if outcome, _ := stall.settled(loopSessRec{id: "s"}, false, nil); outcome != "" || stall.stallDelay != time.Hour {
			t.Fatalf("stall result %d: %q, delay %v", i+1, outcome, stall.stallDelay)
		}
		if outcome, delay := retry.settled(loopSessRec{id: "s", errKind: "rate_limit", errRetryable: true}, false, nil); outcome != "" || delay != 30*time.Minute {
			t.Fatalf("retry result %d: %q, delay %v", i+1, outcome, delay)
		}
	}
}

// A waiting session's job does not resume it while another loop session
// holds the loop's one working slot; the wake is delivered once the slot frees.
func TestJobWakeWaitsForTheWorkingSlot(t *testing.T) {
	var first *Session
	second := newStopSession(t)
	second.ID = "second"
	var mu sync.Mutex
	starts := 0
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.waitGrace = 0
		wl.waitingRecheck = 10 * time.Millisecond
		wl.startSession = func(Config) (*Session, error) {
			mu.Lock()
			starts++
			s := first
			if starts > 1 {
				s = second
			}
			mu.Unlock()
			wl.m.mu.Lock()
			wl.m.sessions[s.ID] = s
			wl.m.mu.Unlock()
			return s, nil
		}
		return wl.realRunSession
	})
	defer m.ReclaimAll()
	overlapped := make(chan bool, 1)
	turner := &scriptedTurner{texts: []string{"started the run; waiting on it", "run checked"}, hook: func(n int) {
		if n == 2 {
			overlapped <- second.Status() == event.StatusRunning
		}
	}}
	s, jr := newJobWakeSession(t, turner)
	first = s
	first.ID, first.Workspace = "first", ws
	second.Workspace = ws
	store := docs.NewStore(ws)
	a, err := store.Create("first", "", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Create("second", "", 2, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first.emitter.Emit(event.TaskFocus, map[string]any{"task": a.ID})
	job := jr.Start("bash", "long run", "coordinator")
	go first.run()
	waitStatus(t, first, event.StatusIdle)
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "the second session to work", func() bool {
		snap, _ := m.GetWorkLoop("demo")
		return snap.CurrentSessionID == second.ID && len(snap.Waiting) == 1
	})

	job.Finish(jobs.Done, "run complete")
	select {
	case <-overlapped:
		t.Fatal("the job resumed the waiting session while the second session worked")
	case <-time.After(400 * time.Millisecond):
	}
	if !first.AwaitingJobs() {
		t.Fatal("the held-back completion was lost")
	}

	for _, id := range []string{a.ID, b.ID} {
		if _, err := store.Update(id, func(tk *docs.Task) { tk.Status = docs.StatusDone }); err != nil {
			t.Fatal(err)
		}
	}
	second.emitter.Emit(event.SessionIdle, map[string]any{"report": "B done"})
	second.setStatus(event.StatusIdle)
	select {
	case both := <-overlapped:
		if both {
			t.Fatal("the waiting session resumed while the second session still worked")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the held-back wake was not delivered once the slot freed")
	}
	final := waitLoopFinished(t, m, "demo")
	if final.SessionsRun != 2 || final.Outcome != "loop complete: no ready tasks remain" {
		t.Fatalf("final loop = %+v", final)
	}
}

// A woken waiting session that ends frees the slot only together with its
// record, so a record reaching the budget is seen before any next start.
func TestSlotFreesWithTheEndedSessionsRecord(t *testing.T) {
	end := make(chan struct{})
	var running sync.Map
	var mu sync.Mutex
	starts := 0
	m, _, ws := loopTestManager(t, func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.loopTokens = 50
		wl.waitingRecheck = 5 * time.Millisecond
		return func(context.Context) (loopSessRec, bool, error) {
			mu.Lock()
			starts++
			n := starts
			mu.Unlock()
			if n != 1 {
				return loopSessRec{id: "started-over-cap"}, false, nil
			}
			running.Store("first", true)
			var took sync.Once
			return loopSessRec{waiter: &loopWaiter{id: "first",
				working: func() bool {
					took.Do(func() { wl.admitWake("first") })
					_, ok := running.Load("first")
					return ok
				},
				resume: func(release <-chan struct{}) loopWaitResult {
					select {
					case <-end:
						running.Delete("first")
						return loopWaitResult{rec: loopSessRec{id: "first", tokens: 100}}
					case <-release:
						return loopWaitResult{released: true}
					}
				}}}, false, nil
		}
	})
	defer m.ReclaimAll()
	if _, err := docs.NewStore(ws).Create("ready", "", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	awaitCond(t, "the woken waiter to hold the working slot", func() bool {
		s, _ := m.GetWorkLoop("demo")
		return s.State == "running" && len(s.Slots) == 1 && s.Slots[0].Detached && s.Slots[0].State == "working"
	})
	close(end)
	final := waitLoopFinished(t, m, "demo")
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 || !strings.HasPrefix(final.Outcome, "loop stopped: budget reached") {
		t.Fatalf("starts %d, final loop = %+v", starts, final)
	}
}

// A wake with nothing to deliver, such as a nudge while the session's shell
// job still runs, never takes the working slot.
func TestEmptyWakeDoesNotTakeTheSlot(t *testing.T) {
	sess, jr := newJobWakeSession(t, &scriptedTurner{})
	jr.Start("bash", "still running", "coordinator")
	sess.setIdle(true)
	wl := &workLoop{}
	sess.setWakeGate(func() bool { return wl.admitWake(sess.ID) }, func() { wl.cancelWake(sess.ID) })
	for i := 0; i < 3; i++ {
		if resumed, err := sess.resumeForFinishedJobs(); resumed || err != nil {
			t.Fatalf("empty wake = %v, %v", resumed, err)
		}
		if len(wl.holders) != 0 {
			t.Fatalf("empty wake took the slot: %v", wl.holders)
		}
	}
	// A finished job is delivered only through the slot.
	job := jr.Start("bash", "finished", "coordinator")
	job.Finish(jobs.Done, "ok")
	wl.holdLocked("someone-else")
	if resumed, _ := sess.resumeForFinishedJobs(); resumed || !jr.CompletionReady("coordinator") {
		t.Fatal("a completion was delivered, or lost, while another session held the slot")
	}
}

// A completion that finishes after the readiness check is not claimed by an
// unadmitted wake; it stays queued for a wake that asks the gate. The job is
// finished at varying points while the wake is in flight.
func TestCompletionRacingTheReadinessCheckStaysQueued(t *testing.T) {
	s, _ := newJobWakeSession(t, &scriptedTurner{})
	wl := &workLoop{holders: map[string]struct{}{"other-session": {}}}
	s.setWakeGate(func() bool { return wl.admitWake(s.ID) }, func() { wl.cancelWake(s.ID) })
	type result struct {
		resumed bool
		err     error
	}
	for i := 0; i < 300; i++ {
		s.setIdle(true)
		jr := jobs.NewRegistry()
		s.deps.Jobs = jr
		job := jr.Start("bash", "finishing during the wake", "coordinator")
		for j := 0; j < 256; j++ {
			jr.Start("bash", "unrelated", "other-owner")
		}
		outcomes := make(chan result, 1)
		go func() {
			resumed, err := s.resumeForFinishedJobs()
			outcomes <- result{resumed, err}
		}()
		for posted := false; !posted; {
			s.mu.Lock()
			posted = s.jobWakePosted
			s.mu.Unlock()
			select {
			case got := <-outcomes:
				outcomes <- got
				posted = true
			default:
				runtime.Gosched()
			}
		}
		for j := 0; j < i%17; j++ {
			runtime.Gosched()
		}
		job.Finish(jobs.Done, "finished")
		got := <-outcomes
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.resumed || countType(s.Log().Snapshot(), event.JobNotified) > 0 {
			t.Fatalf("iteration %d: the completion was claimed without slot admission", i)
		}
		jr.KillAll()
	}
}
