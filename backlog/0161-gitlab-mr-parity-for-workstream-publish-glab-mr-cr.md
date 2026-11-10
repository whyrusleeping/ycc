---
id: "0161"
title: GitLab MR parity for workstream publish (glab mr create)
status: done
priority: 5
created: "2026-07-06"
updated: "2026-11-10"
depends_on:
    - "0155"
    - "0157"
spec_refs:
    - docs/design/forge-integration.md#6. Flow 2 — workstream → PR (publish)
---

## Description
From docs/design/forge-integration.md §6/§11 (design spike 0146).

GitLab parity for workstream publish: `glab mr create --source-branch … --target-branch … --title … --description …` behind the same PublishWorkstream flow, selected by forge/host inference. Same idempotency (detect existing MR), event, and cleanup semantics.

## Acceptance criteria
- [ ] PublishWorkstream against a GitLab remote opens an MR and records its URL; retry converges without duplicates.

## Work log
- 2026-11-10: Closed without implementation. The owner closed the forge workflow tasks (see 0156); there is no PublishWorkstream to reach parity with.
