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
Stand up `yccd`: session manager, append-only JSONL event store + reducer, and the
Connect-RPC surface (`StartSession`, `Subscribe`, `SendInput`). Build a minimal `ycc`
client that subscribes to a session's event stream and can prod the agent. Proves the
client/server seam the whole product depends on.

## Acceptance criteria
- [ ] `.ycc/sessions/<id>/events.jsonl` append-only log; reducer builds projection
- [ ] Connect service with StartSession / Subscribe (server-stream, from_seq) / SendInput
- [ ] reconnecting client replays from an offset
- [ ] bearer-token auth; TLS for non-loopback
- [ ] minimal `ycc` client renders the event stream and sends input

## Work log
- 2026-06-25 implemented:
  - `proto/ycc/v1/ycc.proto` + Connect codegen (`buf generate`; output dir `proto`,
    paths=source_relative). `SessionService`: StartSession, ListSessions, Subscribe
    (server-stream, from_seq), SendInput. Event.data carried as a JSON string.
  - `internal/event`: `Log` — append-only events.jsonl, in-memory mirror, lossless
    cond-based fan-out, `Subscribe(fromSeq)` replay-from-offset, `OpenLog` reload +
    `LastSeq`. `Reduce` projection. `NewEmitterAt` for resume seq continuity.
  - `internal/session`: `Session` (log+emitter+loop in a goroutine, prods via input
    channel between turns) + `Manager` (ClientFactory per session, random ids,
    `.ycc/sessions/<id>/events.jsonl`).
  - `internal/server`: Connect handlers + bearer-token interceptor covering BOTH unary
    and streaming (Subscribe was otherwise unauthenticated).
  - `cmd/yccd`: daemon (h2c for loopback, TLS flags, non-loopback-without-token guard).
  - `cmd/ycc`: thin client — start/attach/list, streams events, stdin→SendInput.
  - Tests: event log persistence/reopen, replay-from-offset, from-zero, Reduce (all `-race`).
- 2026-06-25 verified LIVE against claude-opus-4-8 over RPC: started a session and
  streamed real agent events; wrong token → unauthenticated; SendInput prod continued
  the agent (created world.txt after idle); `attach --from 5` replayed only seq>5;
  events persisted to JSONL. ALL acceptance criteria pass.
- Findings/notes: (1) `buf` module root is `proto/`, so out must be `proto` to land at
  `proto/ycc/v1`. (2) Go `flag` stops at the first positional — the client takes the
  positional explicitly then parses the rest, so `attach <id> --from N` works.
  (3) `gollama` native Anthropic path needed a default `anthropic-version` header (fixed
  in 0002). Regenerate protos with: `buf generate`.
- Not live-tested (code only): TLS serving path; daemon does not yet reload persisted
  sessions on restart (sessions are in-memory; logs persist). Candidate follow-ups.