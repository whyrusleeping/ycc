# ycc — a docs-driven coding harness

> Status: **design** (pre-implementation). This document is the living spec.
> It is meant to be edited continuously as the design firms up and the code lands.
> The harness we are building maintains specs exactly like this one — so this file
> is also the first dogfood of the workflow.

## 1. Vision & philosophy

`ycc` is a personal coding harness built around one idea:

> **The durable state of a project lives in documents (`spec.md` and a structured
> backlog). Everything the harness does is a structured, reviewable transformation
> of those documents, carried out by a coordinator agent that delegates real work
> to specialized subagents.**

Consequences of taking that seriously:

- **Specs are first-class and continuously maintained.** The agent is repeatedly
  prompted to keep the spec true. A drifted spec is a bug.
- **Work is planned before it is done, and reviewed before it is accepted.** No code
  is committed without a plan and at least one review pass.
- **Reviews are multi-perspective.** Different LLM backends (Claude, GPT, GLM, local)
  review the same change so we get genuinely independent takes, not one model
  grading its own homework.
- **The human chooses their level of involvement** per session, from "ask me about
  everything" to "don't stop, I'll review at the end."
- **The session is portable.** Work happens on a workspace machine, but the session
  can be observed and prodded remotely (e.g. from a phone), because session state is
  an append-only event log that any client can subscribe to.

Non-goals (for now): being a general-purpose IDE, supporting arbitrary non-Go projects
specially, or replacing git. We lean on git for history and diffs.

## 2. Core concepts

- **Workspace** — a git repository on the workspace machine that `ycc` operates on.
  Holds `spec.md`, the `backlog/`, and the code.
- **Session** — one continuous unit of interaction, identified by an id, backed by an
  **append-only event log**. A session has a *mode* and an *interaction level*.
- **Event log** — the source of truth for a session. Every model turn, tool call,
  tool result, user input, subagent spawn, and decision is an event. UI state is a
  *projection* (reduction) of the log. "Resume" = replay; "sync"/"remote" = ship the
  log + accept input over the wire.
- **Mode** — what the session is currently doing. Each mode is a *coordinator agent*
  configured with a specific system prompt and a specific subset of tools.
- **Coordinator** — the top-level agent for a session. It orchestrates; it does not
  edit code directly. Its "hands" are subagents.
- **Subagent** — a child agent spawned by the coordinator with its own model, system
  prompt, tool set, and (nested) event stream. Two kinds matter most: the
  **implementer** (writes code) and the **reviewer** (critiques a change).
- **Interaction level** — `interactive` | `judgement` | `autonomous`. Gates whether
  and when the coordinator may stop to ask the user.

## 3. System architecture

Daemon + clients, from day one.

```
 ┌────────────────────────── workspace machine ──────────────────────────┐
 │                                                                        │
 │   ┌──────────────────────  ycc daemon (service)  ───────────────────┐  │
 │   │                                                                  │  │
 │   │   session mgr ── event log store ── reducer/projection          │  │
 │   │        │                                                         │  │
 │   │   coordinator agent (mode-specific)                             │  │
 │   │        │  spawns                                                 │  │
 │   │        ├── implementer subagent ── worker tools ──► workspace FS │  │
 │   │        └── reviewer subagents (Claude / GPT / GLM / local)       │  │
 │   │                                                                  │  │
 │   │   backend registry (gollama clients) ── docs store (spec/backlog)│  │
 │   └───────────────── Connect-RPC over HTTP/2 (TLS + token) ──────────┘  │
 │                                  ▲                                      │
 └──────────────────────────────────┼──────────────────────────────────────┘
                                    │
              ┌─────────────────────┼─────────────────────┐
              │                     │                     │
        ycc TUI (local)      ycc CLI (scripted)     phone app (future)
        subscribe + prod     subscribe + prod        subscribe + prod
```

**Why daemon-first.** Remote prodding, phone access, and "sessions that keep running
while I close my laptop" all require the agent loop to live in a long-running process
that owns the filesystem and is reachable over a network boundary. Clients are thin:
they render an event stream and send commands. The TUI is just the first client.

**Why Connect-RPC** (connectrpc.com/connect):
- Native Go, generates from `.proto`, and speaks gRPC, gRPC-Web, **and** plain
  HTTP/JSON from the *same* server — so a future phone app (or `curl`) can talk to it
  without a gRPC stack.
- Supports server-streaming, which is exactly what an event subscription needs.
- Commands are simple unary RPCs.

## 4. Process & data-flow model

1. Client calls `StartSession(workspace, mode, interactionLevel)` → daemon creates a
   session + event log, instantiates the coordinator for that mode.
2. Client opens `Subscribe(sessionID)` (server-stream) and begins rendering events.
3. Coordinator runs its agent loop. Each turn emits events. Tool calls emit events and
   mutate the workspace / docs. Subagent spawns create nested event streams.
4. When the coordinator (per interaction level) needs the user, it calls the `ask_user`
   tool → emits a `QuestionAsked` event and *suspends*. Client renders it; user answers
   via `AnswerQuestion(sessionID, ...)` → `QuestionAnswered` event → loop resumes.
5. On completion the coordinator commits, updates the backlog/spec, emits `SessionIdle`,
   and returns control. The session persists and can be resumed or re-entered.

The daemon never blocks on a client. A session with no subscribers keeps running (e.g.
in `autonomous` level); a client reconnecting just replays the log from an offset.

## 5. Session & event log

### 5.1 Storage

- Source of truth: **append-only JSONL** per session at
  `<workspace>/.ycc/sessions/<session-id>/events.jsonl`.
- Optional periodic **snapshot** (`state.json`) of the reduced projection for fast
  resume on large logs.
- Sync/remote = copy or stream the JSONL (it is the whole state). A future remote store
  is "an `events.jsonl` somewhere else, plus an input channel."

### 5.2 Event shape

```jsonc
{
  "seq": 128,                       // monotonic per session
  "ts": "2026-06-25T21:30:00Z",
  "session": "s_8f3a…",
  "actor": "coordinator",           // coordinator | implementer | reviewer:gpt | user | system
  "type": "tool_call",              // see types below
  "data": { /* type-specific */ }
}
```

Event `type`s (initial set):

| type | meaning |
|------|---------|
| `session_started` | mode, interaction level, workspace |
| `mode_changed` | transitioned modes within a session |
| `model_turn` | a model produced a message (text + any tool calls) |
| `tool_call` / `tool_result` | a tool was invoked / returned |
| `subagent_spawned` / `subagent_finished` | with role + model + child session ref |
| `question_asked` / `question_answered` | the `ask_user` flow |
| `plan_proposed` / `plan_accepted` | coordinator plan checkpoints |
| `review_submitted` | one reviewer's findings |
| `decision_made` | accept / revise, with rationale |
| `doc_updated` | spec or task file changed (with diff) |
| `commit_made` | git sha + message |
| `session_idle` / `session_error` | terminal-ish states |
| `log` | free-text narration for the UI |

Subagents get their own session-scoped event substreams (`subagent_spawned` carries a
child stream id); the client can drill into an implementer/reviewer's transcript.

## 6. Document model

### 6.1 spec.md

Structured-by-section markdown the coordinator edits section-by-section. Canonical
sections (others allowed): Vision, Goals, Architecture, Components, Constraints,
Open Questions. The `update_spec` tool edits a named section (so two concurrent edits
don't clobber the whole file, and diffs stay legible). The spec is committed with code.

### 6.2 Backlog — structured items, markdown-rendered

Canonical store: **one markdown file per task** with YAML frontmatter, under
`backlog/`. A generated `backlog.md` index gives a human-readable overview. Per-file
storage means git diffs are per-task, the agent edits one task without rewriting the
whole backlog, and a UI can manipulate items reliably.

Task file: `backlog/0007-add-token-auth.md`

```markdown
---
id: "0007"
title: Add token auth to the daemon
status: todo            # todo | in_progress | in_review | done | blocked
priority: 2             # 1 highest
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0003"]
spec_refs: ["Architecture", "RPC protocol"]
---

## Description
Why this exists and what "done" means in prose.

## Acceptance criteria
- [ ] daemon rejects unauthenticated RPCs
- [ ] token configurable via env + config file

## Work log
<!-- appended by the harness as work happens -->
- 2026-06-25 plan: …
- 2026-06-25 implementer report: …
- 2026-06-25 review (gpt-5.5): …
- 2026-06-25 decision: accept; commit abc1234
```

`docs` package responsibilities: parse/write task files, validate frontmatter, render
`backlog.md`, append to a task's work log, and provide `list/get/create/update` used by
the coordinator tools.

## 7. Agent engine

### 7.1 gollama integration (and the one addition we need)

gollama already gives us: per-backend single-shot completions
(`ChatCompletion`, `ChatCompletionAnthropic`, `ChatCompletionBedrock`, `Chat`), the
`Tool` abstraction (`Name`/`Description`/`Params`/`Call`), and `HandleToolCall`.

What it lacks and we add (in gollama, since edits are allowed):

1. **Unified turn dispatch** — a single `func (c *Client) Turn(ctx, opts) (*ResponseMessageGenerate, error)`
   (name TBD) that routes to the right backend method based on the client's mode, so
   the agent loop doesn't branch per provider. Normalizes tool-call + usage shapes.
2. Optionally, a `Backend` enum on the client so the registry can introspect.

The **agent loop itself lives in `ycc`**, not gollama — gollama stays a transport.

### 7.2 The loop

```
Loop{ client, model, system, tools, history, events, policy }

run():
  loop:
    resp = client.Turn(model, system, history, tools)
    emit model_turn
    if resp has tool calls:
       for each call:
          emit tool_call
          result = registry.dispatch(call)   // may be a control tool
          emit tool_result
          append result to history
       continue
    else:
       return final message            // model yielded with no tool call
```

Some tools are **control tools** that don't just return data — they change
orchestration state (`ask_user` suspends; `finish` ends the loop; `spawn_*` runs a
child loop). The registry marks these so the loop can react.

### 7.3 Subagents

A subagent is just another `Loop` with its own client/model/system/tools and an event
substream. The coordinator spawns one via a control tool; the spawn is synchronous from
the coordinator's perspective (it awaits a structured report) but reviewers fan out
**concurrently** (goroutines + a barrier). Reviewer contexts are *retained* so a revise
round can reuse them (`re-review` sends the new diff into the existing reviewer history).

## 8. Tools

**Worker tools** (implementer; read/write the workspace):
`read_file`, `write_file`, `edit_file`, `list_dir`, `grep`, `glob`, `bash`,
`finish_implementation(report)`.

**Coordinator tools** (orchestrate; never edit code directly):
`list_backlog`, `get_task`, `create_task`, `update_task`, `update_spec`,
`spawn_implementer(task, plan)`, `spawn_reviewer(model, task, diff)`,
`send_to_implementer(instructions)` (revise; reuses implementer ctx),
`re_review()` (reuses reviewer ctx), `commit(message)`, `ask_user(question, options?)`,
`propose_plan(plan)`, `finish()`.

Tools are gollama `Tool` values (`Params` is JSON schema, `Call` does the work + emits
events). Worker and orchestration tools are the same kind of object.

## 9. Modes (the home menu)

Each mode = a coordinator system prompt + a tool subset + a state machine.

- **`spec`** — collaboratively author/maintain `spec.md`. Tools: read-only workspace +
  `update_spec`, `ask_user`. Output: an updated, committed spec.
- **`backlog`** — turn the spec into tasks. Tools: read spec + `create_task`,
  `update_task`, `ask_user`. Output: new/updated task files + regenerated index.
- **`work`** — the core loop (see §10). Pick/accept a task, plan, implement, review
  (multi-model), iterate, commit, update backlog.
- **`feature`** / **`bug`** — understand codebase + spec, propose a plan, on acceptance
  update spec (if needed) + backlog, then optionally flow into `work` *in the same
  session* or hand back to the home menu.

Transitions are explicit (`StartSession` picks the initial mode; `feature`/`bug` can
`mode_changed → work`). The home menu is a client concern: it lists modes and calls
`StartSession`.

## 10. The `work` orchestration (in detail)

```
coordinator (FRESH context, mode=work)
  1. read backlog  → list_backlog / get_task
  2. pick a task   (or accept the user-suggested one)
  3. plan          → propose_plan ; (per interaction level) maybe ask_user to confirm
  4. implement     → spawn_implementer(task, plan)
                     implementer runs worker tools, returns a structured report + diff
  5. review        → spawn_reviewer × N  (different models, concurrent)
                     each returns findings {severity, summary, items[]}
  6. judge:
        if acceptable → commit + update_task(status=done) + work-log + finish
        else          → send_to_implementer(consolidated instructions)
                        → re_review()   (reuse reviewer contexts)
                        → back to 6
  7. on finish: regenerate backlog.md, emit session_idle, return to user
```

Fresh context in step 0 is important: each `work` session starts clean so the
coordinator reasons from the durable docs, not from stale conversation.

## 11. Interaction levels

One policy value, enforced at the `ask_user` gate and baked into the coordinator prompt:

- **`interactive`** — ask freely; confirm the plan, surface meaningful choices.
- **`judgement`** — proceed on best judgement; only `ask_user` when genuinely blocked
  or a decision is hard to reverse.
- **`autonomous`** — never `ask_user`; make every call, and accumulate questions /
  assumptions / decisions into the final report for end-of-session review.

The gate lives in the loop: in `autonomous`, an `ask_user` call is converted into a
logged assumption + auto-answer ("proceed") rather than a suspend.

## 12. RPC protocol (Connect)

Service sketch (`proto/ycc/v1/ycc.proto`):

```proto
service Ycc {
  rpc ListModes(ListModesRequest) returns (ListModesResponse);          // home menu
  rpc StartSession(StartSessionRequest) returns (StartSessionResponse);
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
  rpc Subscribe(SubscribeRequest) returns (stream Event);               // server-stream
  rpc SendInput(SendInputRequest) returns (SendInputResponse);          // prod the agent
  rpc AnswerQuestion(AnswerQuestionRequest) returns (AnswerQuestionResponse);
  rpc ChooseMode(ChooseModeRequest) returns (ChooseModeResponse);       // mode switch
  rpc Interrupt(InterruptRequest) returns (InterruptResponse);          // pause/stop
}
```

`Subscribe` takes a `from_seq` so a reconnecting client replays from an offset. Auth: a
bearer token (config/env), TLS for non-loopback. The TUI talks to the loopback daemon;
remote clients dial in over TLS.

## 13. Backends & model registry

A config file maps logical names → gollama clients:

```toml
[models.claude]   backend="anthropic" base_url="…" model="claude-opus-4-8" key_env="ANTHROPIC_API_KEY"
[models.gpt]      backend="openai"    base_url="…" model="gpt-5.5"          key_env="OPENAI_API_KEY"
[models.glm]      backend="openai"    base_url="https://…/glm" model="glm-4.6" key_env="GLM_API_KEY"
[models.local]    backend="ollama"    base_url="http://localhost:11434" model="…"

[roles]
coordinator = "claude"
implementer = "claude"
reviewers   = ["claude", "gpt", "glm"]   # multi-model review
```

The registry hands the engine a configured gollama `Client` + model string for any
logical name. Reviewer fan-out iterates `roles.reviewers`.

## 14. Persistence & remote sync

- Per-session JSONL event log is the unit of persistence and of sync.
- `.ycc/` holds sessions, snapshots, and config; the `spec.md` + `backlog/` live in the
  repo proper (committed with code).
- Remote sync (later milestone): the daemon can push/pull session logs to a remote
  endpoint so another machine's daemon — or a phone app — can observe and prod. Because
  the log is append-only and seq-numbered, sync is "ship events after seq N" + conflict
  is impossible for a single-writer session (the workspace daemon is the only writer of
  workspace mutations; remote clients only append *input* events, which the workspace
  daemon serializes).

## 15. Package layout

**Single binary.** There is one `ycc` binary that is client, TUI, and daemon.
`ycc` (no subcommand) launches the TUI and, for local use, auto-starts a detached
background daemon (which persists after `ycc` exits, so sessions keep running and can
be prodded later); `ycc daemon` runs the service explicitly (for the workspace machine
/ remote); `ycc -addr <url>` attaches to a remote daemon. Sessions bind to the current
directory by default, so one daemon serves many workspaces.

```
ycc/
  cmd/
    ycc/             # the single binary: client + TUI + `ycc daemon`
  proto/ycc/v1/      # .proto + generated (connect)
  internal/
    engine/          # agent loop, control-tool handling
    orchestrator/    # mode coordinators + the work() flow + subagent spawning
    tools/           # worker + coordinator tool implementations
    config/          # model/role config + registry (gollama client wiring)
    docs/            # spec + backlog (structured) read/write/render
    event/           # event types, JSONL store, reducer/projection
    session/         # session lifecycle + state
    server/          # connect handlers, auth
    daemon/          # serve the service + local auto-start + client dialing
    tui/             # Bubble Tea home menu + session view
    git/             # diff/commit helpers
```

## 16. Build plan / milestones

- **M0 — Engine spike.** gollama `Turn` dispatch + the agent loop + worker tools
  (read/write/edit/bash/grep/glob). One agent does a real task end-to-end. Events to
  stdout. *Proves the atom.*
- **M1 — Daemon + event log + one client.** `yccd` with session mgr, JSONL event store,
  Connect `StartSession`/`Subscribe`/`SendInput`, and a minimal `ycc` client that
  subscribes and prods. *Proves the client/server seam.*
- **M2 — `work` happy path.** Coordinator + `spawn_implementer` + a single reviewer +
  commit + structured backlog read/write. N=1, no revise loop.
- **M3 — Multi-model review + revise loop + interaction levels.** Reviewer fan-out
  across Claude/GPT/GLM/local, `send_to_implementer`/`re_review`, the three autonomy
  gates.
- **M4 — Home menu + `spec`/`backlog`/`feature`/`bug` modes + TUI.**
- **M5 — Remote sync** (push/pull session logs; phone-facing HTTP/JSON surface).

## 17. Open questions

- **Diff capture for reviewers:** *Decided.* Reviewers get the full read/inspect tool
  set (read_file, list_dir, grep, glob, bash) and explore as they see fit — run
  `git diff`, read touched files, etc. They are **prompted** not to modify the
  workspace. Hard prevention is impractical while they have bash; **sandboxing reviewer
  bash is deferred future work** (see task 0008).
- **Implementer isolation:** *Decided.* Implementers work **directly on the codebase**
  (single task at a time). Git worktrees revisited only if/when we want parallel tasks.
- **Commit granularity:** one commit per accepted task vs. checkpoints during work.
- **TUI framework:** Bubble Tea is the obvious Go choice for the client.
- **Session GC / retention** of `.ycc/sessions`.
- **Secrets:** keep API keys in env only, or a daemon-side keyring?
```
