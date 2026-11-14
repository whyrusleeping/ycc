---
id: "0003"
title: Daemon, event log, and first client (M1)
status: done
priority: 2
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0002"]
spec_refs: ["System architecture", "Session & event log", "RPC protocol"]
---

## Description
M1 is the daemon/event-log/client milestone in the original spec roadmap (spec.md at 7365ee4; current spec.md no longer lists milestones).

Stand up `yccd`: session manager, append-only JSONL event store + reducer, and the
Connect-RPC surface (`StartSession`, `Subscribe`, `SendInput`). Build a minimal `ycc`
client that subscribes to a session's event stream and can prod the agent. Proves the
client/server seam the whole product depends on.

## Acceptance criteria
- [x] `.ycc/sessions/<id>/events.jsonl` append-only log; reducer builds projection
- [x] Connect service with StartSession / Subscribe (server-stream, from_seq) / SendInput
- [x] reconnecting client replays from an offset
- [x] bearer-token auth; TLS for non-loopback (TLS checked in code only, not live; see Outcome)
- [x] minimal `ycc` client renders the event stream and sends input

## Outcome

Implemented the append-only JSONL event log with replay/reduction, session manager, Connect RPC service, bearer authentication for unary and streaming calls, daemon serving paths, and the start/attach/list CLI. A live Claude RPC session verified streaming, authentication failure, input after idle, offset replay, and persisted events; TLS remained code-verified rather than live-tested, and daemon session reload remained deferred.

Commit: 7365ee4 — ycc: initialize workspace

## Work log
- 2026-11-14 groom: glossed M1 and checked off the criteria on this done task, as the Outcome reports. The TLS criterion is annotated as checked in code only.
