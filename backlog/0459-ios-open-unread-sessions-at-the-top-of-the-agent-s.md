---
id: "0459"
title: 'iOS: open unread sessions at the top of the agent''s newest reply'
status: in_review
priority: 3
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
Opening an unread (non-running) session on iOS scrolls to the top of the agent's newest model message / final report that landed after this device's read mark, instead of the live edge. It behaves like paged-in scrollback: the Jump to latest pill shows, and a drag or the pill hands control back. If the reply plus everything below it fits on screen, follow mode resumes.

Code: `SessionReadStore.mark(for:)` / `unreadReplyRowID(in:after:)` (YccKit, unit-tested), `SessionView.pinUnreadReply`.

Acceptance (on device): open an unread finished session and the reply's heading sits at the top. Read sessions and running sessions still open at the bottom.

## Acceptance criteria

## Work log
