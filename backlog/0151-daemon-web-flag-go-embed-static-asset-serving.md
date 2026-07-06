---
id: "0151"
title: 'Daemon: --web flag + go:embed static asset serving'
status: todo
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

## Work log
