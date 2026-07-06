---
id: "0167"
title: 'Spec update: record MCP client support in §8/§13'
status: proposed
priority: 5
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0164"
    - "0165"
spec_refs:
    - 8. Tools
    - 13. Backends & model registry
---

## Description
From docs/design/mcp.md §9 task 4 (design spike, task 0147). Depends on the core + wiring landing first.

Extend spec §8 (tools) and §13 (config) to document MCP client support: the `[mcp.servers.X]` config shape, the `mcp__server__tool` namespacing, the role matrix including the reviewer hard-exclusion (sandbox rationale), lifecycle (per-session, lazy, session-owned), and the event-log representation (ordinary tool_call/tool_result + mount-time Narration) — keeping the spec true (spec §1).

## Acceptance criteria
- [ ] spec.md §8 and §13 accurately describe the implemented MCP support (config, namespacing, roles, reviewer exclusion, events).
- [ ] `ycc spec-check` passes.

## Work log
