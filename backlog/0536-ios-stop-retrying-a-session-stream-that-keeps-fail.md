---
id: "0536"
title: 'iOS: stop retrying a session stream that keeps failing with the same server error'
status: todo
priority: 3
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs: []
---

## Description
Split from 0509, whose acceptance asked to check iOS and fix it there or file it separately. The web fix is in `clients/web/src/features/session/controller.ts`.

**Problem:** YccKit has the same endless-reconnect issue. `YccClient.mapSessionTransport` maps `.internalError` (and `.unknown`) to `YccError.rpc`, and `SessionViewModel.classify` treats every `.rpc` as `.transient`. A SessionView/SubscribeSessionView that fails with `internal` on every attempt (for example a broken session index) therefore sits in `.reconnecting` forever with backoff, and the server message never shows.

**Change (mirror the web):**
- Map `.internalError` (keep `.unknown`, which also covers network failures, transient) to a distinct "server" transport error that carries its message.
- In `startIndexedLoop` (and the legacy live loop, if it is still reachable), count consecutive identical server errors. Reset the count on a durable state update, not on a keepalive. After 3 repeats, end in `.failed(message)`.
- A manual retry or reopening the session retries from scratch, including a failed persisted reload.

## Acceptance criteria
- A session stream that fails with the same `internal` error on consecutive attempts ends in `.failed` showing the server message, and stops reconnecting.
- Network drops, `unavailable` and daemon restarts still reconnect without limit and resume from the cursor.
- A YccKit test covers both cases. `swift test` passes on the configured Mac.

## Work log
