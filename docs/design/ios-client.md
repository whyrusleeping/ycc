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

### Navigation shell — workspace drawer + active-session inbox

The authenticated app uses a **left-edge workspace drawer**, following the Slack /
Discord interaction model rather than making project selection a small toolbar
filter. A hamburger button opens it for discoverability; a swipe from the left
edge reveals it interactively over the current screen, and tapping the scrim or
swiping it closed returns to the current screen.

The drawer has two levels of navigation:

1. **All active sessions** — a daemon-wide inbox at the top. It merges live
   sessions from every registered project, pins sessions waiting for input, and
   sorts the rest by latest activity. Every row is visibly annotated with its
   project name; the drawer row carries badges for total active and needs-answer
   counts. This is the default landing destination on a multi-project daemon, so
   a question in another workspace cannot be hidden by the currently selected
   project.
2. **Projects** — the registered workspace list, each with active /
   needs-answer badges. Selecting a project closes the drawer and scopes the
   session list and project destinations (backlog, usage, workstreams, and new
   session) to it. “Add project…” remains at the bottom of this list.

The first implementation aggregates client-side: call `ListProjects`, fan out
`ListSessionHistory(project:)`, wrap every returned summary with its project
identity, merge/deduplicate, and retain active rows for **All active**: live
`running` / `paused` sessions (including every `waitingInput` row), plus live
`error` rows as attention items; `idle` / `stopped` history remains inside its
project. This preserves titles and `waitingInput`, which the lighter
`ListSessions` rows do not
carry, and requires no client-specific RPC. Registered project paths are used to
avoid querying the same workspace twice; the daemon-default workspace is used as
a fallback when there are no registered projects. Fan-out tolerates a failed
project by showing the successful rows plus an inline partial-results warning.
If project counts make fan-out material, the same model can move behind a future
daemon-side aggregate query without changing the UI.

On a one-shot/single-project daemon the drawer remains available but compact: the
sole project is selected by default and the all-active/project views are
functionally equivalent. Deep links select the matching project or session but
do not permanently remove access to the daemon-wide inbox.

### Phase 1 — observe, answer, control (parity with the web client)

1. **Connect screen** — base URL + token; validate with `ListProjects`
   (401 → "invalid token"); persist (Keychain for the token).
2. **Session list** — the navigation shell above provides both the daemon-wide
   **All active sessions** inbox and project-scoped `ListSessionHistory` views.
   Rows are most-recent-first with waiting-input pinned; each aggregate row shows
   its project, plus status badge (`running`/`idle`/`error`), live marker, and
   turns. `waitingInput:true` rows are styled loudest ("needs answer" is the
   whole point of a phone client). Pull-to-refresh + refresh on foreground.
3. **Session view** — the transcript feed from `SessionProjection`: live
   sessions via `Subscribe`, persisted via `GetSessionTranscript`. Auto-follow
   scroll with a "jump to latest" pill when the user scrolls up. The
   `session_idle.report` is projected as a dedicated, always-expanded success
   card with native Markdown rendering; an immediately preceding model message
   repeated by the report is coalesced into the card rather than shown twice.
4. **Interactions** — sticky input bar → `SendInput`, including a Photos picker
   for up to four previewable/removable picture attachments (normalized to bounded
   JPEG data and sent as native multimodal user content). On a persisted session,
   sending first calls `ResumeSession` on the existing log, promotes the view to
   a live `Subscribe` tail, then delivers the message. Question sheet (options as
   buttons + free text) → `AnswerQuestion` / `AnswerQuestions` (positional batch;
   `optionIndex >= 0` picks an option, `-1` sends text), dismissed by
   `question_answered`; toolbar/overflow → `Interrupt` / `Resume` /
   `StopSession` (with confirmation on stop).

### Phase 2 — start work, backlog

5. **Start session** — styled as a blank chat: a message-style composer with
   a send arrow, compact mode / interaction-level / project chips above it
   (`ListModes` / `ListProjects`), and presets as tappable suggestion cards
   in the empty space → `StartSession`, then navigate straight into the live
   session view. Plain `work` mode may start with an empty prompt (the agent
   picks the next ready backlog task), like the TUI. **Resume** —
   `ResumeSession` action on persisted rows.
6. **Backlog browser** — `ListBacklog` grouped by status with ready/blocked
   annotations; task detail (`GetTask`) rendering the markdown body; status
   changes via `UpdateTask`; "start work on this task" → `StartSession`
   (mode `work`, task-focused prompt). Optionally `CreateTask` for quick
   capture from the phone.
7. **Deep links** — a `ycc://` URL scheme (`ycc://session/<id>`,
   `ycc://project/<name>`) so an ntfy notification tap can land directly on
   the session that needs an answer (§8).

### Phase 3 — TUI parity

8. **Settings** — two related surfaces mirror the TUI overlay (§18.2):
   - a **session settings sheet** uses `SetInteractionLevel`, `SetThinking`, and
     `SetRoleConfig` (+ `ListModels`) against a live session;
   - a home-screen **global Settings destination** edits persisted default role
     assignments and per-role thinking without requiring a live session, and manages
     logical model backends (add/edit/duplicate/remove, provider discovery, auth,
     endpoint, reasoning, and pricing) through `GetModelConfig`, `UpsertModel`,
     `RemoveModel`, and `DiscoverModels`. Secret values never cross the wire; the
     form configures only the daemon-side credential mechanism / `key_env` reference.
9. **Usage & budget** — `GetUsage` (group by task/model/day) and `GetBudget`
   views (§20.5).
10. **Workstreams & diffs** — `ListWorkstreams`, `PreviewMerge`,
    `MergeWorkstream`/`DiscardWorkstream`; `GetCommitDiff` viewer for
    `commit_made` events (§14.1, §18).
11. **Work loop** — start/stop/observe the unattended backlog drain, which by
    then is **daemon-side** (§9). Blocked on that daemon work.

## 7. RPC coverage map

Phase 1: `ListProjects`, `ListSessionHistory` (fanned out by the client for the
cross-project active-session inbox), `GetSessionTranscript`, `Subscribe`,
`SendInput`, `AnswerQuestion(s)`, `Interrupt`, `Resume`,
`StopSession`. Phase 2 adds: `ListModes`, `StartSession`, `ResumeSession`,
`ListBacklog`, `GetTask`, `UpdateTask` (optionally `CreateTask`). Phase 3 adds:
`SetInteractionLevel`, `SetThinking`, `SetRoleConfig`, `ListModels`,
`GetModelConfig`, `UpsertModel`, `RemoveModel`, `DiscoverModels`, `GetUsage`,
`GetBudget`, `GetCommitDiff`, workstream RPCs, and the loop control surface
from §9. `Notify` remains unnecessary because daemon-side pushes already fire
without a client call. `AddProject` (with the `ListDir` browse RPC, tasks
0192–0194) has since been pulled into client scope so a new workspace can be
registered from the phone; `RemoveProject` stays server-side admin.

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
