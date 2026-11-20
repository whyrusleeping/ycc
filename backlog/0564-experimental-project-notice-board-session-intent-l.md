---
id: "0564"
title: 'Experimental project notice board: session intent lines and expiring coordination notices'
status: todo
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

## Why
Reviews of the wheeler project's session logs from 2026-11-15 to 2026-11-20 found that most useful communication between parallel sessions was announcements: one-to-many, needing no reply, and needed by sessions that had not started yet. Examples:
- "I restarted normalize-v2 on image X at 05:02."
- "Tests A and B are red on master and have no owner." At least 7 sessions independently noted the same two failing `cmd/wheeler` tests on 2026-11-17 and 2026-11-18.
- "I'm measuring until 08:26; don't restart the service."

On 2026-11-20 (03:14–06:30), three sessions deployed over each other on one service:
- 0412 deployed a build at 05:02.
- 0406's session found that build through `docker inspect` and then sat in `ask_user` for 33 minutes.
- The container was recreated on the old image at 05:34. 0412's session later attributed that to the wrong session.

Private pairwise messages would have left the third session uninformed. On 2026-11-18 the user opened a fresh session just to ask why a service was down; another session had stopped it deliberately for a measurement. Claims cover files only; the worst episodes involved a shared running service.

## Design
- **Intent line.** Each session has at most one current intent line, for example "deploying normalize-v2 to cubey; avoid restarts until about 05:30". The session sets, replaces, or clears it with a tool. It is shown in `list_sessions` and the `list_backlog` footer, and is cleared when the session ends.
- **Project notices.** `post_notice(text, expires_in, subject?)` and `clear_notice(id)`:
  - `subject` is a free-form resource or topic, such as a service name or "master tests".
  - Expiry is required and bounded (for example at most 24 hours).
  - The daemon records the poster's session, actor, and focused task. Text length is bounded and the text is sanitized.
  - A session has a small cap on its active notices and on posts per hour.
  - Notices are daemon state for each project, persisted outside git, plus an event in the poster's log. Clearing records who cleared the notice and why.
  - Any session may clear a notice, which suits a resolved "tests are red" notice.
- **Who can use the tools.** Coordinators get the posting tools. Mutating subagents read the board through `list_sessions` and report to their coordinator, which decides what to post.
- **Visibility.** Active notices and intent lines appear:
  - in `list_sessions` and the `list_backlog` footer;
  - in watch-run context;
  - in the opening context of sessions started in the project while the setting is on;
  - in a running session at its next safe checkpoint, as a compact digest of new or changed notices.
- **Waking.** No session is ever woken and no `wait` is ended early for a notice (user decision, 2026-11-20). A digest pending delivery is not restored after a reopen; the session sees the current board at its next checkpoint instead, so expired notices are never delivered.
- **Authority.** Notice text is untrusted peer content entering history with its own actor, never `user`. The framing and the coordinator prompt describe a notice as a reason, not an authorization: the receiver acts only within its own mandate and says so in its report. A notice cannot answer `ask_user`, approve a commit or deploy, or promote proposed work.
- **Clients.** Rendering, the user posting and clearing notices from clients, and showing notices on the pending-question screen are in 0562. This task ships the RPCs that read and clear notices, plus the generic event fallback.

## Acceptance criteria
- With the setting off for a project, none of the tools, digests, or opening-context additions appear for its sessions.
- A session can set and clear its intent line; it appears in `list_sessions` and the `list_backlog` footer and disappears when the session ends.
- A posted notice has a recorded poster, expiry, and subject. It appears in the listed surfaces and in other running sessions' next checkpoint digest without waking them, then disappears when it expires or is cleared.
- Length, active-notice, and rate caps are enforced and tested.
- Notice text enters history with a non-`user` actor and the "reason, not an authorization" framing, and cannot answer a pending question.
- Delivered digests replay identically; pending digests are not restored after reopen.
- spec.md §7.3 (or the design doc chosen by 0443) describes the notice board.

## Work log
