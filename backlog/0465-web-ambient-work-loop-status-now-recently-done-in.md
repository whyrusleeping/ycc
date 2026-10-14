---
id: "0465"
title: 'Web: ambient work-loop status (now / recently done) in the sidebar inbox'
status: done
priority: 2
created: "2026-10-14"
updated: "2026-10-14"
depends_on: []
spec_refs:
    - docs/design/web-client.md#Data access
---

## Description
After the personal inbox (0451) the user feels too out of the loop on work-loop agents. Keep loop sessions out of the personal inbox/unread/notifications, but show ambient loop status at the top of the sidebar inbox.

## Acceptance criteria
- Per project with a live or recently finished loop, the sidebar shows state, what it is working on now (linking the live session) and tasks completed by the latest loop (linking tasks), with blocked/in-review counts highlighted.
- No unread dots, counts, or per-session notifications come from this section.
- A finished loop collapses to its outcome and disappears ~24h after it finished (daemon exposes `finished_at`), or when dismissed.
- The "Active" session filter includes running loop sessions; Inbox/Unread stay personal.

## Work log

## Outcome
Sidebar shows a status card per live loop and per loop finished within 24h (dismissible): state, "Now" task + live session link, up to 3 completed tasks (most recently worked first, +N to the loop page), blocked/in-review counts. No unread/notification effects. Active filter now includes running automatic sessions. Daemon exposes and persists `WorkLoopInfo.finished_at`; finished loops from a pre-upgrade daemon stay hidden. Web/session/server/web-bundle tests pass; visually checked light/dark against the live vals loop. Requires daemon rebuild for finished-loop cards. Commit: `web: ambient work-loop status above the sidebar inbox`.
