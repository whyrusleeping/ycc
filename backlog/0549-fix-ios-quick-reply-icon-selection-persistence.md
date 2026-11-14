---
id: "0549"
title: Fix iOS quick reply icon selection persistence
status: blocked
priority: 2
created: "2026-11-14"
updated: "2026-11-14"
depends_on: []
spec_refs: []
---

## Description
Blocked on deploying daemon icon support; no source change is needed for the reported persistence failure. An older daemon silently discards icons from newer clients, which then display the lightning-bolt fallback. The owner declined updating/restarting the running daemon on 2026-11-14 in the reporting session.

## Acceptance criteria
- A chosen icon survives saving and reopening Quick replies settings and is used in the quick reply menu.
- Global and project-specific replies retain their chosen icons.

## Work log
- 2026-11-14: Reproduced icon loss using an isolated instance of the older daemon binary. Current source retains icons in Set/Get round trips; icon support already landed in `c04daca`. `TestQuickRepliesRPC` passes, including global and project persistence across a fresh manager. The current iOS app builds successfully on macOS. No application code changed; deployment remains deferred.
