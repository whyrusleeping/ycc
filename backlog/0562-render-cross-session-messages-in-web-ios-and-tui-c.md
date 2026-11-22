---
id: "0562"
title: Client surfaces for session awareness and the notice board (web, iOS, TUI)
status: done
priority: 4
created: "2026-11-20"
updated: "2026-11-22"
depends_on:
    - "0443"
    - "0564"
    - "0582"
    - "0583"
    - "0584"
spec_refs:
    - spec.md#7.3
    - spec.md#18.5
---

## Description

Current state: accepted parent, split on 2026-11-22 into 0582 (daemon API and CLI for the project
sessions view and user-posted notices), 0583 (web surfaces) and 0584 (iOS surfaces and the Settings
toggle). All three are done: 0582 (`b5c61d7`), 0583 (`21b5de8`) and 0584 (`ce34174`). It follows 0443
(experimental session awareness: `list_sessions`, path ownership lookup, overlap notices) and 0564
(experimental project notice board), both done (`4096a81`, `41c611e`); they ship only the web
per-project toggle and the generic event fallback. ycc has no separate TUI; the terminal client is
the `ycc` CLI, covered by 0582.

Reviews of the wheeler project's logs on 2026-11-20 put ownership surfaces ahead of transcript
styling. The user asked for those reviews in session s_18f059896f075e5a at event 152. On 2026-11-18 the user opened a fresh session just to ask why a service was down. On
2026-11-20 a session sat in `ask_user` for 33 minutes about a deploy whose context was known
to other sessions.

## Outcome

Parent closed after checking each criterion against its children. CLI (0582, b5c61d7): `ycc notices` and held badges in `ycc task list`. Web (0583, 21b5de8): /p/<project>/live, held badges, question notices, coordination_notice rows; typecheck, vitest and Playwright shots. iOS (0584, ce34174): Live sessions screen, held badges, question-sheet notices, coordination rows, the Settings › Session coordination toggle; swift test, xcodebuild build/test and simulator screenshots on the Mac. No commits after ce34174, so that evidence is current.

Commit: Close 0562: session-awareness client surfaces verified across 0582-0584
