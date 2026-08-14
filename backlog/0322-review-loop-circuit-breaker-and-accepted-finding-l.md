---
id: "0322"
title: Review-loop circuit breaker and accepted-finding ledger
status: proposed
priority: 1
created: "2026-08-12"
updated: "2026-08-12"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - §10 Work orchestration
---

## Description
Make iterative review converge instead of allowing unbounded retain/revise/re-review cycles. Record normalized findings with stable ids and dispositions (accepted, disputed, fixed, deferred, duplicate), pass only unresolved accepted blocker/major findings into revision handoffs, and require an explicit terminal decision after a configured number of review rounds. The terminal choices are: commit when criteria are met, mark in_review with remaining findings/evidence gate, split a newly discovered tractable subproblem, or block on a genuine external decision. Do not keep asking reviewers to restate already resolved nits or immutable temporal/hardware gates.

Acceptance criteria:
- Review results expose structured finding identity/severity/disposition across rounds.
- Re-review handoffs contain unresolved accepted findings and a concise changed-since-last-round summary, not the whole historical transcript.
- After the configured round cap (default aligned with the prompt's current ~3 rounds), work sessions cannot silently enter another ordinary revision round; they must record a structured circuit-breaker decision.
- Provider-unavailable reviewer slots are reported as unavailable and do not manufacture an `unknown` finding that forces another identical round.
- Tests cover duplicate/restated findings, a persistent external evidence gate, mixed reviewer verdicts, and the terminal in_review/split/commit paths.

## Acceptance criteria

## Work log
