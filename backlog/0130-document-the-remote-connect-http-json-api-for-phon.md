---
id: "0130"
title: Document the remote Connect HTTP/JSON API for phone clients
status: done
priority: 3
created: "2026-07-04"
updated: "2026-07-04"
depends_on:
    - "0007"
spec_refs:
    - Persistence & remote sync
    - RPC protocol
---

## Description
Second half of the rescoped M5 (see task 0007 and spec §14): the phone-facing surface is
**documentation only** — no REST/SSE facade. Connect already serves plain HTTP/JSON from
the same handlers, and official client libs (connect-swift, connect-kotlin, connect-es)
cover future phone/web clients. This task writes the doc that makes that surface usable:
`docs/remote-api.md` (linked from spec §14), grounded in the verified examples produced
by task 0007.

Contents:
- **Connection & auth**: base URL, `Authorization: Bearer <token>` header, h2c vs TLS,
  the tailnet deployment model, and how the daemon is started for remote access
  (`ycc daemon` flags: addr/token/tls).
- **Protocol primer**: Connect unary = `POST /ycc.v1.SessionService/<Method>` with a
  JSON body (`Content-Type: application/json`); server-streaming (`Subscribe`,
  `CaptureBacklogItem`) uses the connect+json streaming envelope — show what a curl
  consumer sees and note the official client libs handle framing.
- **Endpoint catalog** for the phone-relevant RPCs, each with a curl example and example
  response: ListProjects, ListSessions, ListSessionHistory, GetSessionTranscript,
  StartSession, Subscribe (incl. `from_seq` resume semantics and tolerating seq-less
  transient `turn_delta` events), SendInput, AnswerQuestion/AnswerQuestions, Interrupt,
  Resume, StopSession, ResumeSession, ListBacklog, GetTask, GetUsage.
- **Event model summary** for client authors: event shape (§5.2), replay-from-seq,
  transient events, and the "UI is a projection of the log" rule.

## Acceptance criteria
- [ ] `docs/remote-api.md` exists, linked from spec §14, and added to the docs set if a
      `doc_globs` config is in use
- [ ] every listed endpoint has a request/response example; at least Subscribe and one
      unary example are copy-paste-verified against a running daemon (from task 0007's
      verification work)
- [ ] auth, streaming envelope, from_seq resume, and transient-event tolerance are all
      explicitly covered
- [ ] doc states the decided deployment model (tailnet/VPN + token, TLS optional) and
      that no separate REST facade exists

## Plan

Write `docs/remote-api.md` — the phone-facing Connect HTTP/JSON API doc — grounded in task 0007's verified behavior, and link it from spec §14.

1. **Doc structure** (`docs/remote-api.md`):
   - *Connection & auth*: base URL, `Authorization: Bearer <token>` header, how the daemon is started for remote access (`ycc daemon --addr <ip:port> --token T` or `YCC_TOKEN`; `--tls-cert/--tls-key` optional), the tailnet/VPN deployment model, the guardrails (refuses non-loopback bind without a token; cleartext warning without TLS), h2c vs TLS, and that unauthenticated/wrong-token requests get HTTP 401 with a Connect error JSON body (`{"code":"unauthenticated",...}`) on unary AND streaming RPCs.
   - *Protocol primer*: every RPC is `POST <base>/ycc.v1.SessionService/<Method>`; unary = `Content-Type: application/json` with a protojson body; server-streaming (`Subscribe`, `CaptureBacklogItem`) = `Content-Type: application/connect+json` with 5-byte enveloped frames (flag byte + big-endian u32 length + JSON payload; data frames flag 0x00, end-of-stream frame flag 0x02 carrying the trailer JSON). Note protojson conventions clients must know: lowerCamelCase field names (proto snake_case also accepted on requests), int64 rendered as JSON strings (`"seq":"128"`), and `data_json` being an embedded JSON *string*. Note official client libs (connect-swift, connect-kotlin, connect-es) handle framing; curl works for unary and (with a hand-built envelope) streaming. Explicitly: **no separate REST/SSE facade**.
   - *Endpoint catalog* with a curl request + example JSON response for each phone-relevant RPC: ListProjects, ListSessions, ListSessionHistory, GetSessionTranscript, StartSession, Subscribe (with `from_seq` resume semantics: replay events with seq > from_seq then live tail; reconnect resumes from the last persisted seq; tolerate seq-less transient events), SendInput, AnswerQuestion, AnswerQuestions, Interrupt, Resume, StopSession, ResumeSession, ListBacklog, GetTask, GetUsage. Field shapes come from `proto/ycc/v1/ycc.proto`; error codes worth noting (NotFound for unknown session, FailedPrecondition for AnswerQuestion with no pending question — both verified in the e2e tests).
   - *Event model summary* for client authors: event shape per spec §5.2 (seq/ts/actor/type/data_json), the event type table pointer, replay-from-seq, transient events (`transient:true`, `seq:0`, never persisted, lossy, must be tolerated; `turn_delta` snapshot semantics), and the "UI is a projection of the log" rule.
2. **Copy-paste verification**: build `ycc`, start a real daemon on loopback with a token (temp workspace; a dummy model config is fine — no live model needed), seed a persisted session (events.jsonl + ResumeSession, mirroring `seedSession` in remote_e2e_test.go), then actually run at least the ListSessions unary curl and the Subscribe connect+json curl from the doc and confirm the documented output matches reality. Record the transcript in the implementer report / work log. (The automated e2e tests in internal/server/remote_e2e_test.go already cover the same shapes.)
3. **Spec link**: in spec.md §14, replace/extend the "documented HTTP/JSON endpoint set (see task 0130)" sentence to link `docs/remote-api.md`.
4. **doc_globs**: no `.ycc/config.toml` is committed in this repo (.ycc is gitignored), so no docs-set config change is needed — note this in the report.
5. Run `go build ./... && go test ./...` (or at least the server package) to confirm nothing regressed (doc-only change plus spec edit).

### Starting points
- proto/ycc/v1/ycc.proto — SessionService + all message shapes (field names/comments for the catalog)
- internal/server/remote_e2e_test.go — verified curl-equivalent examples: JSON unary ListSessions, connect+json Subscribe envelope (writeEnvelope/readEnvelope), auth rejection codes
- plans/remote-access-smoke.md — working curl commands incl. the shell one-liner that builds the Subscribe request envelope
- spec.md §14 (~line 826-838) — deployment model text + the '(see task 0130)' sentence to turn into a doc link
- spec.md §5.2 (~line 157-221) — event shape, types table, transient events + turn_delta contract
- spec.md §12 (~line 683-687) — from_seq + auth summary
- cmd/ycc/main.go — ycc daemon flags (--addr/--token/--tls-cert/--tls-key, YCC_TOKEN) and `ycc --addr --token` attach usage
- internal/daemon/serve.go — guardrails: refuse non-loopback bind without token, cleartext warning

## Work log
- 2026-07-04 plan: Write `docs/remote-api.md` — the phone-facing Connect HTTP/JSON API doc — grounded in task 0007's verified behavior, and link it from spec §14.  1. **Doc structure** (`docs/remote-api.md`):    - 
…[truncated]
- 2026-07-04 context hints: 8 recorded with plan
- 2026-07-04 context hints: proto/ycc/v1/ycc.proto — SessionService + all message shapes (authoritative field names for examples); internal/server/remote_e2e_test.go — verified shapes: JSON unary ListSessions (200 + {"sessio
…[truncated]
- 2026-07-04 implementer report: Wrote the phone-facing Connect HTTP/JSON API doc and linked it from the spec.  ## Changes - **`docs/remote-api.md`** (new, ~21 KB): the phone/web client documentation for the ycc workspace daemon. Sec
…[truncated]
- 2026-07-04 review tier: single-opus — reviewers: Claude
- 2026-07-04 review (Claude): accept — docs/remote-api.md is a thorough, accurate phone-facing Connect HTTP/JSON API doc covering connection/auth, the deployment model (tailnet + token, TLS optional, no REST/SSE facade), the unary and conn
…[truncated]
- 2026-07-04 decision: accept — commit: docs: remote Connect HTTP/JSON API for phone clients (task 0130)  - new docs/remote-api.md: connection/auth (tailnet + bearer token, TLS   optional, guardrails), Connect protocol primer (unary JSON + 
…[truncated]
