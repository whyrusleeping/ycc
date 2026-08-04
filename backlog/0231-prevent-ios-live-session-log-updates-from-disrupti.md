---
id: "0231"
title: Prevent iOS live-session log updates from disrupting scrollback
status: done
priority: 2
created: "2026-08-04"
updated: "2026-08-04"
depends_on: []
spec_refs: []
---

## Description
When a user has scrolled back in a live session, incoming log changes must preserve the visible reading position rather than jumping the screen. Keep normal follow-to-bottom behavior when the user is already at the live edge. Add regression coverage where practical.

## Acceptance criteria

- [x] An explicit user scroll away from the live edge disables auto-follow.
- [x] Incoming durable rows and streaming-tail growth do not reposition scrollback.
- [x] The user can jump back to the live edge and resume auto-follow.
- [x] The iOS smoke plan covers live updates while reading scrollback.

## Work log

- 2026-08-04: Replaced lifecycle-based tail visibility with viewport geometry, removed SwiftUI's content-size-reactive default bottom anchor, invalidated queued scroll requests when the user opts out, and documented a live-scrollback smoke regression. Build verification was unavailable because this environment has neither Swift/Xcode nor XcodeGen installed.
