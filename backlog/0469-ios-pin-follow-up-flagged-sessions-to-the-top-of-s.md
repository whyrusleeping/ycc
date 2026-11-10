---
id: "0469"
title: 'iOS: pin follow-up-flagged sessions to the top of session lists'
status: done
priority: 3
created: "2026-10-20"
updated: "2026-11-10"
depends_on:
    - "0468"
spec_refs: []
---

## Description
Split from 0468, which covers the daemon and web. Flagged follow-up (⚑) sessions pin to the top of the iOS session lists. The daemon now returns flagged rows outside the first history page in `ListSessionHistoryResponse.pinned`, alongside live rows.

## Acceptance criteria
- On the iOS home feed (all projects) and in a project-scoped list, flagged sessions appear in a "Follow up" section at the top. In a scoped list it sits below "Needs answer". Within the section, rows stay in recency order.
- The pure YccKit logic is tested in Docker. On-device check happens on the user's Mac, so in_review is the expected end state.

## Work log
- 2026-11-10: Owner-requested simulator review: a "follow up" section is pinned at the top of Recent (newest first) and of project scopes (below "needs answer" when present). PASS. Screenshots in .ycc/tmp/ios-review/B/.
