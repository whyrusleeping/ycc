---
id: "0164"
title: 'internal/mcp: client manager, [mcp.servers.X] config parsing, gollama.Tool bridge'
status: proposed
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 8. Tools
    - 13. Backends & model registry
    - docs/design/mcp.md#3. Library survey (an acceptance criterion)
---

## Description
From docs/design/mcp.md §3/§4/§5/§7 (design spike, task 0147).

Add the `[mcp.servers.<name>]` config types + validation to internal/config (name ∈ [a-z0-9_-]+, exactly one of command/url, roles ⊆ {chat, pm, implementer} with reviewer/coordinator/capture rejected as a hard config error, env entries resolved via os.Getenv then internal/secrets.Lookup, timeout_s default 60). Add a new `internal/mcp` package on the official `github.com/modelcontextprotocol/go-sdk`: connect (stdio CommandTransport + streamable-HTTP StreamableClientTransport), tools/list, and a per-tool `gollama.Tool` wrapper with `mcp__<server>__<tool>` namespacing (reject-and-warn on >128 chars), per-call timeout, and result mapping (text→Content, image→Images, isError→IsError, resource→text note, transport failure→IsError result). Connection manager owns connect/close/one-lazy-reconnect-per-call.

## Acceptance criteria
- [ ] Unit-tested against an in-process go-sdk MCP server (stdio): connect, list, call (text + image + isError), timeout, and mcp__server__tool naming all covered.
- [ ] Config validation errors are loud and name the offending server; roles listing reviewer/coordinator/capture fail config load.
- [ ] No wiring into sessions yet (that is the follow-on task).

## Work log
