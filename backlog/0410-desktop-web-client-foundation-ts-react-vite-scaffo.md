---
id: "0410"
title: 'Desktop web client foundation: TS/React/Vite scaffold, generated Connect-ES client, app shell, session transcript + interaction (replaces vanilla client)'
status: done
priority: 2
created: "2026-10-02"
updated: "2026-10-05"
depends_on: []
spec_refs:
    - docs/design/web-client.md#Source, toolchain, and embedding
    - docs/design/web-client.md#Data access
    - docs/design/web-client.md#Desktop-first layout
    - Client interaction model
---

## Description
First phase of the desktop web client (docs/design/web-client.md). It reaches session-level parity with the existing vanilla phone client, then replaces it.

## Scope
**Toolchain**
- `clients/web/`: TypeScript + React + Vite, `.nvmrc` pinning Node (v24 is available here via `~/.nvm`; system node is v12 and too old), npm with a committed lockfile.
- `buf.gen.web.yaml` uses a LOCAL `protoc-gen-es` (v2, which includes service descriptors for `@connectrpc/connect`) from `clients/web/node_modules`, outputting to `clients/web/src/gen/` (committed). Document the three-way regen (Go, Swift, web) in CONTRIBUTING.md.
- An npm build script (plus a thin `scripts/web-build.sh`) builds into `internal/web/dist/` (emptying it first) and writes a manifest with a content hash of the build inputs (src, gen, package.json, lockfile, vite/ts config, index.html). A Go test in internal/web recomputes the hash with the same algorithm and fails when the committed bundle is stale. It must run without Node.
- Commit the built bundle. `go build ./...` and `go test ./...` must still work without Node.

**Daemon serving (internal/web, internal/daemon/serve.go)**
- History fallback: unknown non-asset GET paths serve index.html. Connect paths are unaffected.
- Cache headers: fingerprinted `/assets/*` are immutable; index.html is no-cache.
- Keep existing auth guarantees: assets are public, RPCs need the bearer token, and the non-loopback no-token guard is unchanged. Update internal/daemon/web_test.go to match.

**Client**
- Transport: `@connectrpc/connect-web` Connect transport and an auth interceptor (bearer from localStorage). A 401 anywhere goes to token entry. Token entry is validated via ListProjects. The token is never put in a URL.
- Shell: sidebar (project switcher, session list from ListSessionHistory with status/live/needs-answer markers, nav placeholders for later phases), main pane, closable/resizable inspector pane. Path routing (`/p/<project>/s/<session>`, etc.).
- Server-state caching (e.g. TanStack Query), refreshed on focus.
- Session view built on GetSessionView / GetSessionViewPage (load earlier on scroll-up) / GetSessionViewDetail (explicit expand, or open in the inspector) / SubscribeSessionView. Resubscribe from indexed_through_seq with backoff. Transient turn text forms a replaceable live tail that never advances the cursor. Persisted-only sessions get a finite read-only view.
- Rows: user/model turns prominent; tool, reasoning, review, and system detail folded; question plumbing coalesced. All text is rendered as text (plain text is fine here; markdown comes in a later phase). Follow-at-bottom with a jump-to-latest pill and no scroll yank.
- Interaction: multiline composer (Enter sends, Shift+Enter adds a newline) → SendInput, with queued/delivered states. Pending questions come from SessionViewState → an answer panel (AnswerQuestion/AnswerQuestions with option or free text), dismissed by durable state, including answers from other clients. Phase-gated Interrupt/Resume/steer and confirmed Stop. Non-fatal RPC errors appear as toasts.

**Removal and tests**
- Delete the vanilla `internal/web/dist/{app.js,app.css,index.html}`, `internal/web/app_test.js`, and the node runner in web_test.go.
- Vitest unit tests cover the session-view reducer (upsert/delete rows, paging merge, transient tail, resubscribe cursor), plus the `testdata/event-contract/` fixtures as far as they apply to the presentation API.
- Rewrite `plans/web-client-smoke.md` for desktop. Update the package doc in internal/web/web.go and the remote-api/cli docs mentioning the web client. Drop the "Until the foundation phase lands" status note in docs/design/web-client.md.

## Acceptance criteria
- [ ] `go build ./... && go test ./...` passes on a machine without Node, including the bundle freshness test.
- [ ] `npm ci && npm run build && npm test` in clients/web passes with Node from .nvmrc. Rebuilding produces the committed bundle (the freshness test passes after the rebuild).
- [ ] `ycc daemon --web`: token entry → session list → open a live session → stream, page earlier history, expand detail, send input, answer single and batch questions, interrupt/resume, stop. Reload on a deep link restores the view.
- [ ] No event or tool text is inserted as HTML.

## Work log
