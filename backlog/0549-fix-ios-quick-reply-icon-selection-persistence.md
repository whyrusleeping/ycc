---
id: "0549"
title: Fix iOS quick reply icon selection persistence
status: in_review
priority: 2
created: "2026-11-14"
updated: "2026-11-21"
depends_on: []
spec_refs: []
---

## Description
Current state: in review. The deployment blocker is cleared: the daemon on :8787 has run since 2026-11-19 03:38 UTC from a binary built after `c04daca` (quick-reply icons, 0545), and that binary has the icon code. What remains is a check on the device. Pick icons in iOS Settings → Quick replies, then confirm that they persist and appear in the swipe menu. The stored `~/.local/state/ycc/quick-replies.json` was last written on 2026-11-14 and holds no icons yet.

No source change is needed for the reported persistence failure. An older daemon silently discards icons from newer clients, which then display the lightning-bolt fallback. The owner declined updating/restarting the running daemon on 2026-11-14 in the reporting session.

## Acceptance criteria
- A chosen icon survives saving and reopening Quick replies settings and is used in the quick reply menu.
- Global and project-specific replies retain their chosen icons.

## Work log
- 2026-11-14: Reproduced icon loss using an isolated instance of the older daemon binary. Current source retains icons in Set/Get round trips; icon support already landed in `c04daca`. `TestQuickRepliesRPC` passes, including global and project persistence across a fresh manager. The current iOS app builds successfully on macOS. No application code changed; deployment remains deferred.
- 2026-11-21 groom: moved from blocked to in_review. The recorded blocker, deploying daemon icon support, is cleared. The :8787 daemon (pid 818970, started 2026-11-19 03:38 UTC) runs `~/go/bin/ycc`, built 2026-11-19 03:37 and containing `QuickReply.GetIcon` and `session.validQuickReplyIcon`. Added a current-state paragraph. The on-device check is still open.
