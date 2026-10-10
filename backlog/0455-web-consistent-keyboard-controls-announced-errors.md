---
id: "0455"
title: 'Web: consistent keyboard controls, announced errors and complete batch answers'
status: done
priority: 3
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
Accepted UI audit improvement 7. Make menus and radio-style segmented controls match their promised keyboard semantics (or use honest toggle-button semantics), preserve focus through async saves, announce inline form/settings failures, and prevent batch question submissions with untouched empty answers while exposing selected choices accessibly. Source audit also found immediate-save selects disabling themselves mid keyboard navigation; address interaction safely without modifying settings while merely navigating options. Keep scope to current controls and protect real behavior, not implementation details.

## Outcome
Menus now support focus/arrows/Home/End/Escape; session overflow uses the shared menu. Unsupported button-radio roles became honest pressed-button groups, reasoning saves preserve button focus and block repeated changes, and inline errors are announced. Model/status/tier selections use explicit Apply rather than mutating while browsing. Batched answers require a valid answer per question and expose selected choices. Palette shortcuts cannot bypass open editor guards. Focused regressions, native menu/editor browser checks and full web build/test pass (376 tests overall). No live setting changes made during verification. Commit: `web: polish personal inbox and desktop UX`.
