---
id: "0192"
title: 'iOS: add-project affordance in the project pickers'
status: todo
priority: 4
created: "2026-07-09"
updated: "2026-07-09"
depends_on: []
spec_refs: []
---

## Description
The iOS app's project pickers (session list filter, new-session chip, backlog/workstreams/usage filters) can only select among projects already registered on the daemon. There is no way to register a new workspace path from the phone, unlike the TUI picker (`a` → `AddProject`) and the CLI.

Idea: an "Add project…" row at the bottom of the project picker menus (or in the new-session project chip) that prompts for a server-side path (+ optional name) and calls the existing `AddProject` RPC, then refreshes the project list.

Acceptance criteria:
- User can register a new project by absolute daemon-side path from the iOS app.
- Errors from `AddProject` (bad path, etc.) surface gracefully.
- New project appears in all pickers after registration.

## Acceptance criteria

## Work log
