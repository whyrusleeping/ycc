---
id: "0328"
title: 'iOS: edit backlog tasks from task detail'
status: in_review
priority: 3
created: "2026-08-13"
updated: "2026-08-13"
depends_on: []
spec_refs:
    - §6.2 Backlog
    - docs/design/ios-client.md
---

## Description
Allow a user who opens a backlog task in the native iOS app to edit its task fields and save those changes through the daemon, rather than only viewing detail and changing status.

## Acceptance criteria
- Task detail exposes an intentional edit flow for the editable backlog fields supported by the daemon.
- Saving persists through UpdateTask and refreshes the displayed canonical task detail.
- Validation, loading, save failures, and cancellation are handled without losing the current task.
- Daemon/API, Swift client/model, and relevant tests/docs remain consistent.

## Work log
- 2026-08-13: Added the full task editor, expanded UpdateTask for body/dependencies/spec refs, regenerated Go and Swift protobufs, and added server/model tests. Go suite passes; awaiting iOS build and on-device review.
