package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobshim"
)

// waitLong is waitFor with room for a supervised job to finish.
func waitLong(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readLeaseRecords(t *testing.T, path string) []leaseRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f leaseFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f.Leases
}

// releasesIn lists the why of each lease_released for name in evs.
func releasesIn(evs []event.Event, name string) []string {
	var out []string
	for _, ev := range evs {
		if ev.Type == event.LeaseReleased && ev.Data["name"] == name {
			out = append(out, fmt.Sprint(ev.Data["why"]))
		}
	}
	return out
}

// leaseAcrossRestart starts a session that leaves a supervised background job
// running, idle awaiting it, takes a lease bound to that job, and suspends the
// daemon for restart. It returns the persisted leases.json bytes as acquire
// left them: a crash at any later point leaves the same file.
func leaseAcrossRestart(t *testing.T, command string) (m *Manager, ws, id string, launcher *jobshim.Launcher, path string, saved []byte) {
	t.Helper()
	launcher = supervisedTestLauncher(t)
	m, ws = shutdownTestManager(t, []*[2]string{
		{"Bash", `{"command":"` + command + `","run_in_background":true}`},
	})
	m.SetJobLauncher(launcher)
	path = filepath.Join(t.TempDir(), "state", "leases.json")
	m.SetLeasesFile(path)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		ev, ok := s.Log().LastByActor("coordinator", event.SessionIdle)
		return ok && boolVal(ev.Data, "awaiting_jobs")
	})
	if _, err := s.leases.Acquire("deploy-prod", "deploying", time.Hour, "job_1"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("leases.json: %v, %v", info, err)
	}
	recs := readLeaseRecords(t, path)
	if len(recs) != 1 || recs[0].Name != "deploy-prod" || recs[0].SessionID != s.ID || recs[0].JobID != "job_1" {
		t.Fatalf("leases.json = %+v", recs)
	}
	saved, _ = os.ReadFile(path)
	m.ShutdownForRestart()
	if after, _ := os.ReadFile(path); !bytes.Equal(after, saved) {
		t.Fatalf("restart changed leases.json:\n%s", after)
	}
	if got := releasesIn(logEvents(t, ws, s.ID), "deploy-prod"); len(got) != 0 {
		t.Fatalf("restart released the job-bound lease: %v", got)
	}
	return m, ws, s.ID, launcher, path, saved
}

// restartedManager is the next daemon's startup: passive job supervision,
// then lease restoration.
func restartedManager(t *testing.T, m *Manager, launcher *jobshim.Launcher, path string) *Manager {
	t.Helper()
	m2, _ := shutdownTestManager(t, nil)
	m2.SetProjects(m.projects)
	m2.SetJobLauncher(launcher)
	m2.SetLeasesFile(path)
	m2.SupervisePassiveJobs()
	m2.RestoreLeases()
	return m2
}

// A job-bound lease survives a daemon restart (or crash) while its job runs:
// the next daemon holds it for the closed session, refuses it to others,
// lists the holder as closed, and releases it once the job's exit is
// confirmed, recording the release once in the closed session's log.
func TestJobBoundLeaseHeldPassivelyAcrossRestart(t *testing.T) {
	m, ws, id, launcher, path, _ := leaseAcrossRestart(t, "sleep 3; echo done")
	m2 := restartedManager(t, m, launcher, path)
	if got := m2.projectLeases(ws, time.Now()); len(got) != 1 || got[0].SessionID != id || got[0].JobID != "job_1" {
		t.Fatalf("restored leases = %+v", got)
	}
	if _, live := m2.Get(id); live {
		t.Fatal("restoring the lease opened its session")
	}
	b, err := m2.Start(Config{Project: "proj", Mode: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, b, 1)
	if footer := m2.otherSessionsNote(ws, b.ID, time.Now()); !strings.Contains(footer, id+" (chat, closed; its job still holds a lease)") {
		t.Fatalf("footer:\n%s", footer)
	}
	if _, err := b.leases.Acquire("deploy-prod", "second deploy", time.Hour, ""); err == nil || !strings.Contains(err.Error(), id) {
		t.Fatalf("acquire of the held lease: %v", err)
	}
	waitLong(t, "the lease to end with its job", func() bool { return len(m2.projectLeases(ws, time.Now())) == 0 })
	waitFor(t, func() bool { return len(releasesIn(logEvents(t, ws, id), "deploy-prod")) == 1 })
	if got := releasesIn(logEvents(t, ws, id), "deploy-prod"); got[0] != leaseJobExited {
		t.Fatalf("releases = %v", got)
	}
	if recs := readLeaseRecords(t, path); len(recs) != 0 {
		t.Fatalf("leases.json after release = %+v", recs)
	}
	if _, err := b.leases.Acquire("deploy-prod", "second deploy", time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	// A later startup restores nothing.
	m3 := restartedManager(t, m, launcher, path)
	if got := m3.projectLeases(ws, time.Now()); len(got) != 0 {
		t.Fatalf("leases restored again = %+v", got)
	}
	if got := releasesIn(logEvents(t, ws, id), "deploy-prod"); len(got) != 1 {
		t.Fatalf("releases after another startup = %v", got)
	}
}

// Reopening the closed holder transfers its passively held lease: the
// reopened session can release it, and a lease it takes again on the
// re-attached job ends with that job.
func TestReopenAdoptsPassiveLease(t *testing.T) {
	m, ws, id, launcher, path, _ := leaseAcrossRestart(t, "sleep 3; echo done")
	m2 := restartedManager(t, m, launcher, path)
	s, err := m2.Reopen("proj", id)
	if err != nil {
		t.Fatal(err)
	}
	if got := m2.projectLeases(ws, time.Now()); len(got) != 1 || got[0].SessionID != id || got[0].restored {
		t.Fatalf("leases after reopen = %+v", got)
	}
	if _, err := s.leases.Release("deploy-prod"); err != nil {
		t.Fatalf("release by the reopened holder: %v", err)
	}
	if recs := readLeaseRecords(t, path); len(recs) != 0 {
		t.Fatalf("leases.json after release = %+v", recs)
	}
	if _, err := s.leases.Acquire("deploy-prod", "deploying again", time.Hour, "job_1"); err != nil {
		t.Fatalf("acquire on the re-attached job: %v", err)
	}
	waitLong(t, "the lease to end with the re-attached job", func() bool { return len(m2.projectLeases(ws, time.Now())) == 0 })
	if got := releasesIn(s.Log().Snapshot(), "deploy-prod"); len(got) != 2 || got[0] != leaseReleased || got[1] != leaseJobExited {
		t.Fatalf("releases = %v", got)
	}
}

// jobSupervisorPID finds the supervisor process of the job in jobDir.
func jobSupervisorPID(t *testing.T, jobDir string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skip("no /proc on this host")
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		args := bytes.Split(cmdline, []byte{0})
		if len(args) >= 3 && string(args[1]) == jobshim.HelperArg && string(args[2]) == jobDir {
			return pid
		}
	}
	t.Fatalf("no supervisor for %s", jobDir)
	return 0
}

// A restored lease whose job's execution is unknown (its supervisor died
// without an exit record) fails closed: it stays held, never released as
// job_exited, until an operator clears the job.
func TestPassiveLeaseHeldWhileExecutionUnknown(t *testing.T) {
	m, ws, id, launcher, path, _ := leaseAcrossRestart(t, "sleep 20")
	jobDir := launcher.ForSession(filepath.Join(ws, ".ycc", "sessions", id)).JobDir("job_1")
	if err := syscall.Kill(jobSupervisorPID(t, jobDir), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	m2 := restartedManager(t, m, launcher, path)
	if got := m2.projectLeases(ws, time.Now()); len(got) != 1 {
		t.Fatalf("restored leases = %+v", got)
	}
	time.Sleep(3 * time.Second) // past the passive table's first cleared-marker poll
	if got := m2.projectLeases(ws, time.Now()); len(got) != 1 {
		t.Fatalf("leases while execution is unknown = %+v", got)
	}
	if got := releasesIn(logEvents(t, ws, id), "deploy-prod"); len(got) != 0 {
		t.Fatalf("released while execution is unknown: %v", got)
	}
	if err := jobshim.MarkCleared(jobDir, "confirmed stopped"); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "the cleared job's lease to end", func() bool { return len(m2.projectLeases(ws, time.Now())) == 0 })
	waitFor(t, func() bool { return len(releasesIn(logEvents(t, ws, id), "deploy-prod")) == 1 })
}

// Startup releases persisted leases that expired while the daemon was down or
// whose job left evidence that it stopped (here an operator's cleared marker),
// recording each once in its closed session's log, even when restore runs
// again. A lease whose job nothing can follow stays held, through a reopen,
// until its holder releases it. Records naming no known session workspace are
// dropped without writing there.
func TestRestoreLeasesReleasesStaleRecords(t *testing.T) {
	m, ws := shutdownTestManager(t, nil)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, s, 1)
	if err := m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	clearedDir := filepath.Join(jobshim.JobsDir(sessionDir(s.Workspace, s.ID)), "job_9")
	if err := os.MkdirAll(clearedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := jobshim.MarkCleared(clearedDir, "confirmed stopped"); err != nil {
		t.Fatal(err)
	}
	stranger := t.TempDir()
	now := time.Now().UTC()
	rec := func(name, job, workspace string, expires time.Time) leaseRecord {
		return leaseRecord{Primary: canonicalPath(ws), Name: name, Reason: "r", SessionID: s.ID, Workspace: workspace,
			Mode: "chat", JobID: job, AcquiredAt: now.Add(-2 * time.Hour), ExpiresAt: expires}
	}
	data, _ := json.Marshal(leaseFile{Version: 1, Leases: []leaseRecord{
		rec("expired", "job_7", s.Workspace, now.Add(-time.Minute)),
		rec("unverified", "job_8", s.Workspace, now.Add(time.Hour)),
		rec("cleared", "job_9", s.Workspace, now.Add(time.Hour)),
		rec("stranger", "job_10", stranger, now.Add(time.Hour)),
	}})
	path := filepath.Join(t.TempDir(), "leases.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	restore := func() *Manager {
		m2, _ := shutdownTestManager(t, nil)
		m2.SetProjects(m.projects)
		m2.SetLeasesFile(path)
		m2.RestoreLeases()
		return m2
	}
	m2 := restore()
	if got := m2.projectLeases(ws, time.Now()); len(got) != 1 || got[0].Name != "unverified" {
		t.Fatalf("restored = %+v", got)
	}
	if recs := readLeaseRecords(t, path); len(recs) != 1 || recs[0].Name != "unverified" {
		t.Fatalf("leases.json after restore = %+v", recs)
	}
	restore() // a second startup (say, after a crash during the first) records nothing again
	evs := logEvents(t, ws, s.ID)
	if got := releasesIn(evs, "expired"); len(got) != 1 || got[0] != leaseExpired {
		t.Fatalf("expired releases = %v", got)
	}
	if got := releasesIn(evs, "cleared"); len(got) != 1 || got[0] != leaseJobExited {
		t.Fatalf("cleared releases = %v", got)
	}
	if got := append(releasesIn(evs, "unverified"), releasesIn(evs, "stranger")...); len(got) != 0 {
		t.Fatalf("unexpected releases = %v", got)
	}
	if _, err := os.Stat(filepath.Join(stranger, ".ycc")); !os.IsNotExist(err) {
		t.Fatalf("restore wrote under an unknown workspace: %v", err)
	}
	// The reopened holder keeps the unverified lease until it releases it.
	s2, err := m2.Reopen("proj", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := m2.projectLeases(ws, time.Now()); len(got) != 1 || got[0].restored {
		t.Fatalf("leases after reopen = %+v", got)
	}
	if _, err := s2.leases.Release("unverified"); err != nil {
		t.Fatal(err)
	}
	if recs := readLeaseRecords(t, path); len(recs) != 0 {
		t.Fatalf("leases.json after release = %+v", recs)
	}
}

// A job-bound lease is acknowledged, and its explicit release is, only once
// leases.json records the change.
func TestJobBoundLeaseChangesCommitBeforeAcknowledgement(t *testing.T) {
	m, ws := shutdownTestManager(t, nil)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, s, 1)
	job := s.deps.Jobs.Start("bash", "./deploy.sh", "coordinator")
	defer job.Finish("done", "ok")
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(blocker, "leases.json")
	good := filepath.Join(t.TempDir(), "leases.json")

	m.SetLeasesFile(bad)
	if _, err := s.leases.Acquire("prod", "deploying", time.Hour, job.ID()); err == nil {
		t.Fatal("job-bound acquire succeeded without saving")
	}
	if got := m.projectLeases(ws, time.Now()); len(got) != 0 {
		t.Fatalf("leases after a failed save = %+v", got)
	}
	if got := eventsOf(s, event.LeaseAcquired); len(got) != 0 {
		t.Fatalf("lease_acquired recorded for an unsaved lease: %+v", got)
	}

	m.SetLeasesFile(good)
	if _, err := s.leases.Acquire("prod", "deploying", time.Hour, job.ID()); err != nil {
		t.Fatal(err)
	}
	m.SetLeasesFile(bad)
	if _, err := s.leases.Release("prod"); err == nil {
		t.Fatal("release succeeded without saving")
	}
	if got := m.projectLeases(ws, time.Now()); len(got) != 1 || len(leaseReleases(s, "prod")) != 0 {
		t.Fatalf("after a failed release: leases %+v, releases %v", got, leaseReleases(s, "prod"))
	}
	m.SetLeasesFile(good)
	if _, err := s.leases.Release("prod"); err != nil {
		t.Fatal(err)
	}
	if recs := readLeaseRecords(t, good); len(recs) != 0 {
		t.Fatalf("leases.json after release = %+v", recs)
	}
}

// A session stopped while its job's execution could not be confirmed keeps
// the job's lease, held for it while it is closed; reopening it transfers the
// lease, which then ends with the job's execution, recorded once in the
// reopened log.
func TestStoppedHolderLeaseAdoptedOnReopen(t *testing.T) {
	m, ws := shutdownTestManager(t, nil)
	start := func() *Session {
		s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		waitIdleCount(t, s, 1)
		return s
	}
	a, b := start(), start()
	job, ok := a.deps.Jobs.TryStartTracked("bash", "./deploy.sh (unconfirmed stop)", "coordinator")
	if !ok {
		t.Fatal("could not start job")
	}
	go func() { <-job.Context().Done(); job.MarkExecutionUnknown("stop could not be confirmed") }()
	if _, err := a.leases.Acquire("production", "deploying", time.Hour, job.ID()); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(a.ID); err == nil || !strings.Contains(err.Error(), "could not be confirmed stopped") {
		t.Fatalf("stop: %v", err)
	}
	if _, err := b.leases.Acquire("production", "second deploy", time.Hour, ""); err == nil || !strings.Contains(err.Error(), a.ID) {
		t.Fatalf("acquire while the stopped holder's job may run: %v", err)
	}
	a2, err := m.Reopen("proj", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.projectLeases(ws, time.Now()); len(got) != 1 || got[0].SessionID != a.ID {
		t.Fatalf("leases after reopen = %+v", got)
	}
	job.ExecutionComplete()
	waitFor(t, func() bool { return len(m.projectLeases(ws, time.Now())) == 0 })
	if got := releasesIn(a2.Log().Snapshot(), "production"); len(got) != 1 || got[0] != leaseJobExited {
		t.Fatalf("releases = %v", got)
	}
}

// A reopen that takes a passively held job over without following it or
// finding evidence that it stopped (its spec.json became unreadable), whether
// the reopen succeeds or the worktree fence refuses it, keeps the lease held,
// unverified, rather than inferring that the job exited.
func TestReopenWithoutExitEvidenceKeepsLease(t *testing.T) {
	for _, fenced := range []bool{false, true} {
		t.Run(fmt.Sprintf("fenced=%v", fenced), func(t *testing.T) {
			m, ws, id, launcher, path, _ := leaseAcrossRestart(t, "while ! test -f proceed; do sleep 0.05; done")
			t.Cleanup(func() { _ = os.WriteFile(filepath.Join(ws, "proceed"), nil, 0o600) })
			sup := launcher.ForSession(sessionDir(ws, id))
			m2 := restartedManager(t, m, launcher, path)
			if len(m2.passiveJobs.Held(sup.Dir())) != 1 || len(m2.projectLeases(ws, time.Now())) != 1 {
				t.Fatal("the job and its lease are not held passively")
			}
			if err := os.WriteFile(filepath.Join(sup.JobDir("job_1"), "spec.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			if fenced {
				unfence, _ := m2.fenceWorktree(ws)
				defer unfence()
			}
			_, err := m2.Reopen("proj", id)
			if fenced != errors.Is(err, ErrWorktreeSettling) || (!fenced && err != nil) {
				t.Fatalf("reopen: %v", err)
			}
			if fenced {
				// The lease's watcher re-checks once the failed reopen lets go.
				waitFor(t, func() bool {
					got := m2.projectLeases(ws, time.Now())
					return len(got) == 1 && len(m2.passiveJobs.Held(sup.Dir())) == 0
				})
				time.Sleep(100 * time.Millisecond)
			}
			if got, recs := m2.projectLeases(ws, time.Now()), readLeaseRecords(t, path); len(got) != 1 || len(recs) != 1 {
				t.Fatalf("lease dropped without exit evidence: held %+v, records %+v", got, recs)
			}
			if got := releasesIn(logEvents(t, ws, id), "deploy-prod"); len(got) != 0 {
				t.Fatalf("releases = %v", got)
			}
		})
	}
}
