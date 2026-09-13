---
id: "0350"
title: Evaluate command yield policy and expose structured shell execution outcomes
status: blocked
priority: 3
created: "2026-09-08"
updated: "2026-09-10"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - §8 Tools and access policy
---

## Description
The current Bash interface requires a foreground/background decision before runtime is known and conflates that decision with waiting policy. Foreground exit/timeout facts are appended to otherwise successful prose. Evaluate separating execution timeout, initial wait/yield duration, and output budget rather than imposing a new interface without evidence. Evidence: internal/tools/worker.go:319–471.

## Acceptance criteria
- Structured results distinguish exit code, timeout, cancellation, runtime, running-job handle, and output truncation; normal nonzero program exits are distinguishable from harness execution failures.
- Compare existing explicit background mode with an initial-wait/yield option that returns a job handle for a still-running command without restarting or killing it.
- Evaluate fast commands, slow tests, watchers, failed commands, cancellation, and immediate follow-up waits; record correctness/latency/round-trip tradeoffs.
- Implement and document the selected policy only if evidence supports it; preserving explicit background mode with a recorded evaluation is an acceptable outcome.
- Any adopted behavior retains process-tree cancellation, execution-time limits, replay fidelity, and predictable job delivery.

## Work log

- Preflight: this task's backlog file was already staged as an addition at session entry. Verified `internal/git/changeset.go` rejects modifications to baseline-dirty paths, including task bookkeeping; task 0325's durable log records the same delegation failure. Source under `internal/tools` is clean, but status/plan/work-log/finalization changes cannot form an owned changeset for this task in the current tree. No implementation or evaluation was attempted; do not repeat the failed delegation experiment.
- Unblock by providing an isolated clean task worktree/session containing the accepted task, or having existing owners resolve the staged work before a fresh baseline is captured. Preserve unrelated index/worktree changes. Then perform the original structured-outcome implementation and yield-policy evaluation; all acceptance criteria remain outstanding.
