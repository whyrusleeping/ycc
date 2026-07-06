---
id: "0158"
title: 'plans/publish-pr.md runbook: session → PR behind a confirmation gate'
status: proposed
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0155"
spec_refs:
    - 6.3 Reusable plans (runbooks)
    - docs/design/forge-integration.md#7. Flow 3 — work session → PR (non-workstream, prompt-level)
---

## Description
From docs/design/forge-integration.md §7 (design spike 0146). Prompt-level in v1 — no code.

Write a committed `plans/publish-pr.md` runbook for shipping a plain (non-workstream) work session's branch as a PR: verify clean tree + commits present → forge probe checks (gh installed/authed, remote exists and is a recognised forge) → seek explicit human confirmation before any push (spec §11 "Exception — confirmation gates": a real human yes/no even under autonomous; decline if none available) → `git push` → `gh pr create` with title/body from the session's final report → report the PR URL. Include the failure checks from doc §9 and add a line of coordinator prompt guidance pointing at the runbook.

## Acceptance criteria
- [ ] plans/publish-pr.md exists with concrete steps, the confirmation-gate expectation, failure checks, and expected outcome.
- [ ] Coordinator prompt mentions the runbook for "open a PR for this session" requests.

## Work log
