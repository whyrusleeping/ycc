package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// claimSummary lists a path's claims as scope:kind, then "unattributed" when
// the path is marked so.
func claimSummary(ownership *workspacelease.Service, root, rel string) string {
	var parts []string
	for _, c := range ownership.ClaimDetails(root)[rel] {
		kind := "definite"
		if c.Overlap {
			kind = "overlap"
		}
		parts = append(parts, c.Scope+":"+kind)
	}
	if ownership.Unattributed(root)[rel] {
		parts = append(parts, "unattributed")
	}
	return strings.Join(parts, ",")
}

// A short foreground write while another session's background job runs has
// no single likely writer: it is left unattributed, claimed by neither session
// during the job or after it exits, and the command's result says so, naming
// the job.
func TestShortForegroundWriteDuringAnotherSessionsJobIsUnattributed(t *testing.T) {
	root, syncDir, ownership, open := newClaimTestTree(t)
	fg, bg := open("s_fg"), open("s_bg")
	bg.startJob(t, syncDir, "job", fmt.Sprintf("touch %[1]s/job.started; while test ! -f %[1]s/job.stop; do sleep 0.02; done", syncDir))
	res := dispatch(t, fg.reg, "Bash", `{"command":"echo fg > fg.txt"}`)
	if res.IsError || !strings.Contains(res.Content, "[shell attribution]") || !strings.Contains(res.Content, "s_bg (background job)") ||
		!strings.Contains(res.Content, "left unattributed (1): fg.txt") || strings.Contains(res.Content, "release_claim") {
		t.Fatalf("foreground Bash: %s", res.Content)
	}
	if got := claimSummary(ownership, root, "fg.txt"); got != "unattributed" {
		t.Fatalf("fg.txt claims while the job runs = %s", got)
	}
	touch(t, filepath.Join(syncDir, "job.stop"))
	bg.waitJobs(t)
	if got := claimSummary(ownership, root, "fg.txt"); got != "unattributed" {
		t.Fatalf("fg.txt claims after the job = %s", got)
	}
	for _, scope := range []string{"s_fg", "s_bg"} {
		if mine, foreign := ownership.Split(root, scope); len(mine)+len(foreign) != 0 {
			t.Fatalf("%s: mine=%v foreign=%v", scope, mine, foreign)
		}
	}
}

// A background job's exit claims the unclaimed paths that changed during its
// run when no other session's shell ran meanwhile; with another session's job
// or foreground command overlapping they are left unattributed.
func TestBackgroundJobExitClaimsUnclaimedChanges(t *testing.T) {
	root, syncDir, ownership, open := newClaimTestTree(t)
	fg, first, second := open("s_fg"), open("s_first"), open("s_second")
	first.startJob(t, syncDir, "first", fmt.Sprintf("touch %[1]s/first.started; echo first > first.txt; while test ! -f %[1]s/first.stop; do sleep 0.02; done", syncDir))
	second.startJob(t, syncDir, "second", fmt.Sprintf("touch %[1]s/second.started; while test ! -f %[1]s/second.stop; do sleep 0.02; done", syncDir))
	if res := dispatch(t, fg.reg, "Bash", `{"command":"echo fg > fg.txt"}`); res.IsError {
		t.Fatalf("foreground Bash: %s", res.Content)
	}
	if err := os.WriteFile(filepath.Join(root, "hand.txt"), []byte("hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(syncDir, "first.stop"))
	first.waitJobs(t)
	touch(t, filepath.Join(syncDir, "second.stop"))
	second.waitJobs(t)
	for _, rel := range []string{"first.txt", "hand.txt", "fg.txt"} {
		if got := claimSummary(ownership, root, rel); got != "unattributed" {
			t.Fatalf("%s claims = %s", rel, got)
		}
	}

	first.startJob(t, syncDir, "alone", fmt.Sprintf("touch %[1]s/alone.started; echo alone > alone.txt; while test ! -f %[1]s/alone.stop; do sleep 0.02; done", syncDir))
	touch(t, filepath.Join(syncDir, "alone.stop"))
	first.waitJobs(t)
	if got := claimSummary(ownership, root, "alone.txt"); got != "s_first:definite" {
		t.Fatalf("alone.txt claims = %s", got)
	}
}

// A file another session's background job writes during a short foreground
// command is not claimed by the command's session, neither when the command
// ends nor when the job exits: it is left unattributed. A path this session
// already claims definitely stays its own.
func TestShortCommandGetsNoClaimOnAnotherSessionsJobWrite(t *testing.T) {
	root, syncDir, ownership, open := newClaimTestTree(t)
	fg, bg := open("s_fg"), open("s_bg")
	if res := dispatch(t, fg.reg, "Write", `{"file_path":"mine.txt","content":"mine\n"}`); res.IsError {
		t.Fatalf("Write: %s", res.Content)
	}
	bg.startJob(t, syncDir, "job", fmt.Sprintf("touch %[1]s/job.started; while test ! -f %[1]s/fg.running; do sleep 0.02; done; "+
		"echo job > out.txt; touch %[1]s/job.wrote; while test ! -f %[1]s/job.stop; do sleep 0.02; done", syncDir))
	res := dispatch(t, fg.reg, "Bash", fmt.Sprintf(`{"command":%q}`, fmt.Sprintf(
		"touch %[1]s/fg.running; echo again >> mine.txt; while test ! -f %[1]s/job.wrote; do sleep 0.02; done", syncDir)))
	if res.IsError || !strings.Contains(res.Content, "left unattributed (1): out.txt") {
		t.Fatalf("foreground Bash: %s", res.Content)
	}
	if got := claimSummary(ownership, root, "out.txt"); got != "unattributed" {
		t.Fatalf("out.txt claims after the short command = %s", got)
	}
	touch(t, filepath.Join(syncDir, "job.stop"))
	bg.waitJobs(t)
	if got := claimSummary(ownership, root, "out.txt"); got != "unattributed" {
		t.Fatalf("out.txt claims after the job = %s", got)
	}
	if got := claimSummary(ownership, root, "mine.txt"); got != "s_fg:definite" {
		t.Fatalf("mine.txt claims = %s", got)
	}
}

// A job ending while a short command is attributing its writes waits for it,
// so it sees the command's run or its attribution rather than neither: the
// command's write is left unattributed, not claimed by either session.
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
	if got := claimSummary(ownership, root, "fg.txt"); got != "unattributed" {
		t.Fatalf("fg.txt claims = %s", got)
	}
}
