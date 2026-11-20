---
id: "0564"
title: 'Experimental project notice board: session intent lines and expiring coordination notices'
status: done
priority: 3
created: "2026-11-20"
updated: "2026-11-20"
depends_on:
    - "0443"
spec_refs:
    - spec.md#7.3
---

## Description

Current state: accepted on 2026-11-20 (session s_18f059896f075e5a); not started. This task depends on 0443 (experimental session awareness), which adds the per-project `[experimental] session_coordination` setting and `list_sessions`. This task uses that setting and adds passive coordination notices, a project notice board, in place of the directed `message_session` tool that the user dropped.

## Acceptance criteria

- With the setting off for a project, none of the tools, digests, or opening-context additions appear for its sessions.
- A session can set and clear its intent line; it appears in `list_sessions` and the `list_backlog` footer and disappears when the session ends.
- A posted notice has a recorded poster, expiry, and subject. It appears in the listed surfaces and in other running sessions' next checkpoint digest without waking them, then disappears when it expires or is cleared.
- Length, active-notice, and rate caps are enforced and tested.
- Notice text enters history with a non-`user` actor and the "reason, not an authorization" framing, and cannot answer a pending question.
- Delivered digests replay identically; pending digests are not restored after reopen.
- spec.md §7.3 (or the design doc chosen by 0443) describes the notice board.

## Outcome

With [experimental] session_coordination on, coordinators (not integrate mode) get set_intent, post_notice(text, expires_in, subject?) and clear_notice. Caps: 300-char text, 60-char subject, expiry from 1m to 24h, 3 active notices and 6 posts per hour per session; all text is sanitized. Notices persist per project in <state>/ycc/notices/ outside the worktree. Each records its poster's session, actor and tasks, and who cleared it and why; the poster's log gets notice_posted/notice_cleared. Intents and notices appear in list_sessions and the list_backlog footer, and intents in watch-run context. A coordinator's first checkpoint delivers the current board and intents. Later checkpoints deliver up to 20 new or cleared notices; any overflow is deferred, expiry is silent, and nothing wakes a session. A digest is a system-actor coordination_notice framed as a reason, not an authorization, and replays identically; nothing pending survives reopen. Adds ListProjectNotices/ClearProjectNotice RPCs (regenerated Go, Swift, web; bundle rebuilt) and the board guidance in the coordinator prompt. Also fixes work mode dropping list_sessions. Spec §7.3 and remote-api.md are updated. Sol accepted in round 3.

Commit: session coordination: experimental project notice board (intent lines, expiring notices, checkpoint digests)
