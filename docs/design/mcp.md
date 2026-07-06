# Design: MCP client support (mount external tool servers into sessions)

> Status: **proposal** (design spike, task 0147). No code lands with this doc;
> the follow-on implementation tasks in §9 are filed separately as backlog
> tasks **0164–0167** (matching the precedent of the forge spike, task 0146 /
> `docs/design/forge-integration.md`).
> Grounded in the current architecture: spec §8 (tools + the reviewer bash
> sandbox), §13 (backends & model registry — the config TOML idiom), the tool
> registry and workspace confinement in `internal/tools`, the per-mode / per-role
> toolset assembly in `internal/orchestrator`, and the session-scoped background
> job lifecycle (`internal/jobs`, `internal/session`).

## 1. Context / problem

MCP (Model Context Protocol) is the de-facto standard for giving an agent extra
tools: databases, browsers, project trackers, internal APIs, cloud consoles. The
long tail of "please also give the agent access to X" is exactly what MCP exists
to outsource — instead of ycc hand-writing an adapter per integration, an
operator points ycc at a configured MCP *server* and its tools appear in the
agent's toolset.

ycc is unusually well-placed to add an MCP **client**, because its tools are
already a single, uniform shape behind one dispatch path:

- **One tool type, one registry.** Every tool ycc exposes is a plain
  `gollama.Tool` (`Name`, `Description`, `Params` JSON schema, `Call`), held in a
  `tools.Registry` that dispatches by name (`internal/tools/tools.go`:
  `Registry.Add` / `Registry.Dispatch`). Worker and orchestration tools "are the
  same kind of object" (spec §8). Mounting an MCP server's tools therefore means
  exactly one thing: **construct a namespaced `gollama.Tool` wrapper per remote
  tool and `Add` it to the selected registries.** No new dispatch path, no engine
  changes.
- **Role toolsets are assembled in a few known places.** The registry each agent
  gets is built in a handful of seams:
  - `orchestrator.BuildMode` (`internal/orchestrator/modes.go:65`) assembles the
    **chat** and **pm** registries (via `tools.Editing`) and delegates **work**
    to `CoordinatorTools`.
  - `CoordinatorTools` (`internal/orchestrator/orchestrator.go:183`) builds the
    **work coordinator** registry.
  - The implementer registry is built inline in `spawn_implementer`
    (`internal/orchestrator/orchestrator.go:389`, `tools.Worker(...)`).
  - The reviewer registry is built inline in `spawn_reviewers`
    (`internal/orchestrator/orchestrator.go:609`, `tools.Reviewer(...)`).
  Adding MCP tools is a matter of augmenting the *right* subset of these with the
  server's wrappers (§6).
- **Result plumbing already carries what MCP returns.** `gollama.ToolResult`
  already has `Content`, `Images` (base64), `Documents`, and `IsError` — the
  exact shape MCP `tools/call` results map onto (§7). The multimodal `Read`
  path (spec §8) proves images ride end-to-end into a tool result.

The design question this spike answers: **which Go MCP library** ycc should
build on (or whether to hand-roll), **the config shape**, **where the MCP client
lives and when it connects/disconnects** (lifecycle), **how remote tools are
named** to avoid clobbering builtins, **which agent roles get MCP tools and why
the reviewer must not**, and **how MCP tool calls appear in the event log**.

## 2. Goals & non-goals

**Goals**

- Let an operator declare an MCP server in `ycc.toml` and have its tools appear,
  namespaced, in selected agent roles' toolsets.
- Support both dominant transports: **stdio** (spawn a child process — the
  common local deployment) and **streamable HTTP** (a remote endpoint).
- Own a small, well-defined `gollama.Tool` bridge: list the server's tools, wrap
  each, and map results (text / images / `isError`) back through the existing
  tool-result plumbing.
- A **session-scoped** lifecycle: connect lazily, own the connection for the
  life of the session, and tear it down deterministically at session end.
- A security posture that is explicit about the trust boundary and that
  **hard-excludes the reviewer** from arbitrary MCP tools in code (§6).
- Zero new event types for tool calls: MCP tools flow through the existing engine
  loop so `tool_call`/`tool_result` events and replay/reopen work unchanged (§7).

**Non-goals**

- Being an MCP **server** (exposing ycc's own tools to other agents). Out of
  scope; this spike is the client side only.
- MCP features beyond tools: **resources**, **prompts**, **sampling**, **roots**,
  **elicitation**. v1 mounts *tools* only; other capabilities are deferred (§8).
- OAuth / interactive auth flows for HTTP servers. v1 supports static headers /
  env-derived bearer tokens (§3); the full OAuth dance is deferred.
- Per-daemon shared MCP connections. v1 is per-session; sharing is a discussed,
  deferred optimization (§4).
- Giving the reviewer or the read-only capture agent MCP tools. Deliberately
  excluded (§6).

## 3. Library survey (an acceptance criterion)

Four ways to speak MCP from Go, compared against what ycc actually needs
(initialize → `tools/list` → `tools/call`, over stdio *and* streamable HTTP,
with lifecycle, timeouts, and image/`isError` result mapping):

| Option | Transports | Maintenance | Fit for ycc |
|--------|-----------|-------------|-------------|
| **`modelcontextprotocol/go-sdk`** (official, with Google) | stdio (`CommandTransport`) + streamable HTTP (`StreamableClientTransport`) | Active; tracks spec revisions (v1.6.x → v1.7.0 covers MCP `2026-07-28` back to `2024-11-05`) | **Recommended.** Clean client API, both transports, protocol-version negotiation handled. |
| `mark3labs/mcp-go` | stdio + HTTP | Popular community lib; implements recent spec w/ back-compat | Viable, but the official SDK wins on longevity and is the one the ecosystem consolidates on. |
| `metoro-io/mcp-golang` v0.16.0 | HTTP/SSE/stdio exist in the lib | Slower-moving | **Already a transitive dep** via gollama — but insufficient as wired (see below). |
| Hand-roll the JSON-RPC subset | whatever we build | ours to carry | **Rejected** (below). |

### 3.1 Why not reuse gollama's existing bridge

gollama already ships `ToolsFromMCP(ctx, host)` (module cache:
`github.com/whyrusleeping/gollama@…/mcp.go`) built on `metoro-io/mcp-golang`
v0.16.0 (already in ycc's `go.sum` as an indirect dep). It is genuine prior art
and it is **deliberately not used** here, because as written it is missing almost
everything this design needs:

- **HTTP transport only.** It hardcodes `http.NewHTTPClientTransport(host)`. There
  is no stdio path — and stdio (spawn a child) is the dominant local MCP
  deployment mode. This alone disqualifies it for the common case.
- **No namespacing.** It wraps each remote tool under its *bare* name (`mt.Name`).
  Because `Registry.Add` replaces a same-named tool
  (`internal/tools/tools.go:65`), a server exposing a tool literally named
  `Bash` or `Read` would **silently shadow the builtin**. §5 fixes this with a
  prefix; the bridge does not.
- **No lifecycle.** The `*mcpgo.Client` is created inside `ToolsFromMCP` and never
  closed; there is no owner, no shutdown, no reconnect.
- **No per-call timeout.** `mcpCall` calls `client.CallTool(ctx, …)` with the
  caller's context directly — a hung server hangs the agent turn.
- **Text-only result mapping.** It concatenates only `TextContent`, dropping image
  content, and never sets `IsError`. MCP's `isError` and image blocks are lost.

Rather than fork/extend a bridge built on the slower-moving lib, ycc owns a small
bridge on the official SDK where all five gaps are trivial to close. gollama's
`ToolsFromMCP` stays untouched (it is not on ycc's path).

### 3.2 Why not hand-roll

The "subset" is only small on paper. A correct client must do protocol-**version
negotiation** in `initialize`, implement **streamable HTTP** (POST + SSE, session
ids, resumability) as well as stdio framing, handle **`tools/list` pagination**
and `notifications/tools/list_changed`, and cope with server-initiated
notifications. That is exactly the surface the official SDK maintains against a
moving spec. Owning it is a false economy; we would be re-implementing a
dependency Google helps maintain.

### 3.3 Recommendation

Add a new **`internal/mcp`** package that:

1. wraps the official `modelcontextprotocol/go-sdk` `mcp` package;
2. builds the transport from config — `&mcp.CommandTransport{Command:
   exec.Command(cmd[0], cmd[1:]...)}` for stdio, `mcp.StreamableClientTransport`
   for `url`;
3. `client.Connect` → `session.ListTools` → for each tool, produce a
   `*gollama.Tool` whose `Call` invokes `session.CallTool(ctx,
   &mcp.CallToolParams{Name, Arguments})` under a per-call timeout and maps the
   `*mcp.CallToolResult` back to `gollama.ToolResult` (§7);
4. owns connect/close/reconnect (§4).

The bridge is the small, ycc-specific part; the protocol is the SDK's job. New
direct dependency: `github.com/modelcontextprotocol/go-sdk` (the transitive
`metoro-io/mcp-golang` stays only as gollama's dep).

## 4. Config shape

A new `[mcp.servers.<name>]` table in `ycc.toml`, sitting beside `[models.X]` /
`[roles]` and following the same idiom as spec §13 (notably `key_env`: secret
**names**, never secret values, live in the checked-in config):

```toml
[mcp.servers.github]
command = ["github-mcp-server", "stdio"]   # stdio transport: argv to spawn
# url = "https://mcp.example.com/mcp"       # streamable-HTTP transport (mutually exclusive with command)
env     = ["GITHUB_TOKEN"]                  # env var NAMES to pass through, resolved at spawn (see below)
allow   = ["search_issues", "get_issue"]    # optional allowlist (default: all tools)
deny    = ["delete_repo"]                    # optional denylist (applied after allow)
roles   = ["chat", "implementer"]           # which roles mount it (default: ["chat"])
timeout_s = 60                               # per-call timeout (default 60)

[mcp.servers.postgres]
command = ["mcp-server-postgres", "--dsn", "$PG_DSN"]
roles   = ["chat"]
```

Config struct (mirroring the `toml:"…"` style of `internal/config/config.go`),
carried on `config.Config` as `Mcp Mcp \`toml:"mcp,omitempty"\``:

```go
type Mcp struct {
    Servers map[string]MCPServer `toml:"servers,omitempty"`
}
type MCPServer struct {
    Command  []string `toml:"command,omitempty"`   // stdio argv (XOR url)
    URL      string   `toml:"url,omitempty"`        // streamable-HTTP endpoint (XOR command)
    Env      []string `toml:"env,omitempty"`        // env var NAMES to resolve + pass to the child / as headers
    Allow    []string `toml:"allow,omitempty"`      // tool allowlist (default: all)
    Deny     []string `toml:"deny,omitempty"`       // tool denylist (applied after allow)
    Roles    []string `toml:"roles,omitempty"`      // roles that mount it (default: ["chat"])
    TimeoutS int      `toml:"timeout_s,omitempty"`  // per-call timeout seconds (default 60)
}
```

**Rules & validation (fail loudly at config load):**

- **Server name** ∈ `[a-z0-9_-]+`. Names embed into tool names (§5), so this is
  enforced, not advisory.
- **Exactly one of `command` / `url`.** Neither or both ⇒ config error naming the
  server.
- **`roles`** ⊆ the *mountable* roles `{chat, pm, implementer}` (§6). Listing
  `reviewer`, `coordinator`, or `capture` is a **hard config error** — the
  reviewer exclusion in particular is not silently ignored, it is rejected so an
  operator can't believe they granted it.
- **`env`** entries are variable **names**. At spawn (stdio) each is resolved
  from the daemon's environment (`os.Getenv`) and, failing that, the ycc secrets
  store (`internal/secrets.Lookup`) — the same precedence `key_env` uses for
  backend tokens. Literal secrets never appear in the checked-in `ycc.toml`. For
  `url` servers, resolved `env` values become request headers (a `TOKEN` →
  `Authorization: Bearer …` convention; header-name mapping can be refined in
  implementation).
- **`timeout_s`** default 60; `<= 0` ⇒ default.

## 5. Tool namespacing

Remote tools are mounted under **`mcp__<server>__<tool>`** — the Claude Code
convention (double-underscore separators). Rationale:

- **Anthropic's tool-name constraint** is `^[a-zA-Z0-9_-]{1,128}$` — **dots are
  not allowed**, which rules out the tempting `server.tool`. Double underscore is
  a safe, readable separator within the allowed set.
- **It forecloses shadowing of builtins.** `Registry.Add` replaces a tool of the
  same name (`internal/tools/tools.go:65`), so an *unprefixed* MCP tool named
  `Bash`, `Read`, `finish`, or `submit_review` would silently replace the real
  one. The `mcp__` prefix guarantees a remote tool can never collide with a
  builtin (no builtin starts with `mcp__`) nor with another server's tools.
- **It is self-describing in the event log.** Every `tool_call`/`tool_result`
  carrying an `mcp__github__…` name is instantly attributable and filterable
  (§7).

The full name must fit 128 chars. On mount, a name that would exceed the limit is
**rejected with a Narration warning** (that one tool is dropped; the rest mount).
Deterministic truncation is a possible refinement but silent truncation risks
collisions, so v1 rejects-and-warns.

## 6. Which roles get MCP tools, and the security posture

MCP tools run **outside** ycc's workspace confinement. A builtin tool goes
through `Workspace.resolve` / `resolveRead` (`internal/tools/tools.go`) which
confine file writes/reads to the workspace root; an MCP tool call is an opaque
RPC to a process/endpoint ycc does not sandbox. That single fact drives the whole
role matrix.

| Role | MCP tools? | How decided |
|------|-----------|-------------|
| **chat** | **yes, by default** | default `roles = ["chat"]`; the freeform assistant is where "give me tool X" lives. |
| **pm** | opt-in per server | `roles` must list `pm`. Planning rarely needs external tools, but allowed. |
| **implementer** | opt-in per server | `roles` must list `implementer`. Useful (e.g. a DB or tracker during implementation). |
| **work coordinator** | **no** | Excluded. Orchestration stays tight — the coordinator delegates real work to the implementer; giving it external tools blurs that. |
| **capture agent** (`tools.ReadOnly`) | **no** | The quick-add backlog capture agent is intentionally read-only (no shell, no edits); MCP tools contradict its whole purpose. |
| **reviewer** (`tools.Reviewer`) | **no — hard-excluded in code, never configurable** | See below. |

**The reviewer exclusion is the load-bearing security decision.** Reviewer
non-mutation is enforced by the **bash sandbox** (`internal/sandbox`, spec §8):
the reviewer's `Bash` runs with the workspace mounted read-only
(`tools.sandboxedBash`, `internal/tools/reviewer.go:32`) so a reviewer cannot
alter the change it is judging. An arbitrary MCP tool is an **unsandboxed
side-effect channel** — it can write files, hit networks, mutate databases —
entirely outside Landlock/bwrap. Mounting MCP tools on a reviewer would silently
defeat the sandbox that is the reviewer's core safety property. Therefore:

- the reviewer is **not** in the set of mountable roles;
- listing `reviewer` (or `coordinator` / `capture`) in a server's `roles` is a
  **config-load error** (§4), not a silent no-op;
- an implementation task ships an explicit **test** asserting no `mcp__…` tool is
  ever present in a `tools.Reviewer` registry, so a future refactor can't
  regress it.

**Trust model (stated explicitly).** An MCP server is an **operator-configured,
trusted extension**. Its tools run with the daemon's privileges, outside
workspace confinement. The **`ycc.toml` entry is the consent boundary** — adding
a server is an explicit, checked-in act. Two consequences worth naming:

- *Checked-in config = repo collaborators can propose servers.* A malicious
  server entry is, in impact, equivalent to the pre-existing exposure that
  checked-in code is run by the agent's `Bash`; it is not a new privilege
  boundary. The `env`-indirection (§4) keeps actual secrets out of the repo, so a
  merged config change cannot by itself exfiltrate a token.
- *Prompt-injection via tool results.* MCP tool **results** are untrusted text
  entering the agent's context and could attempt prompt injection. This is the
  same risk class as web tool results (`web_search`/`fetch_page`) today: no
  privilege boundary is crossed by the text itself, and the highest-trust role
  (the reviewer) is isolated from MCP entirely. We acknowledge the risk and
  accept the web-tool-equivalent posture for v1 rather than adding result
  sanitization.

## 7. Event-log representation

**No new event types for calls.** Because an MCP tool is just a `gollama.Tool` in
the same `Registry`, its call goes through the identical engine loop that emits
`tool_call` / `tool_result` (`internal/event/event.go`: `ToolCall = "tool_call"`,
`ToolResult = "tool_result"`; rendered in `event.Render`). The events already
carry the tool name and args, so:

- MCP calls are **auditable and replay/reopen-safe** with zero new plumbing — a
  reopened session replays `mcp__github__search_issues` calls like any other.
- The `mcp__server__` prefix makes them **filterable** in the TUI/log without a
  new field.

**Result mapping** (in the `internal/mcp` bridge, `*mcp.CallToolResult` →
`*gollama.ToolResult`):

| MCP result | gollama.ToolResult |
|------------|--------------------|
| `TextContent` block(s) | concatenated into `Content` |
| `ImageContent` block(s) | appended to `Images` (base64) — rides the existing multimodal tool-result path (spec §8) |
| `IsError == true` | `IsError = true` (so the model sees a recoverable error, matching `errResult`) |
| resource / other content | degraded to a short text note appended to `Content` (e.g. `[resource: <uri>]`) — v1 mounts tools only (§2) |
| transport/timeout failure | `IsError` result with a clear message (not a Go error), consistent with `Registry.Dispatch` returning error *results* the model can recover from |

**Mount-time observability.** Connect success/failure per server is surfaced as a
**Narration** event (`event.Narration = "log"`) at mount, e.g. `mcp: connected
'github' (7 tools)` or `mcp: server 'github' unavailable: <err>; its tools will
be absent this session`. This mirrors the existing precedent where
`spawn_reviewers` emits a Narration when the reviewer sandbox is unavailable
(`internal/orchestrator/orchestrator.go:600`). A dedicated
`mcp_server_connected` event type is **deliberately deferred** — Narration is
enough for v1 and adds no schema surface.

## 8. Lifecycle

**Per-session, lazily connected, session-owned.**

- **When it connects.** On the first toolset build in a session that mounts a
  given server (the registry-assembly seams of §1), `internal/mcp` connects that
  server (spawn the child / open the HTTP session), calls `tools/list`, and
  produces the wrappers. A server no role in the session uses is never contacted.
- **Who owns it.** The session. MCP connections are torn down at session end
  alongside the existing background-job teardown — the same place
  `internal/session` calls `jobs.Registry.KillAll` (`internal/session/session.go`
  `killJobs` → `s.deps.Jobs.KillAll()`). A stdio child is spawned with
  `cwd = workspace root` (matching `Bash`'s `cmd.Dir = ws.Root`), so a
  filesystem-touching server sees the same working tree the agent does.
- **Why per-session, not per-daemon.** A per-daemon pool would share one child /
  HTTP session across concurrently-running sessions on different workspaces.
  Per-session wins on: **isolation** (server state and a stdio pipe's request
  context aren't interleaved across sessions), **correct cwd/env** (each session
  binds to its workspace), and a **simpler failure domain** (a wedged server
  affects one session, and dies with it). Per-daemon sharing (a connection pool
  keyed by server config, ref-counted across sessions) is a **deferred
  optimization** worth revisiting only if connect cost or child-process count
  becomes a problem.

**Failure modes:**

| Failure | Behaviour |
|---------|-----------|
| connect / `tools/list` fails at mount | **Session still starts.** A Narration warning is emitted (§7); that server's tools are simply absent. Never a fatal error — one broken server must not sink a session. |
| a `tools/call` exceeds `timeout_s` | the call returns an `IsError` tool result (the model can retry/route around it); the connection stays up. |
| server process crashes / connection drops | the failing call returns an `IsError` result; on the **next** call ycc makes **one lazy reconnect attempt**. Success ⇒ transparent recovery; failure ⇒ another `IsError` result. No crash-loop (bounded to one reconnect per call). |
| session end | all MCP connections closed / children killed via the session teardown path. |

## 9. Proposed follow-on implementation tasks

Filed from this doc (as `proposed` — the spike is accepted scope; implementation
awaits user acceptance, matching tasks 0155–0163 filed from the forge spike).

1. **`internal/mcp`: client manager + config parsing + `gollama.Tool` bridge.**
   *(task 0164)* — Add the `[mcp.servers.X]` config types + validation (§4:
   name charset, command⊕url, roles ⊆ mountable, env resolution via
   `internal/secrets`). Add `internal/mcp` on the official go-sdk: connect (stdio
   `CommandTransport` + HTTP `StreamableClientTransport`), `tools/list`, and a
   per-tool `gollama.Tool` wrapper with namespacing (§5), per-call timeout, and
   result mapping (§7). *Acceptance:* unit-tested against an in-process go-sdk
   MCP server (stdio) — connect, list, call (text + image + `isError`), timeout,
   and the `mcp__server__tool` naming all covered. No wiring into sessions yet.

2. **Session/orchestrator wiring: mount per role + lifecycle + events.**
   *(task 0165)* — Mount each configured server into the registries for its
   `roles` at the assembly seams (`BuildMode`, `CoordinatorTools` [excluded],
   `spawn_implementer`, `spawn_reviewers` [excluded]); lazy connect; session-scoped
   teardown beside `Jobs.KillAll`; Narration on connect/failure. *Acceptance:*
   chat mounts a configured server's tools (namespaced); the reviewer registry
   **never** contains an `mcp__…` tool (explicit test — §6); a broken server
   only warns and the session still starts; connections close at session end.

3. **Observability: `ycc doctor` MCP check + configured-server listing.**
   *(task 0166)* — A non-fatal `ycc doctor` check that each configured server
   is reachable (stdio spawns / HTTP responds), reporting tool counts, mirroring
   the forge doctor precedent (warn, don't fail). A way to list configured
   servers and their tools (e.g. `ycc mcp` / `ycc mcp list`). *Acceptance:*
   `doctor` reports each server ✓/⚠ without changing its exit code on absence;
   the listing shows servers, transports, roles, and discovered tool names.

4. **Spec update: record MCP support (§8/§13).** *(task 0167)* — Extend spec
   §8 (tools) and §13 (config) to document the `[mcp.servers.X]` config, the
   `mcp__server__tool` namespacing, the role matrix incl. the reviewer
   hard-exclusion, and the Narration/event representation — keeping the spec true
   (spec §1). *Depends on tasks 0164–0165 landing.*

**Rollout order:** 0164 → 0165 → (0166, 0167 in parallel). Task 0164 is the
self-contained, unit-testable core; 0165 makes it user-visible; 0166/0167 are
the observability and spec-truth follow-ups.
