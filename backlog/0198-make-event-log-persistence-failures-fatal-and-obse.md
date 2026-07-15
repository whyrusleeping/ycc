---
id: "0198"
title: Make event-log persistence failures fatal and observable
status: todo
priority: 1
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - Session & event log
    - Agent engine#API failure handling (classification, retry, session_error)
---

## Description
`event.Log.Record` currently increments the sequence and appends/broadcasts an event in memory even when JSON marshaling, file append, or `fsync` fails. Live clients can therefore observe state that is absent after restart, violating the event log's source-of-truth invariant.

Redesign the recorder/error path so a durable append failure cannot be treated as success. A session must stop or enter an explicit fatal/error state rather than continue with unreplayable in-memory history.

## Acceptance criteria
- [ ] JSON encoding, file write, and sync failures are surfaced to the owning session rather than only logged.
- [ ] An event is not exposed as durably recorded when its append did not succeed.
- [ ] Sequence handling remains monotonic and restart-safe after any failure.
- [ ] After a persistence failure the session cannot continue mutating from history that is absent on disk.
- [ ] Subscribers receive a clear terminal failure when feasible without pretending that failure itself was durably persisted to the broken log.
- [ ] Tests inject write and sync failures and verify no in-memory/disk divergence is reported as success.
- [ ] Normal append, replay, subscribe, and transient-broadcast behavior remains unchanged.
- [ ] `go test ./...` passes.

## Work log
