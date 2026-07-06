---
id: "0145"
title: 'Design spike: embedded web client served by the daemon (+ optional tsnet)'
status: done
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 14. Persistence & remote sync
    - docs/remote-api.md#Overview
---

## Description
Design spike (docs/design/ doc first, like parallel-workstreams). The daemon already speaks Connect's plain HTTP/JSON with a documented endpoint catalog (docs/remote-api.md) — the phone story is "someone builds a client someday." A minimal embedded web client (single static SPA served by the daemon behind the existing bearer auth) would make remote observation/answering real *today* with no app-store story: `ycc daemon --web` + Tailscale = full phone access.

Scope for a first cut: session list → live event stream (Subscribe over fetch/SSE-style streaming) → send input / answer question pickers / interrupt / stop. Read-mostly; not a full TUI clone.

Also consider in the same spike: embedding Tailscale via tsnet (`ycc daemon --ts-hostname ycc`) so remote access needs zero port/firewall/TLS thought — identity comes from the tailnet.

## Acceptance criteria
- [ ] A design doc (docs/design/web-client.md) covering: asset embedding (go:embed), auth handoff (bearer token entry, or tsnet identity), which RPCs the first cut uses, streaming approach, and phone-form-factor layout of the event stream.
- [ ] Explicit decision on tsnet embedding (in-scope flag vs. documentation-only recommendation).
- [ ] Follow-on implementation tasks filed from the doc.

## Plan

Deliverable: a design doc `docs/design/web-client.md` (no code), modeled on docs/design/parallel-workstreams.md (Status: proposal header, grounded in spec refs), plus follow-on implementation tasks filed from it (coordinator files those via the backlog after the doc is accepted).

Doc must cover, with concrete decisions:

1. **Context/goals/non-goals** — daemon already serves Connect plain HTTP/JSON (docs/remote-api.md); goal is a minimal embedded web client so `ycc daemon --web` + Tailscale = phone access today. Non-goals: full TUI clone, app-store clients, offline support, daemon-to-daemon sync.

2. **Asset embedding** — decide: a dependency-free, no-npm-build vanilla JS/HTML/CSS SPA embedded with `go:embed` (`internal/web/dist` or similar), served from the existing mux in `internal/daemon/serve.go buildHandler` under `/` (Connect handler stays at `/ycc.v1.SessionService/`). Gate behind a `--web` flag on `ycc daemon` (default off). Weigh the alternative (TypeScript + connect-es + npm build step) and reject it for the first cut: the Connect JSON framing is trivial (5-byte envelope, already documented in remote-api.md) and keeping the repo free of a JS toolchain keeps `go install` clean. Note the escape hatch: if the SPA grows, a committed pre-built dist/ or an optional npm build can come later.

3. **Auth handoff** — static assets are served UNauthenticated (a browser can't attach a bearer header to the initial page load; the assets are not sensitive). All RPCs remain behind the existing bearer AuthInterceptor. First run: the SPA shows a token-entry screen, stores the token in localStorage, sends `Authorization: Bearer` on every fetch. Cover the security posture: intended deployment is tailnet/private network (same as remote-api.md); note XSS/token-in-localStorage tradeoff and why it's acceptable here (single-user, private network); mention `--tls` for non-tailnet use. Note the guardrail interplay: `--web` on a non-loopback bind still requires a token (existing Serve() guardrail is unchanged).

4. **RPC surface for the first cut** — read-mostly + prodding: ListProjects, ListSessionHistory (session browser), GetSessionTranscript (dead sessions), Subscribe (live stream), SendInput, AnswerQuestion/AnswerQuestions, Interrupt/Resume, StopSession. Explicitly out of first cut: StartSession/ResumeSession (maybe phase 2), backlog browsing (ListBacklog/GetTask — nice phase 2), GetUsage/GetBudget, Notify.

5. **Streaming approach** — `fetch()` + `ReadableStream` reader parsing the connect+json envelope (flag byte + big-endian u32 length + JSON) exactly as documented in docs/remote-api.md; handle data frames (Event) and end-of-stream frame (0x02, trailer JSON). Reconnect with `fromSeq = last persisted seq`; transient `turn_delta` events (seq 0) render as the live tail row and never advance the cursor. Mention why not SSE/WebSocket (no extra facade — spec §14 says the Connect surface IS the phone API), and that HTTP/1.1 works (no h2 requirement) per remote-api.md.

6. **Phone-form-factor layout** — two screens: (a) session list (project filter chips, status/waitingInput badges, most-recent-first from ListSessionHistory), (b) session view: single-column event feed folding the event model (user_input, model_turn, thinking collapsed, tool_call/tool_result collapsed one-liners, question_asked rendered as an answer picker bottom-sheet wired to AnswerQuestion(s), session_idle/error banners), sticky bottom input bar (SendInput) with interrupt/stop actions. Auto-scroll with "jump to latest" affordance.

7. **tsnet decision (explicit, required by acceptance criteria)** — DECIDE: documentation-only recommendation for now; no tsnet embedding in the first cut. Rationale: tsnet pulls the full Tailscale dependency tree into the ycc binary, host-level tailscaled already covers the deployment story, and the guardrails/auth model doesn't change either way. Analyze what embedding would look like (`--ts-hostname ycc`, tsnet.Listen, identity-based auth replacing the bearer token) and file it as a proposed follow-on rather than in-scope.

8. **Follow-on tasks section** — enumerate the implementation tasks the doc implies (the coordinator will file them in the backlog): (i) `--web` flag + go:embed asset serving + tests; (ii) SPA first cut: token entry, session list, live event stream; (iii) SPA interactions: input/answer/interrupt/stop; (iv) proposed: tsnet embedding.

Verification: doc reads coherently against docs/remote-api.md (endpoint names, framing details, guardrails match reality — check internal/daemon/serve.go and internal/server/auth.go); `gofmt`-irrelevant (docs only); no code changes in the diff.

### Starting points
- docs/remote-api.md — authoritative endpoint catalog, framing, auth guardrails; the doc must stay consistent with it
- docs/design/parallel-workstreams.md — model for structure/tone of design docs (Status header, numbered sections, decision records)
- internal/daemon/serve.go — buildHandler/Serve: where mux, auth interceptor, and the non-loopback token guardrail live; where --web asset serving would hook in
- internal/server/auth.go — bearer AuthInterceptor (applies to RPCs only)
- cmd/ — `ycc daemon` flag definitions (Options: Addr/Token/TLSCert/TLSKey)
- spec.md §12 (RPC protocol), §14 (persistence & remote sync — 'no separate facade' decision), §18 (TUI event rendering, useful as reference for what the web feed folds)

## Work log
- 2026-07-06 plan: Deliverable: a design doc `docs/design/web-client.md` (no code), modeled on docs/design/parallel-workstreams.md (Status: proposal header, grounded in spec refs), plus follow-on implementation tasks fi
…[truncated]
- 2026-07-06 context hints: 6 recorded with plan
- 2026-07-06 context hints: docs/remote-api.md — authoritative endpoint catalog, connect+json framing, auth guardrails; keep the doc consistent with it; docs/design/parallel-workstreams.md — structural model for design docs 
…[truncated]
- 2026-07-06 implementer report: Wrote the design spike `docs/design/web-client.md` (docs only — no code changes; the two modified backlog files are pre-existing task-state edits, not from me).  The doc follows the parallel-workstr
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — Task 0145 is a design spike whose deliverable is a single design doc (docs/design/web-client.md) plus backlog task-state edits; there are no code changes, as required. The doc follows the parallel-wor
…[truncated]
- 2026-07-06 decision: accept — commit: docs: design spike for embedded web client served by the daemon (task 0145)  Adds docs/design/web-client.md: go:embed vanilla SPA behind `ycc daemon --web`, unauthenticated assets + bearer-token RPC a
…[truncated]
- 2026-07-06 usage: 26,672 tok (in 78, out 26,594, cache_r 1,297,816, cache_w 122,942) · cost n/a (unpriced)
  coordinator: 13,229 tok (in 34, out 13,195, cache_r 765,875, cache_w 50,265) · cost n/a (unpriced)
  implementer: 11,200 tok (in 24, out 11,176, cache_r 403,290, cache_w 50,865) · cost n/a (unpriced)
  reviewer:Claude: 2,243 tok (in 20, out 2,223, cache_r 128,651, cache_w 21,812) · cost n/a (unpriced)
