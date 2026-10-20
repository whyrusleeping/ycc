---
id: "0468"
title: Pin follow-up-flagged sessions to the top of session lists (daemon + desktop web)
status: done
priority: 3
created: "2026-10-20"
updated: "2026-10-20"
depends_on: []
spec_refs: []
---

## Description
User request: "star a session and pin it to the top". Decided with the user: no new marker. The existing follow-up flag (⚑) is the star, and flagged sessions pin to the top. Clients: daemon, desktop web, iOS (iOS is a separate task). TUI is out of scope.

## Acceptance criteria
- The first bounded `ListSessionHistory` page includes flagged sessions that fall outside the page in `pinned` (alongside live rows), so a flagged session is visible without paging.
- The desktop web session list (page + sidebar, aggregate and project scope) shows flagged sessions in a "Follow up" section below "Needs answer" and above the rest. Keyboard navigation follows the same order.
- Spec §18.6, the proto comment, and remote-api.md describe the new `pinned` semantics.

## Outcome

The first bounded history page now returns follow-up-flagged rows beyond the page in `pinned`, alongside live ones. The web `sections()` orders the list as Needs answer → Follow up → Recent, with no "Follow up" header when it is the only section. Keyboard navigation follows because it uses `sections()`. Spec §18.6, remote-api.md and the proto comment were updated and the protos regenerated. Session test and web feed/follow-up tests added, and the dist was rebuilt. iOS is tracked as 0469.

Commit: sessions: pin follow-up-flagged sessions to the top (daemon + web) (0468)
