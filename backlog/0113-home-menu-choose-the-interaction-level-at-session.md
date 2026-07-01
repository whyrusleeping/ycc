---
id: "0113"
title: 'Home menu: choose the interaction level at session start'
status: todo
priority: 4
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 11. Interaction levels
    - 9. Modes (the home menu)
---

## Description
## Description
Spec §1 promises "the human chooses their level of involvement per session", but the home menu offers no way to pick the interaction level at `StartSession` — every session starts `judgement` (loop forces `autonomous`) and changing it requires opening the settings overlay mid-session. Add a visible level selector on the home menu (e.g. ←/→ cycles interactive/judgement/autonomous next to the prompt, shown as a pill) passed into `StartSession`. Deliberately not persisted as a global default (a sticky `autonomous` would be a safety footgun — mirror §18.2's reasoning).

## Acceptance criteria
- [ ] Level visible and cyclable on the home menu; passed to StartSession
- [ ] Defaults to judgement each launch (not persisted)
- [ ] Loop start still forces autonomous regardless of the selector
- [ ] Menu footer documents the key

## Acceptance criteria

## Work log
