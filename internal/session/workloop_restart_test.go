package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/notify"
)

// restartLoopManager is a shutdownTestManager that keeps a restart manifest
// and pushes notifications to a sink, with one ready task.
func restartLoopManager(t *testing.T, steps []*[2]string) (m *Manager, ws, path string, sink *notifySink, task *docs.Task) {
	t.Helper()
	m, ws = shutdownTestManager(t, steps)
	sink = newNotifySink(t)
	m.SetNotifier(notify.New(config.Notify{URL: sink.URL}))
	path = filepath.Join(t.TempDir(), "restart-state.json")
	m.SetRestartStateFile(path)
	task, err := docs.NewStore(ws).Create("resume me", "", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m, ws, path, sink, task
}

// loopSlotSession waits until the loop's slot shows a session in state and
// returns its id.
func loopSlotSession(t *testing.T, m *Manager, state string) string {
	t.Helper()
	id := ""
	waitLong(t, "a loop session "+state, func() bool {
		snap, _ := m.GetWorkLoop("proj")
		if snap == nil {
			return false
		}
		for _, slot := range snap.Slots {
			if slot.State == state && slot.SessionID != "" {
				id = slot.SessionID
				return true
			}
		}
		return false
	})
	return id
}

func noDigest(t *testing.T, sink *notifySink) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	for _, rec := range sink.all() {
		if rec.tags == notify.KindDigest {
			t.Fatalf("restart pushed a loop digest: %q", rec.body)
		}
	}
}

func markTask(t *testing.T, ws, id string, status docs.Status) {
	t.Helper()
	if _, err := docs.NewStore(ws).Update(id, func(tk *docs.Task) { tk.Status = status }); err != nil {
		t.Fatal(err)
	}
}

// A graceful restart suspends a loop whose session works mid-turn: the
// session stays out of the manifest's own lists, the loop's checkpoint
// carries the manifest's restart id with its slot and original baseline, and
// no outcome or digest is recorded. The next startup resumes the loop and
// re-adopts the session unattended, with its loop configuration and slot gate,
// before execution is released; the loop then accounts for it once and keeps
// its baseline (a task added during the downtime counts as created).
func TestWorkLoopResumesWorkingSlotAcrossGracefulRestart(t *testing.T) {
	m, ws, path, sink, task := restartLoopManager(t, []*[2]string{nil})
	if _, err := m.StartWorkLoop("proj"); err != nil {
		t.Fatal(err)
	}
	sid := loopSlotSession(t, m, loopSlotWorking)
	waitFor(t, func() bool {
		s, ok := m.Get(sid)
		return ok && s.Status() == event.StatusRunning && len(eventsOf(s, event.UserInput)) == 1
	})
	m.ShutdownForRestart()

	st := readRestartState(t, path)
	if len(st.SuspendedLoops) != 1 || st.SuspendedLoops[0].Workspace != ws || len(st.Waiting)+len(st.Interrupted) != 0 {
		t.Fatalf("manifest = %+v", st)
	}
	p, ok := readPersistedWorkLoop(ws)
	if !ok || p.State != workLoopSuspendedState || p.Restart == nil || p.Restart.RestartID != st.RestartID ||
		p.Outcome != "" || p.FinishedAt != nil {
		t.Fatalf("checkpoint = %+v", p)
	}
	cp := p.Restart
	if len(cp.Slots) != 1 || cp.Slots[0].SessionID != sid || cp.Slots[0].Kind != loopSlotWorking || cp.Slots[0].LastSeq == 0 ||
		cp.Baseline[task.ID] != docs.StatusTodo || cp.State != "running" {
		t.Fatalf("checkpoint scheduler state = %+v", cp)
	}
	noDigest(t, sink)

	// While the daemon is down a task is added (not ready) and the loop's task
	// is finished elsewhere.
	added, err := docs.NewStore(ws).Create("added during the downtime", "", 5, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	markTask(t, ws, added.ID, docs.StatusBlocked)
	markTask(t, ws, task.ID, docs.StatusDone)

	m2 := recoveringManager(t, m, nil, path, nil)
	snap, _ := m2.GetWorkLoop("proj")
	if snap == nil || snap.State != "running" || snap.LoopID != p.LoopID || len(snap.Slots) != 1 || snap.Slots[0].SessionID != sid {
		t.Fatalf("resumed loop = %+v", snap)
	}
	s2, ok := m2.Get(sid)
	if !ok {
		t.Fatal("the loop session was not re-adopted")
	}
	s2.mu.Lock()
	gated := s2.wakeGate != nil
	s2.mu.Unlock()
	if !s2.unattended || !s2.inter.unattended || s2.origin != OriginWorkLoop || s2.deps.ShellWaitCap != loopShellWaitCap || !gated {
		t.Fatalf("re-adopted session: unattended=%v/%v origin=%q wait cap=%v gate=%v",
			s2.unattended, s2.inter.unattended, s2.origin, s2.deps.ShellWaitCap, gated)
	}
	if n := restartMarkers(s2.Log().Snapshot(), st.RestartID); n != 1 {
		t.Fatalf("%d restart markers before release, want 1", n)
	}
	if p, _ := readPersistedWorkLoop(ws); p.State == workLoopSuspendedState {
		t.Fatal("resuming did not consume the checkpoint")
	}
	m2.ReleaseExecution()

	final := waitLoopFinishedLong(t, m2)
	if final.Outcome != "loop complete: no ready tasks remain" || final.SessionsRun != 1 || final.Sessions[0].SessionID != sid {
		t.Fatalf("final loop = %+v", final)
	}
	created := false
	for _, c := range final.Created {
		created = created || c.ID == added.ID
	}
	completed := false
	for _, c := range final.Completed {
		completed = completed || c.ID == task.ID
	}
	if !created || !completed {
		t.Fatalf("digest against the original baseline: created=%+v completed=%+v", final.Created, final.Completed)
	}
}

func waitLoopFinishedLong(t *testing.T, m *Manager) *WorkLoop {
	t.Helper()
	var final *WorkLoop
	waitLong(t, "the loop to finish", func() bool {
		final, _ = m.GetWorkLoop("proj")
		return final != nil && final.State == "finished"
	})
	return final
}

// A checkpoint whose restart id the manifest does not carry is not resumed:
// the loop restores as interrupted, naming its session, which stays closed.
// So does a checkpoint left without any manifest (a crash after writing it).
func TestWorkLoopUnmatchedCheckpointRestoresInterrupted(t *testing.T) {
	for _, tc := range []string{"unmatched", "no manifest", "unconsumable"} {
		t.Run(tc, func(t *testing.T) {
			m, ws, path, _, _ := restartLoopManager(t, []*[2]string{nil})
			if _, err := m.StartWorkLoop("proj"); err != nil {
				t.Fatal(err)
			}
			sid := loopSlotSession(t, m, loopSlotWorking)
			waitFor(t, func() bool {
				s, ok := m.Get(sid)
				return ok && len(eventsOf(s, event.UserInput)) == 1
			})
			m.ShutdownForRestart()
			st := readRestartState(t, path)
			switch tc {
			case "unmatched":
				st.RestartID = "another-restart"
				writeTestRestartState(t, path, st)
			case "no manifest":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "unconsumable":
				// The checkpoint cannot be replaced, so it is not resumed.
				dir := filepath.Dir(workLoopPersistPath(ws))
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
			}

			m2 := recoveringManager(t, m, nil, path, nil)
			if _, ok := m2.Get(sid); ok {
				t.Fatal("a loop session was reopened without a matching checkpoint")
			}
			snap, _ := m2.GetWorkLoop("proj")
			want := "loop interrupted: daemon restarted; sessions were working: " + sid
			if snap == nil || snap.State != "finished" || snap.Outcome != want {
				t.Fatalf("loop = %+v, want outcome %q", snap, want)
			}
			if p, _ := readPersistedWorkLoop(ws); tc != "unconsumable" && p.State != "finished" {
				t.Fatalf("persisted state = %q, want finished", p.State)
			}
		})
	}
}

// A loop session waiting on its own supervised background job is kept as a
// waiting slot and re-adopted as one: it reopens awaiting the job with the
// loop's wake gate, the job's completion wakes it, and the loop accounts for
// its final record once.
func TestWorkLoopResumesWaitingSlotAcrossGracefulRestart(t *testing.T) {
	launcher := supervisedTestLauncher(t)
	m, ws, path, sink, task := restartLoopManager(t, []*[2]string{
		{"Bash", `{"command":"sleep 3; echo survived","run_in_background":true}`},
	})
	m.SetJobLauncher(launcher)
	m.newRunSession = func(wl *workLoop) func(ctx context.Context) (loopSessRec, bool, error) {
		wl.waitGrace = 0
		return wl.realRunSession
	}
	if _, err := m.StartWorkLoop("proj"); err != nil {
		t.Fatal(err)
	}
	sid := loopSlotSession(t, m, loopSlotWaiting)
	m.ShutdownForRestart()

	st := readRestartState(t, path)
	p, _ := readPersistedWorkLoop(ws)
	if len(st.SuspendedLoops) != 1 || p.Restart == nil || p.Restart.RestartID != st.RestartID || len(p.Restart.Slots) != 1 ||
		p.Restart.Slots[0].SessionID != sid || p.Restart.Slots[0].Kind != loopSlotWaiting {
		t.Fatalf("manifest %+v, checkpoint %+v", st, p.Restart)
	}
	noDigest(t, sink)
	markTask(t, ws, task.ID, docs.StatusDone)

	m2 := recoveringManager(t, m, launcher, path, nil)
	s2, ok := m2.Get(sid)
	if !ok || !s2.AwaitingJobs() || !s2.unattended {
		t.Fatalf("waiting loop session re-adopted: live=%v", ok)
	}
	snap, _ := m2.GetWorkLoop("proj")
	if len(snap.Waiting) != 1 || snap.Waiting[0].SessionID != sid {
		t.Fatalf("resumed loop = %+v", snap)
	}
	m2.ReleaseExecution()
	final := waitLoopFinishedLong(t, m2)
	if final.SessionsRun != 1 || final.Sessions[0].SessionID != sid || len(final.Waiting) != 0 {
		t.Fatalf("final loop = %+v", final)
	}
	if n := len(eventsOf(s2, event.JobNotified)); n != 1 {
		t.Fatalf("%d job notifications, want the survivor's completion once", n)
	}
}

// A loop session that ends while a graceful shutdown drains is settled into
// the checkpoint from its closed log, with its final report, and the loop
// starts nothing more: the checkpoint has no slot, and the resumed loop counts
// that session once.
func TestWorkLoopSettlesSessionEndingDuringShutdown(t *testing.T) {
	m, ws, path, sink, task := restartLoopManager(t, []*[2]string{
		{"Bash", `{"command":"sleep 1"}`},
	})
	if _, err := m.StartWorkLoop("proj"); err != nil {
		t.Fatal(err)
	}
	sid := loopSlotSession(t, m, loopSlotWorking)
	s, _ := m.Get(sid)
	waitFor(t, func() bool { return lastCoordinatorToolCall(s, "Bash") })
	m.BeginShutdown()
	waitLong(t, "the session to finish its turn", func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
	time.Sleep(300 * time.Millisecond)
	if snap, _ := m.GetWorkLoop("proj"); snap.SessionsRun != 0 || snap.State == "finished" {
		t.Fatalf("loop during the drain = %+v, want the session left to the checkpoint", snap)
	}
	m.ShutdownForRestart()
	p, _ := readPersistedWorkLoop(ws)
	if p.State != workLoopSuspendedState || p.Restart == nil || len(p.Restart.Slots) != 0 || len(p.Sessions) != 1 ||
		p.Sessions[0].SessionID != sid || p.Sessions[0].Evidence != "done" {
		t.Fatalf("checkpoint = %+v", p)
	}
	noDigest(t, sink)
	markTask(t, ws, task.ID, docs.StatusDone)

	m2 := recoveringManager(t, m, nil, path, nil)
	if _, ok := m2.Get(sid); ok {
		t.Fatal("an ended loop session was reopened")
	}
	m2.ReleaseExecution()
	final := waitLoopFinishedLong(t, m2)
	if final.SessionsRun != 1 || final.Outcome != "loop complete: no ready tasks remain" {
		t.Fatalf("final loop = %+v", final)
	}
}

// The checkpoint takes a session's ending and its focus from its closed log,
// not from what the loop last saw: a status published before the final
// report, or a task claimed after the loop let go of the session, is not
// lost.
func TestWorkLoopCheckpointReadsTheClosedLog(t *testing.T) {
	t.Run("final report", func(t *testing.T) {
		m, ws, _, _, _ := restartLoopManager(t, []*[2]string{nil})
		if _, err := m.StartWorkLoop("proj"); err != nil {
			t.Fatal(err)
		}
		sid := loopSlotSession(t, m, loopSlotWorking)
		s, _ := m.Get(sid)
		waitFor(t, func() bool { return len(eventsOf(s, event.UserInput)) == 1 })
		m.BeginShutdown()
		// The run publishes idle before it records the report.
		s.setIdle(false)
		time.Sleep(300 * time.Millisecond)
		s.emitter.Emit(event.SessionIdle, map[string]any{"report": "late durable report"})
		m.ShutdownForRestart()
		p, _ := readPersistedWorkLoop(ws)
		if len(p.Sessions) != 1 || p.Sessions[0].Evidence != "late durable report" || p.Restart == nil || len(p.Restart.Slots) != 0 {
			t.Fatalf("checkpoint = %+v", p)
		}
	})
	t.Run("failure", func(t *testing.T) {
		m, ws, _, _, _ := restartLoopManager(t, []*[2]string{nil})
		if _, err := m.StartWorkLoop("proj"); err != nil {
			t.Fatal(err)
		}
		sid := loopSlotSession(t, m, loopSlotWorking)
		s, _ := m.Get(sid)
		waitFor(t, func() bool { return len(eventsOf(s, event.UserInput)) == 1 })
		m.BeginShutdown()
		m.suspendLoopsForRestart()
		// The engine records a provider failure before the run marks the
		// session errored.
		s.emitter.Emit(event.SessionError, map[string]any{"kind": "server_error", "msg": "late retryable failure", "retryable": true})
		m.ShutdownForRestart()
		p, _ := readPersistedWorkLoop(ws)
		if len(p.Sessions) != 1 || p.Sessions[0].ErrorKind != "server_error" || !p.Sessions[0].ErrorRetryable ||
			p.Restart == nil || p.Restart.Pacing.RetryDelay == 0 {
			t.Fatalf("checkpoint sessions %+v, scheduler %+v", p.Sessions, p.Restart)
		}
	})
	t.Run("focus", func(t *testing.T) {
		m, ws, _, _, task := restartLoopManager(t, []*[2]string{nil})
		if _, err := m.StartWorkLoop("proj"); err != nil {
			t.Fatal(err)
		}
		sid := loopSlotSession(t, m, loopSlotWorking)
		s, _ := m.Get(sid)
		waitFor(t, func() bool { return len(eventsOf(s, event.UserInput)) == 1 })
		m.BeginShutdown()
		m.suspendLoopsForRestart()
		s.emitter.Emit(event.TaskFocus, map[string]any{"task": task.ID})
		m.ShutdownForRestart()
		p, _ := readPersistedWorkLoop(ws)
		if p.Restart == nil || len(p.Restart.Slots) != 1 || p.Restart.Slots[0].Focus != task.ID ||
			!p.Restart.Slots[0].Claimed || p.Restart.Slots[0].Attempt != 1 {
			t.Fatalf("checkpoint slots = %+v", p.Restart)
		}
	})
}

// A loop session that ends after the loop let go of it, as the sessions are
// suspended, is settled into the checkpoint from its closed log rather than
// kept as a slot.
func TestWorkLoopCheckpointSettlesSessionEndedAfterSuspension(t *testing.T) {
	m, ws, path, _, _ := restartLoopManager(t, []*[2]string{
		{"Bash", `{"command":"sleep 1"}`},
	})
	if _, err := m.StartWorkLoop("proj"); err != nil {
		t.Fatal(err)
	}
	sid := loopSlotSession(t, m, loopSlotWorking)
	s, _ := m.Get(sid)
	waitFor(t, func() bool { return lastCoordinatorToolCall(s, "Bash") })
	m.BeginShutdown()
	m.suspendLoopsForRestart()
	waitLong(t, "the let-go session to finish its turn", func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
	m.ShutdownForRestart()
	st := readRestartState(t, path)
	p, _ := readPersistedWorkLoop(ws)
	if len(st.SuspendedLoops) != 1 || p.Restart == nil || len(p.Restart.Slots) != 0 || len(p.Sessions) != 1 ||
		p.Sessions[0].SessionID != sid || p.Sessions[0].Evidence != "done" {
		t.Fatalf("checkpoint = %+v", p)
	}
}

// A loop session blocked in wait at shutdown is a working slot; re-adopted,
// it resumes its wait, and the survivor's result reaches it.
func TestWorkLoopResumesSlotBlockedInWait(t *testing.T) {
	launcher := supervisedTestLauncher(t)
	m, ws, path, _, task := restartLoopManager(t, []*[2]string{
		{"Bash", `{"command":"sleep 2; echo survived","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1"],"timeout_s":60}`},
	})
	m.SetJobLauncher(launcher)
	if _, err := m.StartWorkLoop("proj"); err != nil {
		t.Fatal(err)
	}
	sid := loopSlotSession(t, m, loopSlotWorking)
	waitFor(t, func() bool {
		s, ok := m.Get(sid)
		return ok && len(eventsOf(s, event.WaitStarted)) == 1
	})
	m.ShutdownForRestart()
	p, _ := readPersistedWorkLoop(ws)
	if p.Restart == nil || len(p.Restart.Slots) != 1 || p.Restart.Slots[0].Kind != loopSlotWorking {
		t.Fatalf("checkpoint = %+v", p.Restart)
	}
	markTask(t, ws, task.ID, docs.StatusDone)

	m2 := recoveringManager(t, m, launcher, path, nil)
	s2, ok := m2.Get(sid)
	if !ok || s2.resumeWait == nil {
		t.Fatalf("slot session re-adopted=%v with its wait restored", ok)
	}
	m2.ReleaseExecution()
	final := waitLoopFinishedLong(t, m2)
	if final.SessionsRun != 1 || final.Sessions[0].SessionID != sid {
		t.Fatalf("final loop = %+v", final)
	}
	if _, res := restoredWaitResult(s2.Log().Snapshot()); res == nil || !strings.Contains(str(res.Data, "result"), "survived") {
		t.Fatalf("restored wait result = %v", res)
	}
}

// Both waiting sessions are working again when the target shrinks. On
// restart each must take its slot back before execution is released, even
// though readopting the second puts the loop over its persisted target.
func TestWorkLoopReadoptedWokenWaiterHoldsItsSlot(t *testing.T) {
	launcher := supervisedTestLauncher(t)
	m, ws, path, _, task := restartLoopManager(t, []*[2]string{
		{"Bash", `{"command":"while [ ! -f trigger ]; do sleep 0.05; done; echo woke","run_in_background":true}`},
		{"", "waiting on the job"},
		nil,
	})
	m.SetJobLauncher(launcher)
	m.newRunSession = func(wl *workLoop) func(ctx context.Context) (loopSessRec, bool, error) {
		wl.waitGrace = 0
		return wl.realRunSession
	}
	if _, err := m.StartWorkLoopWith("proj", WorkLoopOptions{Parallel: 2}); err != nil {
		t.Fatal(err)
	}
	var waiters []WorkLoopWaitingSession
	waitLong(t, "two detached sessions", func() bool {
		s, _ := m.GetWorkLoop("proj")
		waiters = s.Waiting
		return len(waiters) == 2
	})
	if err := os.WriteFile(filepath.Join(ws, "trigger"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "both jobs to wake their sessions", func() bool {
		for _, w := range waiters {
			s, ok := m.Get(w.SessionID)
			if !ok || len(eventsOf(s, event.JobNotified)) != 1 || s.Status() != event.StatusRunning {
				return false
			}
		}
		return true
	})
	if _, err := m.ResizeWorkLoop("proj", "", 1); err != nil {
		t.Fatal(err)
	}
	m.ShutdownForRestart()
	p, _ := readPersistedWorkLoop(ws)
	if p.Parallel != 1 || p.Restart == nil || len(p.Restart.Slots) != 2 {
		t.Fatalf("checkpoint = %+v", p)
	}
	for _, slot := range p.Restart.Slots {
		if slot.Kind != loopSlotWaiting {
			t.Fatalf("non-waiter in checkpoint: %+v", slot)
		}
	}
	markTask(t, ws, task.ID, docs.StatusDone)

	m2 := recoveringManager(t, m, launcher, path, nil)
	m2.loopMu.Lock()
	wl := m2.workLoops[ws]
	m2.loopMu.Unlock()
	wl.mu.Lock()
	for _, w := range waiters {
		if _, held := wl.holders[w.SessionID]; !held || wl.parallel != 1 {
			t.Errorf("restored holders %v at target %d; missing %s or wrong target", wl.holders, wl.parallel, w.SessionID)
		}
	}
	wl.mu.Unlock()
	if wl.admitWake("another-session") {
		t.Fatal("a wake was admitted into the slot the woken session holds")
	}
	m2.ReleaseExecution()
	final := waitLoopFinishedLong(t, m2)
	seen := map[string]int{}
	for _, rec := range final.Sessions {
		seen[rec.SessionID]++
	}
	if final.SessionsRun != 2 || seen[waiters[0].SessionID] != 1 || seen[waiters[1].SessionID] != 1 {
		t.Fatalf("final loop = %+v, want each of %v once", final, waiters)
	}
}

// A follower that reaches a session's terminal cleanup just as a graceful
// restart begins leaves the session to the restart rather than reporting a
// record before the session's log is complete.
func TestLoopFollowerYieldsTerminalCleanupToRestart(t *testing.T) {
	m, _, _, _, _ := restartLoopManager(t, []*[2]string{nil})
	s, err := m.Start(Config{Project: "proj", Mode: "work", Unattended: true, Origin: OriginWorkLoop, Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(eventsOf(s, event.UserInput)) == 1 })
	s.setIdle(false)
	wl := &workLoop{m: m}
	ending, proceed := make(chan struct{}), make(chan struct{})
	released := make(chan bool, 1)
	go func() {
		_, r, _ := wl.awaitSession(m.loopCtx, s, nil, nil, nil, func() {
			close(ending)
			<-proceed
		})
		released <- r
	}()
	<-ending
	m.BeginShutdown()
	close(proceed)
	if !<-released {
		t.Fatal("the follower reported the session's record although the restart owns its teardown")
	}
}

// Once a graceful restart begins, a loop neither admits a waiting session's
// job wake into a working slot (the completion waits for the session
// reopened after the restart) nor claims to finish.
func TestRestartFencesLoopWakesAndFinish(t *testing.T) {
	m, _, _, _, _ := restartLoopManager(t, nil)
	wl := &workLoop{m: m, parallel: 2}
	if !wl.claimFinish() {
		t.Fatal("a loop could not finish before any restart")
	}
	m.BeginShutdown()
	if wl.admitWake("waiter") {
		t.Fatal("a job wake was admitted while the restart suspends the loop")
	}
	if wl.claimFinish() {
		t.Fatal("a loop claimed to finish after the restart began")
	}
}

// A waiting loop session's job that completes during the shutdown drain does
// not wake it: the completion waits for the session reopened after the
// restart, so no slot is taken while the loop is being suspended.
func TestDrainDoesNotWakeWaitingLoopSessions(t *testing.T) {
	launcher := supervisedTestLauncher(t)
	m, ws, _, _, _ := restartLoopManager(t, []*[2]string{
		{"Bash", `{"command":"while [ ! -f trigger ]; do sleep 0.05; done; echo woke","run_in_background":true}`},
		{"", "waiting on my job"},
	})
	m.SetJobLauncher(launcher)
	m.newRunSession = func(wl *workLoop) func(ctx context.Context) (loopSessRec, bool, error) {
		wl.waitGrace = 0
		return wl.realRunSession
	}
	if _, err := m.StartWorkLoop("proj"); err != nil {
		t.Fatal(err)
	}
	sid := loopSlotSession(t, m, loopSlotWaiting)
	s, _ := m.Get(sid)
	m.BeginShutdown()
	if err := os.WriteFile(filepath.Join(ws, "trigger"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "the waiting session's job to finish", func() bool { return len(eventsOf(s, event.JobFinished)) == 1 })
	time.Sleep(500 * time.Millisecond)
	if n := len(eventsOf(s, event.JobNotified)); n != 0 || !s.AwaitingJobs() {
		t.Fatalf("job wake during the drain: %d notifications, awaiting=%v", n, s.AwaitingJobs())
	}
	m.ShutdownForRestart()
}
