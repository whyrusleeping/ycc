# Behavioral model/tool evaluations

`ycc-model-eval` is an opt-in live-model harness for comparing prompt, tool, and implementation-strategy variants against repository facts. It does not change production prompts. The initial suite uses seven small disposable repositories:

- scoped symbol search;
- recovery from a failed exact edit;
- one atomic multi-hunk mutation;
- diagnosis from bounded long failing-test output;
- preservation of unrelated dirty work;
- intentional interruption followed by event replay and recovery; and
- direct or delegated investigation with use of source-attributed evidence.

List the catalog without credentials or model calls:

```sh
go run ./cmd/ycc-model-eval -list
```

## Live A/B runs

Live execution requires all of `-live`, an explicit config, named logical models, and an acknowledged run ceiling. At least two repeats are required. For one model, all seven scenarios, and the default baseline/concise pair, the matrix has 28 runs:

```sh
go run ./cmd/ycc-model-eval \
  -live \
  -config "$HOME/.config/ycc/ycc.toml" \
  -models claude \
  -variants baseline,concise \
  -repeats 2 \
  -max-runs 28 \
  -timeout 2m \
  -max-turns 12 \
  -output /tmp/model-eval.json
```

`-max-runs` must cover the full selected matrix and cannot exceed 100; per-run timeout cannot exceed ten minutes and each loop cannot exceed twenty turns. Runs are sequential. Selecting two models in the example requires `-max-runs 56`. Use `-scenarios name,...` for a cheaper focused comparison. `batched` isolates stronger batching guidance. Compare `direct` with `delegated` to run small mutations in the parent versus a real same-model implementer loop; delegated runs can consume additional model calls within the same per-run wall timeout. The baseline, concise, and batched variants expose only the read-only investigation delegate. No variant makes delegation a production default, and the scenario oracles generally accept any command syntax that establishes the required facts.

Every run starts from a fresh temporary directory, uses the production engine loop and editing tools, and is deleted afterward. The report records:

- logical model, backend, model ID, config hash, executable VCS/build fingerprint, harness/tool/scenario/prompt versions, limits, and repeat number;
- exact expected-file checks and a whole-tree before/after manifest check for unintended mutations; for the dirty-work fixture, separate HEAD, index, staged-diff, and porcelain-status oracles protect repository state while ignoring incidental `.git` storage details;
- required process evidence from engine tool-call/result events, including repository state at the injected interruption/replay boundary and mutation attribution inside the delegated implementer boundary;
- model/API attempts, recovery attempts, model round trips, single-call versus multi-call tool-using turns, token classes, prompt/model/tool byte volumes, elapsed time, and human, escalation, and harness intervention counts; and
- grouped pass rate, means, and min/max observations for repeated model/variant comparisons.

A nonzero `run_error` fails the run even when repository state happens to match. A blocked coordinator outcome is a failed run and records an escalation plus required human intervention; the deliberate rollover is reported separately as a harness intervention. Tool failures and nonzero/timed-out Bash results count as recovery attempts. If cancellation stops a matrix, the CLI writes the accumulated partial report before exiting nonzero.

## Interpreting evidence

Correctness and absence of unintended changes come before efficiency. Compare repeated runs rather than choosing guidance from a single sample. Shorten or remove production guidance only when the corresponding variant preserves pass rate and mutation safety while improving round trips or volume. Provider differences are expected: batching is measured but not assumed possible for every call, and the harness does not declare one tool-call syntax universally best.

This suite measures isolated task behavior, not daemon lifecycle, full orchestrator delegation policy, reviewer quality, user-interface rendering, or broad historical cohort trends. The deterministic `investigate` evidence handoff and `atomic_edit` mutation are evaluation-only tools. The optional `delegate_implementation` boundary does run a second paid model loop, but omits the production orchestrator's worktree, review, and commit lifecycle. The rollover scenario replays captured engine events into a fresh loop/client, but does not exercise daemon persistence or automatic context summarization. Broader event-log cohort analytics remain separate scope.
