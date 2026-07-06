---
id: "0165"
title: 'Session/orchestrator wiring: mount MCP tools per role, session lifecycle, events'
status: proposed
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0164"
spec_refs:
    - 8. Tools
    - docs/design/mcp.md#6. Which roles get MCP tools, and the security posture
    - docs/design/mcp.md#8. Lifecycle
---

## Description
From docs/design/mcp.md §6/§7/§8 (design spike, task 0147).

Mount each configured MCP server's tools into the registries for its configured roles at the toolset-assembly seams (orchestrator.BuildMode for chat/pm; spawn_implementer for implementer; CoordinatorTools and spawn_reviewers stay excluded). Lazy connect on first mounting toolset build; session-scoped teardown beside Jobs.KillAll (internal/session killJobs); stdio children spawned with cwd = workspace root; Narration event per server on connect success/failure at mount.

## Acceptance criteria
- [ ] chat mounts a configured server's tools under mcp__server__tool names; pm/implementer only when listed in the server's roles.
- [ ] Explicit test: a tools.Reviewer registry NEVER contains an mcp__… tool (hard exclusion cannot regress).
- [ ] A broken/unreachable server emits a Narration warning and the session still starts (its tools absent; never fatal).
- [ ] MCP connections/children are closed at session end via the session teardown path.
- [ ] MCP calls appear in the event log as ordinary tool_call/tool_result events (no new event types); replay/reopen unaffected.

## Work log
