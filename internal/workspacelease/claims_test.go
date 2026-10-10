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
