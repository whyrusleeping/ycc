---
id: "0429"
title: Token for every daemon + Host allowlist (tokenless loopback / DNS rebinding / --web)
status: todo
priority: 2
created: "2026-10-06"
updated: "2026-10-06"
depends_on: []
spec_refs: []
---

## Description
From security audit 0427 (F4, F5). The in-process one-shot daemon is always tokenless; --background and loopback `ycc daemon` are tokenless when YCC_TOKEN is unset. On a multi-user host, other users or host-networked containers can then drive them (a shell as the daemon user). No Host/Origin check exists, so DNS rebinding works against tokenless daemons. Nothing stops `ycc daemon --web` without a token from being published through ngrok, which connects from loopback.

## Acceptance criteria
- In-process daemon: random per-process token, passed to the in-process client.
- --background / loopback daemon without YCC_TOKEN: auto-generate a token persisted 0600 in the state dir; local clients read it (`ycc`, `ycc doctor`, completion).
- When no token is configured (if that remains possible), reject requests whose Host is not loopback.
- `--web` requires a token.
- Docs (remote-api.md, cli.md) updated; tests for each.

## Work log
