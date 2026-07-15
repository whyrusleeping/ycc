---
id: "0203"
title: Keep daemon bearer tokens out of spawned process arguments
status: todo
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - Daemon lifecycle & projects
    - RPC protocol (Connect)
---

## Description
The background-daemon startup path currently appends the bearer token as `-token <value>` to the detached child's argv. Even when the caller supplied `YCC_TOKEN`, this exposes the token through process listings and `/proc/.../cmdline` on applicable systems.

Pass the token through the child's environment or another non-argv mechanism while retaining authenticated readiness probing and remote-client behavior.

## Acceptance criteria
- [ ] `EnsureBackgroundDaemon` never places the bearer token in child argv.
- [ ] A newly spawned daemon still receives the token via `YCC_TOKEN` or an equivalently private mechanism and enforces it.
- [ ] An existing token-protected local daemon is probed and attached using the caller's token.
- [ ] Tests inspect the constructed child command/environment without exposing a real token in failure logs.
- [ ] CLI/help documentation recommends `YCC_TOKEN` for secrets and does not encourage process-argument exposure.
- [ ] Token values are not printed in daemon startup errors or logs.
- [ ] `go test ./...` passes.

## Work log
