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

Current state: in review. The deployment blocker is cleared: the daemon on :8787 has run since 2026-11-19 03:38 UTC from a binary built after `c04daca` (quick-reply icons, 0545), and that binary has the icon code. What remains is a check on the device. Pick icons in iOS Settings → Quick replies, then confirm that they persist and appear in the swipe menu. The stored `~/.local/state/ycc/quick-replies.json` was last written on 2026-11-14 and holds no icons yet.

No source change is needed for the reported persistence failure. An older daemon silently discards icons from newer clients, which then display the lightning-bolt fallback. The owner declined updating/restarting the running daemon on 2026-11-14 in the reporting session.

## Acceptance criteria

- A chosen icon survives saving and reopening Quick replies settings and is used in the quick reply menu.
- Global and project-specific replies retain their chosen icons.

## Outcome

The old daemon dropped icons; c04daca preserves them and the RPC test covers global/project persistence across a fresh manager. After the daemon update the owner confirmed the icons work on device. No additional code change was required.

Commit: Close the iOS quick reply icon persistence investigation
