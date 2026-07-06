# Design: Embedded web client served by the daemon (+ optional tsnet)

> Status: **proposal** (design spike, task 0145). No code lands with this doc.
> Grounded in the current architecture: spec §12 (RPC protocol — the daemon
> serves Connect handlers), §14 ("no separate REST/SSE facade" — the Connect
> surface *is* the phone API), and §18 (client UI / event rendering, the
> reference for what a web feed folds). Consistent with the authoritative wire
> contract in [`docs/remote-api.md`](../remote-api.md) and the serving/auth
> guardrails in `internal/daemon/serve.go` and `internal/server/auth.go`.

## 1. Context / problem

The daemon already speaks a complete remote API: plain HTTP/JSON served directly
from the same [Connect](https://connectrpc.com) handlers the TUI uses. Spec §14
decided there is **no separate REST/SSE facade**:

> Remote observation and prodding happen by dialing the workspace daemon's
> Connect endpoint directly — `Subscribe(from_seq)` already *is* "ship events
> after seq N".

That surface is documented, verified, and reachable from `curl` today
([`docs/remote-api.md`](../remote-api.md)). What is missing is a *client a human
can actually use from a phone*. The remote-api doc's own framing is that
official Connect client libs exist "for someone to build a client someday" —
there is no shipping client, so remote observation/answering is real only for
people who write code against the RPC surface.

A minimal **embedded web client**, a single static SPA served by the daemon
behind the existing bearer auth, closes that gap with no app-store story and no
extra moving parts:

```
ycc daemon --web            # serve the SPA alongside the Connect handlers
# + Tailscale/VPN           # reachability
# = full phone access today: watch sessions, answer questions, prod, stop.
```

The design question: **how do we serve a phone-usable client from the daemon
without adding a build toolchain, a second protocol, or a new auth model** — i.e.
reusing the Connect surface and guardrails that already exist.

## 2. Goals & non-goals

**Goals**

- A phone-first web client the daemon serves itself: session list → live event
  stream → *prod* (send input, answer questions, interrupt/resume, stop).
- Zero extra deployment steps beyond one flag: `ycc daemon --web`. No npm, no
  bundler, no separate static host, no change to `go install`.
- Reuse the existing Connect RPC surface **unchanged** — the web client is just
  another client of the documented API.
- Reuse the existing auth + non-loopback guardrails **unchanged**.

**Non-goals**

- A full TUI clone. No doc browser, no config/settings editor, no backlog
  authoring, no usage dashboards in the first cut (spec §18.2/§18.5 features).
- Native apps (that is what connect-swift / connect-kotlin are for).
- Offline support / local caching beyond the auth token.
- Multi-user auth, per-user identity, RBAC. This stays a single-user tool.
- Any change to the RPC/proto surface or the event model. If the first cut needs
  a new RPC, that is out of scope and a signal to reconsider.
- Daemon-to-daemon sync or log replication (spec §14 — already decided against).

## 3. Asset embedding (decision)

**Decision: a dependency-free vanilla HTML/CSS/JS single-page app — a handful of
static files, no framework, no npm/bundler build step — embedded via `go:embed`
and served from the daemon's existing mux, gated behind a new `--web` flag.**

Shape:

- A new `internal/web` package owns the static assets (`internal/web/dist/` or
  similar: `index.html`, one `app.js`, one `app.css`) and exposes an
  `http.Handler` over the embedded FS:

  ```go
  //go:embed dist
  var assets embed.FS
  func Handler() http.Handler { /* http.FileServerFS(sub("dist")) */ }
  ```

- It mounts on the **existing** mux in `buildHandler` (`internal/daemon/serve.go`).
  Today that mux registers exactly one handler:

  ```go
  path, handler := yccv1connect.NewSessionServiceHandler(srv, ...)
  mux.Handle(path, handler)  // path == "/ycc.v1.SessionService/"
  ```

  The Connect handler keeps its `"/ycc.v1.SessionService/"` prefix; the SPA
  mounts at `"/"`. `http.ServeMux` longest-prefix matching routes RPC traffic to
  Connect and everything else (`/`, `/app.js`, …) to the asset handler, so the
  two never collide.

- Gated behind a new `ycc daemon --web` flag (default **off**). Plumbed as a
  `Web bool` on `daemon.Options`; `buildHandler` registers the asset handler
  only when set. The one-shot in-process daemon (`StartInProcess`) leaves it
  off — the SPA is a *remote-access* feature, and the in-process daemon is a
  private ephemeral loopback backing the local TUI.

**Why vanilla, no toolchain.** The alternative is TypeScript + [connect-es](https://github.com/connectrpc/connect-es)
+ a bundler (Vite/esbuild) producing a `dist/`. Rejected for the first cut:

- The wire contract is *trivial*. Unary calls are `POST` + JSON. The only
  non-obvious part — the server-streaming envelope — is a **5-byte frame** (flag
  byte + big-endian u32 length + JSON) fully specified in
  [`docs/remote-api.md`](../remote-api.md). Hand-parsing it is ~30 lines of JS
  (§6). connect-es buys us little here and costs a whole JS ecosystem.
- Keeping the Go module free of a JS toolchain preserves plain `go install
  ./cmd/ycc` and a hermetic `go build` — no node, no lockfile, no `npm ci` in CI,
  no generated-code drift against the proto.
- The first cut is small (two screens). A framework's weight is not repaid.

**Escape hatch.** If the SPA outgrows hand-written JS, we commit a *prebuilt*
`dist/` (the source-of-truth TS/build lives in a subdir, the checked-in
`dist/` is what `go:embed` ships) or add an optional `npm run build` that is not
required for `go build`. This keeps the door open without paying the cost now.

## 4. Auth handoff (decision)

**Decision: static assets are served UNauthenticated; every RPC stays behind the
existing bearer `authInterceptor`, unchanged. The SPA collects the token from the
user on first load, stores it in `localStorage`, and attaches
`Authorization: Bearer <token>` to every fetch (unary and streaming).**

Why assets are unauthenticated:

- A browser navigating to `http://<host>:8787/` cannot attach an
  `Authorization` header to that initial document load — there is nowhere to put
  it before any of our code runs. Requiring auth on the HTML would make the app
  unreachable.
- The assets are **non-sensitive public code** (HTML/CSS/JS). They contain no
  secrets and no data — all data comes from authenticated RPCs. Serving them
  openly leaks nothing.

The interceptor is RPC-only by construction. `NewAuthInterceptor` wraps the
Connect handlers (`internal/server/auth.go`); it never sees the asset handler,
so wiring the SPA at `/` changes nothing about RPC auth. A request with a missing
or wrong token still gets **HTTP 401** + `{"code":"unauthenticated","message":"invalid or missing bearer token"}`
on unary *and* streaming RPCs.

Token flow in the SPA:

1. On load, read `localStorage["ycc_token"]`. If absent, show a **token-entry
   screen** (single password field + "connect").
2. On submit, validate by issuing a cheap authenticated RPC (`ListProjects`). A
   401 → show "invalid token"; success → persist to `localStorage` and proceed.
3. Every subsequent `fetch` — unary and the `Subscribe` stream — sends
   `Authorization: Bearer <token>` (fetch allows custom headers on both request
   kinds). A mid-session 401 (token rotated) bounces back to the entry screen.

**Security posture (stated honestly).** The intended deployment is the same as
`docs/remote-api.md`: a **private network (Tailscale / VPN) plus a bearer
token**, single user.

- *Token in `localStorage` vs XSS.* An XSS bug in the SPA could exfiltrate the
  token. Acceptable here: the app is a few static files we control (no
  third-party scripts, no user-generated HTML rendered as HTML — event text is
  inserted as `textContent`, never `innerHTML`), it is single-user, and it lives
  on a private network. `localStorage` is chosen over a cookie because the token
  must ride on `fetch`/stream requests as a bearer header (the RPC surface is
  header-auth, not cookie-auth) and to keep the daemon stateless. `sessionStorage`
  is a reasonable tighter variant (cleared on tab close) if we prefer re-entry.
- *Transport.* On a tailnet, transport is already encrypted, so plain
  `http://` is fine (spec §14 / remote-api "TLS is optional on a private
  tailnet"). **Outside** a tailnet, use `--tls-cert`/`--tls-key` and `https://`
  so the bearer token is not sent in cleartext.
- *Guardrail interplay (unchanged).* `--web` does **not** relax the existing
  `Serve()` guardrail: binding a non-loopback address without a token still
  refuses to start —
  `refusing to bind non-loopback address <addr> without a token` — and a
  non-loopback bind without TLS still logs
  `warning: binding non-loopback address <addr> without TLS; traffic is cleartext`.
  So `ycc daemon --web --addr <tailnet-ip>:8787` *requires* a token exactly as
  the pure-RPC daemon does; the web client cannot be exposed without auth.

## 5. RPC surface for the first cut

The web client is read-mostly + prodding. It uses only endpoints already in the
catalog (`docs/remote-api.md`); nothing new is added.

| RPC | Use in the web client |
|-----|-----------------------|
| `ListProjects` | project filter chips; also the token-validation probe (§4) |
| `ListSessionHistory` | the session list — live + persisted, most-recent first, with `status`, `title`, `turns`, `live`, `waitingInput` |
| `GetSessionTranscript` | initial render of a **finished/persisted** session (one-shot full log; no live tail) |
| `Subscribe` | live sessions: replay-from-seq + live tail (§6) |
| `SendInput` | the sticky bottom input bar — prod a running/idle session |
| `AnswerQuestion` / `AnswerQuestions` | the answer picker bottom-sheet (single or batched `ask_user`) |
| `Interrupt` / `Resume` | overflow actions — graceful pause-to-steer / continue |
| `StopSession` | overflow action — hard terminate |

**Deferred to a later cut** (explicitly *not* in the first cut):

- `StartSession`, `ResumeSession` — starting/reopening from the phone. Phase 2:
  low effort, but the first cut is "observe + answer what's already running".
- `ListBacklog` / `GetTask` — a read-only backlog browser. Nice phase 2.
- `GetUsage` / `GetBudget` — cost/usage views. Not core to remote answering.
- `Notify` — the client-driven work-loop digest path; no UI need here.
- `ListSessions` — subsumed by `ListSessionHistory` for the browser (history is
  the superset, with the live marker).

## 6. Streaming approach

**Decision: `fetch()` + `ReadableStream` incremental parser over the Connect
server-streaming envelope, exactly as documented in `docs/remote-api.md`. No
SSE, no WebSocket.**

The `Subscribe` RPC is `Content-Type: application/connect+json` and frames every
message in the 5-byte envelope (`docs/remote-api.md` "Server-streaming RPCs"):

```
+--------+------------------+------------------------+
| 1 byte | 4 bytes          | N bytes                |
| flag   | big-endian u32 N | JSON payload (N bytes)  |
+--------+------------------+------------------------+
```

Client algorithm:

1. **Request.** `POST /ycc.v1.SessionService/Subscribe` with
   `Content-Type: application/connect+json`, `Authorization: Bearer <t>`, and a
   body of **one enveloped frame** (flag `0x00`) carrying
   `{"sessionId":"<id>","fromSeq":<n>}`.
2. **Response.** Read `response.body.getReader()` and accumulate bytes in a
   buffer. Repeatedly: if the buffer holds ≥5 bytes, read the flag + length; once
   ≥ `5 + N` bytes are present, slice the payload and dispatch by flag:
   - **data frame** (flag `0x00`): payload is one `Event` JSON → fold it (§7).
   - **end-of-stream frame** (flag `0x02`): payload is the trailer JSON — `{}`
     clean, `{"error":{...}}` on error → close the stream, decide whether to
     reconnect.
3. `Event.dataJson` is an **embedded JSON string**, parsed a *second* time to get
   the per-type payload (`docs/remote-api.md` protojson conventions). `int64`
   fields (`seq`) arrive as **JSON strings**.

**Reconnect discipline** (matches remote-api "Replay-from-seq reconnection"):

- Track the highest **persisted** `seq` folded so far.
- On drop / app-resume, `Subscribe` again with `fromSeq: <last-persisted-seq>`;
  the server replays only `seq > fromSeq`, then tails — no gap, no duplication.
- **Transient events never advance the cursor.** `turn_delta` (and any
  `transient:true` event) carries `seq:"0"`; it is the live in-progress turn
  snapshot. Its payload is the *full accumulated text so far* (a snapshot, not an
  increment): render it as a single **replaceable live-tail row**, replaced by
  the next snapshot, and cleared by the terminating `{"text":"","done":true}`
  delta or the arrival of the durable `model_turn` (whichever comes first).
  Because it has no seq, it must never be persisted into state as a durable event
  and must never move the resume cursor.

**Why not SSE / WebSocket.** Spec §14 decided the Connect surface *is* the
remote API — "no separate REST/SSE facade". Adding SSE or a WebSocket endpoint
would fork the protocol (a second serialization of the event log to maintain and
keep consistent) for zero functional gain: `fetch` + `ReadableStream` already
streams the existing `Subscribe` frames incrementally in every modern mobile
browser. It also keeps the web client a *peer* of the curl/connect-es examples,
not a special case. And per `docs/remote-api.md`, the handler serves streaming
over **HTTP/1.1** as well as h2c, so no HTTP/2 negotiation is required for the
browser stream to work.

## 7. Phone-form-factor layout

Two screens, single-column, thumb-reachable actions at the bottom.

### (a) Session list

- Source: `ListSessionHistory` (most-recent-first), refreshed on focus. Rows are
  live + persisted sessions.
- **Project filter chips** across the top from `ListProjects` (hidden when only
  one/none). Tapping a chip re-queries with `project`.
- Per row: `title` (derived from first prompt), a **status badge**
  (`running` / `idle` / `error`), a **live vs persisted** marker (`live:true`),
  and `turns`. When `waitingInput:true`, the row is **highlighted as "needs
  answer"** — this is the whole point of the phone client, so it sorts/styles
  loudest.
- Tap a row → session view. Live sessions open a `Subscribe` stream (§6); a
  persisted/finished session opens via `GetSessionTranscript` (static, no tail).

### (b) Session view — event feed

A single-column feed produced by **folding the event log** (spec §18, the
"UI is a projection of the log" rule). Event → render mapping:

- `user_input` / `model_turn` → chat bubbles (user vs agent aligned).
- `thinking` → a collapsed one-liner ("💭 thinking"), expandable on tap
  (spec §18.4 — reasoning collapsed by default).
- `tool_call` / `tool_result` → a collapsed one-liner (tool name + status),
  expandable to args/output on tap.
- `question_asked` → raises a **bottom-sheet answer picker**: suggested options
  as buttons + a free-text field. Wired to `AnswerQuestion` (single) or
  `AnswerQuestions` (batch, positional `answers[i]`). `optionIndex >= 0` selects
  an option; `optionIndex:-1` sends the free text (spec §18.3, remote-api
  AnswerQuestion semantics). `question_answered` dismisses the sheet.
- `session_idle` / `session_error` → inline banners.
- `session_started` / `session_stopped` / `session_reopened` / `commit_made` /
  `decision_made` / etc. → compact system rows.
- transient `turn_delta` → the replaceable live-tail row (§6).

Chrome:

- **Sticky bottom input bar** → `SendInput` (prod running/idle sessions).
- **Overflow menu** (⋯) with `Interrupt` / `Resume` / `StopSession`.
- **Auto-follow scroll**: pin to the newest row while at the bottom; when the
  user scrolls up, stop auto-following and show a **"jump to latest" pill** that
  re-pins on tap. (Getting a new answer request must not yank the user's scroll
  position; the pill/needs-answer badge signals it instead.)

Event text is always inserted via `textContent` (never `innerHTML`) — this is
the XSS mitigation referenced in §4.

## 8. tsnet embedding (explicit decision — required by acceptance criteria)

**Decision: documentation-only recommendation for now. No tsnet in the first
cut.** The recommended remote-access path stays host-level Tailscale (or any
VPN) + the bearer token, exactly as `docs/remote-api.md` prescribes.

What embedding *would* look like (sketch, so the follow-on is concrete):

- A `ycc daemon --ts-hostname ycc` flag → construct a
  `tsnet.Server{Hostname: "ycc"}` and serve on `ts.Listen("tcp", ":80")`
  (and/or `:443` with tsnet's TLS) instead of `net.Listen` in `Serve()`. The
  daemon then appears on the tailnet as `ycc` with **zero** host firewall / port
  / TLS thought — reachability comes from the tailnet.
- Identity could replace the bearer token: `tsnet`'s `LocalClient.WhoIs` on each
  connection yields the calling tailnet identity, so auth becomes "is this a
  member of my tailnet" rather than "did they present the shared secret". That
  would let `--web` drop the token-entry screen entirely on a tsnet deployment.

Why defer:

- **Dependency weight.** `tsnet` pulls the full `tailscale.com` module tree into
  what is today a comparatively lean binary — a large, opinionated dependency to
  carry for every `go install`, including for users who never touch remote
  access.
- **Host `tailscaled` already delivers the story.** A user who wants tailnet
  reachability runs Tailscale on the host and binds `--addr <tailnet-ip>`. That
  is the documented deployment and needs **zero** new ycc code; embedding buys
  convenience (no host daemon) at a real dependency cost.
- **Auth model is orthogonal.** The web client's bearer-token handoff (§4) works
  identically whether reachability is host-tailscaled or tsnet. Embedding does
  not *require* changing auth, and the WhoIs-identity option is an enhancement we
  can evaluate independently once the web client proves out.

It stays on the table as a **proposed follow-on** (§9, task iv), to revisit after
the web client ships and if "no host tailscaled" turns out to matter.

## 9. Follow-on implementation tasks

Proposed, well-scoped tasks realizing this doc (to be filed in the backlog by the
coordinator):

1. **Daemon: `--web` flag + `go:embed` asset serving.**
   - New `internal/web` package: embedded static FS + `Handler()`.
   - `Web bool` on `daemon.Options`; `buildHandler` mounts the asset handler at
     `/` when set, leaving the Connect handler at `/ycc.v1.SessionService/`; the
     `daemon` command grows a `--web` flag; in-process daemon leaves it off.
   - Tests: with `--web`, `/` and `/app.js` serve 200 unauthenticated; RPC paths
     still require a token; without `--web`, `/` is 404; the non-loopback
     no-token guardrail is unchanged with `--web` set.

2. **Web SPA first cut: token entry + session list + live event stream.**
   - Token-entry screen → `localStorage`; validate via `ListProjects`.
   - Session list from `ListSessionHistory` (+ `ListProjects` filter chips,
     status/`waitingInput` badges, live marker).
   - Session view feed folding the event model; live sessions via the
     `Subscribe` envelope parser (§6) with replay-from-seq reconnect and the
     `turn_delta` live-tail row; persisted sessions via `GetSessionTranscript`.

3. **Web SPA interactions: prod / answer / control.**
   - Sticky input bar → `SendInput`.
   - Question bottom-sheet → `AnswerQuestion` / `AnswerQuestions` (options +
     free text), dismissed by `question_answered`.
   - Overflow menu → `Interrupt` / `Resume` / `StopSession`.
   - Auto-follow scroll + "jump to latest" pill.

4. **(Proposed / deferred) tsnet embedding.**
   - `ycc daemon --ts-hostname <name>` → `tsnet.Server` + `ts.Listen`; optional
     `WhoIs`-identity auth as an alternative to the bearer token. Evaluate the
     dependency cost after the web client ships (§8).
