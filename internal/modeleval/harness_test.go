package modeleval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

func TestScenarioFixturesHaveFactBasedOracles(t *testing.T) {
	for _, scenario := range Scenarios() {
		t.Run(scenario.Name, func(t *testing.T) {
			root := t.TempDir()
			if err := scenario.Setup(root); err != nil {
				t.Fatal(err)
			}
			baseline, err := snapshotTree(root)
			if err != nil {
				t.Fatal(err)
			}
			if got := unintendedMutations(baseline, root, scenario.Expected); len(got) != 0 {
				t.Fatalf("unchanged fixture reported mutations: %v", got)
			}
			for name, content := range scenario.Expected {
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := unintendedMutations(baseline, root, scenario.Expected); len(got) != 0 {
				t.Fatalf("expected outcome reported unintended mutations: %v", got)
			}
			if err := os.WriteFile(filepath.Join(root, "rogue.txt"), []byte("unrelated\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := unintendedMutations(baseline, root, scenario.Expected); !reflect.DeepEqual(got, []string{"rogue.txt"}) {
				t.Fatalf("unintended mutations = %v", got)
			}
		})
	}
}

func TestAtomicEditAppliesAllOrNone(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "flags.conf")
	original := "alpha=false\nbeta=false\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := atomicEditTool(root)
	failed, err := tool.Call(context.Background(), map[string]any{
		"file_path": "flags.conf",
		"replacements": []any{
			map[string]any{"old": "alpha=false", "new": "alpha=true"},
			map[string]any{"old": "missing=false", "new": "missing=true"},
		},
	})
	if err != nil || !failed.IsError {
		t.Fatalf("failed atomic edit = %#v, %v", failed, err)
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Fatalf("failed mutation partially applied: %q", data)
	}
	passed, err := tool.Call(context.Background(), map[string]any{
		"file_path": "flags.conf",
		"replacements": []any{
			map[string]any{"old": "alpha=false", "new": "alpha=true"},
			map[string]any{"old": "beta=false", "new": "beta=true"},
		},
	})
	if err != nil || passed.IsError {
		t.Fatalf("successful atomic edit = %#v, %v", passed, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "alpha=true\nbeta=true\n" {
		t.Fatalf("atomic result = %q", data)
	}
}

func TestLongFailureFixtureUsesBoundedRealBashOutput(t *testing.T) {
	scenario, _ := scenarioByName("long-failing-test-output")
	root := t.TempDir()
	if err := scenario.Setup(root); err != nil {
		t.Fatal(err)
	}
	reg := tools.New()
	reg.Add(tools.Editing(&tools.Workspace{Root: root})...)
	call := func(command string) *gollama.ToolResult {
		return reg.Dispatch(context.Background(), gollama.ToolCall{
			ID: "test", Function: gollama.ToolCallFunction{Name: "Bash", Arguments: `{"command":` + quoteJSON(command) + `}`},
		})
	}
	failed := call("./check.sh")
	if !strings.Contains(failed.Content, "ERROR expected MODE=stable") || !strings.Contains(failed.Content, "[omitted") || !strings.Contains(failed.Content, "[exit:") {
		t.Fatalf("bounded failure omitted factual tail/truncation metadata: %s", failed.Content)
	}
	if err := os.WriteFile(filepath.Join(root, "config.env"), []byte("MODE=stable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	passed := call("./check.sh")
	if passed.IsError || !strings.Contains(passed.Content, "PASS") {
		t.Fatalf("verification result = %#v", passed)
	}
}

func quoteJSON(value string) string {
	// Commands used in this test contain no control characters; this keeps the
	// tool call itself readable while still producing a JSON string.
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func TestAggregateRetainsRepeatVariance(t *testing.T) {
	got := aggregate([]Result{
		{Scenario: "s", ModelName: "m", Variant: "a", Pass: true, ModelRoundTrips: 2, ElapsedMS: 10, ToolUsingTurns: 2, SingleCallToolTurns: 2},
		{Scenario: "s", ModelName: "m", Variant: "a", ModelRoundTrips: 6, ElapsedMS: 30, ToolUsingTurns: 2},
	})
	if len(got) != 1 || got[0].PassRate != .5 || got[0].MeanRoundTrips != 4 || got[0].MinRoundTrips != 2 || got[0].MaxRoundTrips != 6 || got[0].MeanSingleCallTurnRatio != .5 {
		t.Fatalf("aggregate = %+v", got)
	}
}

func toolCall(seq int, actor, name, id, args string) RunEvent {
	return RunEvent{Seq: seq, Actor: actor, Type: event.ToolCall, Data: map[string]any{"name": name, "id": id, "args": args}}
}

func toolResult(seq int, actor, name, id, result string, failed bool) RunEvent {
	return RunEvent{Seq: seq, Actor: actor, Type: event.ToolResult, Data: map[string]any{"name": name, "id": id, "result": result, "error": failed}}
}

func TestScenarioProcessOraclesRejectUnrelatedEvidence(t *testing.T) {
	t.Run("scoped search accepts rg and requires line", func(t *testing.T) {
		scenario, _ := scenarioByName("scoped-symbol-search")
		events := []RunEvent{toolCall(1, "coordinator", "Bash", "s", `{"command":"rg -n ComputeInvoice pkg"}`), toolResult(2, "coordinator", "Bash", "s", "pkg/billing.go:3:func ComputeInvoice", false)}
		if problems := scenario.CheckEvents(events, "ComputeInvoice is at pkg/billing.go:3", "direct"); len(problems) != 0 {
			t.Fatalf("valid rg evidence rejected: %v", problems)
		}
		if problems := scenario.CheckEvents(events, "ComputeInvoice is in pkg/billing.go", "direct"); len(problems) == 0 {
			t.Fatal("report without the factual line passed")
		}
	})

	t.Run("atomic call must contain multiple hunks", func(t *testing.T) {
		scenario, _ := scenarioByName("atomic-multi-hunk")
		one := []RunEvent{
			toolCall(1, "coordinator", "atomic_edit", "a", `{"file_path":"flags.conf","replacements":[{"old":"alpha=false\\nmiddle=keep\\nbeta=false","new":"alpha=true\\nmiddle=keep\\nbeta=true"}]}`),
			toolResult(2, "coordinator", "atomic_edit", "a", "ok", false),
		}
		if problems := scenario.CheckEvents(one, "", "direct"); len(problems) == 0 {
			t.Fatal("whole-file single replacement passed as multi-hunk")
		}
		two := []RunEvent{
			toolCall(1, "coordinator", "atomic_edit", "a", `{"file_path":"flags.conf","replacements":[{"old":"alpha=false","new":"alpha=true"},{"old":"beta=false","new":"beta=true"}]}`),
			toolResult(2, "coordinator", "atomic_edit", "a", "ok", false),
		}
		if problems := scenario.CheckEvents(two, "", "direct"); len(problems) != 0 {
			t.Fatalf("multi-hunk call rejected: %v", problems)
		}
	})

	t.Run("long output requires paired check calls in order", func(t *testing.T) {
		scenario, _ := scenarioByName("long-failing-test-output")
		events := []RunEvent{
			toolCall(1, "coordinator", "Bash", "f", `{"command":"./check.sh"}`),
			toolResult(2, "coordinator", "Bash", "f", "ERROR expected MODE=stable [exit: 1]", false),
			toolCall(3, "coordinator", "Bash", "p", `{"command":"echo PASS"}`),
			toolResult(4, "coordinator", "Bash", "p", "PASS", false),
		}
		if problems := scenario.CheckEvents(events, "", "direct"); len(problems) == 0 {
			t.Fatal("unrelated PASS command satisfied verification")
		}
		events[2] = toolCall(3, "coordinator", "Bash", "p", `{"command":"./check.sh"}`)
		if problems := scenario.CheckEvents(events, "", "direct"); len(problems) != 0 {
			t.Fatalf("paired check calls rejected: %v", problems)
		}
	})

	t.Run("investigation evidence must precede same-actor mutation", func(t *testing.T) {
		scenario, _ := scenarioByName("delegated-investigation-evidence")
		mutation := toolCall(1, "coordinator", "Edit", "e", `{"file_path":"release.conf","old_string":"pending","new_string":"cobalt"}`)
		mutationResult := toolResult(2, "coordinator", "Edit", "e", "ok", false)
		investigation := toolResult(3, "coordinator", "investigate", "i", "EVIDENCE channel=cobalt source=evidence.txt sha256=x", false)
		if problems := scenario.CheckEvents([]RunEvent{mutation, mutationResult, investigation}, "", "assisted"); len(problems) == 0 {
			t.Fatal("investigation after mutation passed")
		}
		mutation.Seq, mutationResult.Seq = 4, 5
		if problems := scenario.CheckEvents([]RunEvent{investigation, mutation, mutationResult}, "", "assisted"); len(problems) != 0 {
			t.Fatalf("evidence used after investigation was rejected: %v", problems)
		}
		mutation.Actor, mutationResult.Actor = "implementer", "implementer"
		if problems := scenario.CheckEvents([]RunEvent{investigation, mutation, mutationResult}, "", "assisted"); len(problems) == 0 {
			t.Fatal("different actor's mutation counted as evidence use")
		}
	})
}

func TestDirtyWorkOracleProtectsGitState(t *testing.T) {
	scenario, _ := scenarioByName("preserve-unrelated-dirty-work")
	setup := func(t *testing.T) (string, *gitSnapshot) {
		t.Helper()
		root := t.TempDir()
		if err := scenario.Setup(root); err != nil {
			t.Fatal(err)
		}
		before, err := snapshotGit(root)
		if err != nil {
			t.Fatal(err)
		}
		return root, before
	}
	runGit := func(t *testing.T, root string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	t.Run("intended worktree edit", func(t *testing.T) {
		root, before := setup(t)
		if err := os.WriteFile(filepath.Join(root, "app.go"), []byte(scenario.Expected["app.go"]), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := unintendedGitMutations(before, root, scenario.Expected); len(got) != 0 {
			t.Fatalf("intended edit rejected: %v", got)
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"stage unrelated draft", func(t *testing.T, root string) { runGit(t, root, "add", "notes.txt") }},
		{"commit unrelated draft", func(t *testing.T, root string) {
			runGit(t, root, "add", "notes.txt")
			runGit(t, root, "commit", "-qm", "bad")
		}},
		{"remove git metadata", func(t *testing.T, root string) {
			if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, before := setup(t)
			test.mutate(t, root)
			if got := unintendedGitMutations(before, root, scenario.Expected); len(got) == 0 {
				t.Fatal("git mutation was not detected")
			}
		})
	}
}

func TestBoundaryAndCompletionOutcomesAffectScore(t *testing.T) {
	t.Run("mutation before handoff", func(t *testing.T) {
		scenario, _ := scenarioByName("interrupt-rollover-recovery")
		root := t.TempDir()
		if err := scenario.Setup(root); err != nil {
			t.Fatal(err)
		}
		baseline, err := snapshotTree(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "state.txt"), []byte(scenario.Expected["state.txt"]), 0o644); err != nil {
			t.Fatal(err)
		}
		events := []RunEvent{toolResult(1, "coordinator", "handoff", "h", "ok", false), toolResult(2, "coordinator", "finish", "f", "ok", false)}
		got := finishResult(Result{Strategy: "direct", Interrupted: true}, scenario, baseline, nil, root, events, runEvidence{HandoffObserved: true, HandoffPreservedBaseline: false}, time.Now())
		if got.ProcessCorrect {
			t.Fatal("mutation before handoff passed")
		}
		got = finishResult(Result{Strategy: "direct", Interrupted: true}, scenario, baseline, nil, root, events, runEvidence{HandoffObserved: true, HandoffPreservedBaseline: true}, time.Now())
		if !got.ProcessCorrect {
			t.Fatalf("preserved handoff boundary rejected: %v", got.Problems)
		}
	})

	t.Run("no-op child followed by parent completion", func(t *testing.T) {
		root := t.TempDir()
		scenario := Scenario{Expected: map[string]string{"x.txt": "done\n"}, Setup: func(root string) error { return writeFiles(root, map[string]string{"x.txt": "todo\n"}) }}
		if err := scenario.Setup(root); err != nil {
			t.Fatal(err)
		}
		baseline, err := snapshotTree(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("done\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		events := []RunEvent{toolResult(1, "implementer", "finish", "cf", "ok", false), toolResult(2, "coordinator", "delegate_implementation", "d", "ok", false), toolResult(3, "coordinator", "finish", "pf", "ok", false)}
		got := finishResult(Result{Strategy: "delegated-implementation"}, scenario, baseline, nil, root, events, runEvidence{DelegateCalls: 1, DelegateFinished: true}, time.Now())
		if got.ProcessCorrect {
			t.Fatal("parent mutation after no-op child passed as delegated implementation")
		}
	})

	t.Run("blocked outcome records intervention", func(t *testing.T) {
		var got Result
		applyLoopOutcome(&got, &engine.Result{Blocked: true, Report: "choose a format"}, nil)
		if got.RunError == "" || got.Escalations != 1 || got.HumanInterventions != 1 {
			t.Fatalf("blocked outcome = %+v", got)
		}
	})
}

func TestCheckedRunCountAndRawEventVolumes(t *testing.T) {
	if _, ok := checkedRunCount(100, 7, 2, 1, int(^uint(0)>>1)); ok {
		t.Fatal("overflow-sized repeat count passed paid-run ceiling")
	}
	if got, ok := checkedRunCount(100, 7, 2, 2, 2); !ok || got != 56 {
		t.Fatalf("checked count = %d, %t", got, ok)
	}
	result := Result{}
	measureEvents(&result, []event.Event{
		{Type: event.ModelTurn, Data: map[string]any{"text": nil}},
		{Type: event.ToolCall, Data: map[string]any{"args": `{"x":1}`}},
		{Type: event.ToolResult, Data: map[string]any{"result": nil}},
	})
	if result.ModelOutputBytes != 0 || result.ToolInputBytes != len(`{"x":1}`) || result.ToolOutputBytes != 0 {
		t.Fatalf("event byte volumes = model %d, tool in %d, tool out %d", result.ModelOutputBytes, result.ToolInputBytes, result.ToolOutputBytes)
	}
}
