---
id: "0450"
title: 'Web: keep conversation usable with inspector open at laptop widths'
status: done
priority: 2
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
Accepted UI audit improvement 2. At 1024×768, opening the inspector must not erase the session title/status or reduce the conversation/composer to an unusable sliver. Retain side-by-side detail on wide desktops; adapt sidebar, inspector and secondary header actions to preserve usable content at laptop widths. Preserve resizing and keyboard access. Adopt existing layout and preserve baseline UI changes.

## Outcome
Inspector width reserves 480px for the conversation without overwriting the saved desktop preference. Narrow headers retain title/status above actions. Native Chromium at 1024×768 measured a 480px main pane and 448px title; desktop expansion restored 440px preference and keyboard resize worked. Layout regressions and full web build/test pass. No phone redesign; widths below the desktop target remain limited. Commit: `web: polish personal inbox and desktop UX`.
