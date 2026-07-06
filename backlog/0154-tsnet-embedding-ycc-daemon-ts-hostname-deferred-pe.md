---
id: "0154"
title: 'tsnet embedding: ycc daemon --ts-hostname (deferred per web-client design)'
status: proposed
priority: 5
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0152"
spec_refs:
    - docs/design/web-client.md#8. tsnet embedding (explicit decision — required by acceptance criteria)
---

## Description
Deferred by explicit decision in docs/design/web-client.md §8 (documentation-only recommendation for now): embed Tailscale via tsnet so the daemon appears on the tailnet with zero host firewall/port/TLS setup.

Sketch from the design doc:
- `ycc daemon --ts-hostname <name>` → `tsnet.Server{Hostname: ...}` + `ts.Listen("tcp", ":80")` (and/or `:443` with tsnet TLS) replacing `net.Listen` in Serve().
- Optionally use `LocalClient.WhoIs` tailnet identity as an alternative to the bearer token (could drop the web token-entry screen on tsnet deployments).

Revisit only after the web client ships, and weigh the full `tailscale.com` dependency tree against the convenience of not running host tailscaled. If the dependency cost is deemed too high, consider a separate build tag or companion binary.

## Acceptance criteria
- [ ] Decision revisited with real data (binary size / dep-tree impact measured).
- [ ] If pursued: `--ts-hostname` works end-to-end from a phone on the tailnet; guardrails documented.

## Work log
