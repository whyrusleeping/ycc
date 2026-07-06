---
id: "0152"
title: 'Web SPA first cut: token entry, session list, live event stream'
status: done
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0151"
spec_refs:
    - docs/remote-api.md#Event model
    - docs/design/web-client.md#6. Streaming approach
---

## Description
Second slice of docs/design/web-client.md (§4–§7, §9 item 2): the read-mostly SPA, dependency-free vanilla HTML/CSS/JS (no npm/bundler), living in `internal/web/dist/`.

- Token-entry screen → `localStorage["ycc_token"]`; validate via `ListProjects` (401 → "invalid token"); attach `Authorization: Bearer` to every fetch; a mid-session 401 returns to the entry screen.
- Session list from `ListSessionHistory` (most-recent-first): project filter chips from `ListProjects` (hidden when ≤1), status badge (running/idle/error), live marker, turns, and `waitingInput` highlighted as "needs answer".
- Session view: single-column event feed folding the event model (user_input/model_turn bubbles; thinking and tool_call/tool_result as collapsed expandable one-liners; session_idle/error banners; compact system rows). All event text via `textContent`, never `innerHTML`.
- Live sessions: `fetch()` + `ReadableStream` parser for the connect+json 5-byte envelope (data frame 0x00 = one Event, end-of-stream 0x02 = trailer), replay-from-seq reconnect (track highest persisted seq; transient `seq:"0"` events never advance it), `turn_delta` rendered as a replaceable live-tail row cleared by `{"text":"","done":true}` or the durable `model_turn`. Persisted sessions render via `GetSessionTranscript` (static).

Read-only in this slice: no input bar / answering / control actions (that's the follow-on task).

## Acceptance criteria
- [ ] From a phone-sized viewport against a live daemon with `--web`: enter token → see session list → open a live session → watch events stream in, including in-progress turn_delta text.
- [ ] Reconnect (e.g. tab background/foreground or network blip) resumes from last persisted seq without duplicated or missing events.
- [ ] Persisted (non-live) sessions render their full transcript.
- [ ] No JS framework, no build step; `go build ./...` alone produces the working binary.

## Plan

Replace the placeholder SPA in internal/web/dist/ (index.html, app.js, app.css) with the real first-cut client per docs/design/web-client.md §4–§7. No framework, no build step — `go:embed` already ships whatever is in dist/, so this task is almost entirely those three files plus test coverage.

**Architecture (single app.js, hash-routed views)**
- Hash routing: `#/` = session list, `#/s/<sessionId>` = session view, so the phone back button works and the server needs no path fallback.
- App state: token (localStorage["ycc_token"]), current project filter, session list, per-session view state.
- One `rpc(method, body)` helper: `POST <base>/ycc.v1.SessionService/<Method>` with `Content-Type: application/json` + `Authorization: Bearer <token>`; on HTTP 401 anywhere, clear the stored token and return to the token screen.

**Token screen (§4)**
- Shown when no stored token: single password field + Connect button. Validate via `ListProjects`; 401 → inline "invalid token"; success → persist and go to session list.

**Session list (§7a)**
- `ListSessionHistory` (optionally `{project}`), most-recent-first as returned. Project chips from `ListProjects`, hidden when ≤1 project; tapping a chip re-queries.
- Per row: title (fallback to sessionId), status badge (running/idle/error), "live" marker when `live:true`, turns count, and a loud "needs answer" highlight when `waitingInput:true`.
- Refresh on window focus/visibilitychange + a manual refresh button. Remember protojson omits zero/empty fields (`live`, `waitingInput`, `turns` may be absent; int64s are JSON strings).

**Session view (§6/§7b)**
- Persisted sessions (`live` absent/false): one-shot `GetSessionTranscript`, render the full folded feed, no stream.
- Live sessions: `Subscribe` via fetch()+ReadableStream over the connect+json envelope:
  - Request: single frame flag 0x00 + big-endian u32 length + JSON `{"sessionId":id,"fromSeq":n}` (TextEncoder), `Content-Type: application/connect+json`.
  - Response parser: accumulate Uint8Array; while buffer ≥ 5 bytes read flag+len, wait for full payload; flag 0x00 → one Event JSON → fold; flag 0x02 → trailer (`{}` clean / `{"error":{...}}`) → stream over. Factor this as a pure incremental parser function (testable).
  - Reconnect: track highest **persisted** seq (parseInt of `seq`; events with `transient:true` or missing/`"0"` seq never advance it). On stream end/error while the view is open, reconnect with `fromSeq=lastSeq` after a short backoff; also reconnect on visibilitychange→visible. Replayed events with seq ≤ lastSeq are skipped defensively.
- Event folding (all text via `textContent`, never innerHTML):
  - `user_input` / `model_turn` → chat bubbles (user right/accent, agent left), actor label. `user_input` with `queued:true` gets a small "queued" tag.
  - `thinking` → collapsed one-liner "💭 thinking", expandable (use <details>/<summary>).
  - `tool_call` → collapsed one-liner "🔧 <name>" expandable to args; `tool_result` → collapsed "↩ result" (mark errors) expandable to output. Payload keys are snake_case inside dataJson: tool_call {id,name,args}, tool_result {result, duration_ms, is_error}.
  - `question_asked` → highlighted "needs answer" block showing the question(s) and options as a static list (no answering in this slice); `question_answered` renders a compact row.
  - `session_idle` / `session_error` → inline banners.
  - Everything else → compact system row: type + first non-empty string among data keys text/report/msg/plan/summary/role/sha/task (mirror internal/event/event.go Render fallback). Unknown types must render harmlessly.
  - `Event.dataJson` is an embedded JSON *string* — parse it a second time; tolerate absent/unparsable dataJson.
  - transient `turn_delta` → one replaceable live-tail row **per actor** (payload is a full snapshot `{text}`); cleared by `{"text":"","done":true}` or by the durable `model_turn` from that actor. Never persisted into feed state. Other transient events (e.g. `retry`) render as a replaceable transient note or are ignored — must not advance the cursor.
- Auto-scroll: pin to bottom while at bottom; don't yank when the user scrolled up (a minimal version is fine; full "jump to latest" pill is task 0153's chrome).

**CSS**: phone-first single column, dark/light via color-scheme, sticky header with back button, comfortable tap targets. Keep it modest.

**Tests / verification**
- Factor pure logic (envelope encode/parse, event folding/cursor rules) so app.js also works under Node: guard DOM init behind `typeof document !== "undefined"` and export helpers via `if (typeof module !== "undefined") module.exports = ...`.
- Add a Node-based test file (internal/web/dist/app_test.js or internal/web/app_test.js) exercising: frame parser across split/coalesced chunks, request-envelope encoding, seq-cursor rules (transient never advances), turn_delta replace/clear semantics, dataJson double-parse. Wire it into `go test` via a small Go test (internal/web/web_test.go) that runs `node <test file>` and t.Skip when node is not installed — keeps `go build`/`go test` hermetic without node. NOTE: CI node may be old (v12): use only ES2019 features in the shared/pure code (no optional chaining, no ?? operator).
- Keep the existing daemon web_test.go passing; `go build ./... && go test ./...`.
- Manual smoke: start `ycc daemon --web` with a token on loopback, curl `/`, `/app.js`, `/app.css` (200), verify an RPC still 401s without token. Full phone-viewport live-stream check is documented as a runbook step (plans/) since no browser is available in this environment.

### Starting points
- docs/design/web-client.md §4–§7 (the design this implements)
- docs/remote-api.md — wire contract: envelope framing, protojson conventions (int64 as strings, omitted zero fields, dataJson double-parse)
- internal/web/dist/{index.html,app.js,app.css} — current placeholders to replace
- internal/web/web.go — Handler() serving dist/ (unchanged)
- internal/event/event.go — event Type constants + Render() fallback keys for system rows
- internal/export/markdown.go + helpers.go — existing Go-side event folding to mirror
- internal/daemon/web_test.go — existing asset-serving test that must keep passing
- node v12 available at /usr/bin/node — avoid optional chaining/??; no fetch/ReadableStream in node, so test only pure functions

## Work log
- 2026-07-06 plan: Replace the placeholder SPA in internal/web/dist/ (index.html, app.js, app.css) with the real first-cut client per docs/design/web-client.md §4–§7. No framework, no build step — `go:embed` alrea
…[truncated]
- 2026-07-06 context hints: 8 recorded with plan
- 2026-07-06 context hints: docs/design/web-client.md §4–§7 — the design being implemented; docs/remote-api.md — wire contract: 5-byte envelope, protojson conventions (int64 as JSON strings, zero fields omitted, dataJson
…[truncated]
- 2026-07-06 implementer report: Task complete.  ## Summary  Implemented the first-cut web SPA per `docs/design/web-client.md` §4–§7, replacing the placeholder assets in `internal/web/dist/`. Dependency-free vanilla HTML/CSS/JS �
…[truncated]
- 2026-07-06 review tier: high-powered — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change delivers the first-cut web SPA exactly per the task: dependency-free vanilla HTML/CSS/JS in internal/web/dist/, no build step, with token entry (ListProjects validation, localStorage, mid-s
…[truncated]
- 2026-07-06 decision: accept — commit: web: first-cut SPA — token entry, session list, live event stream (task 0152)
- 2026-07-06 usage: 57,049 tok (in 118, out 56,931, cache_r 2,907,246, cache_w 257,006) · cost n/a (unpriced)
  implementer: 37,217 tok (in 42, out 37,175, cache_r 1,463,550, cache_w 93,529) · cost n/a (unpriced)
  coordinator: 12,546 tok (in 40, out 12,506, cache_r 871,343, cache_w 114,828) · cost n/a (unpriced)
  reviewer:Claude: 7,286 tok (in 36, out 7,250, cache_r 572,353, cache_w 48,649) · cost n/a (unpriced)
