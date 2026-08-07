---
id: "0288"
title: 'iOS nav pass: route all pushes through one path-driven router with screen dedupe'
status: in_review
priority: 3
created: "2026-08-07"
updated: "2026-08-07"
depends_on: []
spec_refs: []
---

## Description
User feedback: getting out of a deep view stack takes many Back taps; navigation "feels a bit clunky".

Root cause: cross-links create unbounded push cycles. Session → Backlog → TaskDetail → Session → Backlog … each hop *pushes* (SessionView toolbar pushes backlog/workstreams/usage; BacklogView pushes TaskDetail via view-builder NavigationLink; TaskDetail/WorkLoop/Workstreams push SessionView via local `navigationDestination(item:)` bindings), so the same screens pile up and Back walks every copy.

Plan:
- Introduce `HomeRouter` (@Observable, owns `[HomeDestination]` path, injected via environment). `open(_:)` semantics: if a screen with the same identity (session id / project-scoped screen kind) is already on the stack, pop back to it (merging richer params like a non-empty title, rebuilding if `live` flips) instead of pushing a copy.
- Add `HomeDestination.taskDetail` and convert every nested push (TaskDetail liveTarget, WorkLoop sessionTarget, Workstreams liveTarget, Backlog task links, SessionView toolbar links) to router/value-based navigation so the entire stack is data.
- Side benefit: every level is titled + value-based, so the system long-press-on-Back stack menu works for multi-level pops.
- Update docs/design/ios-client.md §6 navigation shell accordingly.

Acceptance: cycles no longer grow the stack (session→backlog→task→open-active-session pops back to the session); builds via xcodegen+xcodebuild on the user's Mac; user confirms it feels better on device.

## Acceptance criteria

## Work log
