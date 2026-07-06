---
id: "0145"
title: 'Design spike: embedded web client served by the daemon (+ optional tsnet)'
status: todo
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 14. Persistence & remote sync
    - docs/remote-api.md#Overview
---

## Description
Design spike (docs/design/ doc first, like parallel-workstreams). The daemon already speaks Connect's plain HTTP/JSON with a documented endpoint catalog (docs/remote-api.md) — the phone story is "someone builds a client someday." A minimal embedded web client (single static SPA served by the daemon behind the existing bearer auth) would make remote observation/answering real *today* with no app-store story: `ycc daemon --web` + Tailscale = full phone access.

Scope for a first cut: session list → live event stream (Subscribe over fetch/SSE-style streaming) → send input / answer question pickers / interrupt / stop. Read-mostly; not a full TUI clone.

Also consider in the same spike: embedding Tailscale via tsnet (`ycc daemon --ts-hostname ycc`) so remote access needs zero port/firewall/TLS thought — identity comes from the tailnet.

## Acceptance criteria
- [ ] A design doc (docs/design/web-client.md) covering: asset embedding (go:embed), auth handoff (bearer token entry, or tsnet identity), which RPCs the first cut uses, streaming approach, and phone-form-factor layout of the event stream.
- [ ] Explicit decision on tsnet embedding (in-scope flag vs. documentation-only recommendation).
- [ ] Follow-on implementation tasks filed from the doc.

## Work log
