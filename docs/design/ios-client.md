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
facade, proxy daemon, or replicated session store. Unary calls use Connect JSON and subscription
uses server streaming with replay from the last durable sequence. Transient events never move the
cursor. Reconnection clears stale live-tail state and reduces replayed events idempotently.

### State and credentials

A saved profile contains the daemon base URL and non-secret presentation metadata. Bearer tokens
live in Keychain, never preferences or logs. Authentication is established by a real RPC rather
than trusting local profile state. A 401 clears the active authenticated session while retaining
the profile so the user can re-enter or replace the token.

The app treats daemon data as authoritative and keeps only client-local navigation, drafts,
preferences, and read watermarks. Session, backlog, usage, and workstream state are projections of
RPC responses/events and refresh when the app returns to the foreground.

### Navigation and interaction

One workspace drawer anchors project selection and cross-project recents. Project/session routes
flow through a single router so list taps, notifications, and `ycc://` links share authentication,
project selection, and duplicate-push handling. A deep link identifies a project/session but never
carries credentials.

Transcript behavior follows the shared client contract in spec §18: durable rows, one transient
live tail, no scroll jumps while reading history, structured question sheets, graceful
interrupt/steer/resume, and confirmed hard stop. Daemon work loops remain daemon-owned because iOS
background execution cannot reliably host long-running work.

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
