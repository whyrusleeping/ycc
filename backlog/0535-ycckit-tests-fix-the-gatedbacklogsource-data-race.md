---
id: "0535"
title: 'YccKit tests: fix the GatedBacklogSource data race that intermittently crashes swift test'
status: done
priority: 4
created: "2026-11-10"
updated: "2026-11-22"
depends_on: []
spec_refs: []
---

## Description

`swift test` in clients/ios/YccKit occasionally dies with signal 11 (seen 2026-11-10 on the configured Mac, one run in six; earlier xctest crash reports on 2026-11-09 too). The crash report shows `_swift_release_dealloc` inside `Array.subscript.modify` in `GatedBacklogSource.updateTaskStatus(project:id:status:)`, called from `BacklogOptimisticTests.testStatusChangeMovesTheRowAtOnceAndOnlyBusiesThatRow` (OptimisticMutationTests.swift). The `@unchecked Sendable` mock mutates an array from concurrent tasks without a lock.

**Acceptance:** the mock's shared state is synchronized (lock or actor), and `swift test` passes in 10 consecutive runs on the configured Mac.

## Outcome

GatedBacklogSource (OptimisticMutationTests.swift) now guards all shared state with an NSLock, matching AsyncGate. updateTaskStatus releases it before awaiting the gate. On the configured Mac, 10 consecutive full swift test runs passed (665 tests, 0 failures each).

Commit: YccKit tests: synchronize GatedBacklogSource state with a lock (0535)
