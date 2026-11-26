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

// An unattributed path is no scope's claim (in neither side of any Split),
// persists, and settles on a scope that writes it definitely; a commit or
// revert retires the mark, guarded by generation.
func TestUnattributedPathsPersistAndSettle(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	first := NewService()
	first.PersistClaims()
	first.RecordUnattributed(root, []string{"gen.go", "other.go"})

	s := NewService()
	s.PersistClaims()
	if got := s.Unattributed(root); !reflect.DeepEqual(got, map[string]bool{"gen.go": true, "other.go": true}) {
		t.Fatalf("unattributed after restart = %v", got)
	}
	if got := s.Claims(root); len(got) != 0 {
		t.Fatalf("unattributed paths are claimed: %v", got)
	}
	for _, scope := range []string{"s_a", "s_b"} {
		if mine, foreign := s.Split(root, scope); mine["gen.go"] || foreign["gen.go"] {
			t.Fatalf("%s: mine=%v foreign=%v", scope, mine, foreign)
		}
	}
	var notified []Overlap
	s.SetOverlapNotifier(func(o Overlap) string { notified = append(notified, o); return "" })
	s.RecordWrite(filepath.Join(root, "gen.go"), root, s.NewScopedToken("s_a", "a"))
	if len(notified) != 0 {
		t.Fatalf("a possible writer was notified: %+v", notified)
	}
	if got := s.Unattributed(root); !reflect.DeepEqual(got, map[string]bool{"other.go": true}) {
		t.Fatalf("unattributed after s_a's write = %v", got)
	}
	if got := s.Claims(root); !reflect.DeepEqual(got, map[string][]string{"gen.go": {"s_a"}}) {
		t.Fatalf("claims after s_a's write = %v", got)
	}

	// A mark recorded after the caller sampled the generation survives.
	before := s.Generation()
	s.RecordUnattributed(root, []string{"late.go"})
	s.ReleaseAll(root, []string{"other.go", "late.go"}, before)
	restarted := NewService()
	restarted.PersistClaims()
	if got := restarted.Unattributed(root); !reflect.DeepEqual(got, map[string]bool{"late.go": true}) {
		t.Fatalf("unattributed after guarded release = %v", got)
	}
}

// A claims record from a daemon before unattributed marks turns its ambiguous
// claims and released paths into unattributed paths, keeping the definite
// claims.
func TestLegacyAmbiguousAndReleasedRecordsBecomeUnattributed(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	key, err := Canonical(root)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"claims":{"gen.go":["s_a","s_b"],"mine.go":["s_a"],"both.go":["s_a","s_b"]},` +
		`"ambiguous":{"gen.go":["s_a","s_b"],"both.go":["s_b"]},"released":{"gone.go":["s_c"]}}`
	path := claimsFile(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewService()
	s.PersistClaims()
	if got := s.Claims(root); !reflect.DeepEqual(got, map[string][]string{"mine.go": {"s_a"}, "both.go": {"s_a"}}) {
		t.Fatalf("claims = %v", got)
	}
	if got := s.Unattributed(root); !reflect.DeepEqual(got, map[string]bool{"gen.go": true, "both.go": true, "gone.go": true}) {
		t.Fatalf("unattributed = %v", got)
	}
}

// A shell run learns every other scope whose shell ran in its worktree while it
// was registered, including ones that started and ended in the meantime.
func TestShellRunsReportConcurrentScopes(t *testing.T) {
	s := NewService()
	a := s.BeginShell("/w", s.NewScopedToken("s_a", "test"), false)
	same := s.BeginShell("/w", s.NewScopedToken("s_a", "test"), false)
	elsewhere := s.BeginShell("/x", s.NewScopedToken("s_c", "test"), false)
	b := s.BeginShell("/w", s.NewScopedToken("s_b", "test"), false)
	if got := s.EndShell(b); !reflect.DeepEqual(got, ShellOverlap{Ran: []string{"s_a"}, Jobs: []string{}}) {
		t.Fatalf("b saw %+v", got)
	}
	s.EndShell(same)
	s.EndShell(elsewhere)
	if got := s.EndShell(a); !reflect.DeepEqual(got, ShellOverlap{Ran: []string{"s_b"}, Jobs: []string{}}) {
		t.Fatalf("a saw %+v", got)
	}
	if got := s.EndShell(s.BeginShell("/w", s.NewScopedToken("s_a", "test"), false)); len(got.Ran) != 0 {
		t.Fatalf("lone shell saw %+v", got)
	}
}

// A background job overlaps a short foreground command like any other shell,
// from either side, and is reported as a job.
func TestBackgroundJobsOverlapShortCommands(t *testing.T) {
	s := NewService()
	job := s.BeginShell("/w", s.NewScopedToken("s_job", "test"), true)
	short := s.BeginShell("/w", s.NewScopedToken("s_short", "test"), false)
	if got := s.EndShell(short); !reflect.DeepEqual(got, ShellOverlap{Ran: []string{"s_job"}, Jobs: []string{"s_job"}}) {
		t.Fatalf("short command saw %+v", got)
	}
	running := s.BeginShell("/w", s.NewScopedToken("s_running", "test"), false)
	defer s.EndShell(running)
	if got := s.EndShell(job); !reflect.DeepEqual(got, ShellOverlap{Ran: []string{"s_running", "s_short"}, Jobs: []string{}}) {
		t.Fatalf("job saw %+v", got)
	}
}

// A long job keeps no reference to the commands it overlapped once they end.
func TestShellRunsDropEndedRuns(t *testing.T) {
	s := NewService()
	job := s.BeginShell("/w", s.NewScopedToken("s_job", "test"), true)
	for range 3 {
		fg := s.BeginShell("/w", s.NewScopedToken("s_fg", "test"), false)
		s.EndShell(fg)
		if fg.others != nil {
			t.Fatal("an ended command keeps its overlapping runs")
		}
	}
	if got := s.EndShell(job); !reflect.DeepEqual(got.Ran, []string{"s_fg"}) {
		t.Fatalf("job saw %+v", got)
	}
}
