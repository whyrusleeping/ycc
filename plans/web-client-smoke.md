# Web client smoke test (manual, phone-viewport runbook)

Verifies the embedded web SPA (design `docs/design/web-client.md` §4–§7): the
daemon serves a dependency-free vanilla HTML/CSS/JS client behind `--web`, and
from a phone-sized viewport you can enter the token, browse sessions, watch a
live session stream — including in-progress `turn_delta` text — with a clean
replay-from-seq reconnect (task 0152), and **interact**: send input, answer
`ask_user` gates, and interrupt/resume/stop a session (task 0153).

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
3. **No chrome**: a persisted session shows no input bar, no ⋯ overflow menu, and
   no answer sheet — it is read-only.

## 7. Interactions (task 0153, §7 chrome)

All of this is **live sessions only**. Have a live session open (§4).

### 7a. Send input (`SendInput`)

1. A **sticky bottom input bar** (text field + Send) is present, thumb-reachable.
2. Type a message and tap Send (or press Enter). The field clears on success and
   the message appears in the feed as a user bubble (possibly tagged `queued`).
3. Kill the daemon (or answer nothing that errors) and Send → a non-fatal
   **toast** appears with the error message; the app stays usable.

### 7b. Answer a single `ask_user` gate

1. Drive the session to an `ask_user` with one question (interactive/judgement
   level). A **bottom sheet** rises over the input bar: the prompt, any suggested
   **option buttons**, and a free-text field with "Send answer".
2. Tap an **option** → `AnswerQuestion` with that `optionIndex`; the sheet
   dismisses when the durable `question_answered` event arrives, and the answer
   shows in the feed.
3. Repeat with **free text**: type an answer and Send (or Enter) → `AnswerQuestion`
   with `optionIndex:-1` + text.
4. **Cross-client dismiss**: raise a gate, then answer it from another client
   (TUI or `curl AnswerQuestion`). The web sheet dismisses on its own when the
   `question_answered` event streams in.
5. **Error toast**: answer once, then tap an option again quickly (or answer a
   stale gate) → `failed_precondition` "no pending question" surfaces as a toast
   and controls re-enable.

### 7c. Answer a batched `ask_user` gate

1. Drive the session to an `ask_user` posing **multiple questions** in one call.
   The sheet shows each prompt with its own options + free-text field, plus one
   **"Send answers"** button.
2. For different questions, pick an **option** (tap highlights it) and type
   **free text** for another; tap Send answers → `AnswerQuestions` with positional
   `answers[i]` (option → `optionIndex>=0`, free text → `optionIndex:-1`). The
   sheet dismisses on `question_answered`.

### 7d. Overflow menu (`Interrupt` / `Resume` / `StopSession`)

1. Tap the **⋯** button in the session topbar → a small menu: Interrupt, Resume,
   Stop session.
2. **Interrupt** pauses at the next checkpoint (steer with SendInput); **Resume**
   continues — both reflected in the feed / status.
3. **Stop session** requires a **second tap** ("Tap again to stop") before it
   hard-terminates. After stopping, the feed shows the session ending.
4. Tapping outside the menu or pressing **Escape** closes it.

### 7e. Auto-follow scroll + jump-to-latest pill

1. While at the **bottom** of the feed, new events auto-scroll into view
   (auto-follow).
2. **Scroll up**: auto-follow stops and a floating **"↓ jump to latest"** pill
   appears when new events arrive. Crucially, new events must **not yank** your
   scroll position while you're reading history.
3. Tap the **pill** (or scroll back to the bottom) → it re-pins to newest and the
   pill hides.

## Expected outcome

Token → list → live stream (with `turn_delta`) → reconnect with no gaps/dupes →
persisted transcript all work from a phone-sized viewport, with no JS framework
and no build step (`go build ./...` alone produced the binary). Interactions
(task 0153): send input, answer single + batched `ask_user` gates via option and
free text (dismissed by the durable `question_answered`, even cross-client),
interrupt/resume/stop via the overflow menu, non-fatal RPC errors as toasts, and
auto-follow scrolling with a jump-to-latest pill that never yanks the reader's
position.
