---
id: "0152"
title: 'Web SPA first cut: token entry, session list, live event stream'
status: todo
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

## Work log
