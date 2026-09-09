---
id: "0360"
title: Build behavioral model-UX evaluations for tools, prompts, and delegation
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §7 Agent engine
    - §8 Tools and access policy
    - §10 Work orchestration
---

## Description
Evaluate harness changes using disposable repositories and deterministic task outcomes rather than prompt wording tests or model opinion alone. The review's latest-50-session sample had approximately 91% Read/Edit/Bash calls and approximately 95% single-call tool-using turns despite batching guidance; these are motivation to measure, not proof every call could be batched.

## Acceptance criteria
- Begin with a small reproducible suite: scoped symbol search; failed-edit recovery; atomic multi-hunk mutation; long failing-test output; preservation of unrelated dirty work; interrupt/rollover recovery; delegated investigation with evidence use.
- Score task correctness, unintended mutations, recovery attempts, model round trips, input/output volume, elapsed time, and human intervention. Ground pass/fail in repository/process facts.
- Record model/config/tool/prompt versions and repeat runs sufficiently to expose variance; paid/live-model execution is explicit, bounded, and opt-in.
- Support A/B comparison of prompt/tool variants across configured models, including shorter non-redundant guidance, batching behavior, and direct versus delegated implementation for small tasks.
- Use evidence to simplify prompts and retain effective guidance; do not impose mandatory delegation or assume one syntax is best for every provider.
- Keep evaluation fixtures small and extensible; broader cohort analytics in 0320 remain a separate optional scope.

## Outcome
Added `ycc-model-eval` with seven disposable fixtures, repository/Git/process oracles, usage and efficiency metrics, bounded opt-in repeated model/variant comparisons, and reproducibility fingerprints. Includes delegated mutation attribution, blocked-outcome accounting, cancellation reports, and operating documentation. Evaluation-only atomic mutation and deterministic investigation tools are explicitly distinguished from production capabilities; replay recovery does not claim daemon lifecycle coverage. Production prompts remain unchanged pending live evidence.

Verification: targeted Go tests, race tests, vet, and task-only HEAD-overlay checks passed; full Go suite passed during implementation and independent review. Reviewer accepted after scoring corrections. No paid/live model calls performed. Unrelated staged/worktree changes preserved.

Commit subject: Add opt-in behavioral model UX evaluation harness
