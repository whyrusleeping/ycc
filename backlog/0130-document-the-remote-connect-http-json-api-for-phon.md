---
id: "0130"
title: Document the remote Connect HTTP/JSON API for phone clients
status: todo
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

## Work log
