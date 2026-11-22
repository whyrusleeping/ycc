---
id: "0581"
title: Read-only generic subagents in work mode
status: todo
priority: 2
created: "2026-11-22"
updated: "2026-11-22"
depends_on: []
spec_refs:
    - spec.md
---

## Description
Accepted by the user on 2026-11-22 in session s_70ed48449817e056 ("Yes, file it as todo").

Work-mode coordinators cannot start a generic subagent: `spawn_agent` and `send_to_agent` are registered only in chat and watch modes (`internal/orchestrator/modes.go`), and spec.md describes generic subagents for chat only. Investigating 0576 (review cycles), the work coordinator wanted a second opinion from another model. It worked around the gap by running `ycc start --mode chat` from Bash. That created an unrelated top-level session whose opus coordinator only relayed the request to a `spawn_agent` call, and whose write tools were held back by instructions alone. Second opinions on an approach are also the cheapest counter to the "patch each reported case" pattern behind long review cycles (`docs/reports/review-cycles-2026-11.md` cause 2).

Read-only only: a mutating generic agent would be a second writer outside work mode's implementer slot, changeset attribution, and review flow. Reviewers do not get the tool.

## Acceptance criteria
- Work-mode coordinators, direct and delegated, can call `spawn_agent` and `send_to_agent` with any configured logical model; such agents are read-only with the same sandbox and tools as chat's read-only generic agents, and a request for write access is refused.
- The work coordinator prompt says a subagent's report is input, not review, and that `spawn_reviewers` remains the gate before commit.
- spec.md describes generic subagents in work mode and their read-only limit.

## Work log
