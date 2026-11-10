---
id: "0519"
title: 'iOS: "Separate workstream" option in New Session; reach workstreams from All'
status: todo
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on:
    - "0518"
spec_refs:
    - spec.md#14.1 Parallel workstreams
    - docs/design/ios-client.md
---

## Description
iOS counterpart of 0518. Today iOS can list and merge workstreams but not start one, and the list is reachable only from the home **…** menu when a single project is selected.

**Change:**
- The iOS New Session sheet gets the same **Separate workstream** toggle, using 0518's extended spawn RPC (prompt, mode, model, preset, pictures, optional task).
- The session list marks workstream sessions the same way as web.
- Workstreams is reachable from the home **…** menu even in the All scope. Ask for a project if needed, as Backlog does.
- `new_session.submit` analytics carry `workstream=true|false`.

**Acceptance:** with the toggle on, a session starts in a new workstream with the chosen options. With it off, nothing changes. Workstreams is reachable from All. Built and tested on the configured Mac simulator. Left in_review for on-device use.

## Acceptance criteria

## Work log
