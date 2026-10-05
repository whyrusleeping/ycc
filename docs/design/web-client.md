# Design: desktop web client

> Status: accepted; implementation phased through the backlog.

## Context

The daemon's Connect API already carries everything the TUI and iOS app do. A laptop user should be
able to drive ycc from a desktop browser with the same reach as those clients instead of using the
TUI. The first embedded web client was a deliberately narrow, phone-sized, dependency-free page
that watched and steered existing sessions through 11 of the service's RPCs. The native iOS app now
covers the phone, so the web client is redesigned as a desktop-first client targeting full
TUI/iOS parity.

## Decisions

### Desktop-first layout

The client targets laptop and desktop viewports (about 1024px wide and up). It does not maintain a
phone layout; narrow windows remain usable but are not a design target. The shell has three
regions:

- a sidebar with the project switcher, the session list (status, live, needs-answer and unread
  markers), and navigation to Backlog, Work loop, Workstreams, Usage, Files, Memory/Plans, and
  Settings;
- a main pane showing the selected surface, typically a session transcript with its composer;
- a closable, resizable inspector pane for contextual detail: task detail, commit or working-tree
  diffs, file contents, and expanded tool or reasoning rows, so detail opens beside the transcript
  instead of replacing it.

Routes are real URL paths (for example `/p/<project>/s/<session>`, `/p/<project>/backlog/<id>`) so
browser history, reload, multiple tabs, and bookmarks work. Desktop affordances are first-class:
a command palette, keyboard shortcuts listed in a help overlay, drag-and-drop and clipboard-paste
image attachments, and browser notifications for questions, idle completion, and errors while the
tab is in the background.

### Source, toolchain, and embedding

Source lives in `clients/web/` as a TypeScript React application built with Vite, with Node pinned
via `.nvmrc`. The Connect client is generated from `proto/ycc/v1/ycc.proto` by a local
`protoc-gen-es` plugin (`buf.gen.web.yaml`) into `clients/web/src/gen/`, and the generated code is
committed. A proto change therefore regenerates and commits the Go, Swift, and web outputs
together.

The production bundle is built into `internal/web/dist/` and committed, so `go build` and
`go test` still need no Node, npm, or network access; Node is needed only to change the web
client. The build writes a manifest containing a content hash of its inputs (sources, lockfile,
build configuration). A Go test recomputes that hash and fails when the committed bundle is stale
relative to its sources.

The daemon serves the bundle at `/` when `--web` is enabled, as before. Unknown non-asset paths fall
back to `index.html` for client-side routing; the Connect service keeps its own path prefix.
Fingerprinted assets are served as immutable and `index.html` is not cached, so a daemon upgrade
takes effect on reload.

### Data access

Every call goes through the generated typed client over the Connect protocol; no hand-written
wire framing or REST facade exists. Session transcripts use the daemon's indexed presentation APIs
(`GetSessionView`, `GetSessionViewPage`, `GetSessionViewDetail`, `SubscribeSessionView`), as the iOS
app does. This keeps long sessions bounded: the client loads the latest rows, pages earlier rows
on demand, fetches full row detail explicitly, and resubscribes from `indexed_through_seq` after a
disconnect. Transient updates such as live turn text replace a live tail and never move the
resume cursor.

Unary reads are cached per query, refreshed on window focus, and invalidated by the mutations and
session events that change them. Mutations replace local state with the daemon's canonical response
rather than trusting optimistic edits. Unread state is a client-side watermark per session kept in
browser storage, matching the iOS read store.

### Authentication

Static assets are public; every RPC stays behind the daemon's bearer-token middleware. The page asks
for the token, validates it with an authenticated RPC, and keeps it in browser local storage for that
origin. The token is never placed in a URL, query string, fragment, or daemon log. A 401 returns the
user to token entry. This suits a private-network personal tool; a shared or untrusted browser
profile is outside the trust model.

### Rendering safety

Model, tool, and file text is untrusted. Markdown is rendered with raw HTML disabled, links are
restricted to safe schemes, and code, diffs, and tool output render as text with syntax
highlighting applied to escaped tokens. No event payload is inserted as HTML. The shared transcript
invariants in spec §18 apply: model and user turns are prominent, tool, reasoning, review, and
system detail folds, question plumbing coalesces into one exchange, and new events never move a
reader who has scrolled away from the live edge.

### Reachability

The daemon binds an ordinary address chosen by the operator. Tailscale, an SSH tunnel, or another
private network provides reachability outside ycc; the daemon does not embed tsnet. Non-loopback
token enforcement applies regardless of whether web assets are enabled.

### Verification

Projection and reducer logic is unit-tested with Vitest, including the shared
`testdata/event-contract/` fixtures that the TUI and YccKit also consume. Go tests cover asset
routing, history fallback, cache headers, authentication boundaries, and bundle freshness. Manual
browser verification follows `plans/web-client-smoke.md`.

## Rejected alternatives

- Keeping the dependency-free vanilla client: full parity (backlog editing, settings, workstreams,
  usage, diffs, markdown) would grow one untyped script far beyond what can be maintained, and
  hand-rolled wire framing duplicates what the generated Connect client provides.
- Building the bundle during `go build` or `go generate`: this would make every Go build depend on
  Node and the npm registry.
- A separate SPA server: it adds deployment and version-skew failure modes.
- A REST/SSE facade: it duplicates the public API and event semantics.
- Keeping a phone layout alongside the desktop one: the iOS app covers the phone, and a dual layout
  doubles the UI work for every surface.
- Embedding tsnet: it couples ycc to one private-network implementation without changing the client
  protocol.

## Superseded design

The first web client (backlog tasks 0145, 0152, 0153) was a phone-sized, framework-free page
embedded as hand-written `index.html`/`app.js`/`app.css`. It parsed Connect streaming envelopes
itself, folded raw `Subscribe` events client-side, and covered session discovery, transcripts,
input, questions, interrupt/resume, and stop. It was replaced by the client described above once
that client reached the same session-level coverage.
