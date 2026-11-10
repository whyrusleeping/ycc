---
id: "0539"
title: Give read-only inspection subagents web_search/fetch_page
status: proposed
priority: 4
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs: []
---

## Description
From memory note m-dm98dgt1j4vh (observation, 2026-10-20): "Read-only spawn_agent subagents don't get the web_search/fetch_page tools; for research they fell back to curl on primary docs, so tell them to use curl (or make them mutating) when web access matters."

Verified 2026-11-10: `tools.ReadOnlyInspect` (internal/tools/reviewer.go) returns Read, Search, sandboxed Bash and tool_output only, while `tools.Editing` appends `Web()`. Research is the main read-only use case, so the gap pushes agents to grant write access or use curl.

## Acceptance criteria

- Read-only inspection agents get web_search and fetch_page (still disabled when EXA_API_KEY is unset), or the spec states why they must not.
- Reviewer tool set decision recorded (include or exclude, with reason).
- Tool-set tests cover the change; memory note m-dm98dgt1j4vh retired once it lands.

## Work log
