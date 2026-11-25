package workspacelease

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestClaimsAttributeWritesPerScopeThroughAliases(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	s := NewService()
	coordA := s.NewScopedToken("s_a", "session a coordinator")
	implA := s.Child(coordA, "implementer")
	if implA.Scope() != "s_a" || implA == coordA {
		t.Fatalf("child token scope=%q", implA.Scope())
	}
	b := s.NewScopedToken("s_b", "session b coordinator")
	s.RecordWrite(filepath.Join(alias, "pkg", "new.go"), root, implA) // not yet created, via alias
	s.RecordWrite(filepath.Join(root, "shared.go"), root, coordA)
	s.RecordWrite(filepath.Join(root, "shared.go"), root, b)
	s.RecordWrite(filepath.Join(root, "b.go"), root, b)
	s.RecordWrite(filepath.Join(root, "ignored.go"), root, s.NewToken("unscoped"))

	want := map[string][]string{"pkg/new.go": {"s_a"}, "shared.go": {"s_a", "s_b"}, "b.go": {"s_b"}}
	if got := s.Claims(alias); !reflect.DeepEqual(got, want) {
		t.Fatalf("claims = %v, want %v", got, want)
	}
	mine, foreign := s.Split(root, "s_a")
	if !reflect.DeepEqual(mine, map[string]bool{"pkg/new.go": true, "shared.go": true}) ||
		!reflect.DeepEqual(foreign, map[string]bool{"shared.go": true, "b.go": true}) {
		t.Fatalf("split mine=%v foreign=%v", mine, foreign)
	}
	if m, f := s.Split(root, ""); m != nil || f != nil {
		t.Fatal("unscoped split should be nil")
	}

	if notice := s.RecordWrite(filepath.Join(root, "shared.go"), root, b); notice != "" {
		t.Fatalf("notice without a notifier: %q", notice)
	}
	var overlaps []Overlap
	s.SetOverlapNotifier(func(o Overlap) string { overlaps = append(overlaps, o); return "n" })
	s.RecordWrite(filepath.Join(root, "b.go"), root, b) // only b claims it
	if notice := s.RecordWrite(filepath.Join(root, "shared.go"), root, b); notice != "n" ||
		len(overlaps) != 1 || overlaps[0].Path != "shared.go" || overlaps[0].Writer != "s_b" || !reflect.DeepEqual(overlaps[0].Others, []string{"s_a"}) {
		t.Fatalf("overlap notice=%q overlaps=%+v", notice, overlaps)
	}

	s.ReleaseClaims(root, "s_a", []string{"shared.go"})
	if got := s.Claims(root)["shared.go"]; !reflect.DeepEqual(got, []string{"s_b"}) {
		t.Fatalf("after release shared.go = %v", got)
	}
	s.ReleaseScope("s_b")
	if got := s.Claims(root); !reflect.DeepEqual(got, map[string][]string{"pkg/new.go": {"s_a"}}) {
		t.Fatalf("after scope release = %v", got)
	}
}

func TestAcquireWaitWakesOnReleaseAndHonorsDeadline(t *testing.T) {
	root := t.TempDir()
	s := NewService()
	held, err := s.Acquire(root, s.NewToken("commit"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := s.AcquireWait(ctx, root, s.NewToken("writer")); err == nil {
		t.Fatal("wait succeeded while the section was held")
	} else if conflict := (*Conflict)(nil); !errors.As(err, &conflict) || conflict.Owner != "commit" {
		t.Fatalf("deadline error = %v", err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		held.Release()
	}()
	lease, err := s.AcquireWait(context.Background(), root, s.NewToken("writer"))
	if err != nil {
		t.Fatalf("waiter not woken by release: %v", err)
	}
	lease.Release()
}

func TestClaimsPersistAcrossRestartAndGenerationGuardsRelease(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	first := NewService()
	first.PersistClaims()
	first.RecordWrite(filepath.Join(root, "a.go"), root, first.NewScopedToken("s_a", "a"))
	first.RecordWrite(filepath.Join(root, "b.go"), root, first.NewScopedToken("s_b", "b"))

	restarted := NewService()
	restarted.PersistClaims()
	want := map[string][]string{"a.go": {"s_a"}, "b.go": {"s_b"}}
	if got := restarted.Claims(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("claims after restart = %v, want %v", got, want)
	}
	if details := restarted.ClaimDetails(root)["a.go"]; len(details) != 1 || details[0].Since.IsZero() {
		t.Fatalf("claim time lost across restart: %+v", details)
	}

	// A claim recorded after the caller sampled the generation survives a
	// release of that path.
	before := restarted.Generation()
	restarted.RecordWrite(filepath.Join(root, "a.go"), root, restarted.NewScopedToken("s_c", "c"))
	restarted.ReleaseAll(root, []string{"a.go", "b.go"}, before)
	if got := restarted.Claims(root); !reflect.DeepEqual(got, map[string][]string{"a.go": {"s_c"}}) {
		t.Fatalf("claims after guarded release = %v", got)
	}
	again := NewService()
	again.PersistClaims()
	if got := again.Claims(root); !reflect.DeepEqual(got, map[string][]string{"a.go": {"s_c"}}) {
		t.Fatalf("persisted claims after release = %v", got)
	}
}

// An overlap claim is not the claiming scope's own path, persists as such, and
// becomes an ordinary claim once that scope writes the path itself.
func TestOverlapClaimsPersistAndUpgrade(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	first := NewService()
	first.PersistClaims()
	first.RecordWrite(filepath.Join(root, "a.go"), root, first.NewScopedToken("s_a", "a"))
	first.RecordOverlaps(root, []string{"a.go"}, first.NewScopedToken("s_b", "b"))
	first.RecordOverlaps(root, []string{"a.go"}, first.NewScopedToken("s_a", "a")) // keeps s_a's claim

	restarted := NewService()
	restarted.PersistClaims()
	if mine, foreign := restarted.Split(root, "s_b"); mine["a.go"] || !foreign["a.go"] {
		t.Fatalf("s_b after restart: mine=%v foreign=%v", mine, foreign)
	}
	if mine, foreign := restarted.Split(root, "s_a"); !mine["a.go"] || !foreign["a.go"] {
		t.Fatalf("s_a after restart: mine=%v foreign=%v", mine, foreign)
	}
	restarted.RecordWrite(filepath.Join(root, "a.go"), root, restarted.NewScopedToken("s_b", "b"))
	again := NewService()
	again.PersistClaims()
	if mine, _ := again.Split(root, "s_b"); !mine["a.go"] {
		t.Fatalf("s_b's own write did not upgrade its overlap claim: %v", mine)
	}
}

// Ambiguous claims count as each scope's own, persist, and become ordinary
// with a scope's own write; Disown drops a claim and keeps the path out of the
// scope's view until it is retired.
func TestAmbiguousClaimsAndDisown(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	first := NewService()
	first.PersistClaims()
	first.RecordAmbiguous(root, []string{"gen.go", "other.go"}, []string{"s_a", "s_b"})

	s := NewService()
	s.PersistClaims()
	if got := s.Ambiguous(root); !reflect.DeepEqual(got, map[string]bool{"gen.go": true, "other.go": true}) {
		t.Fatalf("ambiguous after restart = %v", got)
	}
	for _, scope := range []string{"s_a", "s_b"} {
		if mine, foreign := s.Split(root, scope); !mine["gen.go"] || !foreign["gen.go"] {
			t.Fatalf("%s: mine=%v foreign=%v", scope, mine, foreign)
		}
	}
	var notified []Overlap
	s.SetOverlapNotifier(func(o Overlap) string { notified = append(notified, o); return "" })
	s.RecordWrite(filepath.Join(root, "gen.go"), root, s.NewScopedToken("s_a", "a"))
	if len(notified) != 0 {
		t.Fatalf("an ambiguous claimant was notified: %+v", notified)
	}
	for _, c := range s.ClaimDetails(root)["gen.go"] {
		if (c.Scope == "s_a") != c.Definite() {
			t.Fatalf("gen.go after s_a's write: %+v", s.ClaimDetails(root)["gen.go"])
		}
	}

	if held := s.Disown(root, "s_b", []string{"gen.go", "unclaimed.go"}); !reflect.DeepEqual(held, []string{"gen.go"}) {
		t.Fatalf("held = %v", held)
	}
	if got := s.Claims(root)["gen.go"]; !reflect.DeepEqual(got, []string{"s_a"}) {
		t.Fatalf("gen.go after disown = %v", got)
	}
	restarted := NewService()
	restarted.PersistClaims()
	if got := restarted.Disowned(root, "s_b"); !reflect.DeepEqual(got, map[string]bool{"gen.go": true, "unclaimed.go": true}) {
		t.Fatalf("disowned after restart = %v", got)
	}
	restarted.ReleaseAll(root, []string{"unclaimed.go"}, restarted.Generation())
	restarted.RecordWrites(root, []string{"gen.go"}, restarted.NewScopedToken("s_b", "b"))
	if got := restarted.Disowned(root, "s_b"); len(got) != 0 {
		t.Fatalf("disowned after retire and rewrite = %v", got)
	}
	restarted.SettleAmbiguous(root, "s_b", []string{"other.go"})
	if c := restarted.ClaimDetails(root)["other.go"]; len(c) != 2 || !c[1].Definite() || c[0].Definite() {
		t.Fatalf("other.go after s_b's commit = %+v", c)
	}
}

func sansClaimants(o ShellOverlap) ShellOverlap {
	o.Claimants = nil
	return o
}

// A shell run learns every other scope whose shell ran in its worktree while it
// was registered, including ones that started and ended in the meantime.
func TestShellRunsReportConcurrentScopes(t *testing.T) {
	s := NewService()
	a := s.BeginShell("/w", s.NewScopedToken("s_a", "test"), false)
	same := s.BeginShell("/w", s.NewScopedToken("s_a", "test"), false)
	elsewhere := s.BeginShell("/x", s.NewScopedToken("s_c", "test"), false)
	b := s.BeginShell("/w", s.NewScopedToken("s_b", "test"), false)
	if got := sansClaimants(s.EndShell(b, time.Minute)); !reflect.DeepEqual(got, ShellOverlap{Ran: []string{"s_a"}, Concurrent: []string{"s_a"}}) {
		t.Fatalf("b saw %+v", got)
	}
	s.EndShell(same, time.Minute)
	s.EndShell(elsewhere, time.Minute)
	if got := sansClaimants(s.EndShell(a, time.Minute)); !reflect.DeepEqual(got, ShellOverlap{Ran: []string{"s_b"}, Concurrent: []string{"s_b"}}) {
		t.Fatalf("a saw %+v", got)
	}
	if got := s.EndShell(s.BeginShell("/w", s.NewScopedToken("s_a", "test"), false), time.Minute); len(got.Ran) != 0 || len(got.Concurrent) != 0 {
		t.Fatalf("lone shell saw %+v", got)
	}
}

// The narrowest window wins: a background job and a foreground command shorter
// than the limit ran together but are not concurrent, from either side; two
// jobs are, and so is a job and a foreground command over the limit.
func TestBackgroundJobsAreNotConcurrentWithShortCommands(t *testing.T) {
	const limit = 200 * time.Millisecond
	s := NewService()
	job := s.BeginShell("/w", s.NewScopedToken("s_job", "test"), true)
	other := s.BeginShell("/w", s.NewScopedToken("s_other", "test"), true)
	short := s.BeginShell("/w", s.NewScopedToken("s_short", "test"), false)
	if got := sansClaimants(s.EndShell(short, limit)); !reflect.DeepEqual(got, ShellOverlap{Ran: []string{"s_job", "s_other"}, Concurrent: []string{}}) {
		t.Fatalf("short command saw %+v", got)
	}
	long := s.BeginShell("/w", s.NewScopedToken("s_long", "test"), false)
	time.Sleep(limit + 50*time.Millisecond)
	if got := sansClaimants(s.EndShell(long, limit)); !reflect.DeepEqual(got, ShellOverlap{Ran: []string{"s_job", "s_other"}, Concurrent: []string{"s_job", "s_other"}}) {
		t.Fatalf("long command saw %+v", got)
	}
	running := s.BeginShell("/w", s.NewScopedToken("s_running", "test"), false) // still short when the job ends
	defer s.EndShell(running, limit)
	window := map[string]PathStamp{"dirty.go": {Exists: true, Size: 1}}
	s.SetShellSnapshot(running, window)
	if got := sansClaimants(s.EndShell(job, limit)); !reflect.DeepEqual(got, ShellOverlap{Ran: []string{"s_long", "s_other", "s_running", "s_short"},
		Concurrent: []string{"s_long", "s_other"}, ShortWindows: []map[string]PathStamp{window}}) {
		t.Fatalf("job saw %+v", got)
	}
	// The running command's window holds what changed since its snapshot.
	got := s.EndShell(s.BeginShell("/w", s.NewScopedToken("s_job2", "test"), true), limit) // overlapped only the running command
	if !got.InShortWindow("new.go", PathStamp{Exists: true}) || !got.InShortWindow("dirty.go", PathStamp{Exists: true, Size: 2}) ||
		got.InShortWindow("dirty.go", PathStamp{Exists: true, Size: 1}) {
		t.Fatalf("short windows = %+v", got.ShortWindows)
	}
	if got := s.EndShell(other, limit); !reflect.DeepEqual(got.Concurrent, []string{"s_job", "s_job2", "s_long"}) {
		t.Fatalf("other job saw %+v", got)
	}
}

// A session's end drops its ambiguous claims, in memory and on disk, keeps its
// definite ones and other scopes' ambiguous ones, and records no new
// ambiguous claims for the ended instance. A reopened instance's claims are
// its own: the old instance's end, even a late one, leaves them alone.
func TestEndScopeDropsOnlyTheInstancesAmbiguousClaims(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	first := NewService()
	first.PersistClaims()
	first.RecordAmbiguous(root, []string{"gen.go"}, []string{"s_a", "s_b"})
	first.RecordWrites(root, []string{"mine.go"}, first.NewScopedToken("s_a", "a"))

	s := NewService() // a restarted daemon: root's claims are not loaded yet
	s.PersistClaims()
	old := s.NewScopedToken("s_a", "a")
	agent := s.Child(old, "agent")
	s.RecordShellAmbiguous(root, []string{"old.go"}, []ShellClaimant{{"s_a", agent.Incarnation()}, {"s_b", 0}})
	// The session reopens before the old instance's end has run.
	reopened := s.NewScopedToken("s_a", "a reopened")
	s.RecordShellAmbiguous(root, []string{"new.go"}, []ShellClaimant{{"s_a", reopened.Incarnation()}})
	s.EndScope(old, root)
	want := map[string][]string{"gen.go": {"s_b"}, "mine.go": {"s_a"}, "old.go": {"s_b"}, "new.go": {"s_a"}}
	if got := s.Claims(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("claims after the old instance ended = %v", got)
	}
	// A shell that overlapped the ended instance records no claim for it.
	s.RecordShellAmbiguous(root, []string{"late.go"}, []ShellClaimant{{"s_a", old.Incarnation()}, {"s_b", 0}})
	want["late.go"] = []string{"s_b"}
	restarted := NewService()
	restarted.PersistClaims()
	if got := restarted.Claims(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted claims after the old instance ended = %v", got)
	}
	if got := restarted.Ambiguous(root); !reflect.DeepEqual(got, map[string]bool{"gen.go": true, "old.go": true, "new.go": true, "late.go": true}) {
		t.Fatalf("ambiguous after the old instance ended = %v", got)
	}
	s.EndScope(reopened, root)
	if got := s.Claims(root)["new.go"]; len(got) != 0 {
		t.Fatalf("new.go after the reopened instance ended = %v", got)
	}
}

// A long job keeps no dirty set of the commands it overlapped once they end,
// and its claimants carry the session incarnation each run began in, so a
// session that ended and reopened gets no ambiguous claim from its old run.
func TestShellRunsDropSnapshotsAndCarryIncarnations(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	key, err := Canonical(root)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService()
	old := s.NewScopedToken("s_old", "old")
	job := s.BeginShell(key, s.NewScopedToken("s_job", "test"), true)
	for range 3 {
		fg := s.BeginShell(key, s.NewScopedToken("s_fg", "test"), false)
		s.SetShellSnapshot(fg, map[string]PathStamp{"x": {Exists: true}})
		s.EndShell(fg, time.Minute)
	}
	for other := range job.others {
		if other.before != nil {
			t.Fatal("a long job retains an ended command's dirty set")
		}
	}
	oldRun := s.BeginShell(key, old, true)
	s.EndShell(oldRun, time.Minute)
	s.EndScope(old, root)
	s.NewScopedToken("s_old", "reopened")
	got := s.EndShell(job, time.Minute)
	s.RecordShellAmbiguous(root, []string{"job.txt"}, got.Claimants)
	if claims := s.Claims(root)["job.txt"]; !reflect.DeepEqual(claims, []string{"s_job"}) {
		t.Fatalf("job.txt claims = %v (claimants %+v)", claims, got.Claimants)
	}
}
