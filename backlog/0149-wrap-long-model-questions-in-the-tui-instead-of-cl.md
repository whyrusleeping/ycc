---
id: "0149"
title: Wrap long model questions in the TUI instead of clipping at screen edge
status: todo
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs: []
---

## Description
## Problem

In PM mode (possibly other modes too — unverified), when the model asks the user a question whose text is longer than the terminal width, the question is clipped at the right edge of the screen instead of wrapping. The overflowing text is unreadable.

## Notes

- Reproduced in PM mode; check whether the same rendering path is used for questions in other modes/session views and fix generally rather than per-mode.
- Likely a missing word-wrap (e.g. lipgloss width/wrap) on the question rendering path in `internal/tui`.

## Acceptance Criteria

- [ ] A model question longer than the terminal width wraps onto multiple lines and is fully readable.
- [ ] Wrapping re-flows correctly on terminal resize.
- [ ] Verified in PM mode and any other mode sharing the question rendering path.
- [ ] Regression test (or snapshot test) covering long question wrapping.

## Work log
