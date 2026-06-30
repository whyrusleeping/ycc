---
id: "0077"
title: Surface the plan library in the TUI / RPC
status: todo
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
