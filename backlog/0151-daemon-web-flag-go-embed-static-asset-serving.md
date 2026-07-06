---
id: "0151"
title: 'Daemon: --web flag + go:embed static asset serving'
status: done
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0145"
spec_refs:
    - 12. RPC protocol (Connect)
    - docs/design/web-client.md#3. Asset embedding (decision)
---

## Description
First implementation slice of docs/design/web-client.md (§3, §9 item 1).

- New `internal/web` package owning the embedded static assets (`internal/web/dist/`: index.html, app.js, app.css — placeholder shell is fine at this stage) and exposing an `http.Handler` over the embedded FS (`go:embed dist`).
- Add `Web bool` to `daemon.Options`; `buildHandler` (internal/daemon/serve.go) mounts the asset handler at `/` only when set, leaving the Connect handler at `/ycc.v1.SessionService/` (ServeMux longest-prefix routing keeps them separate).
- `ycc daemon` grows a `--web` flag (default off). The one-shot in-process daemon (StartInProcess) leaves it off.
- Static assets are served UNauthenticated by design (see design doc §4); RPCs stay behind the bearer AuthInterceptor unchanged.

## Acceptance criteria
- [ ] With `--web`: `GET /` and `GET /app.js` return 200 with content, no auth required; RPC paths still enforce the bearer token.
- [ ] Without `--web`: `GET /` returns 404; RPC behavior unchanged.
- [ ] Non-loopback no-token guardrail in Serve() unchanged and still effective with `--web` set (test).
- [ ] docs/cli.md daemon flag table updated.

## Plan

Implement the first slice of docs/design/web-client.md (§3, §4 guardrail interplay, §9 item 1): a --web-gated embedded static asset handler on the daemon mux.

1. New package `internal/web`:
   - `internal/web/dist/` with placeholder shell assets: `index.html` (references app.js/app.css, minimal "ycc web client" shell), `app.js`, `app.css`. Content can be minimal but valid — the real SPA lands in task 0152.
   - `web.go`: `//go:embed dist` on an `embed.FS`; `Handler() http.Handler` returning `http.FileServerFS` over `fs.Sub(assets, "dist")` (or equivalent). Doc comment explaining the design decision (unauthenticated by design per web-client.md §4; assets are public code, RPCs stay behind the bearer interceptor).

2. `internal/daemon/serve.go`:
   - Add `Web bool` to `Options` with a comment (serve embedded SPA at `/`; RPC auth unchanged; off for the one-shot in-process path).
   - In `buildHandler`, after mounting the Connect handler, `if o.Web { mux.Handle("/", web.Handler()) }`. ServeMux longest-prefix routing keeps `/ycc.v1.SessionService/` on Connect.
   - `StartInProcess` must not set Web (it already passes caller Options with Web zero-value; ensure it stays off — explicitly zero it there with a comment, matching how Addr is forced).

3. `cmd/ycc/main.go` daemonCommand: add `&cli.BoolFlag{Name: "web", Usage: ...}` and plumb `Web: cmd.Bool("web")` into daemon.Options.

4. Tests (internal/daemon):
   - With Web:true — build handler (or start a test server), GET `/` and `/app.js` return 200 with non-empty content and no Authorization header; an RPC path request without a token (when Token is set) still returns 401/unauthenticated.
   - With Web:false — GET `/` returns 404; RPC behavior unchanged.
   - Guardrail: `Serve(Options{Addr:"0.0.0.0:0", Web:true, ...})` without a token still refuses to start (extend/parallel to TestServeRefusesNonLoopbackWithoutToken).
   - StartInProcess leaves web off: GET `/` on the in-process daemon returns 404.

5. Docs: add `--web` row to the `ycc daemon` flag table in docs/cli.md (default off; "serve the embedded web client at / — static assets are unauthenticated, RPCs still require the bearer token").

Verification: `go build ./... && go test ./internal/daemon/... ./internal/web/...` (plus full `go test ./...` if quick), `go vet ./...`.

### Starting points
- internal/daemon/serve.go — Options struct + buildHandler mux wiring + Serve guardrail
- cmd/ycc/main.go daemonCommand() (~line 540) — flag list + daemon.Options construction
- internal/daemon/serve_guardrails_test.go, internal/daemon/inprocess_test.go — existing test patterns
- docs/design/web-client.md §3/§4 — decisions to follow (unauth'd assets, --web gate, ServeMux prefix routing)
- docs/cli.md '### `ycc daemon`' flag table
- server.NewAuthInterceptor in internal/server/auth.go — RPC-only auth, unchanged

## Work log
- 2026-07-06 plan: Implement the first slice of docs/design/web-client.md (§3, §4 guardrail interplay, §9 item 1): a --web-gated embedded static asset handler on the daemon mux.  1. New package `internal/web`:    - `
…[truncated]
- 2026-07-06 context hints: 6 recorded with plan
- 2026-07-06 context hints: internal/daemon/serve.go — Options struct, buildHandler mux wiring (mux.Handle(path, handler) for /ycc.v1.SessionService/), Serve guardrail, StartInProcess forces Addr; cmd/ycc/main.go daemonCommand
…[truncated]
- 2026-07-06 implementer report: Implemented the first slice of docs/design/web-client.md (§3, §9 item 1): a `--web`-gated embedded static asset handler on the daemon mux.  Changes: - New `internal/web` package:   - `internal/web/d
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change implements the first slice cleanly and completely. A new internal/web package embeds placeholder dist assets via go:embed and exposes an http.Handler (with a well-documented unauthenticated
…[truncated]
- 2026-07-06 decision: accept — commit: daemon: --web flag serving embedded web client via go:embed (task 0151)  New internal/web package embeds placeholder SPA shell assets (dist/) and exposes an http.Handler; buildHandler mounts it at "/"
…[truncated]
- 2026-07-06 usage: 13,328 tok (in 70, out 13,258, cache_r 673,488, cache_w 59,024) · cost n/a (unpriced)
  implementer: 7,563 tok (in 36, out 7,527, cache_r 351,330, cache_w 22,389) · cost n/a (unpriced)
  coordinator: 4,572 tok (in 24, out 4,548, cache_r 292,240, cache_w 24,781) · cost n/a (unpriced)
  reviewer:Claude: 1,193 tok (in 10, out 1,183, cache_r 29,918, cache_w 11,854) · cost n/a (unpriced)
