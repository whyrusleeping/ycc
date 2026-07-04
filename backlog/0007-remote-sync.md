---
id: "0007"
title: Remote access (M5) — verify + harden the direct-dial remote client path
status: done
priority: 3
created: "2026-06-25"
updated: "2026-07-04"
depends_on:
    - "0006"
spec_refs:
    - Persistence & remote sync
    - RPC protocol
---

## Description
M5 rescoped (2026-07-07 pm session with user; spec §14/§16 updated). Daemon-to-daemon
log sync/replication is **dropped**: remote observation and prodding happen by dialing
the workspace daemon's Connect endpoint directly. Deployment model is a private network
(Tailscale/VPN) with the bearer token required on non-loopback binds; TLS optional.

Most of the plumbing already exists: bearer-token auth interceptor on every RPC, the
daemon refuses non-loopback binds without a token (and warns when non-loopback without
TLS), `daemon.DialClient` speaks h2c/TLS with the bearer token, `Subscribe(from_seq)`
replays from an offset, and `SendInput`/`AnswerQuestion(s)`/`Interrupt`/`Resume`/
`StopSession` cover prodding. What this task adds is **end-to-end verification and
hardening of that remote path** so it is a supported, tested configuration rather than
an untested one:

- An automated e2e test exercising the remote shape: daemon bound with a token
  (loopback bind is fine as a stand-in for the tailnet — the shape under test is
  token-auth'd h2c dialing, not routing), a client dialing via `daemon.DialClient`
  with the token: `Subscribe(from_seq)` replay + live tail, `SendInput`,
  `AnswerQuestion` round-trip; and rejection (unauthenticated / wrong token) on both
  unary and streaming RPCs.
- Verify the Connect **HTTP/JSON** protocol path works for the phone-relevant RPCs,
  including server-streaming `Subscribe` with the connect+json streaming envelope
  (curl-able) — this grounds the API doc (task 0130).
- Hardening review of the non-loopback path: startup guardrails and log messages match
  the spec (§12, §14); `ycc -addr` attach flow works against a token'd daemon; document
  flags in `--help` where thin.
- A saved runbook (plans/) for manually smoke-testing remote access over a real
  tailnet, so it can be replayed when the environment allows.

## Acceptance criteria
- [ ] e2e test: token'd daemon + remote-shaped client — Subscribe(from_seq) replay +
      live events, SendInput, AnswerQuestion all pass; unauthenticated and wrong-token
      requests are rejected on unary AND streaming RPCs
- [ ] verified (test or recorded transcript) that Subscribe works over the Connect
      JSON protocol with curl, and at least one unary RPC (e.g. ListSessions) via
      plain HTTP/JSON + bearer header
- [ ] non-loopback guardrails confirmed: refuse bind without token; cleartext warning
      without TLS; behavior documented in `ycc daemon --help` text
- [ ] `ycc -addr <url>` with a token attaches and drives a session end to end
- [ ] saved plan (runbook) for manual tailnet smoke-testing
- [ ] single-writer invariant needs no new code (remote clients only issue RPCs) —
      asserted in the spec and unchanged by this task

## Plan

Rescoped M5 (user decisions 2026-07-07): drop daemon-to-daemon log sync entirely; remote = client dials the workspace daemon directly over a private network (Tailscale/VPN), bearer token required on non-loopback binds, TLS optional; phone surface = documented Connect HTTP/JSON protocol (task 0130 covers the doc; this task produces the verified behavior it documents).

Steps:
1. e2e remote-shape test (new test, likely internal/daemon or internal/server): start the daemon serve path with a Token set (loopback bind stands in for the tailnet — what's under test is token-auth'd h2c dialing, not routing). Using daemon.DialClient(addr, token): StartSession, Subscribe(from_seq=0) replay + live tail, SendInput, AnswerQuestion round-trip. Negative cases: no token and wrong token rejected with CodeUnauthenticated on a unary RPC AND on streaming Subscribe (first recv errors).
2. Connect HTTP/JSON verification: in the same test (or a focused one), issue a plain HTTP POST /ycc.v1.SessionService/ListSessions with application/json + bearer header and assert a JSON response; consume Subscribe via the connect+json streaming envelope (raw http client) to prove the curl path works. Capture working example requests/responses — they seed task 0130's doc.
3. Hardening review: confirm serve.go guardrails match spec §12/§14 (refuse non-loopback bind without token; cleartext warning without TLS); make sure `ycc daemon` help text documents addr/token/tls flags; verify `ycc -addr <url>` + token attaches (manual or covered by the e2e shape).
4. Save a runbook (save_plan, plans/remote-access-smoke.md): manual tailnet smoke procedure — start `ycc daemon` with token on the tailscale interface, attach with `ycc -addr`, curl a unary RPC and Subscribe from another machine/phone.
5. No new code for single-writer: assert in the report that remote clients only issue RPCs; spec already updated.

Out of scope: any push/pull replication, REST/SSE facade, cert auto-generation (tailnet model), the API doc itself (task 0130, depends on this).

### Starting points
- internal/daemon/serve.go — non-loopback guardrails (refuses bind without Token; TLS warning), Options{Addr,Token,TLSCert,TLSKey}
- internal/daemon/client.go — DialClient(addr, token), bearer interceptor covers unary + streaming, h2c for http://
- internal/server/auth.go — NewAuthInterceptor; check streaming coverage
- internal/server/subscribe_transient_test.go + workstream_rpc_test.go — existing patterns for spinning up an httptest server with h2c and dialing it
- proto/ycc/v1/ycc.proto — SessionService method list for the curl examples
- Connect JSON streaming: POST with Content-Type application/connect+json, enveloped frames — verify with curl against Subscribe

## Work log
- 2026-07-07 pm scoping with user: M5 rescoped. Decisions: (1) drop daemon-to-daemon
  log sync — direct dial only; (2) deployment is Tailscale/VPN, token-only, TLS
  optional; (3) phone surface is documentation of the Connect HTTP/JSON protocol (no
  REST/SSE facade) — split out as task 0130 (docs, depends on this task's verified
  examples). Spec §14/§16/§12 updated. Unblocked; priority raised 5→3.
- 2026-07-05 pm grooming with user: confirmed keep parked — not a priority yet. Stays
  blocked deliberately; revisit when remote/phone access becomes a goal.
- 2026-07-02 blocked: parked for the overnight autonomous run — milestone-sized (M5) and underspecified (sync target/protocol undecided); needs a scoping/design pass with the user before implementation. Unblock after that pass splits it into concrete tasks.
- 2026-07-04 plan: Rescoped M5 (user decisions 2026-07-07): drop daemon-to-daemon log sync entirely; remote = client dials the workspace daemon directly over a private network (Tailscale/VPN), bearer token required on n
…[truncated]
- 2026-07-04 context hints: 6 recorded with plan
- 2026-07-04 context hints: internal/daemon/serve.go — buildHandler/Serve/StartInProcess, guardrails at Serve() lines ~120-128, isLoopback at bottom; internal/daemon/client.go — DialClient, bearer interceptor (unary+streamin
…[truncated]
- 2026-07-04 implementer report: Verified and hardened the direct-dial remote client path (M5 rescoped: no daemon-to-daemon sync; remote = dial the workspace daemon with a bearer token over a private network). All code paths already 
…[truncated]
- 2026-07-04 review tier: single-opus — reviewers: Claude
- 2026-07-04 review (Claude): revise — The change is a high-quality verification/hardening pass: a thorough e2e test (Subscribe from_seq replay + live tail, SendInput, AnswerQuestion, ListSessions over daemon.DialClient with the bearer tok
…[truncated]
- 2026-07-04 revision: Fixed the guardrail gap the reviewers found: an empty/wildcard host was misclassified as loopback, which would let a token-free daemon bind all interfaces.  ## Changes 1. **internal/daemon/serve.go `i
…[truncated]
- 2026-07-04 review (Claude): accept — The revision fixes the loopback-classification gap I flagged: internal/daemon/serve.go isLoopback now returns false for empty-host/wildcard binds (":8787", "") — so a token-free daemon can no longer
…[truncated]
- 2026-07-04 decision: accept — commit: remote access (M5): e2e remote-client tests, Connect HTTP/JSON verification, wildcard-bind guardrail fix, daemon help text, tailnet smoke runbook (task 0007)
