package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobshim"
)

// supervisedTestLauncher is a job launcher re-executing the test binary, with
// a private runtime directory outside the workspace.
func supervisedTestLauncher(t *testing.T) *jobshim.Launcher {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rt, err := os.MkdirTemp("/tmp", "ycc-rt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	_ = os.Chmod(rt, 0o700)
	return &jobshim.Launcher{Executable: exe, RuntimeDir: rt}
}

func logEvents(t *testing.T, ws, id string) []event.Event {
	t.Helper()
	log, err := event.OpenLog(filepath.Join(ws, ".ycc", "sessions", id, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	return log.Snapshot()
}

// idList reads a string-list event field in its live or decoded shape.
func idList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func indexOf(evs []event.Event, match func(event.Event) bool) int {
	return slices.IndexFunc(evs, match)
}

func isType(t event.Type) func(event.Event) bool {
	return func(ev event.Event) bool { return ev.Type == t }
}

// restartAwaitingSession starts a session that leaves a supervised background
// job running and goes idle awaiting it, then suspends the daemon for restart.
func restartAwaitingSession(t *testing.T, command string) (*Manager, string, string, *jobshim.Launcher) {
	t.Helper()
	launcher := supervisedTestLauncher(t)
	m, ws := shutdownTestManager(t, []*[2]string{
		{"Bash", `{"command":"` + command + `","run_in_background":true}`},
	})
	m.SetJobLauncher(launcher)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		ev, ok := s.Log().LastByActor("coordinator", event.SessionIdle)
		return ok && boolVal(ev.Data, "awaiting_jobs")
	})
	m.ShutdownForRestart()
	if i := indexOf(logEvents(t, ws, s.ID), isType(event.JobFinished)); i >= 0 {
		t.Fatal("graceful restart recorded the supervised job finished")
	}
	return m, ws, s.ID, launcher
}

func reopenAfterRestart(t *testing.T, m *Manager, launcher *jobshim.Launcher, id string) *Session {
	t.Helper()
	m2, _ := shutdownTestManager(t, nil)
	m2.SetProjects(m.projects)
	m2.SetJobLauncher(launcher)
	s, err := m2.Reopen("proj", id)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	return s
}

// A session idle on a supervised job survives a graceful restart: the job
// keeps running, the reopen records it in surviving_jobs and reopens straight
// into awaiting idle, and the job's completion wakes the coordinator with its
// report, exactly once.
func TestRestartReopenSurvivingJobWakesSession(t *testing.T) {
	m, ws, id, launcher := restartAwaitingSession(t, "sleep 2; echo survived")
	s := reopenAfterRestart(t, m, launcher, id)
	waitFor(t, func() bool { _, ok := s.Log().LastByActor("coordinator", event.SessionReopened); return ok })
	ev, _ := s.Log().LastByActor("coordinator", event.SessionReopened)
	if got := idList(ev.Data["surviving_jobs"]); len(got) != 1 || got[0] != "job_1" || !boolVal(ev.Data, "awaiting_jobs") {
		t.Fatalf("session_reopened = %v", ev.Data)
	}
	if !s.AwaitingJobs() {
		t.Fatal("reopened session is not awaiting its surviving job")
	}
	waitFor(t, func() bool { return indexOf(s.Log().Snapshot(), isType(event.JobNotified)) >= 0 })
	waitFor(t, func() bool { return s.Status() == event.StatusIdle && !s.AwaitingJobs() })
	evs := logEvents(t, ws, id)
	var finished, notified int
	for _, ev := range evs {
		switch ev.Type {
		case event.JobFinished:
			finished++
			if ev.Data["status"] != "done" || !strings.Contains(str(ev.Data, "tail"), "survived") {
				t.Fatalf("job_finished = %v", ev.Data)
			}
		case event.JobNotified:
			notified++
		}
	}
	if finished != 1 || notified != 1 {
		t.Fatalf("job_finished=%d job_notified=%d, want one each", finished, notified)
	}
}

// A supervised job that finished while the daemon was down is recorded
// finished before session_reopened and wakes the reopened session.
func TestRestartReopenDowntimeFinish(t *testing.T) {
	m, ws, id, launcher := restartAwaitingSession(t, "sleep 0.5; echo during-downtime")
	jobDir := filepath.Join(ws, ".ycc", "sessions", id, "jobs", "job_1")
	waitFor(t, func() bool { _, err := jobshim.ReadExit(jobDir); return err == nil })
	s := reopenAfterRestart(t, m, launcher, id)
	waitFor(t, func() bool { return indexOf(s.Log().Snapshot(), isType(event.JobNotified)) >= 0 })
	evs := s.Log().Snapshot()
	fin := indexOf(evs, isType(event.JobFinished))
	reopened := indexOf(evs, isType(event.SessionReopened))
	if fin < 0 || reopened < 0 || fin > reopened {
		t.Fatalf("job_finished at %d, session_reopened at %d: the downtime finish must come first", fin, reopened)
	}
	if !boolVal(evs[fin].Data, "recovered") || !strings.Contains(str(evs[fin].Data, "tail"), "during-downtime") {
		t.Fatalf("job_finished = %v", evs[fin].Data)
	}
	if _, ok := evs[reopened].Data["surviving_jobs"]; ok || !boolVal(evs[reopened].Data, "awaiting_jobs") {
		t.Fatalf("session_reopened = %v", evs[reopened].Data)
	}
}

// A reopen without surviving work keeps the legacy marker and idle state.
func TestReopenWithoutJobsKeepsLegacyMarker(t *testing.T) {
	m, ws := shutdownTestManager(t, nil)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return s.Status() == event.StatusIdle })
	m.ShutdownForRestart()
	s = reopenAfterRestart(t, m, supervisedTestLauncher(t), s.ID)
	waitFor(t, func() bool { _, ok := s.Log().LastByActor("coordinator", event.SessionReopened); return ok })
	ev, _ := s.Log().LastByActor("coordinator", event.SessionReopened)
	if _, ok := ev.Data["surviving_jobs"]; ok || boolVal(ev.Data, "awaiting_jobs") || s.AwaitingJobs() {
		t.Fatalf("session_reopened = %v awaiting=%t", ev.Data, s.AwaitingJobs())
	}
	_ = ws
}

// A workstream whose session left a supervised job with unknown execution in
// its worktree is not discarded: the tree is kept and the stream needs
// attention, until the job's stop is confirmed.
func TestDiscardBlockedByUnsettledSupervisedJob(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Prompt: "do the thing"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	if err := m.Stop(s.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	jobDir := filepath.Join(ws.WorktreePath, ".ycc", "sessions", s.ID, "jobs", "job_1")
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"spec.json": "{}", "execution_unknown": "supervisor lost\n"} {
		if err := os.WriteFile(filepath.Join(jobDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.DiscardWorkstream(ws.ID); err == nil || !strings.Contains(err.Error(), "execution is unknown") {
		t.Fatalf("DiscardWorkstream = %v, want it blocked", err)
	}
	if _, err := os.Stat(ws.WorktreePath); err != nil {
		t.Fatalf("worktree removed: %v", err)
	}
	if got, _ := m.workstreams.Get(ws.ID); got.Status != "needs_attention" {
		t.Fatalf("status = %s, want needs_attention", got.Status)
	}
	// An exit record that turns up confirms the stop.
	if err := os.Remove(filepath.Join(jobDir, "execution_unknown")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "exit.json"), []byte(`{"version":1,"job_id":"job_1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.DiscardWorkstream(ws.ID); err != nil {
		t.Fatalf("DiscardWorkstream after the confirmed stop: %v", err)
	}
}

// A discard admitted before the shutdown fence stops its session during
// shutdown; that stop kills and joins the session's supervised shell rather
// than detaching it, so the worktree is settled when it is removed.
func TestDiscardDuringShutdownDoesNotDetachShells(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	launcher := supervisedTestLauncher(t)
	m.SetJobLauncher(launcher)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(s, event.SessionStarted); return ok })
	job, ok := s.deps.Jobs.TryStartTracked("bash", "sleep 30", "coordinator")
	if !ok {
		t.Fatal("register job")
	}
	sup := s.deps.JobSupervisor
	pending, err := sup.Prepare(jobshim.Request{JobID: job.ID(), Command: "sleep 30", Dir: w.WorktreePath, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	s.Log().Record("coordinator", event.JobStarted, map[string]any{"id": job.ID(), "kind": "bash", "supervised": true})
	h, err := pending.Start()
	if err != nil {
		t.Fatal(err)
	}
	job.SetDetacher(h.Detach)
	job.SetStopper(h.Kill)
	go func() { <-h.Done(); job.ExecutionComplete() }()
	m.BeginShutdown()
	if err := m.DiscardWorkstream(w.ID); err != nil {
		t.Fatalf("DiscardWorkstream during shutdown: %v", err)
	}
	out := h.Outcome()
	if out.Detached || out.Exit == nil || out.Exit.State != jobshim.StateKilled {
		t.Fatalf("outcome = %+v, want the shell killed, not detached", out)
	}
}

// With execution held (startup recovery), a survivor that finishes before the
// run records session_reopened is recorded after the marker that lists it.
func TestReopenMarkerPrecedesHeldSurvivorFinish(t *testing.T) {
	m, ws, id, launcher := restartAwaitingSession(t, "sleep 1")
	m2, _ := shutdownTestManager(t, nil)
	m2.SetProjects(m.projects)
	m2.SetJobLauncher(launcher)
	m2.HoldExecution()
	s, err := m2.Reopen("proj", id)
	if err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(ws, ".ycc", "sessions", id, "jobs", "job_1")
	waitFor(t, func() bool { _, err := jobshim.ReadExit(jobDir); return err == nil })
	time.Sleep(300 * time.Millisecond)
	m2.ReleaseExecution()
	waitFor(t, func() bool { return indexOf(s.Log().Snapshot(), isType(event.JobFinished)) >= 0 })
	evs := s.Log().Snapshot()
	reopened, fin := indexOf(evs, isType(event.SessionReopened)), indexOf(evs, isType(event.JobFinished))
	if got := idList(evs[reopened].Data["surviving_jobs"]); len(got) != 1 || fin < reopened {
		t.Fatalf("surviving_jobs=%v job_finished at %d, session_reopened at %d", got, fin, reopened)
	}
}

// A job whose stop cannot be confirmed during discard keeps the workstream in
// flight: discard reports the failure, the stream needs attention, and the
// tree stays, so discard can be retried.
func TestDiscardUnjoinedDuringStopStaysRetryable(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(s, event.SessionStarted); return ok })
	j, ok := s.deps.Jobs.TryStartTracked("bash", "test shell", "coordinator")
	if !ok {
		t.Fatal("register shell")
	}
	go func() { <-j.Context().Done(); j.MarkExecutionUnknown("stop failed to confirm exit") }()
	err = m.DiscardWorkstream(w.ID)
	if _, statErr := os.Stat(w.WorktreePath); statErr != nil {
		t.Fatalf("unjoined worktree removed: %v", statErr)
	}
	// The stop is confirmed before the discard is recorded, not after.
	if _, ok := snapshotHasEvent(s, event.WorkstreamDiscarded); ok {
		t.Fatal("workstream_discarded recorded although a job's stop was unconfirmed")
	}
	got, _ := m.workstreams.Get(w.ID)
	if err == nil || got.Status != "needs_attention" || !got.Status.InFlight() {
		t.Fatalf("discard error=%v status=%s, want a retryable needs_attention", err, got.Status)
	}
	j.ExecutionComplete() // the stop is confirmed after all
	if err := m.DiscardWorkstream(w.ID); err != nil {
		t.Fatalf("retried discard: %v", err)
	}
}

// A discard that keeps the worktree leaves its session logs there, still the
// authoritative copy a reopen resolves.
func TestFailedDiscardKeepsWorktreeLogAuthoritative(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(s, event.SessionStarted); return ok })
	j, _ := s.deps.Jobs.TryStartTracked("bash", "test shell", "coordinator")
	go func() { <-j.Context().Done(); j.MarkExecutionUnknown("stop failed to confirm exit") }()
	if err := m.DiscardWorkstream(w.ID); err == nil {
		t.Fatal("discard succeeded with an unjoined job")
	}
	if root, _, err := m.SessionLogPath("demo", s.ID); err != nil || root != w.WorktreePath {
		t.Fatalf("SessionLogPath root=%s err=%v, want the kept worktree %s", root, err, w.WorktreePath)
	}
}

// A supervised job re-attached across a restart defers a workstream merge
// until it finishes: the base is left alone and the stream needs attention.
func TestMergeDeferredWhileReattachedJobRuns(t *testing.T) {
	m, primary := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	launcher := supervisedTestLauncher(t)
	m.SetJobLauncher(launcher)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(s, event.SessionStarted); return ok })
	before := sessionGitAt(t, primary, "rev-parse", w.BaseBranch)
	commitInto(t, w.WorktreePath, "feature.txt", "feature\n", "feature")
	j, _ := s.deps.Jobs.TryStartTracked("bash", "sleep 30", "coordinator")
	sup := s.deps.JobSupervisor
	pending, err := sup.Prepare(jobshim.Request{JobID: j.ID(), Command: "sleep 30", Dir: w.WorktreePath, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	s.Log().Record("coordinator", event.JobStarted, map[string]any{"id": j.ID(), "kind": "bash", "label": "sleep 30", "supervised": true})
	h, err := pending.Start()
	if err != nil {
		t.Fatal(err)
	}
	j.SetDetacher(h.Detach)
	j.SetStopper(h.Kill)
	t.Cleanup(func() { a := sup.Attach(j.ID()); _ = a.Kill(); <-a.Done() })
	if err := s.deps.Jobs.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	r, err := m.Reopen("demo", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(r, event.SessionReopened); return ok })
	out, err := m.MergeWorkstream(w.ID, true)
	if err == nil || out.Merged || sessionGitAt(t, primary, "rev-parse", w.BaseBranch) != before {
		t.Fatalf("merge with a running re-attached job: outcome=%+v err=%v", out, err)
	}
	if got, _ := m.workstreams.Get(w.ID); got.Status != "needs_attention" {
		t.Fatalf("status = %s", got.Status)
	}
}
