package session

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobshim"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// Startup passive supervision holds a closed session's surviving job without
// touching its log or reopening it; a later reopen takes the job over and
// reports it surviving, and its completion wakes the session once.
func TestStartupPassiveSupervisionThenReopen(t *testing.T) {
	m, ws, id, launcher := restartAwaitingSession(t, "sleep 2; echo survived")
	m2, _ := shutdownTestManager(t, nil)
	m2.SetProjects(m.projects)
	m2.SetJobLauncher(launcher)
	logged := len(logEvents(t, ws, id))
	m2.SupervisePassiveJobs()
	dir, err := filepath.Abs(filepath.Join(ws, ".ycc", "sessions", id))
	if err != nil {
		t.Fatal(err)
	}
	if got := m2.passiveJobs.Held(dir); !slices.Equal(got, []string{"job_1"}) {
		t.Fatalf("held = %v", got)
	}
	if _, live := m2.Get(id); live {
		t.Fatal("passive supervision opened the session")
	}
	if n := len(logEvents(t, ws, id)); n != logged {
		t.Fatalf("passive supervision appended %d events", n-logged)
	}
	s, err := m2.Reopen("proj", id)
	if err != nil {
		t.Fatal(err)
	}
	if got := m2.passiveJobs.Held(dir); len(got) != 0 {
		t.Fatalf("held after reopen = %v", got)
	}
	waitFor(t, func() bool { _, ok := s.Log().LastByActor("coordinator", event.SessionReopened); return ok })
	ev, _ := s.Log().LastByActor("coordinator", event.SessionReopened)
	if got := idList(ev.Data["surviving_jobs"]); !slices.Equal(got, []string{"job_1"}) {
		t.Fatalf("session_reopened = %v", ev.Data)
	}
	waitFor(t, func() bool { return indexOf(s.Log().Snapshot(), isType(event.JobNotified)) >= 0 })
}

// A closed workstream session's passively held job guards the worktree: the
// stream is neither treated as finished nor merged while it runs, and merges
// once its exit is confirmed.
func TestPassiveJobGuardsWorkstream(t *testing.T) {
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
	// The daemon goes away leaving the job running and the session closed.
	if err := s.deps.Jobs.SuspendForRestart("coordinator"); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	m.SupervisePassiveJobs()
	if got := m.passiveJobs.Held(sup.Dir()); !slices.Equal(got, []string{j.ID()}) {
		t.Fatalf("held = %v", got)
	}
	if !m.workstreamSessionBusy(w) {
		t.Fatal("readiness would be derived while a held job runs")
	}
	out, err := m.MergeWorkstream(w.ID, true)
	if err == nil || out.Merged || !strings.Contains(err.Error(), "kept running across a daemon restart") ||
		sessionGitAt(t, primary, "rev-parse", w.BaseBranch) != before {
		t.Fatalf("merge with a held job: outcome=%+v err=%v", out, err)
	}
	if got, _ := m.workstreams.Get(w.ID); got.Status != "needs_attention" {
		t.Fatalf("status = %s", got.Status)
	}
	a := sup.Attach(j.ID())
	_ = a.Kill()
	<-a.Done()
	waitFor(t, func() bool { return len(m.passiveJobs.Held(sup.Dir())) == 0 })
	if out, err := m.MergeWorkstream(w.ID, true); err != nil || !out.Merged {
		t.Fatalf("merge after the job exited: outcome=%+v err=%v", out, err)
	}
}

// A merge confirms that a live session's jobs have stopped before it advances
// the base: one whose stop cannot be confirmed blocks the merge with the base,
// the session and its log untouched, and the session can still start jobs.
func TestMergeSettlesLiveJobsBeforeAdvancing(t *testing.T) {
	m, primary := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(s, event.SessionStarted); return ok })
	before := sessionGitAt(t, primary, "rev-parse", w.BaseBranch)
	commitInto(t, w.WorktreePath, "feature.txt", "feature\n", "feature")
	j, _ := s.deps.Jobs.TryStartTracked("bash", "test shell", "coordinator")
	go func() { <-j.Context().Done(); j.MarkExecutionUnknown("stop failed to confirm exit") }()
	out, err := m.MergeWorkstream(w.ID, true)
	if err == nil || out.Merged || sessionGitAt(t, primary, "rev-parse", w.BaseBranch) != before {
		t.Fatalf("merge with an unconfirmed stop: outcome=%+v err=%v", out, err)
	}
	if _, ok := snapshotHasEvent(s, event.WorkstreamMerged); ok {
		t.Fatal("workstream_merged recorded")
	}
	if _, live := m.Get(s.ID); !live {
		t.Fatal("the session was stopped by a merge that did not happen")
	}
	if got, _ := m.workstreams.Get(w.ID); got.Status != "needs_attention" {
		t.Fatalf("status = %s", got.Status)
	}
	j2, ok := s.deps.Jobs.TryStartTracked("bash", "later shell", "coordinator")
	if !ok {
		t.Fatal("the session's jobs stayed held after the blocked merge")
	}
	go func() { <-j2.Context().Done(); j2.ExecutionComplete() }()
	j.ExecutionComplete() // the stop is confirmed after all
	out, err = m.MergeWorkstream(w.ID, true)
	if err != nil || !out.Merged {
		t.Fatalf("retried merge: outcome=%+v err=%v", out, err)
	}
	if j2.ExecutionRunning() {
		t.Fatal("the merge left a job running")
	}
}

// The join of cancelled subagent shells shares one deadline: two shells that
// never report an outcome do not hold startup past it.
func TestPassiveStopJoinDeadlineCoversEveryShell(t *testing.T) {
	launcher := supervisedTestLauncher(t)
	dir := filepath.Join(t.TempDir(), ".ycc", "sessions", "s_join")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sup := launcher.ForSession(dir)
	var handles []*jobshim.Handle
	for _, id := range []string{"job_1", "job_2"} {
		pending, err := sup.Prepare(jobshim.Request{JobID: id, Command: "sleep 30", Dir: t.TempDir(), Env: os.Environ()})
		if err != nil {
			t.Fatal(err)
		}
		h, err := pending.Start()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.Kill(); <-h.Done() })
		handles = append(handles, h)
	}
	start := time.Now()
	joinPassiveStops(handles, 300*time.Millisecond)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("join took %s past its deadline", d)
	}
}

// A session directory in the worktree that the workstream does not name (an
// integration session a crash left before its id was recorded) still guards
// the worktree.
func TestUnrecordedWorktreeSessionGuardsDiscard(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Prompt: "do the thing"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(w.WorktreePath, ".ycc", "sessions", "s_unrecorded", "jobs", "job_1")
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "spec.json"), []byte(`{"version":1,"job_id":"job_1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.DiscardWorkstream(w.ID); err == nil || !strings.Contains(err.Error(), "s_unrecorded") {
		t.Fatalf("DiscardWorkstream = %v, want it blocked by the unrecorded session", err)
	}
	if _, err := os.Stat(w.WorktreePath); err != nil {
		t.Fatalf("worktree removed: %v", err)
	}
}

// While a merge or discard settles a worktree, no session can start there.
func TestSettlingWorktreeRefusesNewSessions(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(s, event.SessionStarted); return ok })
	release, ids, why := m.settleWorkstreamExecution(w, "test")
	if why != "" || !slices.Contains(ids, s.ID) {
		t.Fatalf("settle: ids=%v why=%q", ids, why)
	}
	if _, err := m.start(Config{Workspace: w.WorktreePath, Mode: "integrate", Prompt: "x", Unattended: true}, false); !errors.Is(err, ErrWorktreeSettling) {
		t.Fatalf("start while settling = %v", err)
	}
	release()
	late, err := m.start(Config{Workspace: w.WorktreePath, Mode: "integrate", Prompt: "x", Unattended: true}, false)
	if err != nil {
		t.Fatalf("start after release: %v", err)
	}
	_ = m.Stop(late.ID)
}

// A live session rooted elsewhere whose id also names a session directory in
// the worktree (an archived attempt reopened from the primary) is neither
// settled nor stopped by the workstream's discard; an unrecorded worktree
// session resolves to the worktree for a reopen.
func TestWorkstreamCleanupScopedToWorktreeSessions(t *testing.T) {
	m, primary := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(s, event.SessionStarted); return ok })
	orphan := filepath.Join(w.WorktreePath, ".ycc", "sessions", "s_orphan")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "events.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if root, _, err := m.SessionLogPath("demo", "s_orphan"); err != nil || root != w.WorktreePath {
		t.Fatalf("SessionLogPath(orphan) root=%s err=%v, want the worktree", root, err)
	}
	if proj, ok := m.persistedSessionProject("s_orphan"); !ok || proj != "demo" {
		t.Fatalf("persistedSessionProject(orphan) = %q, %t", proj, ok)
	}
	p, err := m.Start(Config{Project: "demo", Mode: "chat", Prompt: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(p, event.SessionStarted); return ok })
	if err := os.MkdirAll(filepath.Join(w.WorktreePath, ".ycc", "sessions", p.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.DiscardWorkstream(w.ID); err != nil {
		t.Fatalf("DiscardWorkstream: %v", err)
	}
	if _, live := m.Get(p.ID); !live {
		t.Fatal("discard stopped a session rooted outside the worktree")
	}
	if err := nonEmptyLog(filepath.Join(primary, ".ycc", "sessions", "s_orphan", "events.jsonl")); err != nil {
		t.Fatalf("the unrecorded session's log was not archived: %v", err)
	}
	// Integration recovery checks readiness under the merge lock: a discarded
	// stream gets no new session, and its removed tree is not recreated.
	if _, err := m.prepareIntegrateSession(w.ID, func(workstream.Workstream) string { return "x" }); err == nil {
		t.Fatal("an integration session was prepared for a discarded stream")
	}
	if _, err := os.Stat(w.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree after discard: %v", err)
	}
}

// Every spelling of a worktree is the same worktree to the fence, and no
// session may be rooted strictly inside one.
func TestWorktreeFenceUsesPhysicalIdentity(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(s, event.SessionStarted); return ok })
	if err := os.MkdirAll(filepath.Join(w.WorktreePath, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.start(Config{Workspace: filepath.Join(w.WorktreePath, "sub"), Mode: "chat", Prompt: "x"}, false); err == nil {
		t.Fatal("a session started inside a workstream worktree")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(w.WorktreePath, link); err != nil {
		t.Fatal(err)
	}
	via, err := m.start(Config{Workspace: link, Mode: "chat", Prompt: "x"}, false)
	if err != nil {
		t.Fatal(err)
	}
	release, ids, _ := m.settleWorkstreamExecution(w, "test")
	defer release()
	if !slices.Contains(ids, via.ID) {
		t.Fatalf("settle ids = %v, missing the session started through a symlink", ids)
	}
	if _, err := m.start(Config{Workspace: link, Mode: "chat", Prompt: "x"}, false); !errors.Is(err, ErrWorktreeSettling) {
		t.Fatalf("start through a symlink while settling = %v", err)
	}
}

// Integration recovery does not archive the previous attempt, nor start the
// next, while a session open in the worktree still has a job running there.
func TestIntegrationRetryWaitsForRunningWorktreeJob(t *testing.T) {
	m, primary := newWorkstreamManager(t)
	m.HoldExecution()
	t.Cleanup(m.ReclaimAll)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	const prior = "s_closed_attempt"
	dir := filepath.Join(w.WorktreePath, ".ycc", "sessions", prior)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.workstreams.SetIntegrateSessionID(w.ID, prior); err != nil {
		t.Fatal(err)
	}
	if err := m.workstreams.SetStatus(w.ID, workstream.StatusReady); err != nil {
		t.Fatal(err)
	}
	j, ok := s.deps.Jobs.TryStartTracked("bash", "test background job", "coordinator")
	if !ok {
		t.Fatal("register tracked job")
	}
	go func() { <-j.Context().Done(); j.ExecutionComplete() }()
	if _, err := m.prepareIntegrateSession(w.ID, func(workstream.Workstream) string { return "integrate" }); err == nil {
		t.Fatal("an integration attempt was prepared while a worktree job ran")
	}
	if nonEmptyLog(filepath.Join(primary, ".ycc", "sessions", prior, "events.jsonl")) == nil {
		t.Fatal("the previous attempt was archived while a worktree job ran")
	}
	if j.Status() != "running" {
		t.Fatalf("the running job was disturbed: %s", j.Status())
	}
}

// A session directory reached through a symlinked project and through its
// workstream's worktree is one session: passive supervision holds its job
// once, and a reopen through either spelling takes the live job over.
func TestPassiveSupervisionUsesPhysicalSessionDir(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	m.HoldExecution()
	t.Cleanup(m.ReclaimAll)
	launcher := supervisedTestLauncher(t)
	m.SetJobLauncher(launcher)
	w, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(w.WorktreePath, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := m.projects.Add(alias, "alias"); err != nil {
		t.Fatal(err)
	}
	const id = "s_alias"
	dir := filepath.Join(alias, ".ycc", "sessions", id)
	log, err := event.OpenLog(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	log.Record("coordinator", event.JobStarted, map[string]any{"id": "job_1", "kind": "bash", "label": "sleep 30", "supervised": true})
	log.Close()
	pending, err := launcher.ForSession(dir).Prepare(jobshim.Request{JobID: "job_1", Command: "sleep 30", Dir: alias, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	h, err := pending.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Kill(); <-h.Done() })
	h.Detach()
	m.SupervisePassiveJobs()
	if why := m.passiveUnsettled(w.WorktreePath, id); !strings.Contains(why, "kept running") {
		t.Fatalf("held through the worktree spelling: %q", why)
	}
	s, err := m.Reopen("demo", id)
	if err != nil {
		t.Fatal(err)
	}
	j, ok := s.deps.Jobs.Get("job_1")
	if !ok || j.ExecutionUnknown() || !slices.Equal(s.reopenSurviving, []string{"job_1"}) {
		t.Fatalf("reopen did not take the live job over: surviving=%v", s.reopenSurviving)
	}
}

// While integration recovery archives the previous attempt, no session can be
// admitted to the worktree to run a job meanwhile.
func TestIntegrationRetryFencesWorktreeAdmission(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	m.HoldExecution()
	t.Cleanup(m.ReclaimAll)
	w, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.workstreams.SetStatus(w.ID, workstream.StatusReady); err != nil {
		t.Fatal(err)
	}
	var admitted error
	s, err := m.prepareIntegrateSession(w.ID, func(workstream.Workstream) string {
		_, admitted = m.Start(Config{Workspace: w.WorktreePath, Mode: "chat", Prompt: "late start"})
		return "integrate"
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !errors.Is(admitted, ErrWorktreeSettling) {
		t.Fatalf("a session was admitted to the worktree during the archive: %v", admitted)
	}
	if s == nil {
		t.Fatal("the replacement attempt was not started")
	}
}

// An implementation session that has left the live map but is still joining
// its job keeps integration recovery from archiving the previous attempt.
func TestIntegrationRetryWaitsForEndingSession(t *testing.T) {
	m, primary := newWorkstreamManager(t)
	t.Cleanup(m.ReclaimAll)
	w, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := snapshotHasEvent(s, event.SessionStarted); return ok })
	const prior = "s_closed_attempt"
	dir := filepath.Join(w.WorktreePath, ".ycc", "sessions", prior)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.workstreams.SetIntegrateSessionID(w.ID, prior); err != nil {
		t.Fatal(err)
	}
	if err := m.workstreams.SetStatus(w.ID, workstream.StatusReady); err != nil {
		t.Fatal(err)
	}
	j, _ := s.deps.Jobs.TryStartTracked("bash", "job with a slow stop", "coordinator")
	stopped := make(chan error, 1)
	go func() { stopped <- m.Stop(s.ID) }()
	<-j.Context().Done() // out of the live map, still joining the job
	_, err = m.prepareIntegrateSession(w.ID, func(workstream.Workstream) string { return "integrate" })
	archived := nonEmptyLog(filepath.Join(primary, ".ycc", "sessions", prior, "events.jsonl")) == nil
	j.ExecutionComplete()
	<-stopped
	if err == nil || archived {
		t.Fatalf("prepare = %v, archived = %t, want it refused while the session is still stopping", err, archived)
	}
}
