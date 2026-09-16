# Design: native iOS client

> Status: accepted and implemented in `clients/ios/`.

## Context

The daemon already owns sessions and exposes a remote Connect API. A phone client should observe,
answer, steer, and start work without duplicating the agent loop or relying on a continuously
foregrounded app. Native iOS behavior matters for credential storage, notifications, deep links,
and long-lived navigation state.

## Decisions

### Repository and transport

The app lives in this repository so its generated protocol bindings change atomically with the Go
server. XcodeGen defines the app project and a Swift package contains headless networking, event
projection, and model logic. Generated Swift protobuf and Connect sources are committed; building
the app does not require code generation or a JavaScript toolchain.

The app calls the daemon's existing Connect service directly. It does not introduce a mobile REST
facade, proxy daemon, or replicated session store. Calls use Connect binary protobuf, avoiding an
outer JSON decode around the per-event JSON strings; subscriptions replay from the last durable
sequence. Transient events never move the cursor. Reconnection clears stale live-tail state and
reduces replayed events idempotently. Initial transcript requests opt out of opaque provider replay
blocks (`omit_provider_state`); all display events and reducer state remain available, and the daemon's
source log is unchanged.

Unary transport response decoding and history reduction run off the UI executor. Replay publishes
atomically, discarding cancelled or superseded loads. Transient initial-fetch failures retry with
backoff rather than bypassing bulk replay through an event-by-event subscription. Initial rendering
mounts only the latest 200 durable rows; **Load earlier** reveals preceding pages while retaining the
previous first row as the reading anchor. The full projection remains available for lifecycle,
questions, tool pairing, and reconnect cursors. This bounds initial eager layout without reintroducing
lazy-stack geometry failures during streaming; explicitly loaded pages and subsequent live rows can
grow the mounted history. A one-shot successful-replay edge replaces the loading scroll container
and pins the installed transcript at the latest row with bounded layout-settling corrections.
Reconnects and paging do not recreate the container, and user scrollback cancels initial pinning.

### State and credentials

A saved profile contains the daemon base URL and non-secret presentation metadata. Bearer tokens
live in Keychain, never preferences or logs. Authentication is established by a real RPC rather
than trusting local profile state. A 401 clears the active authenticated session while retaining
the profile so the user can re-enter or replace the token.

The app treats daemon data as authoritative and keeps only client-local navigation, drafts,
preferences, and read watermarks. Session, backlog, usage, and workstream state are projections of
RPC responses/events and refresh when the app returns to the foreground. Backlog task detail has an
explicit editor for title, status, priority, dependencies, spec references, and Markdown body;
`UpdateTask` saves the whole draft and the returned canonical detail replaces the projection.
Cancellation leaves the loaded task untouched, while a failed save retains the draft for retry.

### Settings

The global model editor can discover model ids and test a model draft, but these are intentionally
separate operations. Discovery queries the provider's listing endpoint and may fall back to curated
ids; it does not prove that generation works. **Test model** sends one small, potentially billed
inference request through the daemon using the exact unsaved backend, endpoint, model id, auth,
credential reference, and reasoning controls. The key remains daemon-side, the draft is neither
saved nor installed, and progress plus success/provider failure appear inline in the editor.

### Navigation and interaction

One workspace drawer anchors project selection and cross-project recents. Project/session routes
flow through a single router so list taps, notifications, and `ycc://` links share authentication
and project selection. Navigation is hub-and-spoke: the recents list is the hub, and a cross-link
jump between screens (session → backlog, task → session, drawer → anywhere) replaces the stack
rather than pushing, so a single Back always returns to recents. Only genuine drill-ins (recents →
session, backlog → task) push; task dependency references are lateral hops that swap the detail
screen in place, so a blocked task leads directly to each blocker without growing the stack when
dependencies cross-link. A deep link identifies a project/session but never carries credentials.

Transcript behavior follows the shared client contract in spec §18: durable rows, stable per-actor
transient live tails for concurrent agents, no scroll jumps while reading history, structured
question sheets, graceful interrupt/steer/resume, and confirmed hard stop. Within a session, each
subagent receives a stable plant emoji from a fixed palette; every actor-owned durable or live row
keeps the emoji alongside the textual actor identity, while coordinator/user/system rows remain
unadorned. User-message picture metadata carries opaque
session attachment ids; the app lazily fetches retained bytes through the authenticated daemon API
and renders thumbnails, degrading to a labelled placeholder for legacy or reclaimed payloads.
Outgoing pictures reach the composer draft two ways — the Photos picker and pasting an image
directly into the message field (a UIKit-backed text view, since SwiftUI text fields refuse image
pastes) — both funneled through one normalize/merge pipeline that caps count and per-image bytes to
the daemon's limits. In both new-session and existing-session composers, Return inserts a newline
(with normal IME composition handling); it never submits the draft. Sending requires the explicit
send/start control, avoiding accidental sends while composing multiline messages.
Daemon work loops remain daemon-owned because iOS background execution cannot reliably host
long-running work.

### Recent-session loading

Recent publishes each project's history as it arrives, retaining prior rows for pending or
failed projects. Supplemental work-loop badges never gate history display; failed badge requests
retain known ownership, while successful empty results clear it. Aggregate ordering and duplicate
routing follow registry order, not request completion order. Histories are sorted on ingestion;
section/filter reads preserve that order without re-parsing timestamps. Read-watermark baselining
runs once after all history requests finish, so request order cannot turn first-load history into
unread activity or hide genuinely new sessions.

### Notifications

Push notifications are daemon-side ntfy-compatible webhooks with a `ycc://` click-through URL.
This avoids APNs infrastructure and works even when no app process is running. Notification
payloads carry routing identifiers and short event, question, report, or error text, but no bearer
token.

## Alternatives rejected

- A web wrapper would reduce initial UI work but would not provide the desired Keychain,
  notification, deep-link, and native navigation behavior.
- A client-owned work loop would stop when iOS suspends the app and could race another client.
- Copying generated code by hand or generating during every build makes protocol drift easy and
  builds dependent on network tooling.
- Separate per-feature networking models create competing session projections; shared headless
  models keep event reduction and auth failure handling consistent.

## Verification boundary

Headless models and frame/reducer logic are automated in the Swift package; project generation and
compilation are build checks. `plans/ios-client-smoke.md` contains only the release checks that
need a simulator/device, Keychain, app lifecycle, real networking, and native navigation.
