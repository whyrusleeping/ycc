// Package modeleval runs opt-in behavioral model/tool evaluations in disposable
// repositories. It is intentionally separate from production prompts and session
// analytics.
package modeleval

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

const (
	HarnessVersion = "model-ux-eval/v2"
	ToolVersion    = "production-editing+eval-boundaries/v2"
)

// RunEvent is the durable process evidence captured from the real engine loop.
type RunEvent = event.Event

// Variant describes an isolated prompt/tool/implementation choice. None of
// these strings modify production guidance.
type Variant struct {
	Name               string `json:"name"`
	Version            string `json:"version"`
	Strategy           string `json:"strategy"`
	Guidance           string `json:"-"`
	DelegationTool     bool   `json:"delegation_tool"`
	ImplementationTool bool   `json:"implementation_tool"`
}

// Variants returns the built-in A/B choices.
func Variants() []Variant {
	return []Variant{
		{
			Name: "baseline", Version: "1", Strategy: "assisted", DelegationTool: true,
			Guidance: "Work carefully in the disposable repository. Inspect relevant files before changing them. Use the provided tools, batch independent calls when useful, recover from tool failures, preserve unrelated work, verify the factual outcome, and call finish with a concise report. Delegation is available for investigation but is not mandatory outside tasks that request it.",
		},
		{
			Name: "concise", Version: "1", Strategy: "assisted", DelegationTool: true,
			Guidance: "Inspect, make only the requested change, verify repository facts, preserve unrelated work, and finish. Batch independent calls when useful; use investigation delegation when the task benefits from it.",
		},
		{
			Name: "batched", Version: "1", Strategy: "assisted", DelegationTool: true,
			Guidance: "Solve and verify the task without unrelated changes. Put independent tool calls in one model turn when that reduces round trips; do not batch dependent calls. Delegate investigation when useful, then finish.",
		},
		{
			Name: "direct", Version: "1", Strategy: "direct",
			Guidance: "Implement this small task directly. Inspect, make only the requested change, verify repository facts, preserve unrelated work, and finish. Batch independent calls when useful.",
		},
		{
			Name: "delegated", Version: "1", Strategy: "delegated-implementation", DelegationTool: true, ImplementationTool: true,
			Guidance: "For a task that changes files, call delegate_implementation with the complete scoped task, then inspect and verify its repository result yourself. Preserve unrelated work and finish. Use read-only investigation delegation when useful.",
		},
	}
}

// Options bounds one live suite invocation.
type Options struct {
	Registry          *config.Registry
	Models            []string
	VariantNames      []string
	ScenarioNames     []string
	Repeats           int
	Timeout           time.Duration
	MaxTurns          int
	MaxRuns           int
	ConfigFingerprint string
}

// Report is stable JSON output suitable for paired A/B analysis.
type Report struct {
	HarnessVersion    string      `json:"harness_version"`
	ToolVersion       string      `json:"tool_version"`
	ConfigFingerprint string      `json:"config_fingerprint"`
	Executable        Fingerprint `json:"executable"`
	Settings          RunSettings `json:"settings"`
	StartedAt         time.Time   `json:"started_at"`
	Runs              []Result    `json:"runs"`
	Comparisons       []Aggregate `json:"comparisons"`
}

type RunSettings struct {
	Models        []string `json:"models"`
	Variants      []string `json:"variants"`
	Scenarios     []string `json:"scenarios"`
	Repeats       int      `json:"repeats"`
	TimeoutMillis int64    `json:"timeout_ms"`
	MaxTurns      int      `json:"max_turns"`
	MaxRuns       int      `json:"max_runs"`
}

type Fingerprint struct {
	GoVersion string `json:"go_version"`
	Module    string `json:"module,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Dirty     bool   `json:"dirty"`
}

// Result records correctness separately from efficiency and intervention
// metrics so a fast wrong run cannot look successful.
type Result struct {
	Scenario             string      `json:"scenario"`
	ScenarioVersion      string      `json:"scenario_version"`
	FixtureFingerprint   string      `json:"fixture_fingerprint"`
	ModelName            string      `json:"model_name"`
	Backend              string      `json:"backend"`
	ModelID              string      `json:"model_id"`
	Variant              string      `json:"variant"`
	VariantVersion       string      `json:"variant_version"`
	Strategy             string      `json:"strategy"`
	ToolsetVersion       string      `json:"toolset_version"`
	PromptFingerprint    string      `json:"prompt_fingerprint"`
	Repeat               int         `json:"repeat"`
	Pass                 bool        `json:"pass"`
	RepositoryCorrect    bool        `json:"repository_correct"`
	ProcessCorrect       bool        `json:"process_correct"`
	Problems             []string    `json:"problems,omitempty"`
	UnintendedMutations  []string    `json:"unintended_mutations,omitempty"`
	RecoveryAttempts     int         `json:"recovery_attempts"`
	ModelRoundTrips      int         `json:"model_round_trips"`
	ModelAPIAttempts     int         `json:"model_api_attempts"`
	ToolCalls            int         `json:"tool_calls"`
	ToolUsingTurns       int         `json:"tool_using_turns"`
	SingleCallToolTurns  int         `json:"single_call_tool_turns"`
	MultiCallToolTurns   int         `json:"multi_call_tool_turns"`
	Usage                event.Usage `json:"usage"`
	PromptBytes          int         `json:"prompt_bytes"`
	ModelOutputBytes     int         `json:"model_output_bytes"`
	ToolInputBytes       int         `json:"tool_input_bytes"`
	ToolOutputBytes      int         `json:"tool_output_bytes"`
	ElapsedMS            int64       `json:"elapsed_ms"`
	HumanInterventions   int         `json:"human_interventions"`
	Escalations          int         `json:"escalations"`
	HarnessInterventions int         `json:"harness_interventions"`
	Interrupted          bool        `json:"interrupted"`
	RunError             string      `json:"run_error,omitempty"`
	FinalReport          string      `json:"final_report,omitempty"`
}

// Aggregate groups repeated observations without hiding variance.
type Aggregate struct {
	Scenario                string  `json:"scenario"`
	Model                   string  `json:"model"`
	Variant                 string  `json:"variant"`
	Runs                    int     `json:"runs"`
	Passes                  int     `json:"passes"`
	PassRate                float64 `json:"pass_rate"`
	MeanRoundTrips          float64 `json:"mean_round_trips"`
	MinRoundTrips           int     `json:"min_round_trips"`
	MaxRoundTrips           int     `json:"max_round_trips"`
	MeanElapsedMS           float64 `json:"mean_elapsed_ms"`
	MinElapsedMS            int64   `json:"min_elapsed_ms"`
	MaxElapsedMS            int64   `json:"max_elapsed_ms"`
	MeanInputTokens         float64 `json:"mean_input_tokens"`
	MeanOutputTokens        float64 `json:"mean_output_tokens"`
	MeanToolInputBytes      float64 `json:"mean_tool_input_bytes"`
	MeanToolOutputBytes     float64 `json:"mean_tool_output_bytes"`
	MeanRecoveryAttempts    float64 `json:"mean_recovery_attempts"`
	MeanHumanInterventions  float64 `json:"mean_human_interventions"`
	MeanSingleCallTurnRatio float64 `json:"mean_single_call_tool_turn_ratio"`
}

func checkedRunCount(limit int, factors ...int) (int, bool) {
	if limit <= 0 {
		return 0, false
	}
	planned := 1
	for _, factor := range factors {
		if factor <= 0 || planned > limit/factor {
			return 0, false
		}
		planned *= factor
	}
	return planned, true
}

// Run executes the configured live matrix sequentially. Sequential ordering
// makes the paid-call ceiling obvious and avoids provider concurrency surprises.
func Run(ctx context.Context, opts Options) (Report, error) {
	if opts.Registry == nil {
		return Report{}, fmt.Errorf("model registry is required")
	}
	if len(opts.Models) == 0 {
		return Report{}, fmt.Errorf("at least one model is required")
	}
	if opts.Repeats < 2 {
		return Report{}, fmt.Errorf("repeats must be at least 2 to expose variance")
	}
	if opts.MaxRuns <= 0 || opts.MaxRuns > 100 {
		return Report{}, fmt.Errorf("max runs must be in [1,100]")
	}
	if opts.Timeout <= 0 || opts.Timeout > 10*time.Minute {
		return Report{}, fmt.Errorf("timeout must be in (0, 10m]")
	}
	if opts.MaxTurns <= 0 || opts.MaxTurns > 20 {
		return Report{}, fmt.Errorf("max turns must be in [1,20]")
	}

	scenarios, err := selectScenarios(opts.ScenarioNames)
	if err != nil {
		return Report{}, err
	}
	variants, err := selectVariants(opts.VariantNames)
	if err != nil {
		return Report{}, err
	}
	if planned, ok := checkedRunCount(opts.MaxRuns, len(scenarios), len(variants), len(opts.Models), opts.Repeats); !ok {
		return Report{}, fmt.Errorf("planned matrix exceeds max runs %d", opts.MaxRuns)
	} else if planned == 0 {
		return Report{}, fmt.Errorf("planned matrix is empty")
	}
	variantLabels := make([]string, len(variants))
	for i, variant := range variants {
		variantLabels[i] = variant.Name + "@" + variant.Version
	}
	scenarioLabels := make([]string, len(scenarios))
	for i, scenario := range scenarios {
		scenarioLabels[i] = scenario.Name + "@" + scenario.Version
	}
	report := Report{
		HarnessVersion: HarnessVersion, ToolVersion: ToolVersion,
		ConfigFingerprint: opts.ConfigFingerprint, Executable: executableFingerprint(),
		Settings: RunSettings{
			Models: append([]string(nil), opts.Models...), Variants: variantLabels, Scenarios: scenarioLabels,
			Repeats: opts.Repeats, TimeoutMillis: opts.Timeout.Milliseconds(), MaxTurns: opts.MaxTurns, MaxRuns: opts.MaxRuns,
		},
		StartedAt: time.Now().UTC(),
	}
	for _, scenario := range scenarios {
		for _, model := range opts.Models {
			for _, variant := range variants {
				for repeat := 1; repeat <= opts.Repeats; repeat++ {
					if err := ctx.Err(); err != nil {
						report.Comparisons = aggregate(report.Runs)
						return report, err
					}
					result := runOne(ctx, opts, scenario, model, variant, repeat)
					report.Runs = append(report.Runs, result)
					if err := ctx.Err(); err != nil {
						report.Comparisons = aggregate(report.Runs)
						return report, err
					}
				}
			}
		}
	}
	report.Comparisons = aggregate(report.Runs)
	return report, nil
}

type runEvidence struct {
	HandoffObserved          bool
	HandoffPreservedBaseline bool
	HandoffObservationError  string
	DelegateCalls            int
	DelegateAppliedExpected  bool
	DelegateFinished         bool
}

func runOne(parent context.Context, opts Options, scenario Scenario, modelName string, variant Variant, repeat int) Result {
	started := time.Now()
	toolsetVersion := ToolVersion
	if variant.DelegationTool {
		toolsetVersion += "+investigate/v1"
	}
	if variant.ImplementationTool {
		toolsetVersion += "+delegate-implementation/v1"
	}
	result := Result{
		Scenario: scenario.Name, ScenarioVersion: scenario.Version,
		ModelName: modelName, Variant: variant.Name, VariantVersion: variant.Version,
		Strategy: variant.Strategy, ToolsetVersion: toolsetVersion, Repeat: repeat,
		HumanInterventions: 0,
	}
	root, err := os.MkdirTemp("", "ycc-model-eval-"+scenario.Name+"-")
	if err != nil {
		result.RunError = err.Error()
		return result
	}
	defer os.RemoveAll(root)
	if err := scenario.Setup(root); err != nil {
		result.RunError = "setup: " + err.Error()
		return result
	}
	baseline, err := snapshotTree(root)
	if err != nil {
		result.RunError = "baseline: " + err.Error()
		return result
	}
	var gitBaseline *gitSnapshot
	if _, statErr := os.Stat(filepath.Join(root, ".git")); statErr == nil {
		gitBaseline, err = snapshotGit(root)
		if err != nil {
			result.RunError = "git baseline: " + err.Error()
			return result
		}
	}
	result.FixtureFingerprint = scenarioFingerprint(scenario, baseline)
	result.PromptFingerprint = hashText(variant.Guidance + "\x00" + scenario.Prompt + "\x00" + scenario.Resume)
	result.PromptBytes = len(variant.Guidance) + len(scenario.Prompt) + len(scenario.Resume)

	modelCfg, ok := opts.Registry.GetModel(modelName)
	if !ok {
		result.RunError = fmt.Sprintf("unknown model %q", modelName)
		return result
	}
	result.Backend, result.ModelID = modelCfg.Backend, modelCfg.Model

	var collector eventCollector
	var evidence runEvidence
	runCtx, cancel := context.WithTimeout(parent, opts.Timeout)
	defer cancel()
	var interruptCancel context.CancelFunc
	recorder := &evalRecorder{collector: &collector}
	recorder.onEvent = func(ev event.Event) {
		if scenario.Resume != "" && ev.Type == event.ToolResult && fmt.Sprint(ev.Data["name"]) == "handoff" && !asBool(ev.Data["error"]) && interruptCancel != nil {
			evidence.HandoffObserved = true
			atHandoff, snapshotErr := snapshotTree(root)
			if snapshotErr != nil {
				evidence.HandoffObservationError = snapshotErr.Error()
			} else {
				evidence.HandoffPreservedBaseline = len(changedPaths(baseline, atHandoff, nil)) == 0
			}
			interruptCancel()
		}
	}
	emitter := event.NewEmitter(recorder, "coordinator")
	reg := tools.New()
	ws := &tools.Workspace{Root: root}
	reg.Add(tools.Worker(ws)...)
	reg.Add(atomicEditTool(root), handoffTool())
	if variant.DelegationTool {
		reg.Add(investigateTool(root))
	}

	var attempts apiCounter
	if variant.ImplementationTool {
		reg.Add(delegateImplementationTool(func(ctx context.Context, task string) (string, error) {
			evidence.DelegateCalls++
			beforeChild, err := snapshotTree(root)
			if err != nil {
				return "", fmt.Errorf("snapshot before delegated implementation: %w", err)
			}
			childEventStart := len(collector.snapshot())
			client, modelID, err := opts.Registry.BuildContext(ctx, modelName)
			if err != nil {
				return "", err
			}
			childTools := tools.New()
			childTools.Add(tools.Worker(ws)...)
			childTools.Add(atomicEditTool(root))
			if variant.DelegationTool {
				childTools.Add(investigateTool(root))
			}
			thinking := opts.Registry.ThinkingFor(modelName)
			child := &engine.Loop{
				Client: &countingTurner{inner: client, attempts: &attempts}, Model: modelID, ModelName: modelName, Backend: modelCfg.Backend,
				System: "You are a delegated implementer in a disposable repository. Complete only the scoped task, preserve unrelated work, verify facts, and call finish.",
				Tools:  childTools, Emitter: emitter.With("implementer"),
				MaxTurns: opts.MaxTurns, MaxTok: opts.Registry.MaxTokens(),
				Thinking: thinking.Thinking, Effort: thinking.Effort, ThinkingDisplay: thinking.ThinkingDisplay,
				Retry: opts.Registry.RetryPolicy(),
			}
			child.Seed(task)
			outcome, err := child.Run(ctx)
			if err != nil {
				return "", err
			}
			if outcome == nil {
				return "", fmt.Errorf("delegated implementer returned no outcome")
			}
			if outcome.Blocked {
				return "", fmt.Errorf("delegated implementer blocked: %s", outcome.Report)
			}
			childEvents := collector.snapshot()
			if childEventStart < len(childEvents) && hasSuccessfulToolByActor(childEvents[childEventStart:], "finish", "implementer") {
				evidence.DelegateFinished = true
			} else {
				return "", fmt.Errorf("delegated implementer did not complete through finish")
			}
			afterChild, err := snapshotTree(root)
			if err != nil {
				return "", fmt.Errorf("snapshot after delegated implementation: %w", err)
			}
			if expectedAppliedDuringDelegate(baseline, beforeChild, afterChild, scenario.Expected) {
				evidence.DelegateAppliedExpected = true
			}
			return outcome.Report, nil
		}))
	}
	buildLoop := func(ctx context.Context) (*engine.Loop, error) {
		client, modelID, err := opts.Registry.BuildContext(ctx, modelName)
		if err != nil {
			return nil, err
		}
		thinking := opts.Registry.ThinkingFor(modelName)
		return &engine.Loop{
			Client: &countingTurner{inner: client, attempts: &attempts}, Model: modelID, ModelName: modelName, Backend: modelCfg.Backend,
			System: variant.Guidance, Tools: reg, Emitter: emitter,
			MaxTurns: opts.MaxTurns, MaxTok: opts.Registry.MaxTokens(),
			Thinking: thinking.Thinking, Effort: thinking.Effort, ThinkingDisplay: thinking.ThinkingDisplay,
			Retry: opts.Registry.RetryPolicy(),
		}, nil
	}

	loop, err := buildLoop(runCtx)
	if err != nil {
		result.RunError = "build model: " + err.Error()
		return finishResult(result, scenario, baseline, gitBaseline, root, collector.snapshot(), evidence, started)
	}
	emitter.EmitAs("user", event.UserInput, map[string]any{"text": scenario.Prompt})
	loop.Seed(scenario.Prompt)
	firstCtx := runCtx
	if scenario.Resume != "" {
		var phaseCancel context.CancelFunc
		firstCtx, phaseCancel = context.WithCancel(runCtx)
		interruptCancel = phaseCancel
	}
	loopResult, runErr := loop.Run(firstCtx)
	if loopResult != nil {
		result.FinalReport = loopResult.Report
	}

	if scenario.Resume != "" && firstCtx.Err() == context.Canceled && runCtx.Err() == nil {
		result.Interrupted = true
		result.HarnessInterventions++
		events := collector.snapshot()
		rolled, buildErr := buildLoop(runCtx)
		if buildErr != nil {
			runErr = fmt.Errorf("build rollover model: %w", buildErr)
		} else {
			rolled.SetHistory(engine.ReplayHistory(events))
			emitter.EmitAs("user", event.UserInput, map[string]any{"text": scenario.Resume})
			rolled.Seed(scenario.Resume)
			loopResult, runErr = rolled.Run(runCtx)
			if loopResult != nil {
				result.FinalReport = loopResult.Report
			}
		}
	}
	applyLoopOutcome(&result, loopResult, runErr)
	result.ModelAPIAttempts = attempts.value()
	return finishResult(result, scenario, baseline, gitBaseline, root, collector.snapshot(), evidence, started)
}

func expectedAppliedDuringDelegate(baseline, before, after treeSnapshot, expected map[string]string) bool {
	if len(expected) == 0 {
		return false
	}
	changed := false
	for name, content := range expected {
		path := filepath.ToSlash(filepath.Clean(name))
		baselineState, baselineExists := baseline[path]
		beforeState, beforeExists := before[path]
		if baselineExists != beforeExists || (baselineExists && baselineState != beforeState) {
			return false
		}
		afterState, exists := after[path]
		if !exists || afterState.Digest != sha256.Sum256([]byte(content)) {
			return false
		}
		if !beforeExists || beforeState != afterState {
			changed = true
		}
	}
	return changed
}

func applyLoopOutcome(result *Result, outcome *engine.Result, runErr error) {
	if runErr != nil {
		result.RunError = runErr.Error()
		return
	}
	if outcome == nil {
		result.RunError = "model loop returned no outcome"
		return
	}
	if outcome.Blocked {
		recordEscalation(result, outcome.Report)
	}
}

func recordEscalation(result *Result, reason string) {
	result.Escalations++
	result.HumanInterventions++
	result.RunError = "model blocked and requested human intervention"
	if strings.TrimSpace(reason) != "" {
		result.RunError += ": " + reason
	}
}

func finishResult(result Result, scenario Scenario, baseline treeSnapshot, gitBaseline *gitSnapshot, root string, events []event.Event, evidence runEvidence, started time.Time) Result {
	result.ElapsedMS = time.Since(started).Milliseconds()
	result.UnintendedMutations = unintendedMutations(baseline, root, scenario.Expected)
	result.UnintendedMutations = append(result.UnintendedMutations, unintendedGitMutations(gitBaseline, root, scenario.Expected)...)
	sort.Strings(result.UnintendedMutations)
	result.RepositoryCorrect = len(result.UnintendedMutations) == 0
	var repositoryProblems []string
	for name, want := range scenario.Expected {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			result.RepositoryCorrect = false
			repositoryProblems = append(repositoryProblems, fmt.Sprintf("%s does not have the expected content", name))
		}
	}
	var processProblems []string
	if scenario.CheckEvents != nil {
		processProblems = append(processProblems, scenario.CheckEvents(events, result.FinalReport, result.Strategy)...)
	}
	if !hasSuccessfulToolByActor(events, "finish", "coordinator") {
		processProblems = append(processProblems, "coordinator did not complete through finish")
	}
	if scenario.Resume != "" {
		if !result.Interrupted || !evidence.HandoffObserved {
			processProblems = append(processProblems, "intentional interruption and replay rollover did not occur")
		} else if evidence.HandoffObservationError != "" {
			processProblems = append(processProblems, "could not observe repository at handoff: "+evidence.HandoffObservationError)
		} else if !evidence.HandoffPreservedBaseline {
			processProblems = append(processProblems, "repository was mutated before the handoff boundary")
		}
	}
	if result.Strategy == "delegated-implementation" && len(scenario.Expected) > 0 {
		if evidence.DelegateCalls == 0 || !hasTool(events, "delegate_implementation", false) || !evidence.DelegateFinished {
			processProblems = append(processProblems, "delegated implementation did not complete through a finished child")
		}
		if !evidence.DelegateAppliedExpected {
			processProblems = append(processProblems, "expected repository mutation was not applied inside the delegated child boundary")
		}
	}
	result.ProcessCorrect = len(processProblems) == 0
	result.Problems = append(result.Problems, repositoryProblems...)
	result.Problems = append(result.Problems, processProblems...)
	result.Pass = result.RepositoryCorrect && result.ProcessCorrect && result.RunError == ""
	measureEvents(&result, events)
	return result
}

func measureEvents(result *Result, events []event.Event) {
	for _, ev := range events {
		switch ev.Type {
		case event.ModelTurn:
			result.ModelRoundTrips++
			calls := asInt(ev.Data["tool_calls"])
			if calls > 0 {
				result.ToolUsingTurns++
				if calls == 1 {
					result.SingleCallToolTurns++
				} else {
					result.MultiCallToolTurns++
				}
			}
			if text, ok := ev.Data["text"].(string); ok {
				result.ModelOutputBytes += len(text)
			}
			addUsage(&result.Usage, ev.Data["usage"])
		case event.Retry:
			result.RecoveryAttempts++
		case event.ToolCall:
			result.ToolCalls++
			if args, ok := ev.Data["args"].(string); ok {
				result.ToolInputBytes += len(args)
			}
		case event.ToolResult:
			toolOutput, _ := ev.Data["result"].(string)
			result.ToolOutputBytes += len(toolOutput)
			failedCommand := fmt.Sprint(ev.Data["name"]) == "Bash" && (strings.Contains(toolOutput, "[exit:") || strings.Contains(toolOutput, "[command timed out"))
			if asBool(ev.Data["error"]) || failedCommand {
				result.RecoveryAttempts++
			}
		}
	}
	if result.Interrupted {
		result.RecoveryAttempts++
	}
}

func addUsage(dst *event.Usage, value any) {
	switch u := value.(type) {
	case event.Usage:
		dst.Input += u.Input
		dst.Output += u.Output
		dst.CacheRead += u.CacheRead
		dst.CacheWrite += u.CacheWrite
		dst.Total += u.Total
		dst.ReasoningTokens += u.ReasoningTokens
	case *event.Usage:
		if u != nil {
			addUsage(dst, *u)
		}
	case map[string]any:
		dst.Input += asInt(u["input"])
		dst.Output += asInt(u["output"])
		dst.CacheRead += asInt(u["cache_read"])
		dst.CacheWrite += asInt(u["cache_write"])
		dst.Total += asInt(u["total"])
		dst.ReasoningTokens += asInt(u["reasoning_tokens"])
	}
}

func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func asBool(v any) bool { b, _ := v.(bool); return b }

type eventCollector struct {
	mu     sync.Mutex
	events []event.Event
}

func (c *eventCollector) add(ev event.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *eventCollector) snapshot() []event.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]event.Event(nil), c.events...)
}

type apiCounter struct {
	mu sync.Mutex
	n  int
}

func (c *apiCounter) increment() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *apiCounter) value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// countingTurner preserves streaming behavior while counting actual provider
// attempts, including final failures and context-cancelled requests that do not
// produce a model_turn event.
type countingTurner struct {
	inner    engine.Turner
	attempts *apiCounter
}

func (t *countingTurner) TurnCtx(ctx context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.attempts.increment()
	return t.inner.TurnCtx(ctx, opts)
}

func (t *countingTurner) TurnStreamCtx(ctx context.Context, opts gollama.RequestOptions, onDelta func(string)) (*gollama.ResponseMessageGenerate, error) {
	t.attempts.increment()
	if streaming, ok := t.inner.(engine.StreamTurner); ok {
		return streaming.TurnStreamCtx(ctx, opts, onDelta)
	}
	return t.inner.TurnCtx(ctx, opts)
}

func (t *countingTurner) ReasoningTokens() int {
	if reporter, ok := t.inner.(engine.ReasoningTokenReporter); ok {
		return reporter.ReasoningTokens()
	}
	return 0
}

// evalRecorder captures durable events plus retry broadcasts. Turn deltas are
// intentionally discarded: they are UI snapshots rather than task evidence and
// can dwarf the bounded evaluation report.
type evalRecorder struct {
	mu        sync.Mutex
	seq       int
	collector *eventCollector
	onEvent   func(event.Event)
}

func (r *evalRecorder) Record(actor string, typ event.Type, data map[string]any) event.Event {
	r.mu.Lock()
	r.seq++
	ev := event.Event{Seq: r.seq, TS: time.Now(), Actor: actor, Type: typ, Data: data}
	r.mu.Unlock()
	r.collector.add(ev)
	if r.onEvent != nil {
		r.onEvent(ev)
	}
	return ev
}

func (r *evalRecorder) Broadcast(actor string, typ event.Type, data map[string]any) event.Event {
	ev := event.Event{TS: time.Now(), Actor: actor, Type: typ, Data: data, Transient: true}
	if typ == event.Retry {
		r.collector.add(ev)
	}
	return ev
}

func selectScenarios(names []string) ([]Scenario, error) {
	if len(names) == 0 {
		return Scenarios(), nil
	}
	var out []Scenario
	for _, name := range names {
		s, ok := scenarioByName(name)
		if !ok {
			return nil, fmt.Errorf("unknown scenario %q", name)
		}
		out = append(out, s)
	}
	return out, nil
}

func selectVariants(names []string) ([]Variant, error) {
	all := Variants()
	if len(names) == 0 {
		return []Variant{all[0], all[1]}, nil
	}
	byName := make(map[string]Variant, len(all))
	for _, v := range all {
		byName[v.Name] = v
	}
	var out []Variant
	for _, name := range names {
		v, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown variant %q", name)
		}
		out = append(out, v)
	}
	return out, nil
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("sha256:%x", sum)
}

func ConfigFingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hashText(string(data)), nil
}

func executableFingerprint() Fingerprint {
	f := Fingerprint{GoVersion: runtime.Version()}
	if info, ok := debug.ReadBuildInfo(); ok {
		f.Module = info.Main.Path + "@" + info.Main.Version
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				f.Revision = setting.Value
			case "vcs.modified":
				f.Dirty = setting.Value == "true"
			}
		}
	}
	if f.Revision == "" {
		cmd := exec.Command("git", "rev-parse", "HEAD")
		if out, err := cmd.Output(); err == nil {
			f.Revision = strings.TrimSpace(string(out))
		}
	}
	return f
}

// Encode writes the report as indented JSON.
func Encode(w io.Writer, report Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func aggregate(runs []Result) []Aggregate {
	type key struct{ scenario, model, variant string }
	groups := map[key][]Result{}
	for _, run := range runs {
		k := key{run.Scenario, run.ModelName, run.Variant}
		groups[k] = append(groups[k], run)
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].scenario != keys[j].scenario {
			return keys[i].scenario < keys[j].scenario
		}
		if keys[i].model != keys[j].model {
			return keys[i].model < keys[j].model
		}
		return keys[i].variant < keys[j].variant
	})
	out := make([]Aggregate, 0, len(keys))
	for _, k := range keys {
		group := groups[k]
		a := Aggregate{Scenario: k.scenario, Model: k.model, Variant: k.variant, Runs: len(group), MinRoundTrips: group[0].ModelRoundTrips, MaxRoundTrips: group[0].ModelRoundTrips, MinElapsedMS: group[0].ElapsedMS, MaxElapsedMS: group[0].ElapsedMS}
		var roundTrips, input, output, toolIn, toolOut, recovery, human int
		var elapsed int64
		var ratios float64
		for _, r := range group {
			if r.Pass {
				a.Passes++
			}
			roundTrips += r.ModelRoundTrips
			elapsed += r.ElapsedMS
			input += r.Usage.Input + r.Usage.CacheRead + r.Usage.CacheWrite
			output += r.Usage.Output
			toolIn += r.ToolInputBytes
			toolOut += r.ToolOutputBytes
			recovery += r.RecoveryAttempts
			human += r.HumanInterventions
			if r.ToolUsingTurns > 0 {
				ratios += float64(r.SingleCallToolTurns) / float64(r.ToolUsingTurns)
			}
			if r.ModelRoundTrips < a.MinRoundTrips {
				a.MinRoundTrips = r.ModelRoundTrips
			}
			if r.ModelRoundTrips > a.MaxRoundTrips {
				a.MaxRoundTrips = r.ModelRoundTrips
			}
			if r.ElapsedMS < a.MinElapsedMS {
				a.MinElapsedMS = r.ElapsedMS
			}
			if r.ElapsedMS > a.MaxElapsedMS {
				a.MaxElapsedMS = r.ElapsedMS
			}
		}
		n := float64(len(group))
		a.PassRate = float64(a.Passes) / n
		a.MeanRoundTrips = float64(roundTrips) / n
		a.MeanElapsedMS = float64(elapsed) / n
		a.MeanInputTokens = float64(input) / n
		a.MeanOutputTokens = float64(output) / n
		a.MeanToolInputBytes = float64(toolIn) / n
		a.MeanToolOutputBytes = float64(toolOut) / n
		a.MeanRecoveryAttempts = float64(recovery) / n
		a.MeanHumanInterventions = float64(human) / n
		a.MeanSingleCallTurnRatio = ratios / n
		out = append(out, a)
	}
	return out
}
