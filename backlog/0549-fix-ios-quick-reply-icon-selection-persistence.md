---
id: "0549"
title: Fix iOS quick reply icon selection persistence
status: done
priority: 2
created: "2026-11-14"
updated: "2026-11-27"
depends_on: []
spec_refs: []
---

## Description

Investigate reports that icons chosen in iOS Settings → Quick replies revert to the lightning-bolt fallback after saving.

## Acceptance criteria

- A chosen icon survives saving and reopening Quick replies settings and is used in the quick reply menu.
- Global and project-specific replies retain their chosen icons.

## Outcome

The old daemon dropped icons; c04daca preserves them and the RPC test covers global/project persistence across a fresh manager. After the daemon update the owner confirmed the icons work on device. No additional code change was required.

Commit: Close the iOS quick reply icon persistence investigation
