---
id: "0547"
title: 'iOS: create and edit watches'
status: in_review
priority: 3
created: "2026-11-13"
updated: "2026-11-13"
depends_on: []
spec_refs:
    - docs/design/watches.md#Creation and surfaces
---

## Description
User request (session s_cffea2c64076045f, event 28, 2026-11-13): add iOS UI for creating and managing watches.

Status: in_review, awaiting on-device use. Implemented in fe081c7. That session reported 610 YccKit tests passing, including new tests for the form's checks, its conversion to and from a watch definition, and saving, and said create and edit were tried on the Mac simulator against a test daemon. Its report does not mention a screenshot.

The iOS Watches screens list and control watches but send users to the web client or an agent to create or edit one. Add a native editor matching the web WatchEditor (docs/design/watches.md).

Acceptance criteria:
- Watches list has a + toolbar button and an empty-state "Create a watch" button that open a create sheet; after creating, the app opens the new watch's detail.
- Watch detail has an Edit toolbar button (list rows also offer Edit… in the context menu) opening the same sheet seeded once from the watch, with the name fixed.
- Fields: name, brief, every N minutes/hours/days, model (coordinator default or an enabled model; a disabled current model stays shown), probe with "run the model even when the probe passes", optional expiry, allowed actions one per line.
- Client-side validation mirrors the web draftError; daemon errors show in the sheet and keep the draft.
- The expired-watch footer and empty-state copy no longer send users to the web client; the design doc status line reflects iOS creation/editing.
- YccKit draft/editor logic has unit tests; app builds on the Mac and the sheet is screenshotted against a scratch daemon.

## Work log
- 2026-11-14 groom: added the source request and a current-state line from that session's report (fe081c7; f294066 set in_review), and removed the empty Acceptance criteria heading.
