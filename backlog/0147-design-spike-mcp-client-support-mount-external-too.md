---
id: "0147"
title: 'Design spike: MCP client support (mount external tool servers into sessions)'
status: done
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

## Plan

Design spike: write docs/design/mcp.md (status: proposal, matching the style of docs/design/forge-integration.md), grounded in the current architecture. No production code lands. After review, the coordinator files follow-on tasks (as 'proposed', mirroring the forge spike precedent) and back-fills their ids into the doc.

Doc must cover, with positions taken (not open-ended surveys):

1. **Context & seams**: ycc tools are plain gollama.Tool values behind one Registry/dispatch (internal/tools); role toolsets assembled in orchestrator.BuildMode (chat/pm) and CoordinatorTools/spawn paths (implementer/reviewers). Mounting an MCP server's tools = adding namespaced gollama.Tool wrappers to selected registries.

2. **Library survey & recommendation** (acceptance criterion): compare (a) official modelcontextprotocol/go-sdk (v1.6.x, maintained with Google; stdio + streamable-HTTP transports), (b) mark3labs/mcp-go (popular community lib), (c) metoro-io/mcp-golang v0.16.0 — already a transitive dep via gollama's existing ToolsFromMCP bridge (gollama mcp.go: HTTP transport only, no namespacing/lifecycle/timeouts/image mapping), (d) hand-rolling the JSON-RPC subset (initialize, tools/list, tools/call). Recommend: official go-sdk inside a new internal/mcp package, with ycc owning the Tool bridge (~small); note gollama.ToolsFromMCP as prior art deliberately not used and why. Hand-rolling rejected (protocol version negotiation, streamable HTTP, notifications — not worth owning).

3. **Config shape**: `[mcp.servers.<name>]` in ycc.toml with `command = ["...", "args"]` (stdio) XOR `url = "..."` (streamable HTTP); optional `env` (names of env vars to resolve via os.Getenv + secrets store, mirroring key_env — literal secrets never in checked-in ycc.toml), `allow`/`deny` tool filters, `roles = ["chat","implementer"]` (default ["chat"]), `timeout_s`. Server name constrained to [a-z0-9_-].

4. **Lifecycle**: per-SESSION spawn/connect, lazy on first registry build that mounts the server; owned by the session, closed at session end alongside jobs.Registry KillAll; stdio child spawned with cwd = workspace root. Justify vs per-daemon sharing (isolation, per-workspace cwd/state, simpler failure domain; sharing is a deferred optimization). Failure modes: connect failure → session still starts, Narration warning, tools absent; call timeout; crash → error tool result + one lazy reconnect attempt on next call.

5. **Namespacing**: `mcp__<server>__<tool>` (Claude Code convention; satisfies Anthropic's [a-zA-Z0-9_-] name regex, dots disallowed). Prefix guarantees builtins (Read/Bash/finish/submit_review) can't be shadowed since Registry.Add replaces same-name tools.

6. **Roles / security posture**: chat by default; implementer opt-in per server; pm opt-in; coordinator excluded (orchestration stays tight); reviewer HARD-excluded in code, not config — reviewer non-mutation is sandbox-enforced (internal/sandbox) and arbitrary MCP tools would bypass it entirely. Capture agent (ReadOnly) excluded. Trust model stated explicitly: an MCP server is an operator-configured trusted extension running OUTSIDE workspace confinement (no Workspace.resolve); config is the consent boundary; prompt-injection surface of tool results acknowledged.

7. **Event-log representation**: no new event types for calls — MCP tools flow through the same engine loop, so tool_call/tool_result events carry the namespaced name (auditable, replay-safe); result mapping text→Content, image blocks→ToolResult.Images, isError→IsError, other content degraded to a text note. One new observability event or Narration line for server connect/failure at mount time (take a position: Narration is enough for v1).

8. **Follow-on tasks section** (titles + acceptance criteria; ids back-filled after filing): (i) internal/mcp client manager + gollama.Tool bridge; (ii) config parsing + session wiring/mounting + lifecycle + events; (iii) doctor check & `ycc mcp` listing (observability); (iv) spec §8/§13 update recording MCP support. Also update spec.md ONLY if needed — no, spike lands doc only; spec update is follow-on (iv).

Verification: doc reads coherently against the named code seams (spot-check file/symbol references are real); `go build ./...` untouched (doc-only change); ycc spec-check if it validates doc references.

### Starting points
- internal/tools/tools.go — Registry, Workspace confinement (resolve/resolveRead)
- internal/tools/worker.go:52-66 — Editing()/Worker() toolsets; reviewer.go — Reviewer() + sandboxedBash
- internal/orchestrator/modes.go:65 BuildMode — per-mode registries; orchestrator.go:389/609 — implementer/reviewer spawn registries
- gollama mcp.go — existing ToolsFromMCP via metoro-io/mcp-golang v0.16.0 (HTTP-only bridge): $GOMODCACHE/github.com/whyrusleeping/gollama@v0.0.0-20260706030410-d8e738f47e06/mcp.go
- spec.md §8 Tools (line ~467), §13 Backends & model registry (line ~750) — config TOML shape precedent
- docs/design/forge-integration.md — design-doc style/structure to mirror (status header, numbered sections, follow-on tasks section)
- internal/secrets/secrets.go — key_env/secrets resolution pattern for MCP server env
- internal/event/event.go — event types incl. Narration, ToolCall/ToolResult

## Work log
- 2026-07-06 plan: Design spike: write docs/design/mcp.md (status: proposal, matching the style of docs/design/forge-integration.md), grounded in the current architecture. No production code lands. After review, the coo
…[truncated]
- 2026-07-06 context hints: 8 recorded with plan
- 2026-07-06 context hints: docs/design/forge-integration.md — structure/voice to mirror; internal/tools/tools.go — Registry.Add semantics (same-name replacement!), Workspace.resolve confinement; internal/tools/worker.go:52 
…[truncated]
- 2026-07-06 implementer report: Wrote docs/design/mcp.md — a design spike (status: proposal, task 0147) for MCP client support, mirroring the structure/voice of docs/design/forge-integration.md. No production code lands.  The doc 
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change is a doc-only design spike (docs/design/mcp.md) plus backlog bookkeeping, exactly as the task requires. The doc mirrors the forge-integration spike's status header and structure and covers 
…[truncated]
- 2026-07-06 decision: accept — commit: docs: MCP client design spike — config, lifecycle, namespacing, role/security posture, go-sdk recommendation (task 0147); file follow-on tasks 0164–0167
