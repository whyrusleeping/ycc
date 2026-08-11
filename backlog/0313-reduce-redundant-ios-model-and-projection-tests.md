---
id: "0313"
title: Reduce redundant iOS model and projection tests
status: done
priority: 3
created: "2026-08-10"
updated: "2026-08-11"
depends_on:
    - "0310"
spec_refs:
    - CONTRIBUTING.md#Tests
    - docs/design/ios-client.md#Verification strategy
---

## Description
Audit YccKit tests for duplicated mapping, literal-default, formatting, and one-assert property coverage. Keep high-signal tests for async state machines, navigation, projection correctness, interaction races, and observed iOS regressions.

## Acceptance criteria
- Tests of trivial assignments, static labels, and the same mapping repeated across model/projection/view-model layers are removed or consolidated.
- Async ordering, session projection, question answering, navigation dedupe, composer regressions, work-loop state, and error recovery retain focused coverage.
- Tests assert durable behavior rather than incidental display prose where possible.
- No coverage percentage or deletion quota is introduced.
- `swift test` passes in clients/ios/YccKit on a Swift-capable machine.

## Plan

1. Inventory the YccKit test suite and classify tests by regression value: preserve async ordering/state transitions, transcript/session projections, question answering, navigation dedupe, composer regressions, work-loop state, error recovery, auth, persistence, parsing, and observed bug coverage; identify trivial literal/default/label assertions and behavior duplicated across model/projection layers.
2. Remove or consolidate only low-value duplicated tests, keeping each durable behavior covered at its lowest useful boundary. Prefer table-driven consolidation where it improves signal, but do not introduce a deletion or coverage quota and do not change production behavior.
3. Re-read the resulting suite against every acceptance-criteria category and inspect the diff for accidental loss of meaningful edge/race/error cases.
4. Run `swift test` in `clients/ios/YccKit` if Swift is available; otherwise report the environment limitation and perform available static checks (including ensuring test sources remain syntactically coherent and no unrelated files changed).

### Starting points
- clients/ios/YccKit/Tests/YccKitTests/ contains the audit scope; prioritize large model suites such as BacklogModelTests, NewSessionModelTests, SessionListModelTests, and mapping/formatting-only suites.
- CONTRIBUTING.md#Tests defines the lean-test standard.
- docs/design/ios-client.md#Verification strategy identifies projection, request-shaping, and connection-store coverage to preserve.
- The worktree already has unrelated uncommitted changes outside clients/ios/YccKit tests; do not modify or revert them.

## Work log
- 2026-08-11 plan: 1. Inventory the YccKit test suite and classify tests by regression value: preserve async ordering/state transitions, transcript/session projections, question answering, navigation dedupe, composer re
…[truncated]
- 2026-08-11 context hints: 4 recorded with plan
- 2026-08-11 context hints: Audit all files under clients/ios/YccKit/Tests/YccKitTests, not just the largest ones; production code should remain unchanged.; Preserve focused coverage for the specific categories named in the acce
…[truncated]
- 2026-08-11 preload: 3 file(s), ~7 KiB seeded into implementer context
- 2026-08-11 implementer report: Implemented task 0313 entirely within `clients/ios/YccKit/Tests/YccKitTests` (no production changes).  Changes: - Removed the static-label-only `GitSyncBadgeTests` suite and pruned literal title/badge
…[truncated]
- 2026-08-11 review tier: high-powered — reviewers: sol, claude
- 2026-08-11 review (sol): accept — The test-only change satisfies task 0313: it removes or consolidates low-value literal, formatting, direct-assignment, and duplicate-layer assertions while retaining focused coverage for projection/re
…[truncated]
- 2026-08-11 review (claude): accept — The change prunes literal-default, static-label, formatting-prose, and cross-layer duplicate tests across 19 YccKit test files (plus deleting the label-only GitSyncBadgeTests), while consolidating rat
…[truncated]
- 2026-08-11 decision: accept — commit: Prune redundant iOS model tests
