---
id: "0194"
title: 'iOS: Add-project flow with server-backed directory picker'
status: todo
priority: 3
created: "2026-07-10"
updated: "2026-07-10"
depends_on:
    - "0192"
    - "0193"
spec_refs: []
---

## Description
Upgrade the iOS add-project flow (task 0192 ships the entry affordance + manual path entry) with a server-backed directory picker.

UI: the add-project sheet from 0192 gains:
1. **Suggestions** — likely projects (git-repo siblings of registered projects, from the daemon's ListDir suggestions) as a one-tap list.
2. **Browse** — drill-down directory browser over the new `ListDir` RPC (NavigationStack, breadcrumb/up navigation, dirs only, git repos highlighted, registered projects marked), with a "Use this folder" confirm.
The manual path text field from 0192 remains as a fallback.

Acceptance:
- New `DirectoryBrowserModel` (or similar) in YccKit behind a Source protocol with headless unit tests (navigation, annotations, error/unauthorized handling), matching the existing model patterns.
- Swift proto regen for the new RPC (BSR remote plugins, network required).
- `YccClient` gains listDir (addProject arrives with 0192); unauthorized routes to connect screen like other models.
- Works end-to-end against a local daemon.

## Acceptance criteria

## Work log
