# Design: embedded web client

> Status: accepted and implemented.

## Context

The daemon's Connect API already supports remote observation and interaction. A small browser
client provides immediate phone access without installing a native app, operating a second
service, or maintaining another protocol. It is intentionally narrower than the TUI and iOS app.

## Decisions

### Embedded, dependency-free assets

The daemon embeds static HTML, CSS, and JavaScript and serves them only when web serving is
enabled. The assets are built into the Go binary, so normal builds do not require Node, package
installation, generated bundles, or runtime asset directories. Keeping the client small and
framework-free is a deployment decision, not a general prohibition on JavaScript dependencies.

Static assets are not secret. RPC endpoints remain behind the daemon's bearer-token middleware.
The page asks for a token, validates it with an authenticated RPC, and stores it in browser local
storage for that origin. The token is never placed in the URL, query string, fragment, or daemon
logs. This is suitable for a private-network personal tool; a shared or untrusted browser profile
is outside the trust model.

### Direct Connect protocol

The browser calls the same service as every other client. Unary commands use Connect JSON.
Subscription uses Connect's length-prefixed streaming envelopes and an incremental parser that
handles arbitrary network chunk boundaries. The client advances its resume cursor only for
durable positive sequences; transient turn snapshots update a replaceable live tail but never
alter replay position.

A reconnect starts from the last persisted sequence, clears stale transient presentation, and
reduces replayed events idempotently. Persisted-only sessions use a finite transcript read rather
than pretending to be live.

### Scope and presentation

The web client provides project/session discovery, live and persisted transcripts, input,
structured questions, interrupt/resume, and confirmed stop in a phone-sized layout. It follows the
shared transcript invariants in spec §18: untrusted event text is rendered as text, tool/reasoning
detail folds, reading history is not yanked by new events, and durable answer events resolve gates
across clients.

The client does not host agent execution, store a session copy, edit daemon configuration, or aim
for full TUI parity. Native-only behavior such as Keychain and deep links belongs to iOS.

### Reachability

The daemon binds an ordinary address chosen by the operator. Tailscale or another private network
provides reachability outside ycc; the daemon does not embed tsnet. This avoids a second network
identity, control-plane lifecycle, and credential surface. Non-loopback token enforcement applies
regardless of whether web assets are enabled.

## Rejected alternatives

- A separate SPA server adds deployment and version-skew failure modes.
- A REST/SSE facade duplicates the public API and event semantics.
- Embedding tsnet couples ycc to one private-network implementation without changing the client
  protocol.
- Rendering event payloads as HTML risks transcript injection; all model/tool text remains plain
  text.

Manual browser/device verification is limited to `plans/web-client-smoke.md`; frame parsing and
asset/auth boundaries are automated.
