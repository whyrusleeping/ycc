package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/notify"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

func autoIntegrationManager(t *testing.T, verify string) (*Manager, string) {
	t.Helper()
	m, proj := newWorkstreamManager(t)
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "auto", Verify: verify})
	t.Cleanup(m.ReclaimAll)
	return m, proj
}

func readyForIntegration(t *testing.T, m *Manager, ws workstream.Workstream) {
	t.Helper()
	m.evaluateWorkstreamReadiness(ws.ID, event.StatusIdle, false)
	m.integrationWG.Wait()
}

func TestWorkstreamAutoIntegrationFastPath(t *testing.T) {
	m, proj := autoIntegrationManager(t, "true")
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	commitInto(t, ws.WorktreePath, "auto.txt", "green\n", "auto green")

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusMerged {
		t.Fatalf("status = %s, want merged (reason %q)", got.Status, got.StatusReason)
	}
	if content, err := os.ReadFile(filepath.Join(proj, "auto.txt")); err != nil || string(content) != "green\n" {
		t.Fatalf("primary auto.txt = %q, %v", content, err)
	}
	if _, err := os.Stat(ws.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if _, ok := snapshotHasEvent(s, event.WorkstreamIntegrating); !ok {
		t.Fatal("missing workstream_integrating event")
	}
	if _, ok := snapshotHasEvent(s, event.WorkstreamMerged); !ok {
		t.Fatal("missing workstream_merged event")
	}
	for _, ev := range s.Log().Snapshot() {
		if ev.Type == event.ModelTurn || ev.Type == event.SubagentFinished {
			t.Fatalf("zero-token fast path emitted agent event %s", ev.Type)
		}
	}
}

func TestWorkstreamAutoIntegrationSequential(t *testing.T) {
	m, proj := autoIntegrationManager(t, "true")
	ws1, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	ws2, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws1.WorktreePath, "one.txt", "one\n", "one")
	commitInto(t, ws2.WorktreePath, "two.txt", "two\n", "two")

	m.evaluateWorkstreamReadiness(ws1.ID, event.StatusIdle, false)
	m.evaluateWorkstreamReadiness(ws2.ID, event.StatusIdle, false)
	m.integrationWG.Wait()

	for _, id := range []string{ws1.ID, ws2.ID} {
		got, _ := m.workstreams.Get(id)
		if got.Status != workstream.StatusMerged {
			t.Fatalf("%s status = %s (%q)", id, got.Status, got.StatusReason)
		}
	}
	if _, err := os.Stat(filepath.Join(proj, "one.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(proj, "two.txt")); err != nil {
		t.Fatal(err)
	}
	if got := sessionGitAt(t, proj, "rev-list", "--count", ws1.BaseCommit+".."+ws1.BaseBranch); got != "2" {
		t.Fatalf("base commit count = %s, want 2", got)
	}
	if merges := sessionGitAt(t, proj, "rev-list", "--merges", ws1.BaseCommit+".."+ws1.BaseBranch); merges != "" {
		t.Fatalf("unexpected merge commits: %s", merges)
	}
}

func TestWorkstreamAutoIntegrationVerifyFailure(t *testing.T) {
	m, proj := autoIntegrationManager(t, "printf 'verification-red'; exit 1")
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "red.txt", "red\n", "red")
	baseBefore := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch)

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusNeedsAttention || !strings.Contains(got.StatusReason, "verification-red") {
		t.Fatalf("status = %s, reason = %q", got.Status, got.StatusReason)
	}
	if baseAfter := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch); baseAfter != baseBefore {
		t.Fatalf("base advanced: %s -> %s", baseBefore, baseAfter)
	}
	if _, err := os.Stat(ws.WorktreePath); err != nil {
		t.Fatalf("worktree removed after red verify: %v", err)
	}
}

func TestWorkstreamAutoIntegrationConflict(t *testing.T) {
	m, proj := autoIntegrationManager(t, "true")
	commitInto(t, proj, "shared.txt", "original\n", "shared base")
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "shared.txt", "workstream\n", "workstream edit")
	commitInto(t, proj, "shared.txt", "base\n", "base edit")
	baseBefore := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch)

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusNeedsAttention || !strings.Contains(got.StatusReason, "shared.txt") {
		t.Fatalf("status = %s, reason = %q", got.Status, got.StatusReason)
	}
	if baseAfter := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch); baseAfter != baseBefore {
		t.Fatalf("base changed: %s -> %s", baseBefore, baseAfter)
	}
	if _, err := os.Stat(ws.WorktreePath); err != nil {
		t.Fatalf("worktree removed after conflict: %v", err)
	}
}

func TestWorkstreamVerifyContentMutationAborts(t *testing.T) {
	tests := []struct {
		name   string
		verify string
	}{
		{name: "tracked file", verify: `printf 'changed\n' >> candidate.txt`},
		{name: "new untracked file", verify: `printf 'generated\n' > generated.txt`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, proj := autoIntegrationManager(t, tc.verify)
			ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
			if err != nil {
				t.Fatal(err)
			}
			commitInto(t, ws.WorktreePath, "candidate.txt", "candidate\n", "candidate")
			baseBefore := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch)

			readyForIntegration(t, m, ws)

			got, _ := m.workstreams.Get(ws.ID)
			if got.Status != workstream.StatusReady {
				t.Fatalf("status = %s, want ready", got.Status)
			}
			if baseAfter := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch); baseAfter != baseBefore {
				t.Fatalf("base advanced after verify mutated content: %s -> %s", baseBefore, baseAfter)
			}
			projection := event.Reduce(s.Log().Snapshot())
			if projection.WorkstreamState != "ready" {
				t.Fatalf("projection state = %q, want ready", projection.WorkstreamState)
			}
			lastReason := ""
			for _, ev := range s.Log().Snapshot() {
				if ev.Type == event.WorkstreamReady {
					lastReason = str(ev.Data, "reason")
				}
			}
			if !strings.Contains(lastReason, "worktree contents changed") {
				t.Fatalf("ready reason = %q, want content-change detail", lastReason)
			}
		})
	}
}

func TestWorkstreamStableBootstrapUntrackedFileMerges(t *testing.T) {
	m, proj := autoIntegrationManager(t, `test -f .env`)
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "candidate.txt", "candidate\n", "candidate")
	if err := os.WriteFile(filepath.Join(ws.WorktreePath, ".env"), []byte("TOKEN=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusMerged {
		t.Fatalf("status = %s, reason = %q", got.Status, got.StatusReason)
	}
	if _, err := os.Stat(filepath.Join(proj, "candidate.txt")); err != nil {
		t.Fatalf("verified commit did not land: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".env")); !os.IsNotExist(err) {
		t.Fatalf("untracked bootstrap file unexpectedly landed in base: %v", err)
	}
}

func TestWorkstreamAutoIntegrationPinsVerifiedTip(t *testing.T) {
	verify := `printf 'after verify\n' > moved.txt && git add moved.txt && git commit -m 'move branch during verify'`
	m, proj := autoIntegrationManager(t, verify)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "verified.txt", "verified\n", "verified candidate")
	verifiedTip := sessionGitAt(t, proj, "rev-parse", ws.Branch)
	baseBefore := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch)

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusReady {
		t.Fatalf("status = %s, want ready", got.Status)
	}
	if baseAfter := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch); baseAfter != baseBefore {
		t.Fatalf("base advanced to an unverified branch tip: %s -> %s", baseBefore, baseAfter)
	}
	if branchAfter := sessionGitAt(t, proj, "rev-parse", ws.Branch); branchAfter == verifiedTip {
		t.Fatalf("verify command did not move branch from pinned tip %s", verifiedTip)
	}
	projection := event.Reduce(s.Log().Snapshot())
	if projection.WorkstreamState != "ready" {
		t.Fatalf("projection state = %q, want ready after pinned-ref abort", projection.WorkstreamState)
	}
}

func TestWorkstreamLateDirtyBaseRestoresReadyProjection(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	verify := fmt.Sprintf("printf dirty > %q", filepath.Join(proj, "became-dirty.txt"))
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "auto", Verify: verify})
	t.Cleanup(m.ReclaimAll)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "late-defer.txt", "candidate\n", "candidate")
	baseBefore := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch)

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusReady {
		t.Fatalf("status = %s, want ready", got.Status)
	}
	if baseAfter := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch); baseAfter != baseBefore {
		t.Fatalf("late dirty base advanced: %s -> %s", baseBefore, baseAfter)
	}
	projection := event.Reduce(s.Log().Snapshot())
	if projection.WorkstreamState != "ready" {
		t.Fatalf("projection state = %q, want ready after late defer", projection.WorkstreamState)
	}
	lastReadyReason := ""
	for _, ev := range s.Log().Snapshot() {
		if ev.Type == event.WorkstreamReady {
			lastReadyReason = str(ev.Data, "reason")
		}
	}
	if !strings.Contains(lastReadyReason, "base tree dirty") {
		t.Fatalf("restored ready reason = %q, want dirty-base detail", lastReadyReason)
	}
}

func TestWorkstreamAutoWithoutVerifyDegradesToGate(t *testing.T) {
	m, proj := autoIntegrationManager(t, "")
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "gate.txt", "gate\n", "gate")
	baseBefore := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch)

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusReady {
		t.Fatalf("status = %s, want ready", got.Status)
	}
	if baseAfter := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch); baseAfter != baseBefore {
		t.Fatalf("base changed: %s -> %s", baseBefore, baseAfter)
	}
}

func TestWorkstreamManualModeDoesNotEnqueue(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "manual", Verify: "true"})
	t.Cleanup(m.ReclaimAll)
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "manual.txt", "manual\n", "manual")
	m.evaluateWorkstreamReadiness(ws.ID, event.StatusIdle, false)
	if got, _ := m.workstreams.Get(ws.ID); got.Status != workstream.StatusReady {
		t.Fatalf("status = %s, want ready", got.Status)
	}
	out, err := m.MergeWorkstream(ws.ID, true)
	if err != nil || !out.Merged {
		t.Fatalf("manual MergeWorkstream = %+v, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(proj, "manual.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestWorkstreamMaxParallel(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "manual", MaxParallel: 1})
	t.Cleanup(m.ReclaimAll)
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"}); err == nil || !strings.Contains(err.Error(), "max_parallel") {
		t.Fatalf("second spawn error = %v, want max_parallel", err)
	}
	if err := m.DiscardWorkstream(ws.ID); err != nil {
		t.Fatal(err)
	}
	if ws2, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"}); err != nil {
		t.Fatalf("spawn after discard: %v", err)
	} else {
		_ = m.DiscardWorkstream(ws2.ID)
	}
}

func TestWorkstreamAutoIntegrationDirtyBaseDefers(t *testing.T) {
	m, proj := autoIntegrationManager(t, "true")
	sink := newNotifySink(t)
	n := notify.New(config.Notify{URL: sink.URL})
	m.SetNotifier(n)
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "defer.txt", "defer\n", "defer")
	branchBefore := sessionGitAt(t, proj, "rev-parse", ws.Branch)
	if err := os.WriteFile(filepath.Join(proj, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusReady {
		t.Fatalf("status = %s, want ready", got.Status)
	}
	if branchAfter := sessionGitAt(t, proj, "rev-parse", ws.Branch); branchAfter != branchBefore {
		t.Fatalf("branch rebased despite dirty base: %s -> %s", branchBefore, branchAfter)
	}
	n.Flush()
	found := false
	for _, rec := range sink.all() {
		if rec.tags == notify.KindAttention && strings.Contains(rec.body, "base tree dirty") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing dirty-base attention notification: %+v", sink.all())
	}
}

func TestIntegrationVerifyUsesWorktreeEnvironment(t *testing.T) {
	m, _ := autoIntegrationManager(t, `test "$INTEGRATION_TEST_ENV" = green`)
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "auto", Verify: `test "$INTEGRATION_TEST_ENV" = green`})
	// Project-local [worktree] configuration wins and is also supplied to verify.
	primary, _ := m.projects.Resolve("demo")
	commitInto(t, primary, "ycc.toml", "[worktree.env]\nINTEGRATION_TEST_ENV = \"green\"\n", "configure worktree env")
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "env.txt", "env\n", "env")
	readyForIntegration(t, m, ws)
	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusMerged {
		t.Fatalf("status = %s, reason = %q", got.Status, got.StatusReason)
	}
}
