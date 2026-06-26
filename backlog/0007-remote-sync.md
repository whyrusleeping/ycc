---
id: "0007"
title: Remote session sync + phone-facing surface (M5)
status: todo
priority: 5
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0006"]
spec_refs: ["Persistence & remote sync", "RPC protocol"]
---

## Description
Let a session be observed and prodded from another machine or a future phone app. The
workspace daemon stays the single writer of workspace mutations; remote clients append
only input events, serialized by the workspace daemon. Expose the HTTP/JSON surface
Connect already provides so a phone app can talk to it without a gRPC stack.

## Acceptance criteria
- [ ] push/pull of per-session event logs after a given seq
- [ ] remote client can Subscribe + SendInput + AnswerQuestion over TLS
- [ ] single-writer invariant preserved (workspace daemon serializes input)
- [ ] documented HTTP/JSON endpoints for the future phone app

## Work log
