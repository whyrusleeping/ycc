package session

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/project"
)

// leaseReleases lists the why of each lease_released event for name in s's log.
func leaseReleases(s *Session, name string) []string {
	var out []string
	for _, ev := range eventsOf(s, event.LeaseReleased) {
		if ev.Data["name"] == name {
			out = append(out, fmt.Sprint(ev.Data["why"]))
		}
	}
	return out
}

// Exclusive leases: offered without session coordination, contention fails at
// once naming the holder, listings show held leases, and a lease ends on
// release, expiry, its job's exit, or its session's end.
func TestLeasesContentionReleaseAndListing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ws := t.TempDir()
	sessionGitAt(t, ws, "init", "-q")
	sessionGitAt(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
	stub, _ := writeStub(t, ws)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ReclaimAll)

	start := func() *Session {
		s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		waitIdleCount(t, s, 1)
		return s
	}
	a, b := start(), start()
	if a.deps.Leases == nil {
		t.Fatal("lease tools not offered with session coordination off")
	}
	tools := func(mode string) map[string]bool {
		reg, _ := orchestrator.BuildMode(mode, a.deps, false)
		names := map[string]bool{}
		for _, def := range reg.APIDefs() {
			names[def.Function.Name] = true
		}
		return names
	}
	for _, mode := range []string{"work", "chat"} {
		if got := tools(mode); !got["acquire_lease"] || !got["release_lease"] {
			t.Fatalf("%s mode lacks the lease tools", mode)
		}
	}
	if tools("integrate")["acquire_lease"] {
		t.Fatal("integrate mode offers acquire_lease")
	}

	// Contention fails at once, naming the holder; names are case-insensitive.
	out, err := a.leases.Acquire("Deploy-Cubey", "restart normalize-v2", time.Hour, "")
	if err != nil || !strings.Contains(out, `Acquired lease "deploy-cubey"`) {
		t.Fatalf("acquire = %q, %v", out, err)
	}
	if _, err := b.leases.Acquire("deploy-cubey", "deploy", time.Hour, ""); err == nil ||
		!strings.Contains(err.Error(), "session "+a.ID) || !strings.Contains(err.Error(), `"restart normalize-v2"`) ||
		!strings.Contains(err.Error(), "expiring") {
		t.Fatalf("contended acquire err = %v", err)
	}
	if _, err := b.leases.Release("deploy-cubey"); err == nil || !strings.Contains(err.Error(), "not this session") {
		t.Fatalf("non-holder release err = %v", err)
	}
	for _, bad := range []struct {
		reason string
		ttl    time.Duration
	}{{"", time.Hour}, {"x", time.Second}, {"x", 25 * time.Hour}} {
		if _, err := b.leases.Acquire("other", bad.reason, bad.ttl, ""); err == nil {
			t.Fatalf("acquire(%q, %s) succeeded", bad.reason, bad.ttl)
		}
	}

	// Listings: the list_backlog footer shows the lease under its holder.
	footer := m.otherSessionsNote(ws, b.ID, time.Now())
	if !strings.Contains(footer, `lease "deploy-cubey": "restart normalize-v2", expires`) {
		t.Fatalf("footer lacks the lease:\n%s", footer)
	}

	// Re-acquiring renews; release frees it for another session.
	if out, err := a.leases.Acquire("deploy-cubey", "still restarting", 2*time.Hour, ""); err != nil || !strings.HasPrefix(out, "Renewed") {
		t.Fatalf("renew = %q, %v", out, err)
	}
	if _, err := a.leases.Release("deploy-cubey"); err != nil {
		t.Fatal(err)
	}
	if got := leaseReleases(a, "deploy-cubey"); len(got) != 1 || got[0] != leaseReleased {
		t.Fatalf("a's releases = %v", got)
	}
	acquired := eventsOf(a, event.LeaseAcquired)
	if len(acquired) != 2 || acquired[0].Data["reason"] != "restart normalize-v2" || acquired[1].Data["renewed"] != true {
		t.Fatalf("a's lease_acquired events = %+v", acquired)
	}
	if _, err := b.leases.Acquire("deploy-cubey", "deploy", time.Hour, ""); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}

	// Expiry: the timer releases it, and listings never show an expired lease.
	if _, _, err := m.acquireLease(a, ws, "gpu", "benchmark", time.Minute, "", time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(leaseReleases(a, "gpu")) == 1 })
	if got := leaseReleases(a, "gpu"); got[0] != leaseExpired {
		t.Fatalf("expiry releases = %v", got)
	}
	if _, err := b.leases.Acquire("gpu", "benchmark", time.Hour, ""); err != nil {
		t.Fatalf("acquire after expiry: %v", err)
	}

	// A job-attached lease ends when its job exits; a finished job is refused.
	job := a.deps.Jobs.Start("bash", "./deploy.sh", "coordinator")
	if out, err := a.leases.Acquire("prod", "deploying", time.Hour, job.ID()); err != nil || !strings.Contains(out, job.ID()+" exits") {
		t.Fatalf("job-attached acquire = %q, %v", out, err)
	}
	if footer := m.otherSessionsNote(ws, b.ID, time.Now()); !strings.Contains(footer, "or when "+job.ID()+" exits") {
		t.Fatalf("footer lacks the job attachment:\n%s", footer)
	}
	// Renewing without job_id keeps the job.
	if _, err := a.leases.Acquire("prod", "still deploying", time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	job.Finish(jobs.Done, "ok")
	waitFor(t, func() bool { return len(leaseReleases(a, "prod")) == 1 })
	if got := leaseReleases(a, "prod"); got[0] != leaseJobExited {
		t.Fatalf("job-exit releases = %v", got)
	}
	if _, err := a.leases.Acquire("prod", "deploying", time.Hour, job.ID()); err == nil || !strings.Contains(err.Error(), "not a running background job") {
		t.Fatalf("acquire with a finished job err = %v", err)
	}

	// Session end releases its leases (job-attached ones included).
	other := b.deps.Jobs.Start("bash", "sleep 100", "coordinator")
	if _, err := b.leases.Acquire("db", "migrating", time.Hour, other.ID()); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(b.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"deploy-cubey", "gpu", "db"} {
		if got := leaseReleases(b, name); len(got) != 1 || (got[0] != leaseSessionEnded && got[0] != leaseJobExited) {
			t.Fatalf("b's %s releases = %v", name, got)
		}
	}
	if got := m.projectLeases(ws, time.Now()); len(got) != 0 {
		t.Fatalf("leases after session end = %+v", got)
	}
	if _, err := a.leases.Acquire("db", "migrating", time.Hour, ""); err != nil {
		t.Fatalf("acquire after the holder ended: %v", err)
	}
}

// A stopping session's job-attached lease stays held until the job's
// execution has actually stopped, not merely until the session is cancelled.
func TestLeaseHeldWhileStoppingJobRuns(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ws := t.TempDir()
	sessionGitAt(t, ws, "init", "-q")
	sessionGitAt(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
	stub, _ := writeStub(t, ws)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ReclaimAll)
	start := func() *Session {
		s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		waitIdleCount(t, s, 1)
		return s
	}
	a, b := start(), start()
	job, ok := a.deps.Jobs.TryStartTracked("bash", "./deploy.sh (slow to stop)", "coordinator")
	if !ok {
		t.Fatal("could not start job")
	}
	if _, err := a.leases.Acquire("production", "deploying", time.Hour, job.ID()); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- m.Stop(a.ID) }()
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not kill the job")
	}
	// Killed, but its execution has not stopped: the lease is still held.
	if _, err := b.leases.Acquire("production", "second deploy", time.Hour, ""); err == nil || !strings.Contains(err.Error(), a.ID) {
		t.Fatalf("acquire while the holder's job still runs: %v", err)
	}
	if got := m.projectLeases(ws, time.Now()); len(got) != 1 {
		t.Fatalf("leases while the job stops = %+v", got)
	}
	// Listings keep showing the stopping holder while its lease is held.
	if footer := m.otherSessionsNote(ws, b.ID, time.Now()); !strings.Contains(footer, a.ID+" (chat, stopping; its job still holds a lease)") ||
		!strings.Contains(footer, `lease "production"`) {
		t.Fatalf("footer while the holder stops:\n%s", footer)
	}
	if live, err := m.ProjectLiveSessions("proj"); err != nil || len(live) != 1 || live[0].ID != a.ID || live[0].Activity != "stopped" {
		t.Fatalf("client listing while the holder stops = %+v, %v", live, err)
	}
	job.ExecutionComplete()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(m.projectLeases(ws, time.Now())) == 0 })
	if _, err := b.leases.Acquire("production", "second deploy", time.Hour, ""); err != nil {
		t.Fatalf("acquire after the job stopped: %v", err)
	}
}

// With session coordination off, client listings still show lease holders,
// without focus, intent or claims.
func TestLeaseHoldersListedWithCoordinationOff(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ws := t.TempDir()
	sessionGitAt(t, ws, "init", "-q")
	sessionGitAt(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
	stub, _ := writeStub(t, ws)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ReclaimAll)
	a, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, a, 1)
	if got, err := m.ProjectLiveSessions("proj"); err != nil || len(got) != 0 {
		t.Fatalf("live sessions without leases = %+v, %v", got, err)
	}
	if _, err := a.leases.Acquire("deploy-staging", "rolling out", time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	got, err := m.ProjectLiveSessions("proj")
	if err != nil || len(got) != 1 || got[0].ID != a.ID || len(got[0].Leases) != 1 || got[0].Leases[0].Name != "deploy-staging" ||
		got[0].Focus != nil || got[0].Claims != nil || got[0].Intent != "" {
		t.Fatalf("live sessions with coordination off = %+v, %v", got, err)
	}
	// list_sessions lists only lease holders; path queries stay off.
	b, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, b, 1)
	out, err := b.deps.ListSessions("")
	if err != nil || !strings.Contains(out, "only the other sessions holding exclusive leases") || !strings.Contains(out, a.ID) ||
		!strings.Contains(out, `lease "deploy-staging"`) {
		t.Fatalf("list_sessions with coordination off = %q, %v", out, err)
	}
	if _, err := b.deps.ListSessions("README.md"); !errors.Is(err, orchestrator.ErrCoordinationDisabled) {
		t.Fatalf("list_sessions path= with coordination off: %v", err)
	}
	if _, err := a.deps.ListSessions(""); !errors.Is(err, orchestrator.ErrCoordinationDisabled) {
		t.Fatalf("list_sessions without other lease holders: %v", err)
	}
}
