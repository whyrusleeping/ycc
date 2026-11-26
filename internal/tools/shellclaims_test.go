package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// claimTestSession is one session's tool registry and job registry in a
// shared worktree.
type claimTestSession struct {
	reg  *Registry
	jobs *jobs.Registry
}

func newClaimTestTree(t *testing.T) (root, syncDir string, ownership *workspacelease.Service, open func(scope string) claimTestSession) {
	t.Helper()
	root = t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	syncDir = t.TempDir()
	ownership = workspacelease.NewService()
	open = func(scope string) claimTestSession {
		jr := jobs.NewRegistry()
		t.Cleanup(func() { _ = jr.KillAll() })
		reg := New()
		reg.Add(Worker(&Workspace{Root: root, Ownership: ownership, Jobs: jr,
			Emitter: event.NewEmitter(&captureRec{}, "coordinator"), MutationToken: ownership.NewScopedToken(scope, scope)})...)
		return claimTestSession{reg, jr}
	}
	return root, syncDir, ownership, open
}

// startJob starts command as a background job and returns once it has run
// its first line (it must touch syncDir/<name>.started).
func (s claimTestSession) startJob(t *testing.T, syncDir, name, command string) {
	t.Helper()
	if res := dispatch(t, s.reg, "Bash", fmt.Sprintf(`{"command":%q,"run_in_background":true}`, command)); res.IsError {
		t.Fatalf("background Bash: %s", res.Content)
	}
	waitForFile(t, filepath.Join(syncDir, name+".started"))
}

// waitJobs waits until none of the session's jobs runs; a job attributes its
// writes before it is finished.
func (s claimTestSession) waitJobs(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		running := false
		for _, info := range s.jobs.List("", true) {
			running = running || info.Status == jobs.Running
		}
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background job did not finish")
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func claimSummary(ownership *workspacelease.Service, root, rel string) string {
	var parts []string
	for _, c := range ownership.ClaimDetails(root)[rel] {
		kind := "definite"
		switch {
		case c.Ambiguous:
			kind = "ambiguous"
		case c.Overlap:
			kind = "overlap"
		}
		parts = append(parts, c.Scope+":"+kind)
	}
	return strings.Join(parts, ",")
}

// A session whose only running shell is a background job is not a co-claimant
// of another session's short foreground write: that write is definite for the
// command's scope, during the job and after it exits.
func TestBackgroundJobGainsNoClaimFromShortForegroundWrite(t *testing.T) {
	root, syncDir, ownership, open := newClaimTestTree(t)
	fg, bg := open("s_fg"), open("s_bg")
	bg.startJob(t, syncDir, "job", fmt.Sprintf("touch %[1]s/job.started; while test ! -f %[1]s/job.stop; do sleep 0.02; done", syncDir))
	if res := dispatch(t, fg.reg, "Bash", `{"command":"echo fg > fg.txt"}`); res.IsError || strings.Contains(res.Content, "[shell attribution]") {
		t.Fatalf("foreground Bash: %s", res.Content)
	}
	if got := claimSummary(ownership, root, "fg.txt"); got != "s_fg:definite" {
		t.Fatalf("fg.txt claims while the job runs = %s", got)
	}
	touch(t, filepath.Join(syncDir, "job.stop"))
	bg.waitJobs(t)
	if got := claimSummary(ownership, root, "fg.txt"); got != "s_fg:definite" {
		t.Fatalf("fg.txt claims after the job = %s", got)
	}
	if mine, _ := ownership.Split(root, "s_bg"); len(mine) != 0 {
		t.Fatalf("the job's session claims %v", mine)
	}
}

// A background job's exit claims the unclaimed paths that changed during its
// run. With another session's job overlapping they are ambiguous between the
// two jobs' sessions only, not a short foreground command's; a job running
// alone claims definitely.
func TestBackgroundJobExitClaimsUnclaimedChanges(t *testing.T) {
	root, syncDir, ownership, open := newClaimTestTree(t)
	fg, first, second := open("s_fg"), open("s_first"), open("s_second")
	first.startJob(t, syncDir, "first", fmt.Sprintf("touch %[1]s/first.started; echo first > first.txt; while test ! -f %[1]s/first.stop; do sleep 0.02; done", syncDir))
	second.startJob(t, syncDir, "second", fmt.Sprintf("touch %[1]s/second.started; while test ! -f %[1]s/second.stop; do sleep 0.02; done", syncDir))
	if res := dispatch(t, fg.reg, "Bash", `{"command":"echo fg > fg.txt"}`); res.IsError || strings.Contains(res.Content, "[shell attribution]") {
		t.Fatalf("foreground Bash: %s", res.Content)
	}
	if err := os.WriteFile(filepath.Join(root, "hand.txt"), []byte("hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(syncDir, "first.stop"))
	first.waitJobs(t)
	touch(t, filepath.Join(syncDir, "second.stop"))
	second.waitJobs(t)
	for _, rel := range []string{"first.txt", "hand.txt"} {
		if got := claimSummary(ownership, root, rel); got != "s_first:ambiguous,s_second:ambiguous" {
			t.Fatalf("%s claims = %s", rel, got)
		}
	}
	if got := claimSummary(ownership, root, "fg.txt"); got != "s_fg:definite" {
		t.Fatalf("fg.txt claims = %s", got)
	}

	first.startJob(t, syncDir, "alone", fmt.Sprintf("touch %[1]s/alone.started; echo alone > alone.txt; while test ! -f %[1]s/alone.stop; do sleep 0.02; done", syncDir))
	if res := dispatch(t, fg.reg, "Bash", `{"command":"cat alone.txt"}`); res.IsError {
		t.Fatalf("foreground read: %s", res.Content)
	}
	touch(t, filepath.Join(syncDir, "alone.stop"))
	first.waitJobs(t)
	if got := claimSummary(ownership, root, "alone.txt"); got != "s_first:definite" {
		t.Fatalf("alone.txt claims = %s", got)
	}
}

// The known cost of the rule and its recovery: a job's write that lands
// during another session's short command is claimed definitely by that
// session; once it releases the path, the job's exit claims it.
func TestReleasedShortCommandClaimGoesToTheJobOnExit(t *testing.T) {
	root, syncDir, ownership, open := newClaimTestTree(t)
	fg, bg := open("s_fg"), open("s_bg")
	bg.startJob(t, syncDir, "job", fmt.Sprintf("touch %[1]s/job.started; while test ! -f %[1]s/fg.running; do sleep 0.02; done; "+
		"echo job > out.txt; touch %[1]s/job.wrote; while test ! -f %[1]s/job.stop; do sleep 0.02; done", syncDir))
	if res := dispatch(t, fg.reg, "Bash", fmt.Sprintf(`{"command":%q}`, fmt.Sprintf(
		"touch %[1]s/fg.running; while test ! -f %[1]s/job.wrote; do sleep 0.02; done", syncDir))); res.IsError {
		t.Fatalf("foreground Bash: %s", res.Content)
	}
	if got := claimSummary(ownership, root, "out.txt"); got != "s_fg:definite" {
		t.Fatalf("out.txt claims after the short command = %s", got)
	}
	if held := ownership.Disown(root, "s_fg", []string{"out.txt"}); !reflect.DeepEqual(held, []string{"out.txt"}) {
		t.Fatalf("released %v", held)
	}
	touch(t, filepath.Join(syncDir, "job.stop"))
	bg.waitJobs(t)
	if got := claimSummary(ownership, root, "out.txt"); got != "s_bg:definite" {
		t.Fatalf("out.txt claims after the job = %s", got)
	}
}

// runForeground starts a foreground command that runs script, touches
// syncDir/fg.wrote and waits for syncDir/fg.stop; the returned func ends it.
func (s claimTestSession) runForeground(t *testing.T, syncDir, script string) func() {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		res := dispatch(t, s.reg, "Bash", fmt.Sprintf(`{"command":%q}`, fmt.Sprintf(
			"%[2]s; touch %[1]s/fg.wrote; while test ! -f %[1]s/fg.stop; do sleep 0.02; done", syncDir, script)))
		if res.IsError {
			done <- res.Content
			return
		}
		done <- ""
	}()
	waitForFile(t, filepath.Join(syncDir, "fg.wrote"))
	return func() {
		t.Helper()
		touch(t, filepath.Join(syncDir, "fg.stop"))
		if msg := <-done; msg != "" {
			t.Fatalf("foreground Bash: %s", msg)
		}
	}
}

// The short command's window wins in either exit order: a job that exits while
// another session's short command still runs leaves what changed in that
// command's window to it, even a copy that keeps an old mtime, and still
// claims what it changed before, a deletion included.
func TestBackgroundJobExitingDuringShortCommandLeavesItsWindow(t *testing.T) {
	root, syncDir, ownership, open := newClaimTestTree(t)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	touch(t, filepath.Join(root, "obsolete.txt"))
	git("add", "obsolete.txt")
	git("commit", "-qm", "init")
	src := filepath.Join(syncDir, "src")
	if err := os.WriteFile(src, []byte("copied\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(src, old, old); err != nil {
		t.Fatal(err)
	}
	fg, bg := open("s_fg"), open("s_bg")
	bg.startJob(t, syncDir, "job", fmt.Sprintf("echo early > early.txt; rm obsolete.txt; touch %[1]s/job.started; "+
		"while test ! -f %[1]s/fg.wrote; do sleep 0.02; done", syncDir))
	finish := fg.runForeground(t, syncDir, fmt.Sprintf("echo fg > fg.txt; cp -p %q copied.txt", src))
	bg.waitJobs(t)
	for _, rel := range []string{"fg.txt", "copied.txt"} {
		if got := claimSummary(ownership, root, rel); got != "" {
			t.Fatalf("%s claims after the job, before the command ends = %s", rel, got)
		}
	}
	finish()
	for rel, want := range map[string]string{"fg.txt": "s_fg:definite", "copied.txt": "s_fg:definite", "early.txt": "s_bg:definite", "obsolete.txt": "s_bg:definite"} {
		if got := claimSummary(ownership, root, rel); got != want {
			t.Fatalf("%s claims = %s, want %s", rel, got, want)
		}
	}
}

// A job ending while a short command is attributing its writes waits for it,
// so it sees the command's claims rather than neither the command nor them.
func TestBackgroundJobExitWaitsForForegroundAttribution(t *testing.T) {
	root, syncDir, ownership, open := newClaimTestTree(t)
	bg := open("s_bg")
	reached, allow := make(chan struct{}), make(chan struct{})
	fgJobs := jobs.NewRegistry()
	t.Cleanup(func() { _ = fgJobs.KillAll() })
	fg := New()
	fg.Add(Worker(&Workspace{Root: root, Ownership: ownership, Jobs: fgJobs, Emitter: event.NewEmitter(&captureRec{}, "coordinator"),
		MutationToken: ownership.NewScopedToken("s_fg", "s_fg"), SharedBookkeeping: func(path string) bool {
			if filepath.Base(path) == "fg.txt" {
				close(reached)
				<-allow
			}
			return false
		}})...)
	bg.startJob(t, syncDir, "job", fmt.Sprintf("touch %[1]s/job.started; while test ! -f %[1]s/job.stop; do sleep 0.02; done", syncDir))
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatch(t, fg, "Bash", `{"command":"echo fg > fg.txt"}`)
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the foreground command did not reach attribution")
	}
	touch(t, filepath.Join(syncDir, "job.stop"))
	time.Sleep(300 * time.Millisecond) // the job exits and reaches its attribution
	close(allow)
	<-done
	bg.waitJobs(t)
	if got := claimSummary(ownership, root, "fg.txt"); got != "s_fg:definite" {
		t.Fatalf("fg.txt claims = %s", got)
	}
}
