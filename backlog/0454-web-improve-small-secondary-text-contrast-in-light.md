---
id: "0454"
title: 'Web: improve small secondary-text contrast in light and dark themes'
status: done
priority: 3
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
Accepted UI audit improvement 6. Improve readability of useful small metadata and control labels in both themes (audit found secondary text contrast around 3.21:1 light and 3.82:1 on dark cards). Aim for at least 4.5:1 for meaningful normal-size text; retain subtle visual hierarchy and current palette rather than redesigning it. Check actual common surfaces and active/disabled distinctions.

## Outcome
Improved secondary-text tokens, placeholders, syntax comments and filter counts without changing the palette. Calculated useful muted/subtle text against 12 common backgrounds per theme: worst checked ratios 4.574:1 light and 4.604:1 dark. Native Chromium light/dark visual checks and full web build/test pass. Commit: `web: polish personal inbox and desktop UX`.
