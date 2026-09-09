---
id: "0361"
title: Prevent infinite tool-call ID collision loops during session replay
status: done
priority: 1
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §5.1 Storage and durability
    - §7.1 Provider boundary and history
---

## Description
ReplayHistory.canon chooses call_len(idMap), then repeatedly assigns call_(len(idMap)+len(usedIDs)) on collision without advancing a counter. If both candidates already exist, reopening spins forever. Example: recorded valid IDs call_2 and call_4 followed by an empty/invalid ID. Evidence: internal/engine/replay.go:69–83. File was already dirty during review; preserve unrelated continuation changes.

## Acceptance criteria
- Generated canonical IDs always make progress and are unique and provider-valid while repeated references to one raw ID resolve consistently.
- Regression tests include the concrete collision sequence, multiple collisions, invalid/empty IDs, and normal valid IDs.
- Preserve matching of tool results, legacy positional repair, and replay history ordering.
- A malformed history cannot hang canonicalization; test the actual ReplayHistory path, not only a copied helper.
- Keep the change narrowly scoped.

## Outcome
- Canonical ID generation advances on every collision while preserving raw-ID mapping and result matching. Added ReplayHistory regression coverage for collisions, invalid/empty IDs, valid IDs, positional results, and ordering.
- Self-review accepted. Engine tests passed in the workspace and an isolated task-only checkout; the new regression timed out against the original implementation, confirming it catches the hang. Unrelated continuation changes preserved.
- Commit: `fix(engine): advance replay tool ID collision candidates`
