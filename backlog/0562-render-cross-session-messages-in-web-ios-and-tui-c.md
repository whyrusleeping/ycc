---
id: "0562"
title: Client surfaces for session awareness and the notice board (web, iOS, TUI)
status: proposed
priority: 4
created: "2026-11-20"
updated: "2026-11-20"
depends_on:
    - "0443"
    - "0564"
spec_refs:
    - spec.md#7.3
    - spec.md#18.5
---

## Description
Current state: proposed follow-up to 0443 (experimental session awareness: `list_sessions`, path
ownership lookup, overlap notices) and 0564 (experimental project notice board). Those tasks ship
only the web per-project toggle and the generic event fallback. This task is separate because it
touches three clients and needs the iOS build and screenshot check on the Mac.

Reviews of the wheeler project's logs on 2026-11-20 put ownership surfaces ahead of transcript
styling. On 2026-11-18 the user opened a fresh session just to ask why a service was down. On
2026-11-20 a session sat in `ask_user` for 33 minutes about a deploy whose context was known
to other sessions.

## Idea, in priority order
- A project sessions view: live sessions with intent line, held task, state, and claimed paths, plus
  active notices with poster, subject, and expiry. Users can post and clear notices there.
- Backlog browsers (web and iOS) badge a task held by a live session and link to it.
- The pending-question screen shows active notices, so a user answering a question about another
  session sees that session's announcements.
- Distinct transcript rendering for overlap notices and notice digests, linking the other session.
- iOS Settings toggle for the per-project `session_coordination` setting, using the RPC from 0443.

## Acceptance (draft)
- Web and iOS show the sessions view with notices and held-task badges; TUI shows at least the
  notice list and held badges.
- The pending-question screen shows active notices.
- Overlap notices and digests render distinctly with links to the other session.
- iOS Settings shows and changes the per-project setting.
- iOS changes are built and screenshotted on the configured Mac; web is checked with typecheck and a
  Playwright screenshot.

## Work log
