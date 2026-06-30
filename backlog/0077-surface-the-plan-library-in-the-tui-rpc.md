---
id: "0077"
title: Surface the plan library in the TUI / RPC
status: done
priority: 4
created: "2026-06-30"
updated: "2026-06-30"
depends_on:
    - "0020"
spec_refs:
    - Reusable plans (runbooks)
---

## Description
## Description
Task 0020 added the in-repo plan library (`plans/*.md`) plus agent-facing tools
(`list_plans`/`run_plan`/`save_plan`) and the docs API (`ListPlans`/`ReadPlan`/`SavePlan`/`PlansDir`).
The agent side and a tool-based listing are done; what remains is human-facing discoverability:
let a user browse and read saved plans from the TUI (and, if needed, over RPC).

## Acceptance criteria
- [ ] an RPC/daemon method (or reuse of existing docs surface) exposes the plan library list + a plan's content to clients
- [ ] the TUI can list saved plans and view a plan's markdown
- [ ] (optional) a way to trigger "run this plan" from the TUI, or at least surface that the tool exists
- [ ] tests where reasonable; build/vet/test green

## Work log


## Acceptance criteria

## Work log
- 2026-06-30 plan: Surface the in-repo plan library (`plans/*.md`, from task 0020) to human clients via RPC + TUI, reusing the existing `docs.Store` plan methods (`ListPlans`/`ReadPlan`).  1. **Proto** (`proto/ycc/v1/yc
…[truncated]
- 2026-06-30 implementer report: Surfaced the in-repo plan library (`plans/*.md`) to human clients via RPC + TUI, reusing the existing `docs.Store` plan methods (read-only browse + view).  ## Proto (`proto/ycc/v1/ycc.proto`) - Added 
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change fully satisfies the task. It adds read-only ListPlans/GetPlan RPCs (reusing docs.Store.ListPlans/ReadPlan), a modal TUI plan-library browser with list + markdown detail views reachable via 
…[truncated]
- 2026-06-30 decision: accept — commit: plans: surface the plan library over RPC + TUI (task 0077)  Add read-only ListPlans/GetPlan RPCs (reusing docs.Store.ListPlans/ReadPlan) and a modal TUI plan-library browser (list + markdown detail) r
…[truncated]
- 2026-06-30 usage: 26,699 tok (in 168, out 26,531, cache_r 2,566,837, cache_w 129,075) · cost n/a (unpriced)
