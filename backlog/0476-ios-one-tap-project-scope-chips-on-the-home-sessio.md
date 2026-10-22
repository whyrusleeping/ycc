---
id: "0476"
title: 'iOS: one-tap project scope chips on the home session list'
status: in_review
priority: 3
created: "2026-10-22"
updated: "2026-10-22"
depends_on: []
spec_refs: []
---

## Description
Usage analytics showed the drawer is mostly a project switcher (66 opens → 53 select_project in 8 days, usually drawer → home → session). Try a horizontal scope-chip row on the home list so switching is one tap.

## Acceptance criteria
- Home list shows a horizontally scrolling chip row: "Recent" + each registered project, current scope highlighted, with the drawer's activity badges (question / unread / active).
- Projects ordered by most recent session activity (fallback: registry order); the selected chip is scrolled into view.
- Tapping a chip sets the scope instantly (client-side filter, same as the drawer).
- Analytics: scope changes record `scope.select` with via (chip / drawer) plus position; drawer path keeps `drawer.select_project`.
- Built and tested on the Mac mini (iOS simulator + YccKit tests); ends in_review for on-device feel.
- Out of scope: restoring the last scope on launch (undecided).

## Work log
