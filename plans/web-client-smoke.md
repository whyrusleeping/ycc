# Web client smoke test (manual, phone-viewport runbook)

Verifies the first-cut embedded web SPA (design `docs/design/web-client.md`
§4–§7, task 0152): the daemon serves a dependency-free vanilla HTML/CSS/JS client
behind `--web`, and from a phone-sized viewport you can enter the token, browse
sessions, and watch a live session stream — including in-progress `turn_delta`
text — with a clean replay-from-seq reconnect.

The automated coverage is:

- `internal/daemon/web_test.go` — assets serve at `/`, `/app.js`, `/app.css`
  unauthenticated; RPC paths stay behind the bearer token; `--web` off ⇒ 404.
- `internal/web/web_test.go` → `internal/web/app_test.js` — the pure client logic
  (request-envelope byte layout, incremental 5-byte frame parser across split /
  coalesced chunks, `dataJson` double-parse, seq-cursor rules, `turn_delta`
  snapshot replace/clear).

This runbook covers what a test in this environment can't: a real browser on a
phone-sized viewport driving the live stream over the network.

## Prerequisites

- `ycc` built (`go build ./cmd/ycc`).
- A model API key on the daemon host (e.g. `ANTHROPIC_API_KEY`) so a session can
  actually run and emit a live turn.
- A browser. Use a phone on the same tailnet/LAN, or desktop devtools' device
  toolbar (e.g. iPhone SE / 375×667) against loopback.

## 1. Start the daemon with the web client

```
YCC_TOKEN=testtok ycc daemon --web --addr 127.0.0.1:8791 --workspace <some-repo>
```

For a real phone, bind a reachable address instead (a token is then required by
the existing guardrail — `--web` never relaxes it):

```
YCC_TOKEN=$(head -c 32 /dev/urandom | base64) \
  ycc daemon --web --addr <tailnet-ip>:8791 --workspace <some-repo>
```

Quick asset sanity (no auth needed for the static files):

```
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8791/          # 200
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8791/app.js    # 200
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8791/app.css   # 200
# RPC without a token is rejected:
curl -sS -o /dev/null -w '%{http_code}\n' -X POST \
  -H 'Content-Type: application/json' -d '{}' \
  http://127.0.0.1:8791/ycc.v1.SessionService/ListProjects               # 401
```

## 2. Token entry (§4)

1. Open `http://<host>:8791/` in a phone-sized viewport.
2. Expect the **token entry** screen (single password field + Connect).
3. Enter a wrong token → inline **"Invalid token."**, no navigation.
4. Enter the real token → it is stored in `localStorage["ycc_token"]` and you land
   on the session list. Reloading the page skips the token screen.

## 3. Session list (§7a)

1. Start (or already have) at least one session in the workspace so the list is
   non-empty (e.g. via the TUI or `StartSession`).
2. Expect rows most-recent-first, each with a title (fallback: session id), a
   **status badge** (running/idle/error), a **live** marker for in-memory
   sessions, and a turns count.
3. If ≥2 projects are registered, the **project filter chips** appear across the
   top; tapping one re-queries that project (and "all" clears the filter). With
   ≤1 project the chip row is hidden.
4. A session blocked on a question shows a loud **"needs answer"** highlight.
5. The ↻ button refreshes; backgrounding and refocusing the tab also refreshes.

## 4. Live session stream (§6/§7b)

1. Tap a **live** session (start one that will produce output if needed).
2. Expect the folded event feed: user/agent chat bubbles, `thinking` and
   `tool_call`/`tool_result` as collapsed expandable one-liners, `session_idle` /
   `session_error` banners, and compact system rows for the rest.
3. While the agent is producing a turn, watch the **in-progress text stream in**
   as a single replaceable live-tail bubble (dashed border + caret). It is
   replaced by each snapshot and then folds into the durable agent bubble when the
   turn completes.
4. All event text renders as `textContent` — try a prompt containing `<b>` or
   `&`; it must appear literally, never as HTML.

## 5. Reconnect (replay-from-seq)

1. With a live session open and streaming, background the tab (switch apps) for a
   few seconds, or toggle the network briefly, then return.
2. Expect the stream to **resume from the last persisted seq**: no duplicated
   rows, no missing rows. The connection status in the header briefly shows
   "reconnecting…" then "live". (Transient `turn_delta`/`retry` never advance the
   resume cursor, so an in-flight turn re-streams cleanly.)

## 6. Persisted (non-live) session

1. Stop a session (or open one with no `live` marker).
2. Tap it → the full transcript renders once via `GetSessionTranscript` (static,
   no live tail, no reconnect churn).

## Expected outcome

Token → list → live stream (with `turn_delta`) → reconnect with no gaps/dupes →
persisted transcript all work from a phone-sized viewport, with no JS framework
and no build step (`go build ./...` alone produced the binary). Read-only in this
cut: there is no input bar, answer picker, or control actions yet (task 0153).
