---
id: "0296"
title: 'iOS: project memory viewer (GetMemory RPC + MemoryView)'
status: in_review
priority: 3
created: "2026-08-08"
updated: "2026-08-08"
depends_on: []
spec_refs:
    - 6.5 Project memory — agent-learned, advisory
---

## Description

Expose the project's agent memory (memory.md, spec §6.5) to clients and add an iOS screen to view it.

- New read-only `GetMemory(project)` RPC returning `{content, path}`; missing file → empty content, not an error (`docs.Store.ReadMemory` semantics). Handler mirrors `GetPlan` via `Manager.Backlog`.
- Proto regen: Go (local) + Swift (remote BSR plugins).
- iOS: `YccClient.getMemory`, `HomeDestination.memory(project:)` with router dedupe key, "Memory" entry (brain glyph) in the project overflow menus on the landing session list and the session view, and a new `MemoryView` (App target) rendering the whole file with `MarkdownText`, with empty/error states, pull-to-refresh, path footer, and unauthorized routing.
- Docs: remote-api.md `GetMemory` section, ios-client.md overflow-menu/screen list, spec §6.5 read path note.

## Acceptance criteria

- `GetMemory` returns memory.md verbatim + absolute path; empty content when absent; InvalidArgument on unknown project (covered by `TestGetMemory`).
- iOS Memory screen reachable from both project overflow menus; renders markdown; shows a friendly empty state when no memory exists.
- Verified on-device (in_review until then).


## Work log
