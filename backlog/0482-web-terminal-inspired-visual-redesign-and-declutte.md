---
id: "0482"
title: 'Web: terminal-inspired visual redesign and declutter'
status: done
priority: 2
created: "2026-10-28"
updated: "2026-10-28"
depends_on: []
spec_refs: []
---

## Description
User request (2026-10-27): make the web app look less "vibe coded". Direction from the user: unique palette avoiding blue/purple defaults, nothing over the top, more "cyber"; references Are.na, internet.dev, sacred.computer, vmax.ai. User authorized removing elements freely ("less is better").

Direction: neutral near-black / off-white grounds, one acid-chartreuse accent (used as fill/marker, not text on light), signal orange for needs-attention, square corners, 1px rules instead of card+shadow stacks, monospace for labels/metadata/buttons, no pill badges, one status signal per row.

Acceptance:
- [ ] Tokens: one palette (light+dark), a small type scale, ≤3 radii; no stray literal font sizes/radii in styles.css.
- [ ] Pills/badges replaced by plain mono status text or a single marker; one attention signal per row.
- [ ] Duplicates and explanation removed: second New-session button, help text under inbox search, question-panel keyboard hint row, raw session/loop ids as primary header metadata, ctx/turns on every list row.
- [ ] Light + dark screenshots of inbox, session/question, work loop, backlog reviewed; `npm run build`/typecheck/tests pass; internal/web/dist rebuilt.

## Acceptance criteria

## Work log
