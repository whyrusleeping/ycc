---
id: "0459"
title: 'iOS: open unread sessions at the top of the agent''s newest reply'
status: done
priority: 3
created: "2026-10-10"
updated: "2026-11-10"
depends_on: []
spec_refs: []
---

## Description
Opening an unread (non-running) session on iOS scrolls to the top of the agent's newest model message / final report that landed after this device's read mark, instead of the live edge. It behaves like paged-in scrollback: the Jump to latest pill shows, and a drag or the pill hands control back. If the reply plus everything below it fits on screen, follow mode resumes.

Code: `SessionReadStore.mark(for:)` / `unreadReplyRowID(in:after:)` (YccKit, unit-tested), `SessionView.pinUnreadReply`.

Acceptance (on device): open an unread finished session and the reply's heading sits at the top. Read sessions and running sessions still open at the bottom.

## Acceptance criteria

## Work log
- 2026-11-10: Owner-requested simulator review found the behavior worked after a cold launch but not on a same-run reopen. The cached model pinned against stale rows before the resume caught up, so the session opened at the bottom. Fixed: `SessionViewModel` tracks a resume catch-up (`isCatchingUpAfterResume`, `resumeCatchUpCount`), and `SessionView` re-checks for the unread reply once the catch-up lands, unless the reader has scrolled or the session is running. Four YccKit tests were added (`SessionModelCacheTests`); `swift test` passes 598/598 on the Mac and the app builds. In the simulator, same-run reopens (repeated) and cold launches open at the new reply's top, while read and running sessions stay at the bottom or live edge. Not driven in the simulator: a stale resume (>300 s parked; unit-tested) and a scroll before the catch-up lands.
