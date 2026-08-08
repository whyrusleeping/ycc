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
	attempts := 0 // legacy fast-path tests exercise 0252 behavior without a model
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "auto", Verify: verify, AgentAttempts: &attempts})
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

func TestWorkstreamAutoIntegrationConflictResolvedByAgent(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	attempts := 1
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "auto", Verify: "true", AgentAttempts: &attempts})
	t.Cleanup(m.ReclaimAll)
	commitInto(t, proj, "shared.txt", "original\n", "shared base")
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "shared.txt", "workstream\n", "workstream edit")
	commitInto(t, proj, "shared.txt", "base\n", "base edit")
	baseBefore := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch)
	calls := 0
	m.integrateAgent = func(got workstream.Workstream, attempt int, out integrationOutcome) integrateAgentResult {
		calls++
		if attempt != 1 || out.kind != integrationConflict || len(out.conflicts) != 1 || out.conflicts[0] != "shared.txt" {
			t.Fatalf("agent input = attempt %d, outcome %+v", attempt, out)
		}
		if out.verifyCmd != "true" {
			t.Fatalf("conflict recovery verify command = %q, want true", out.verifyCmd)
		}
		if baseAtAgent := sessionGitAt(t, proj, "rev-parse", got.BaseBranch); baseAtAgent != baseBefore {
			t.Fatalf("daemon advanced base before agent: %s -> %s", baseBefore, baseAtAgent)
		}
		sessionGitAt(t, got.WorktreePath, "reset", "--hard", out.base)
		commitInto(t, got.WorktreePath, "shared.txt", "base + workstream\n", "resolve shared intent")
		return integrateAgentResult{report: "resolved shared.txt preserving both changes"}
	}

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusMerged || calls != 1 {
		t.Fatalf("status = %s (%q), agent calls = %d", got.Status, got.StatusReason, calls)
	}
	content, err := os.ReadFile(filepath.Join(proj, "shared.txt"))
	if err != nil || string(content) != "base + workstream\n" {
		t.Fatalf("resolved primary content = %q, %v", content, err)
	}
}

func TestWorkstreamAutoIntegrationVerifyFailureFixedByAgent(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	attempts := 1
	verify := `test ! -f base-required.txt || test -f agent-fixed.txt`
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "auto", Verify: verify, AgentAttempts: &attempts})
	t.Cleanup(m.ReclaimAll)
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "candidate.txt", "candidate\n", "candidate")
	commitInto(t, proj, "base-required.txt", "new base contract\n", "advance base contract")
	baseBefore := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch)
	calls := 0
	m.integrateAgent = func(got workstream.Workstream, attempt int, out integrationOutcome) integrateAgentResult {
		calls++
		if out.kind != integrationVerifyFailed || out.verifyCmd != verify {
			t.Fatalf("agent outcome = %+v", out)
		}
		if baseAtAgent := sessionGitAt(t, proj, "rev-parse", got.BaseBranch); baseAtAgent != baseBefore {
			t.Fatalf("daemon advanced base before agent: %s -> %s", baseBefore, baseAtAgent)
		}
		commitInto(t, got.WorktreePath, "agent-fixed.txt", "fixed\n", "adapt to advanced base")
		return integrateAgentResult{report: "adapted candidate to advanced base"}
	}

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusMerged || calls != 1 {
		t.Fatalf("status = %s (%q), calls = %d", got.Status, got.StatusReason, calls)
	}
	if _, err := os.Stat(filepath.Join(proj, "agent-fixed.txt")); err != nil {
		t.Fatalf("agent fix did not land: %v", err)
	}
}

func TestWorkstreamAutoIntegrationAgentBlocked(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	attempts := 1
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "auto", Verify: "false", AgentAttempts: &attempts})
	t.Cleanup(m.ReclaimAll)
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "candidate.txt", "candidate\n", "candidate")
	baseBefore := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch)
	calls := 0
	m.integrateAgent = func(workstream.Workstream, int, integrationOutcome) integrateAgentResult {
		calls++
		return integrateAgentResult{blocked: true, report: "product decision required"}
	}

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusNeedsAttention || calls != 1 || !strings.Contains(got.StatusReason, "product decision required") {
		t.Fatalf("status = %s, reason = %q, calls = %d", got.Status, got.StatusReason, calls)
	}
	if baseAfter := sessionGitAt(t, proj, "rev-parse", ws.BaseBranch); baseAfter != baseBefore {
		t.Fatalf("base advanced in blocked case: %s -> %s", baseBefore, baseAfter)
	}
	if _, err := os.Stat(ws.WorktreePath); err != nil {
		t.Fatalf("blocked worktree removed: %v", err)
	}
}

func TestWorkstreamAutoIntegrationAgentAttemptsExhausted(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	attempts := 1
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "auto", Verify: "printf still-red; exit 1", AgentAttempts: &attempts})
	t.Cleanup(m.ReclaimAll)
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "candidate.txt", "candidate\n", "candidate")
	calls := 0
	m.integrateAgent = func(workstream.Workstream, int, integrationOutcome) integrateAgentResult {
		calls++
		return integrateAgentResult{report: "could not improve it"}
	}

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusNeedsAttention || calls != 1 || !strings.Contains(got.StatusReason, "still-red") {
		t.Fatalf("status = %s, reason = %q, calls = %d", got.Status, got.StatusReason, calls)
	}
}

func TestWorkstreamAutoIntegrationSecondAgentAttemptFixes(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	attempts := 2
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "auto", Verify: "test -f fixed.txt", AgentAttempts: &attempts})
	t.Cleanup(m.ReclaimAll)
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "candidate.txt", "candidate\n", "candidate")
	calls := 0
	m.integrateAgent = func(got workstream.Workstream, attempt int, _ integrationOutcome) integrateAgentResult {
		calls++
		if attempt == 2 {
			commitInto(t, got.WorktreePath, "fixed.txt", "fixed\n", "fix on second attempt")
		}
		return integrateAgentResult{}
	}

	readyForIntegration(t, m, ws)

	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusMerged || calls != 2 {
		t.Fatalf("status = %s (%q), calls = %d", got.Status, got.StatusReason, calls)
	}
}

func TestWorkstreamAutoIntegrationPreservesAgentTranscript(t *testing.T) {
	m, proj := newWorkstreamManager(t)
	attempts := 1
	m.reg = testRegistryWithIntegrationConfig(config.Integration{Mode: "auto", Verify: "test -f fixed.txt", AgentAttempts: &attempts})
	t.Cleanup(m.ReclaimAll)
	ws, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "candidate.txt", "candidate\n", "candidate")
	const fakeID = "s_integrate_fake"
	m.integrateAgent = func(got workstream.Workstream, _ int, _ integrationOutcome) integrateAgentResult {
		if err := m.workstreams.SetIntegrateSessionID(got.ID, fakeID); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(got.WorktreePath, ".ycc", "sessions", fakeID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("agent resolution transcript\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		commitInto(t, got.WorktreePath, "fixed.txt", "fixed\n", "agent fix")
		return integrateAgentResult{report: "fixed verification"}
	}

	readyForIntegration(t, m, ws)

	preserved := filepath.Join(proj, ".ycc", "sessions", fakeID, "events.jsonl")
	content, err := os.ReadFile(preserved)
	if err != nil || !strings.Contains(string(content), "agent resolution transcript") {
		t.Fatalf("preserved integrate transcript = %q, %v", content, err)
	}
}

func TestRunIntegrateSessionCreatesScopedSessionAndSurfacesBackendError(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	m.reg = config.NewRegistry(&config.Config{
		Models: map[string]config.Model{
			"offline": {Backend: "ollama", BaseURL: "http://127.0.0.1:1", Model: "offline"},
		},
		Roles: config.Roles{Coordinator: "offline", Implementer: "offline", Reviewers: []string{"offline"}},
		Retry: config.Retry{MaxAttempts: 1},
	})
	t.Cleanup(m.ReclaimAll)
	ws, original, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Stop(original.ID)
	commitInto(t, ws.WorktreePath, "candidate.txt", "candidate\n", "candidate")
	if _, err := m.workstreams.Transition(ws.ID, workstream.StatusReady, "",
		workstream.StatusActive, workstream.StatusNeedsAttention); err != nil {
		t.Fatal(err)
	}

	res := m.runIntegrateSession(ws, 1, integrationOutcome{
		kind: integrationVerifyFailed, base: ws.BaseBranch, reason: "verify failed",
		verifyCmd: "false", verifyErr: "exit status 1",
	})
	if res.err == nil {
		t.Fatalf("offline integrate session result = %+v, want backend error", res)
	}
	got, _ := m.workstreams.Get(ws.ID)
	if got.IntegrateSessionID == "" {
		t.Fatal("integrate session id was not recorded")
	}
	path := filepath.Join(ws.WorktreePath, ".ycc", "sessions", got.IntegrateSessionID, "events.jsonl")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read integrate session log: %v", err)
	}
	if !strings.Contains(string(contents), `"mode":"integrate"`) && !strings.Contains(string(contents), `"mode": "integrate"`) {
		t.Fatalf("integrate session_started not present in log:\n%s", contents)
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
