# Design: SwiftUI iOS client (`clients/ios/`)

> Status: **accepted** (planned; implementation tasks filed in the backlog).
> Grounded in spec §12 (RPC protocol), §14 (remote access — the Connect surface
> *is* the phone API, no separate facade), and §18 (client UI / event
> rendering). The authoritative wire contract is
> [`docs/remote-api.md`](../remote-api.md); the sibling design for the embedded
> web client is [`web-client.md`](web-client.md).

## 1. Context / problem

The daemon speaks a complete remote API: plain HTTP/JSON Connect handlers, the
same ones the TUI uses. `docs/remote-api.md` documents the wire contract and
names [connect-swift](https://github.com/connectrpc/connect-swift) as the
intended iOS client library. The embedded web client (tasks 0151–0153) covers
"observe + answer from a phone browser" with a deliberately minimal scope.

This design goes further: a **native SwiftUI iPhone app** whose end-state is
**feature parity with the TUI** — connect to a persistent daemon on a server
(over Tailscale/VPN + bearer token), pick a project, browse live and persisted
sessions, watch a live event stream, reply to questions, steer/interrupt/stop,
start new sessions in any mode, browse the backlog, and eventually settings,
usage/cost, and workstreams.

## 2. Goals & non-goals

**Goals**

- A native iPhone client of the existing Connect RPC surface, **unchanged** —
  same auth, same endpoints, same event model as the TUI and web client.
- Phased delivery: observe/answer/control first, then start/resume + backlog,
  then settings/usage/workstreams. End state ≈ TUI parity.
- An agent-friendly project layout: logic in a headless-testable Swift package,
  text-based project generation, no hand-edited `.xcodeproj`.

**Non-goals**

- iPad / macOS layouts (iPhone-only, **iOS 17+**; revisit later).
- App Store distribution (personal tool; sideload / TestFlight / dev build).
- Native APNs push (deferred — see §8; ntfy + deep links instead).
- Any change to the RPC/proto surface *for the client's own sake*. (The one
  daemon change this plan does motivate — the daemon-side work loop, §9 — is a
  client-independent improvement filed as its own task.)
- Multi-user auth / RBAC. Single-user tool, private network.
- Offline mode beyond cached connection settings.

## 3. Location & toolchain (decision)

**Decision: the app lives in this repo at `clients/ios/`.** The ycc backlog and
work pipeline manage it like any other part of the project, and the generated
Swift protos stay next to their proto source.

- The Go module is unaffected: nothing under `clients/` is imported by Go code
  and `go build ./...` never touches it.
- Project generation via **XcodeGen** (`clients/ios/project.yml`) so the
  `.xcodeproj` is derived, never hand-edited or committed. `xcodegen generate`
  is the only extra step; building is `xcodebuild` (or opening the generated
  project in Xcode).
- Layout:

  ```
  clients/ios/
    project.yml            # XcodeGen manifest (app target, iOS 17 deployment)
    YccKit/                # SPM package: ALL non-UI logic (see §5)
      Package.swift
      Sources/YccKit/      #   client, models, event projection
      Sources/YccProto/    #   buf-generated Swift (committed)
      Tests/YccKitTests/   #   headless unit tests (`swift test` on macOS)
    App/                   # thin SwiftUI shell (views only)
  ```

**Agent-friendliness is a design constraint.** Everything that can be
unit-tested headlessly lives in `YccKit` and runs under plain `swift test` on
the macOS workspace machine — the work pipeline can build and verify without
booting a simulator. The app target is a thin view layer over `YccKit`
observables; simulator/device verification is a manual smoke (runbook to be
added as `plans/ios-client-smoke.md` when the first cut lands).

## 4. Generated Swift client (decision)

**Decision: use connect-swift with buf-generated code, committed to the repo.**

- A `buf.gen.swift.yaml` at the repo root generates from `proto/ycc/v1/ycc.proto`
  with the `connect-swift` and `swift-protobuf` remote plugins into
  `clients/ios/YccKit/Sources/YccProto/`.
- Generated code is **committed** (same posture as the Go generated code) so
  `swift test` / `xcodebuild` need no buf step; regeneration happens whenever
  the proto changes (`buf generate --template buf.gen.swift.yaml`).
- Unlike the web client (which hand-parses the 5-byte Connect envelope to avoid
  a JS toolchain), Swift gets the official library: connect-swift handles the
  streaming envelope, protojson quirks (int64-as-string, camelCase), and typed
  request/response models natively over `URLSession`. The daemon's HTTP/1.1
  streaming support means no h2c negotiation issues.
- Auth: a connect-swift **interceptor** attaches `Authorization: Bearer <token>`
  to every request, unary and streaming — mirroring the TUI/web clients.

## 5. App architecture

SwiftUI + Observation (`@Observable`, iOS 17). `YccKit` owns:

- **`YccClient`** — thin wrapper over the generated connect-swift service
  client: base URL + token, the auth interceptor, typed async methods, and a
  `subscribe(sessionId:fromSeq:)` `AsyncStream<Event>`.
- **`SessionProjection`** — the event-fold engine, the heart of the app and the
  part most worth unit-testing. It is a pure reducer implementing spec §5.2 /
  §18 and the remote-api event model:
  - fold persisted events into an ordered transcript of render rows
    (`user_input`/`model_turn` → bubbles; `thinking`, `tool_call`/`tool_result`
    → collapsed expandable rows; `question_asked` → pending-question state;
    lifecycle events → system rows; etc.);
  - track the highest **persisted** seq for replay-from-seq reconnect;
  - handle **transient** events (`seq:0`, never persisted, never advance the
    cursor): `turn_delta` snapshots render as a single replaceable live-tail
    row, cleared by the terminating `{"text":"","done":true}` delta or the
    durable `model_turn`;
  - be identical for live (Subscribe) and persisted (GetSessionTranscript)
    sources — persisted is just "fold with no tail".
- **`ConnectionStore`** — server profiles (name, base URL) in `UserDefaults`;
  the bearer token in the **Keychain** (never `UserDefaults`). Multiple saved
  servers are supported but one is active at a time.

**Reconnect discipline** (remote-api "Replay-from-seq reconnection"): on stream
drop or app foregrounding (`scenePhase` → `.active`), re-`Subscribe` with
`fromSeq = <last persisted seq>`; the server replays only newer events — no
gap, no duplication. iOS suspends sockets in the background; the app simply
reconnects on return rather than fighting the OS.

**Transport security**: the intended deployment is a tailnet, where `http://`
is acceptable (spec §14). The app's ATS config allows insecure loads
(`NSAllowsArbitraryLoads`) with this documented rationale; `https://` daemons
work unchanged and are recommended off-tailnet.

## 6. Screens & feature phases

### Phase 1 — observe, answer, control (parity with the web client)

1. **Connect screen** — base URL + token; validate with `ListProjects`
   (401 → "invalid token"); persist (Keychain for the token).
2. **Session list** — `ListSessionHistory` most-recent-first; project filter
   (`ListProjects`) as a menu/chips; per-row status badge
   (`running`/`idle`/`error`), live marker, turns; `waitingInput:true` rows
   styled loudest ("needs answer" is the whole point of a phone client).
   Pull-to-refresh + refresh on foreground.
3. **Session view** — the transcript feed from `SessionProjection`: live
   sessions via `Subscribe`, persisted via `GetSessionTranscript`. Auto-follow
   scroll with a "jump to latest" pill when the user scrolls up.
4. **Interactions** — sticky input bar → `SendInput`; question sheet
   (options as buttons + free text) → `AnswerQuestion` / `AnswerQuestions`
   (positional batch; `optionIndex >= 0` picks an option, `-1` sends text),
   dismissed by `question_answered`; toolbar/overflow → `Interrupt` /
   `Resume` / `StopSession` (with confirmation on stop).

### Phase 2 — start work, backlog

5. **Start session** — mode picker (`ListModes`), interaction level, prompt
   composer → `StartSession`, then navigate straight into the live session
   view. **Resume** — `ResumeSession` action on persisted rows.
6. **Backlog browser** — `ListBacklog` grouped by status with ready/blocked
   annotations; task detail (`GetTask`) rendering the markdown body; status
   changes via `UpdateTask`; "start work on this task" → `StartSession`
   (mode `work`, task-focused prompt). Optionally `CreateTask` for quick
   capture from the phone.
7. **Deep links** — a `ycc://` URL scheme (`ycc://session/<id>`,
   `ycc://project/<name>`) so an ntfy notification tap can land directly on
   the session that needs an answer (§8).

### Phase 3 — TUI parity

8. **Session settings sheet** — `SetInteractionLevel`, `SetThinking`,
   `SetRoleConfig` (+ `ListModels`) for a live session — the phone analog of
   the TUI settings overlay (§18.2).
9. **Usage & budget** — `GetUsage` (group by task/model/day) and `GetBudget`
   views (§20.5).
10. **Workstreams & diffs** — `ListWorkstreams`, `PreviewMerge`,
    `MergeWorkstream`/`DiscardWorkstream`; `GetCommitDiff` viewer for
    `commit_made` events (§14.1, §18).
11. **Work loop** — start/stop/observe the unattended backlog drain, which by
    then is **daemon-side** (§9). Blocked on that daemon work.

## 7. RPC coverage map

Phase 1: `ListProjects`, `ListSessionHistory`, `GetSessionTranscript`,
`Subscribe`, `SendInput`, `AnswerQuestion(s)`, `Interrupt`, `Resume`,
`StopSession`. Phase 2 adds: `ListModes`, `StartSession`, `ResumeSession`,
`ListBacklog`, `GetTask`, `UpdateTask` (optionally `CreateTask`). Phase 3 adds:
`SetInteractionLevel`, `SetThinking`, `SetRoleConfig`, `ListModels`,
`GetUsage`, `GetBudget`, `GetCommitDiff`, workstream RPCs, and the loop
control surface from §9. Not planned: `Notify` (daemon-side pushes already
fire without a client call), model-registry editing (`UpsertModel` etc. —
config-file territory), `AddProject`/`RemoveProject` (server-side admin).

## 8. Notifications (decision)

**Decision: reuse the existing ntfy-compatible webhook notifier; no APNs.**
The daemon already pushes `question` / `idle` / `error` / `blocked` / `digest`
events to a configured webhook (spec §14). The user runs the ntfy app for
delivery. The ycc app's contribution is the `ycc://` **deep-link scheme** plus
a documented ntfy `click` URL convention so a notification tap opens the app on
the right session. Concretely (task 0186): the daemon sets an ntfy `Click`
header of `ycc://session/<id>` on every notification sent with a session id
(`question`/`idle`/`error`/`blocked`, and a `digest` routed with the
loop-driver session's id), and the iOS
app registers the `ycc://` scheme (`ycc://session/<id>[?server=<name>]`,
`ycc://project/<name>`) to route the tap — see docs/remote-api.md "Notify".
Native APNs (needing a push relay + signing identity) is deferred indefinitely;
revisit only if ntfy proves inadequate.

## 9. Daemon-side work loop (decision, prerequisite for loop parity)

The `work (loop)` backlog drain is today a **client** concern (spec §9): the
TUI starts the next `work` session when one finishes, enforces the per-loop
budget caps (§20.6), and accumulates the digest. A phone cannot host that
driver — iOS suspends backgrounded apps, so a client-driven loop would silently
die mid-drain.

**Decision (user-accepted): move the loop driver into the daemon**, exposed
over the RPC surface (e.g. `StartWorkLoop` / `StopWorkLoop` / loop status in
session listings, exact shape to be designed in its task). The TUI migrates to
the same RPCs (shedding its client driver), the loop keeps running when every
client disconnects, and any client — TUI, web, iOS — can start, observe, and
gracefully halt it. Loop-cap enforcement (§20.6 "loop cap (client-driven)")
moves daemon-side with it. Filed as its own backlog task; the iOS loop screen
(§6 phase 3) depends on it.

## 10. Verification strategy

- **Headless:** `swift test` on `YccKit` (macOS) — projection-engine fixtures
  built from real `events.jsonl` transcripts (including transient `turn_delta`
  interleaving and replay-from-seq), client request shaping, keychain-free
  connection-store logic.
- **Build:** `xcodegen generate && xcodebuild -project ... -scheme Ycc
  -destination 'generic/platform=iOS Simulator' build` must pass.
- **Manual smoke:** a `plans/ios-client-smoke.md` runbook (added with the first
  cut) mirroring `plans/remote-access-smoke.md`: daemon on a tailnet address
  with a token, connect from the app, answer a live `ask_user`, kill the
  network mid-stream and verify replay-from-seq reconnect.
