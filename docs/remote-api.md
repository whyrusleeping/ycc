# Remote Connect HTTP/JSON API (phone & web clients)

This document describes how to talk to a **ycc workspace daemon** from a phone, a
web app, or `curl` over a private network. It is the phone-facing surface referred
to in spec §14: there is **no separate REST/SSE facade** — the daemon serves plain
HTTP/JSON directly from the same [Connect](https://connectrpc.com) handlers the TUI
uses, so this HTTP/JSON surface *is* the phone API.

Official Connect client libraries handle the wire protocol (framing, auth headers,
streaming) natively:

- **iOS / macOS** — [connect-swift](https://github.com/connectrpc/connect-swift)
- **Android** — [connect-kotlin](https://github.com/connectrpc/connect-kotlin)
- **Web** — [connect-es](https://github.com/connectrpc/connect-es)

Point any of them at the `SessionService` definition in
[`proto/ycc/v1/ycc.proto`](../proto/ycc/v1/ycc.proto) and add a bearer-token
interceptor (see [Connection & auth](#connection--auth)). `curl` works too — unary
calls are trivial, and server-streaming works with a hand-built request envelope
(shown below).

The examples below are copy-paste-verified against a running daemon (see
[Verification](#verification)).

---

## Connection & auth

### Base URL

Every RPC is an HTTP `POST` to:

```
<base-url>/ycc.v1.SessionService/<Method>
```

where `<base-url>` is `http://<host>:<port>` (h2c / cleartext) or
`https://<host>:<port>` (TLS). For a tailnet deployment the host is the workspace
machine's tailnet IP, e.g. `http://100.64.0.1:8787`.

### Bearer token

Send the token on **every** request (unary and streaming):

```
Authorization: Bearer <token>
```

An unauthenticated or wrong-token request is rejected with **HTTP 401** and a
Connect error JSON body, on unary *and* streaming RPCs alike:

```json
{"code":"unauthenticated","message":"invalid or missing bearer token"}
```

### Starting the daemon for remote access

Run the persistent daemon on the workspace machine, bound to a reachable address,
with a token:

```
YCC_TOKEN=$(head -c 32 /dev/urandom | base64)         # generate once
YCC_TOKEN=$YCC_TOKEN ycc daemon --addr 100.64.0.1:8787
```

`ycc daemon` flags relevant to remote access:

| flag | meaning |
|------|---------|
| `--addr <ip:port>` | listen address (default `127.0.0.1:8787`). A non-loopback bind **requires** a token. |
| `--token <t>` | bearer token clients must present. Also read from the `YCC_TOKEN` env var. Empty disables auth (loopback only). |
| `--tls-cert <file>` / `--tls-key <file>` | enable HTTPS. Optional on a private tailnet. |
| `--workspace <dir>` | default workspace for sessions that don't specify one. |

### Deployment model (decided)

Remote observation and prodding happen by dialing the workspace daemon's Connect
endpoint **directly** — there is no daemon-to-daemon log replication (spec §14).
`Subscribe(from_seq)` already *is* "ship events after seq N", and the single-writer
invariant holds trivially: remote clients only issue RPCs, which the one workspace
daemon serializes.

The intended deployment is a **private network (Tailscale / VPN) plus a bearer
token**. TLS is *optional* because the tailnet already encrypts transport. Two
guardrails enforce this:

- The daemon **refuses to start** when binding a non-loopback address without a
  token:

  ```
  refusing to bind non-loopback address 100.64.0.1:8787 without a token
  ```

- Bound non-loopback **without** TLS, it logs a cleartext warning and continues:

  ```
  warning: binding non-loopback address 100.64.0.1:8787 without TLS; traffic is cleartext
  ```

### h2c vs TLS

An `http://` daemon speaks HTTP/2 cleartext (**h2c**), but the handler also accepts
plain **HTTP/1.1** — both unary and streaming responses work over HTTP/1.1, which is
what makes `curl` (and any HTTP client) usable without HTTP/2 negotiation. Configure
`--tls-cert`/`--tls-key` and use `https://` when you want transport encryption
outside a trusted tailnet.

---

## Protocol primer

### Unary RPCs

`Content-Type: application/json`, a [protojson](https://protobuf.dev/programming-guides/json/)
request body, a protojson response body:

```
curl -sS \
  -H 'Authorization: Bearer <token>' \
  -H 'Content-Type: application/json' \
  -d '<json request>' \
  <base>/ycc.v1.SessionService/<Method>
```

### Server-streaming RPCs (`Subscribe`, `CaptureBacklogItem`)

`Content-Type: application/connect+json`. The Connect streaming protocol frames
every message in a **5-byte envelope**:

```
+--------+------------------+------------------------+
| 1 byte | 4 bytes          | N bytes                |
| flag   | big-endian u32 N | JSON payload (N bytes)  |
+--------+------------------+------------------------+
```

- **Request:** a single enveloped frame (flag `0x00`) carrying the request message
  JSON.
- **Response:** a sequence of frames —
  - **data frames** (flag `0x00`) each carry one `Event` message JSON;
  - a final **end-of-stream frame** (flag `0x02`) whose payload is the trailer JSON:
    `{}` on a clean close, or `{"error":{...}}` on error.

A raw `xxd` of a `Subscribe` response looks like this (data frame, then the
end-of-stream frame at the tail):

```
00000000: 0000 0000 7f7b 2273 6571 223a 2231 222c  .....{"seq":"1",
          ^flag ^--len--^  {"seq":"1", ...  Event JSON payload
...
00001930: ...  session_stop
00001940: 7065 6422 7d02 0000 0002 7b7d            ped"}.....{}
                    ^flag 0x02  ^--len 2--^ {}   end-of-stream, clean close
```

The official client libs decode this framing for you — you just receive a stream of
`Event` objects. `curl` can drive it with a hand-built request envelope (see the
[Subscribe example](#subscribe)).

### protojson conventions clients must know

- **Field names are lowerCamelCase** in responses (`sessionId`, `dataJson`,
  `fromSeq`). Requests accept **both** lowerCamelCase and the proto snake_case
  (`session_id`), so either works when you build a request by hand.
- **`int64` fields are JSON strings**, not numbers: `"seq":"128"`, `"turns":"1"`.
- **Zero-valued and empty fields are omitted.** An empty list RPC returns `{}` (e.g.
  `ListProjects` with no projects), and a `false` bool / `0` int simply isn't
  present.
- **`Event.dataJson` is an embedded JSON *string***, not a nested object — the
  per-type payload is carried as a string so the heterogeneous data needs no proto
  schema. Clients parse it a *second* time:
  `"dataJson":"{\"text\":\"add a hello.txt file\"}"`.

### Connect error model

Errors are an HTTP 4xx/5xx status plus a JSON body `{"code":"...","message":"..."}`.
Codes seen in practice on this surface:

| code | HTTP | when |
|------|------|------|
| `unauthenticated` | 401 | missing/wrong bearer token |
| `not_found` | 404 | unknown session id / unknown task id |
| `failed_precondition` | 400 | e.g. `AnswerQuestion` with no pending question |
| `invalid_argument` | 400 | malformed request (e.g. a bad stream envelope) |

---

## Endpoint catalog

Phone-relevant RPCs. Field shapes are authoritative in
[`proto/ycc/v1/ycc.proto`](../proto/ycc/v1/ycc.proto). Set `$B` to your base URL and
`$T` to your token for the examples:

```
B=http://100.64.0.1:8787
T=$YCC_TOKEN
AUTH="Authorization: Bearer $T"
JSON="Content-Type: application/json"
```

| Method | Purpose |
|--------|---------|
| [`ListProjects`](#listprojects) | list registered projects (multi-project daemon) |
| [`ListSessions`](#listsessions) | live sessions (optionally filtered by project) |
| [`ListSessionHistory`](#listsessionhistory) | live + persisted sessions, most-recent first |
| [`GetSessionTranscript`](#getsessiontranscript) | full event log for one session |
| [`StartSession`](#startsession) | start a new session |
| [`Subscribe`](#subscribe) | stream a session's events (replay + live) |
| [`SendInput`](#sendinput) | prod a session with a user message |
| [`AnswerQuestion`](#answerquestion--answerquestions) / [`AnswerQuestions`](#answerquestion--answerquestions) | answer an `ask_user` gate |
| [`Interrupt`](#interrupt--resume) / [`Resume`](#interrupt--resume) | graceful pause-to-steer / continue |
| [`StopSession`](#stopsession) | hard-terminate a session |
| [`ResumeSession`](#resumesession) | re-open a persisted session on its existing log |
| [`ListBacklog`](#listbacklog) / [`GetTask`](#gettask) | browse the durable backlog |
| [`GetUsage`](#getusage) | priced token-usage breakdown |

### ListProjects

List the daemon's registered projects (name → workspace path). Empty request.

```
curl -sS -H "$AUTH" -H "$JSON" -d '{}' \
  $B/ycc.v1.SessionService/ListProjects
```

```json
{}
```

(Empty when no projects are registered — the omitted `projects` field is an empty
list.) With projects registered:

```json
{"projects":[{"name":"ycc","path":"/home/me/code/ycc"}]}
```

### ListSessions

Live sessions only. Optional `project` filters to one project's workspace.

```
curl -sS -H "$AUTH" -H "$JSON" -d '{}' \
  $B/ycc.v1.SessionService/ListSessions
```

```json
{"sessions":[{"sessionId":"s_doc","mode":"work","status":"running","workspace":"/home/me/work"}]}
```

`status` is `running` | `idle` | `error`.

### ListSessionHistory

Both live sessions and persisted on-disk logs, most-recent first — the session
browser's list. Optional `project`.

```
curl -sS -H "$AUTH" -H "$JSON" -d '{}' \
  $B/ycc.v1.SessionService/ListSessionHistory
```

```json
{"sessions":[{
  "sessionId":"s_doc","mode":"work","status":"running",
  "workspace":"/home/me/work","title":"add a hello.txt file",
  "startedAt":"2026-07-04T10:00:00.000Z","lastActivity":"2026-07-04T17:10:32.643Z",
  "turns":"1","live":true
}]}
```

Notes: `title` is derived from the first user prompt; `turns`/`toolCalls` are int64
(JSON strings); `live` is true for in-memory sessions; `waitingInput` is present
(`true`) only when a live session is blocked on an unanswered `ask_user` question.
Omitted fields (`toolCalls`, `focusTasks`, `waitingInput` here) are zero/empty.

### GetSessionTranscript

The full event log for a session — live or persisted — for a read-only replayed
transcript. Requires `sessionId` (optional `project`).

```
curl -sS -H "$AUTH" -H "$JSON" -d '{"sessionId":"s_doc"}' \
  $B/ycc.v1.SessionService/GetSessionTranscript
```

```json
{"events":[
  {"seq":"1","ts":"2026-07-04T10:00:00.000Z","actor":"user","type":"user_input","dataJson":"{\"text\":\"add a hello.txt file\"}"},
  {"seq":"2","ts":"2026-07-04T10:00:01.000Z","actor":"coordinator","type":"session_started","dataJson":"{\"interaction_level\":\"judgement\",\"mode\":\"work\",\"workspace\":\"/home/me/work\"}"},
  {"seq":"3","ts":"2026-07-04T10:00:02.000Z","actor":"coordinator","type":"model_turn","dataJson":"{\"text\":\"On it — creating hello.txt now.\"}"},
  {"seq":"4","ts":"2026-07-04T17:10:32.643Z","actor":"coordinator","type":"session_reopened"}
]}
```

See [Event model](#event-model) for the `Event` shape and `dataJson` parsing.

### StartSession

Start a new session. `workspace` (or a registered `project` name), `mode`,
`interactionLevel` (`interactive` | `judgement` | `autonomous`), and an initial
`prompt`.

```
curl -sS -H "$AUTH" -H "$JSON" \
  -d '{"mode":"work","interactionLevel":"judgement","prompt":"summarize the README"}' \
  $B/ycc.v1.SessionService/StartSession
```

```json
{"sessionId":"s_65d79bf4919dd36c"}
```

Then `Subscribe` to the returned `sessionId` to follow it.

### Subscribe

Server-streaming. Stream a session's events: the server **replays persisted events
with `seq > fromSeq`**, then tails live events. This is the core of "the UI is a
projection of the log".

**`from_seq` resume semantics.** A fresh subscriber uses `fromSeq: 0` to replay the
whole log then follow live. A reconnecting client passes the **last seq it saw** so
only newer events replay — no gap, no duplication. Because transient events carry
`seq: 0` (see below), they **never advance the resume cursor**: always resume from
the last *persisted* seq you observed.

Unary clients would just call it; over `curl` you build the request envelope by
hand. This needs a shell whose `printf` understands `\xNN` (bash/zsh — **not** dash),
because the 5-byte header is raw bytes:

```bash
SID=s_doc
MSG="{\"sessionId\":\"$SID\",\"fromSeq\":0}"
LEN=$(printf '%s' "$MSG" | wc -c)
# 5-byte header: flag 0x00 + big-endian uint32 length, then the JSON message.
{ printf '\x00'; printf "$(printf '%08x' "$LEN" | sed 's/../\\x&/g')"; printf '%s' "$MSG"; } \
  | curl -sS --http2-prior-knowledge \
      -H "Authorization: Bearer $T" \
      -H 'Content-Type: application/connect+json' \
      --data-binary @- \
      "$B/ycc.v1.SessionService/Subscribe" | xxd
```

Each response **data frame** (flag `0x00`) carries one `Event`:

```json
{"seq":"1","ts":"2026-07-04T10:00:00.000Z","actor":"user","type":"user_input","dataJson":"{\"text\":\"add a hello.txt file\"}"}
```

The stream ends with an **end-of-stream frame** (flag `0x02`) whose payload is the
trailer JSON — `{}` on a clean close. See the [protocol primer](#server-streaming-rpcs-subscribe-capturebacklogitem)
for the byte layout.

**Clients must tolerate seq-less transient events** interleaved in the live tail
(e.g. `turn_delta` with `transient:true`, `seq:0`) — see [Event model](#event-model).

### SendInput

Prod a running or idle session with a user message (queued and delivered at the next
safe checkpoint under steer-by-default).

```
curl -sS -H "$AUTH" -H "$JSON" \
  -d '{"sessionId":"s_doc","text":"keep going"}' \
  $B/ycc.v1.SessionService/SendInput
```

```json
{}
```

### AnswerQuestion / AnswerQuestions

Answer an open `ask_user` gate. `AnswerQuestion` answers a single question:
`optionIndex >= 0` selects a suggested option (0-based); `optionIndex: -1` takes
`text` as a free-text answer.

```
curl -sS -H "$AUTH" -H "$JSON" \
  -d '{"sessionId":"s_doc","optionIndex":0,"text":""}' \
  $B/ycc.v1.SessionService/AnswerQuestion
```

`AnswerQuestions` answers a **batch** posed in one `ask_user` call; `answers` is
positional (`answers[i]` answers the i-th question):

```
curl -sS -H "$AUTH" -H "$JSON" \
  -d '{"sessionId":"s_doc","answers":[{"optionIndex":1,"text":""},{"optionIndex":-1,"text":"use TOML"}]}' \
  $B/ycc.v1.SessionService/AnswerQuestions
```

Both return `{}` on success. Error cases (verified):

- No question open →
  `{"code":"failed_precondition","message":"session s_doc has no pending question"}`
- Unknown session →
  `{"code":"not_found","message":"no such session"}`

### Interrupt / Resume

`Interrupt` gracefully **pauses** a running session at its next safe checkpoint
(without aborting an in-flight tool) so you can steer it with `SendInput`; `Resume`
continues the same loop. Distinct from the hard `StopSession`.

```
curl -sS -H "$AUTH" -H "$JSON" -d '{"sessionId":"s_doc"}' \
  $B/ycc.v1.SessionService/Interrupt
curl -sS -H "$AUTH" -H "$JSON" -d '{"sessionId":"s_doc"}' \
  $B/ycc.v1.SessionService/Resume
```

Both return `{}`.

### StopSession

**Hard-terminate** a session: cancel its agent loop, close its event log, and remove
it from the daemon. There is no resume from `Interrupt`'s graceful pause — but the
durable log survives, so [`ResumeSession`](#resumesession) can re-open it later.

```
curl -sS -H "$AUTH" -H "$JSON" -d '{"sessionId":"s_doc"}' \
  $B/ycc.v1.SessionService/StopSession
```

```json
{}
```

### ResumeSession

Re-open a **persisted** (finished/idle/stopped) session on its **existing** event
log — "resume = replay": the coordinator is re-instantiated with history
reconstructed from the log, and new activity appends to the same continuous
`events.jsonl`. Idempotent if already live.

```
curl -sS -H "$AUTH" -H "$JSON" -d '{"sessionId":"s_doc"}' \
  $B/ycc.v1.SessionService/ResumeSession
```

```json
{"sessionId":"s_doc","mode":"work","status":"running","workspace":"/home/me/work"}
```

### ListBacklog

Read-only list of the durable backlog tasks. Optional `project`.

```
curl -sS -H "$AUTH" -H "$JSON" -d '{}' \
  $B/ycc.v1.SessionService/ListBacklog
```

```json
{"tasks":[
  {"id":"0130","title":"Document the remote Connect HTTP/JSON API","status":"in_progress","priority":3,"ready":true},
  {"id":"0131","title":"Follow-up work","status":"todo","priority":3,"dependsOn":["0130"],"blockedBy":["0130"]}
]}
```

(`{}` when the workspace has no backlog.) `ready` is true when no dependency is
blocking; `blockedBy` lists not-yet-done dependency ids.

### GetTask

Full detail for one backlog task, including its markdown `body` and file `path`.

```
curl -sS -H "$AUTH" -H "$JSON" -d '{"id":"0130"}' \
  $B/ycc.v1.SessionService/GetTask
```

```json
{"task":{
  "id":"0130","title":"Document the remote Connect HTTP/JSON API",
  "status":"in_progress","priority":3,
  "specRefs":["14"],"created":"2026-07-01","updated":"2026-07-04",
  "body":"## Description\n...markdown...","ready":true,
  "path":"/home/me/code/ycc/backlog/0130-remote-api.md"
}}
```

Unknown id → `{"code":"not_found","message":"no task with id \"9999\""}`.

### GetUsage

Priced token-usage breakdown, grouped and filtered. `groupBy` is any of
`task` | `model` | `session` | `agent` | `day` (default `task`); `since`/`until` are
`YYYY-MM-DD` inclusive.

```
curl -sS -H "$AUTH" -H "$JSON" -d '{"groupBy":["task"]}' \
  $B/ycc.v1.SessionService/GetUsage
```

```json
{"rows":[{"priceStatus":"unpriced"}],"total":{"priceStatus":"unpriced"},"workspace":"/home/me/work"}
```

With real usage, each row carries int64 token counts (as JSON strings) and a `cost`
double; `priceStatus` is `priced` | `unpriced` | `partial`:

```json
{"rows":[
  {"task":"0130","model":"claude","input":"12000","output":"3400","total":"15400","cost":0.081,"priceStatus":"priced"}
],"total":{"input":"12000","output":"3400","total":"15400","cost":0.081,"priceStatus":"priced"},
 "workspace":"/home/me/work"}
```

---

## Event model

For client authors: the daemon's UI is a **projection of the event log**. Render
state by folding the event stream; a reconnect + replay-from-seq reproduces exactly
the same state. See spec §5.2 for the authoritative model.

### Event shape

Each `Event` (proto/protojson) carries:

| field | type | notes |
|-------|------|-------|
| `seq` | int64 (JSON string) | monotonic per session; `0` for transient events |
| `ts` | string | RFC3339 timestamp |
| `actor` | string | `coordinator` \| `implementer` \| `reviewer:<model>` \| `user` \| `system` |
| `type` | string | event kind (table below) |
| `dataJson` | string | embedded JSON string; type-specific payload — **parse it separately** |
| `transient` | bool | broadcast-only, never persisted (omitted when false) |

Common `type` values (initial set; full table in spec §5.2):

`session_started`, `model_turn`, `thinking`, `tool_call` / `tool_result`,
`subagent_spawned` / `subagent_finished`, `question_asked` / `question_answered`,
`interrupted` / `resumed`, `user_input` / `user_input_delivered`, `plan_proposed`,
`review_submitted`, `decision_made`, `doc_updated`, `commit_made`,
`session_idle` / `session_error`, `session_stopped` / `session_reopened`, `log`,
and the transient `turn_delta`.

### Replay-from-seq reconnection

1. On first `Subscribe`, use `fromSeq: 0` and fold every replayed event into your UI
   state, tracking the highest **persisted** `seq` you have seen.
2. On reconnect (dropped stream, app resumed), `Subscribe` again with
   `fromSeq: <last-persisted-seq>`. The server replays only `seq > fromSeq`, then
   tails live — no gap, no duplication.

### Transient events (must be tolerated)

Some events are ephemeral UI hints, not durable facts. A transient event:

- is marked **`transient: true`** and carries **`seq: 0`** (never assigned a
  sequence number);
- is **broadcast to live subscribers only** — never written to `events.jsonl`, never
  in transcripts or late replays;
- is **best-effort and lossy under backpressure** — a slow client may drop some;
- **must never advance your resume cursor** (it has no seq) — always resume from the
  last persisted seq.

Clients **must** tolerate seq-less events safely.

The motivating case is **`turn_delta`**: it streams a model's in-progress turn text
to live clients while the durable `model_turn` (written on turn completion) remains
the source of truth. Its payload is a **snapshot**, not an increment:
`{"text": <full-accumulated-text-so-far>}`. Replace your live tail row with the
latest snapshot each time. A turn's tail is cleared by a terminating delta
`{"text":"","done":true}` (on success **or** error) and, redundantly, by the arrival
of the persisted `model_turn`.

### The rule

> The UI is a projection of the log. Render state by folding events; a reconnect
> plus replay-from-seq reproduces the same state.

---

## Verification

The examples here were copy-paste-verified against a real daemon on loopback with a
token (`ycc daemon --addr 127.0.0.1:8790 --workspace <tmp>`, `YCC_TOKEN=testtok`),
seeded with a persisted session (`events.jsonl` + `ResumeSession`). The
`ListSessions` / `GetSessionTranscript` / `ResumeSession` unary responses and the
`Subscribe` `connect+json` enveloped stream (data frames flag `0x00`, end-of-stream
frame flag `0x02` payload `{}`) shown above are actual daemon output, as is the 401
auth rejection. The automated end-to-end coverage lives in
`internal/server/remote_e2e_test.go`; the manual tailnet runbook is
[`plans/remote-access-smoke.md`](../plans/remote-access-smoke.md).
