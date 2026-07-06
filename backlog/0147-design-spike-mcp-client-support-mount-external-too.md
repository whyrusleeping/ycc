---
id: "0147"
title: 'Design spike: MCP client support (mount external tool servers into sessions)'
status: todo
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 8. Tools
---

## Description
Design spike. MCP (Model Context Protocol) is the de-facto standard for giving agents extra tools (databases, browsers, project trackers, internal APIs). ycc's tool registry is already gollama `Tool` values behind one dispatch path, so an MCP *client* that mounts a configured server's tools into a session (chat/implementer first; coordinator likely excluded to keep orchestration tight) is a contained addition with outsized ecosystem payoff — it outsources the long tail of integrations.

## Acceptance criteria
- [ ] Design doc (docs/design/mcp.md): config shape (`[mcp.servers.X] command/url`), lifecycle (spawn/connect per daemon vs. per session), tool namespacing, which roles get MCP tools, security posture (reviewer sandbox must NOT get arbitrary MCP tools), and event-log representation of MCP tool calls.
- [ ] Survey of Go MCP client libraries vs. hand-rolling the (small) protocol subset needed.
- [ ] Follow-on implementation tasks filed from the doc.

## Work log
