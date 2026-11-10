---
id: "0510"
title: 'Session view: clients drop stale rows when the server rebuilds a session index'
status: todo
priority: 3
created: "2026-11-09"
updated: "2026-11-09"
depends_on: []
spec_refs: []
---

## Description
Follow-up from PR #1 (session log holes). When `internal/sessionview` detects that a log's source changed (`sourceChanged` in `catchUpLocked`: different inode, truncation, head or boundary hash mismatch), it deletes every row and update for the session and re-indexes from scratch. It writes no tombstones and no generation signal. A client subscribed across that rebuild keeps receiving incremental updates against its old cursor. It can keep showing rows that no longer exist (or miss rows that were renumbered) until it happens to install a fresh snapshot.

## Acceptance criteria
- After an index rebuild, every subscribed client (web and iOS) converges to the rebuilt view without the user reloading: no leftover rows from before the rebuild and none missing.
- Normal incremental streaming, with no rebuild, is unchanged: no extra snapshots and no full reloads.
- If the fix needs a wire change, regenerate and commit all proto outputs (Go, Swift, web) and rebuild the web bundle.

## Work log
