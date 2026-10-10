---
id: "0451"
title: 'Web: personal session inbox, meaningful unread and visible session filters'
status: done
priority: 2
created: "2026-10-10"
updated: "2026-10-10"
depends_on:
    - "0456"
spec_refs:
    - docs/design/web-client.md#Desktop-first layout
    - docs/design/web-client.md#Data access
---

## Description
Accepted UI audit improvement 3 with user's clarified priority: default session surfaces should emphasize sessions the user actually prompted, especially unread replies, plus ANY session asking the user a question. Automatic memory-groom sessions and routine work-loop task sessions should not create default inbox/unread clutter; all remain browsable in history. Manual follow-up flags keep sessions discoverable. Provide visible search/filter controls and lean sidebar metadata. Use reliable session-origin metadata, not titles or mode guesses; include later user engagement with an automatic session where reliable metadata permits. Keep pagination/search honest about loaded history. Align badges and notifications with attention rules so counts do not lead to invisible routine sessions.

## Outcome
Implemented personal Inbox, Unread, Needs answer, Follow-up, Active and All history filters with visible loaded-history search. Known routine automation is excluded from personal unread counts, default suggestions and per-session notifications; human engagement/bookmarks opt it in, pending questions override filtering, and unknown legacy origins remain visible. Sidebar navigation shares its filter and retains the opened row as a read-transition anchor. Summary titles now retain up to 160 runes. Pure regressions and native Chromium protobuf-fixture checks pass; all history is retained. Full web build and session/server/web Go tests pass. Requires rebuilt daemon for new provenance fields; running daemon was not restarted. Commit: `web: polish personal inbox and desktop UX`.
