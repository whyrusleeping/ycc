---
id: "0483"
title: 'iOS: match the web''s terminal-inspired palette and declutter'
status: done
priority: 2
created: "2026-10-28"
updated: "2026-11-10"
depends_on: []
spec_refs: []
---

## Description
Follow-up to the web redesign: carry the same palette (chartreuse accent, signal orange attention, neutral greys) to the iOS app while staying native.

Acceptance:
- [ ] App accent colour asset replaced (no system blue/indigo); ad-hoc .orange/.green/.teal/.yellow uses go through a small semantic palette.
- [ ] Coloured capsule status badges replaced by a single quiet indicator (mono caption / dot).
- [ ] Answer sheet: option labels in primary colour, one Submit.
- [ ] Builds and tests pass on the Mac mini; simulator screenshots of home, session, questions reviewed.

## Acceptance criteria

## Work log
- 2026-11-10: Owner-requested simulator review (iPhone 18 Pro, app from HEAD 7a226ef, scratch daemon with mock model), light and dark: accent, quiet status captions, answer sheet single/batch all as described. PASS. Screenshots in .ycc/tmp/ios-review/B/.
