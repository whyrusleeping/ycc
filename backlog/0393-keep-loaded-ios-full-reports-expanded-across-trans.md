---
id: "0393"
title: Keep loaded iOS full reports expanded across transcript refreshes
status: in_review
priority: 3
created: "2026-09-17"
updated: "2026-09-17"
depends_on: []
spec_refs: []
---

## Description
Long agent responses expanded using “Load full report” regularly revert to their shortened preview while the user is reading. Investigate and fix refresh/reconnect replacement of fetched row detail.

Acceptance criteria:
- Unchanged full reports/messages already loaded in an open session do not revert to previews on reconnect, paging, or repeated row updates.
- Genuinely newer row versions and deletions remain authoritative; stale detail cannot overwrite them.
- Add focused regression coverage and leave ready for on-device verification.

## Acceptance criteria

## Work log

- Reconnect installed abbreviated indexed snapshots over already-fetched full row text. Added open-session detail retention keyed by stable row ID and exact update sequence across snapshots, paging, and repeated upserts; newer versions/deletions invalidate retained detail.
- Regression coverage includes reconnect snapshots, direct and buffered paging, repeated upserts, newer versions, and stale detail/deletion races. Focused review found no blockers; `git diff --check` passes. Swift tests could not run here (no Swift toolchain).
- Awaiting Mac build/test and on-device reading verification. Changes left uncommitted.
