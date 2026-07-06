---
id: "0160"
title: Opt-in issue close/comment when an imported task goes done
status: proposed
priority: 5
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0155"
    - "0156"
spec_refs:
    - docs/design/forge-integration.md#8. Safety & gating
---

## Description
From docs/design/forge-integration.md §5 (design spike 0146).

When a task imported from an issue (has `origin:`) transitions to done, optionally close the issue and/or drop a comment (`gh issue close <url> --comment "Resolved by ycc: <commit/PR>"`). Public, hard-to-reverse side effect on someone else's tracker, so: config-gated (`forge.close_issue_on_done`), **off by default**, and behind the doc §8 confirmation semantics when enabled. Hook point is the backlog done-transition path; failures to close must not block the task going done (warn + work-log breadcrumb instead).

## Acceptance criteria
- [ ] Config flag, default off; no forge call ever happens when off.
- [ ] When on, done-transition closes/comments the origin issue per gating; failure is non-fatal and logged.

## Work log
