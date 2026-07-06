---
id: "0166"
title: 'Observability: ycc doctor MCP check + ycc mcp list (servers & tools)'
status: proposed
priority: 5
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0164"
spec_refs:
    - docs/design/mcp.md#9. Proposed follow-on implementation tasks
---

## Description
From docs/design/mcp.md §9 task 3 (design spike, task 0147).

A non-fatal `ycc doctor` check that each configured MCP server is reachable (stdio spawns / HTTP responds), reporting tool counts — mirroring the forge doctor precedent (warn, don't fail). Plus a way to list configured servers and their discovered tools (e.g. `ycc mcp` / `ycc mcp list`).

## Acceptance criteria
- [ ] `ycc doctor` reports each configured server ✓/⚠ without changing its exit code when a server is absent/unreachable.
- [ ] The listing shows servers, transports (command/url), roles, and discovered tool names.

## Work log
