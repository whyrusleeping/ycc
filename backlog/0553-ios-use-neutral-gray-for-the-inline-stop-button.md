---
id: "0553"
title: 'iOS: use neutral gray for the inline stop button'
status: done
priority: 3
created: "2026-11-14"
updated: "2026-11-14"
depends_on: []
spec_refs: []
---

## Description

Change the running session composer's stop icon from red to an enabled neutral gray. Preserve its tap, long-press, confirmation, and disabled behavior.

## Outcome

Changed the composer stop icon to explicit Color.secondary, preserving control behavior. Built successfully with Xcode on the configured Mac; simulator checks confirmed neutral gray in light/dark appearances, enabled state, and unchanged stop confirmation.

Commit: Use neutral gray for the iOS inline stop button
