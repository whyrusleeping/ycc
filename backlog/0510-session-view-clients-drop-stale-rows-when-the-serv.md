---
id: "0510"
title: 'Session view: clients drop stale rows when the server rebuilds a session index'
status: done
priority: 3
created: "2026-11-09"
updated: "2026-11-10"
depends_on: []
spec_refs: []
---

## Description

Follow-up from PR #1 (session log holes). When `internal/sessionview` detects that a log's source changed (`sourceChanged` in `catchUpLocked`: different inode, truncation, head or boundary hash mismatch), it deletes every row and update for the session and re-indexes from scratch. It writes no tombstones and no generation signal. A client subscribed across that rebuild keeps receiving incremental updates against its old cursor. It can keep showing rows that no longer exist (or miss rows that were renumbered) until it happens to install a fresh snapshot.

## Acceptance criteria

- After an index rebuild, every subscribed client (web and iOS) converges to the rebuilt view without the user reloading: no leftover rows from before the rebuild and none missing.
- Normal incremental streaming, with no rebuild, is unchanged: no extra snapshots and no full reloads.
- If the fix needs a wire change, regenerate and commit all proto outputs (Go, Swift, web) and rebuild the web bundle.

## Outcome

sessionview records a random per-session index generation (persistent generations table, bumped before a rebuild's deletes; 0 until the first rebuild). SessionViewState.index_generation; SubscribeSessionViewRequest.optional index_generation; SessionViewUpdate.index_rebuilt. When a client names its generation, the server catches up at subscribe start and checks on every update and keepalive; a mismatch sends one index_rebuilt update and ends the stream. Clients that omit the generation stream exactly as before. Web and iOS then reinstall a full snapshot (with backoff) and resubscribe. They also drop expanded-row details and in-flight detail responses from the old generation, refetching a pending detail if one is still required. Tests: Go store/server, web controller (440 pass), YccKit swift test on the Mac (589 pass), and an iOS app build. Protos regenerated for Go/Swift/web, dist rebuilt, docs/remote-api.md updated.

Commit: Session view index generations: subscribers reset and reload a fresh snapshot after an index rebuild (web + iOS) (0510)
