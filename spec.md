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
- **Project** — a named workspace a persistent daemon manages. A persistent daemon
  holds a registry of projects (name → path) so a client can list them and pick one to
  work in; a one-shot daemon has exactly one implicit project — the current directory.
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

### 3.1 Daemon lifecycle & projects

Persistence is **opt-in**. The daemon runs in one of two lifecycles:

- **One-shot (the default `ycc`).** When no daemon is requested and none is already
  running locally, `ycc` starts the daemon **in-process** on an ephemeral loopback
  address and ties it to the client's lifetime — closing `ycc` tears it down. The
  current directory is the single project and the client skips the project picker.
  Closing the client therefore ends any in-flight agent work; that is the trade.
- **Persistent (`ycc daemon`).** An explicitly-started, long-lived, **multi-project**
  daemon at a well-known local address. It survives client exits, so autonomous
  sessions keep running. `ycc --background` is a convenience that spawns one (detached)
  and attaches the TUI to it.

Resolution for plain `ycc` (no `-addr`, no `daemon` subcommand):
1. If a persistent local daemon answers at the well-known address, **attach** to it and
   show the project picker.
2. Otherwise run **one-shot** in-process on the current directory.

`ycc -addr <url>` always attaches to the given (persistent/remote) daemon and shows the
picker.

A persistent daemon manages **multiple projects**. The registry (name → path) is durable
state in the daemon's state dir (e.g. `~/.local/state/ycc/projects.json`). Projects are
registered explicitly (`ycc project add <path>` / `AddProject`) **and** auto-registered
when a session starts in a not-yet-known workspace. Clients `ListProjects`, pick one,
then drive the existing mode/session flow scoped to that project. Sessions and their
event logs still live under each project's own `<workspace>/.ycc/` (§5.1, §14).

This supersedes the earlier "always auto-start a detached daemon that persists after
exit" decision: that default orphaned daemons serving a stale binary and capturing a
stale environment. Persistence now happens only when explicitly requested.

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

### 7.4 Reasoning (extended/adaptive thinking + effort)

Every agent's request carries per-model reasoning settings (Anthropic extended/adaptive
thinking). The engine `Loop` holds `Thinking` / `Effort` / `ThinkingDisplay` fields beside
`MaxTok` and sets them on the `gollama.RequestOptions` for every turn; these reach the
coordinator loop, the implementer, and each reviewer, resolved from the role's model in the
registry (`ThinkingFor`, §13). `Thinking=""` disables reasoning; `"adaptive"` enables it;
`Effort` (`low`..`max`) tunes depth/spend; `ThinkingDisplay="summarized"` opts into reasoning
summaries. The provider's reasoning blocks round-trip automatically because the engine
appends the returned assistant `Message` (which carries `ThinkingBlocks`) to history. When a
turn returns a reasoning summary, the loop emits a dedicated `thinking` event (before the
`model_turn`) for the UI (§18). Non-Anthropic backends ignore these fields.

## 8. Tools

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

`ask_user(question, options?)` is the structured-question control tool. The optional
`options` parameter is a list of suggested answers; when present, the client renders a
selectable picker (Claude-Code style) with an "other…" escape to free text. When
absent, the user answers with free (multiline) text. See §18.3 for the UI side. The
`Asker.Ask(ctx, question, options)` interface already carries `options` end-to-end;
exposing it on the tool schema + answering by option is the remaining wiring.

Tools are gollama `Tool` values (`Params` is JSON schema, `Call` does the work + emits
events). Worker and orchestration tools are the same kind of object.

## 9. Modes (the home menu)

Each mode = a coordinator system prompt + a tool subset + a state machine. There are three:

- **`pm` (project manager)** — the single planning / intake / docs mode. Talk to it about
  the project: iterate `spec.md`, groom the backlog (`create_task` / `update_task`),
  investigate a feature or bug, and record plans (`propose_plan`). It does **no
  implementation** — it edits the *docs* (spec is a plain file; backlog tasks) but not the
  code. That boundary is *soft* (prompt-enforced): `pm` holds `Read`/`Write`/`Edit`/`Bash`
  so it can maintain `spec.md`, and is told not to touch code; a hard boundary (path
  scoping / isolation) is future work. Tools: `Read`/`Write`/`Edit`/`Bash`,
  `list_backlog`/`get_task`/`create_task`/`update_task`, `propose_plan`, `switch_to_work`,
  `ask_user`, `finish`. This **replaces** the former `spec`, `backlog`, `feature`, and `bug` modes —
  they were one capability set under four prompt framings. The home menu keeps those
  framings as **opening-prompt presets** that drop into `pm` ("New feature" → explore then
  propose; "Bug report" → reproduce then localize; "Author spec"; "Build backlog"), so the
  affordances survive without four modes.
- **`chat`** — open-ended assistant that *can* edit code directly, with no fixed workflow.
  Kept as the freeform "just do it" counterpart to `pm`'s "just plan it."
- **`work`** — the orchestrated implementation pipeline (§10): pick/accept a task, plan,
  spawn implementer, multi-model review, revise, commit, update backlog.

**Hand-off `pm` → `work`.** `pm` may offer `switch_to_work`, but it is *deliberate*, never
automatic: (1) it requires explicit interactive **user approval** before transitioning, and
(2) it carries the planning **context plus the specific target task** into the `work`
session, so the coordinator implements *that* task rather than re-picking "the next ready
task." (The old `feature`/`bug` `switch_to_work` spun up a fresh coordinator that was free
to wander to an unrelated task — that is the behaviour this fixes.) Authoring plans in `pm`
pays off only if those plans are durably retained and tracked (see task 0020).

Transitions are explicit: `StartSession` picks the initial mode; `pm` can `mode_changed →
work` via the approved, context-carrying `switch_to_work`. The home menu is a client
concern: it lists the modes (plus the `pm` presets) and calls `StartSession`.

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

## 11. Interaction levels

One policy value, enforced at the `ask_user` gate and baked into the coordinator prompt:

- **`interactive`** — ask freely; confirm the plan, surface meaningful choices.
- **`judgement`** — proceed on best judgement; only `ask_user` when genuinely blocked
  or a decision is hard to reverse.
- **`autonomous`** — never `ask_user`; make every call, and accumulate questions /
  assumptions / decisions into the final report for end-of-session review.

The gate lives in the loop: in `autonomous`, an `ask_user` call is converted into a
logged assumption + auto-answer ("proceed") rather than a suspend.

**Exception — confirmation gates.** A high-impact, hard-to-reverse action exposes a
`Confirm` gate (yes/no) rather than `ask_user`. Starting the `pm` → `work` implementation
pipeline is one: its `switch_to_work` confirmation seeks a *real human answer even in
`autonomous`* (it does not auto-answer), and if no human is available the action is
**declined** and the session stays put rather than silently launching work.

The level is chosen at `StartSession` and can be **changed mid-session** from the
client's settings overlay (§18.2) via `SetInteractionLevel(sessionID, level)`. The
daemon updates the live policy used by the next gate check and emits an event so the
change is recorded in the log and visible to other subscribers. Raising autonomy
(e.g. interactive → autonomous) takes effect immediately; lowering it means the *next*
decision point will pause for the user.

## 12. RPC protocol (Connect)

Service sketch (`proto/ycc/v1/ycc.proto`):

```proto
service SessionService {
  rpc ListModes(ListModesRequest) returns (ListModesResponse);          // home menu
  rpc StartSession(StartSessionRequest) returns (StartSessionResponse);
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
  rpc Subscribe(SubscribeRequest) returns (stream Event);               // server-stream
  rpc SendInput(SendInputRequest) returns (SendInputResponse);          // prod the agent
  rpc AnswerQuestion(AnswerQuestionRequest) returns (AnswerQuestionResponse);
  rpc Interrupt(InterruptRequest) returns (InterruptResponse);          // pause/stop (task 0009)

  // Projects — persistent multi-project daemon (§3.1).
  rpc ListProjects(ListProjectsRequest) returns (ListProjectsResponse);
  rpc AddProject(AddProjectRequest) returns (AddProjectResponse);
  rpc RemoveProject(RemoveProjectRequest) returns (RemoveProjectResponse);

  // Settings overlay (§18.2) — change session config mid-flight.
  rpc ListModels(ListModelsRequest) returns (ListModelsResponse);       // available logical models
  rpc SetInteractionLevel(SetInteractionLevelRequest) returns (SetInteractionLevelResponse);
  rpc SetRoleConfig(SetRoleConfigRequest) returns (SetRoleConfigResponse);
  rpc SetThinking(SetThinkingRequest) returns (SetThinkingResponse);    // session-wide reasoning level
}
```

Notable message shapes for the settings + structured-question work:

- `ProjectInfo { string name; string path }`; `ListProjectsResponse { repeated ProjectInfo
  projects }`; `AddProjectRequest { string path; string name }` →
  `AddProjectResponse { ProjectInfo project }`; `RemoveProjectRequest { string name }` (§3.1).
  `StartSessionRequest` gains an optional `project` (name) that resolves to a workspace — an
  unknown workspace is auto-registered. `ListSessionsRequest` may carry a `project` filter.
- `AnswerQuestionRequest { session_id; oneof answer { string text; int32 option_index } }`
  — answer a structured question by chosen option or free text. `question_asked` events
  gain a `repeated string options` field so the client can render the picker.
- `SetInteractionLevelRequest { session_id; string level }` (§11).
- `SetRoleConfigRequest { session_id; string coordinator; string implementer;
  repeated string reviewers }` — per-role model assignment by logical model name (§13).
  Empty fields leave that role unchanged.
- `SetThinkingRequest { session_id; string level }` — a session-wide reasoning level
  (`off | low | medium | high | xhigh | max`) applied to every agent (coordinator +
  spawned subagents), overriding each model's per-config thinking until changed (§13).
  `off` disables reasoning; any effort level maps to adaptive thinking at that effort
  with summarized display.
- `ListModelsResponse { repeated ModelInfo models }` where `ModelInfo` carries the
  logical name + backend + model id, so the client can populate the role pickers.

`Subscribe` takes a `from_seq` so a reconnecting client replays from an offset. Auth: a
bearer token (config/env), TLS for non-loopback. The TUI talks to the loopback daemon;
remote clients dial in over TLS.

(Mode switching is currently **agent-driven** via the `switch_to_work` control tool +
`StartSession` from the home menu rather than a client `ChooseMode` RPC; revisit if
client-driven mode switching is wanted.)

## 13. Backends & model registry

A config file maps logical names → gollama clients:

```toml
[models.claude]
backend = "anthropic"  base_url = "…"  model = "claude-opus-4-8"  key_env = "ANTHROPIC_API_KEY"
thinking = "adaptive"  effort = "high"  thinking_display = "summarized"   # reasoning (see §7.4)
[models.gpt]      backend="openai"    base_url="…" model="gpt-5.5"          key_env="OPENAI_API_KEY"
[models.glm]      backend="openai"    base_url="https://…/glm" model="glm-4.6" key_env="GLM_API_KEY"
[models.local]    backend="ollama"    base_url="http://localhost:11434" model="…"

[roles]
coordinator = "claude"
implementer = "claude"
reviewers   = ["claude", "gpt", "glm"]   # multi-model review

max_tokens  = 8192   # per-turn output token cap (0 => backend default)
max_turns   = 200    # per-Run tool-call turn cap; runaway/cost backstop (0 => engine default, 200)
```

The registry hands the engine a configured gollama `Client` + model string for any
logical name. Reviewer fan-out iterates `roles.reviewers`.

**Per-model reasoning** (`thinking` / `effort` / `thinking_display`) is configured on each
`[models.X]` block and resolved by the registry (`ThinkingFor(name)`), paralleling
`max_tokens` / `MaxTokens()`. These map to Anthropic extended/adaptive thinking + effort
(see §7.4); they are honored by the anthropic backend and ignored harmlessly by others.
**Defaults are reasoning-on** (`thinking="adaptive"`, `effort="high"`,
`thinking_display="summarized"`) — this is an agentic coding harness, so reasoning is
desired by default, including on the no-config single-backend path. Set `thinking="off"`
(or `""`) on a model to disable reasoning. The resolved settings are applied per **role/model**
to every agent: coordinator, implementer, and each reviewer.

`max_turns` bounds how many tool-call turns a single engine `Run` may take. It is a
**runaway backstop**, not a normal stopping condition: the high default (200) keeps the
implementer's read → edit → build → test → fix cycles from being cut off mid-task while
still stopping a degenerate infinite tool-call loop. The cap is **per `Run`**, so each
`send_to_implementer` revise round gets a fresh budget rather than accumulating across
rounds. Interaction with context-window management (§ task 0010): a higher turn cap lets a
run accumulate more conversation history, so until context budgeting lands a very high
`max_turns` can trade a turn-limit abort for a context-window-limit abort on long runs.

## 14. Persistence & remote sync

- Per-session JSONL event log is the unit of persistence and of sync.
- `.ycc/` holds sessions, snapshots, and config; the `spec.md` + `backlog/` live in the
  repo proper (committed with code).
- A persistent daemon's **project registry** (name → path) is durable state in the
  daemon's state dir (`~/.local/state/ycc/projects.json`), separate from each project's
  per-workspace `.ycc/`. One-shot daemons keep no registry (one implicit project = cwd).
- Remote sync (later milestone): the daemon can push/pull session logs to a remote
  endpoint so another machine's daemon — or a phone app — can observe and prod. Because
  the log is append-only and seq-numbered, sync is "ship events after seq N" + conflict
  is impossible for a single-writer session (the workspace daemon is the only writer of
  workspace mutations; remote clients only append *input* events, which the workspace
  daemon serializes).

## 15. Package layout

**Single binary.** There is one `ycc` binary that is client, TUI, and daemon.
`ycc` (no subcommand) attaches to a persistent local daemon if one is already running,
otherwise launches the TUI over an **in-process, ephemeral** daemon bound to the current
directory and torn down when `ycc` exits (§3.1). `ycc daemon` runs an explicit,
persistent, **multi-project** service (for the workspace machine / remote); `ycc -addr
<url>` attaches to a remote one; `ycc --background` spawns a detached persistent daemon
and attaches. Persistence is opt-in — the default no longer leaves a daemon running
after exit.

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
    daemon/          # serve + lifecycle (one-shot in-process vs persistent) + project registry + client dialing
    tui/             # Bubble Tea home menu + session view
    git/             # diff/commit helpers
```

## 16. Build plan / milestones

## 16. Build plan / milestones

- **M0 — Engine spike.** gollama `Turn` dispatch + the agent loop + worker tools
  (read/write/edit/bash/grep/glob). One agent does a real task end-to-end. Events to
  stdout. *Proves the atom.* — **done**
- **M1 — Daemon + event log + one client.** `yccd` with session mgr, JSONL event store,
  Connect `StartSession`/`Subscribe`/`SendInput`, and a minimal `ycc` client that
  subscribes and prods. *Proves the client/server seam.* — **done**
- **M2 — `work` happy path.** Coordinator + `spawn_implementer` + a single reviewer +
  commit + structured backlog read/write. N=1, no revise loop. — **done**
- **M3 — Multi-model review + revise loop + interaction levels.** Reviewer fan-out
  across Claude/GPT/GLM/local, `send_to_implementer`/`re_review`, the three autonomy
  gates. — **done**
- **M4 — Home menu + `spec`/`backlog`/`feature`/`bug` modes + TUI.** — **done**
- **M5 — Remote sync** (push/pull session logs; phone-facing HTTP/JSON surface).
- **M6 — Interactive UX polish.** Multiline `textarea` input (Enter sends, Shift+Enter
  newline), the **settings overlay** (esc; mid-session interaction level + per-role
  model configuration + UI prefs + intentional "back to home menu"), and
  **structured `ask_user` questions** (option pickers). New RPCs: `ListModels`,
  `SetInteractionLevel`, `SetRoleConfig`; `AnswerQuestion`/`question_asked` extended for
  options. See §18.

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

## 18. Client UI (TUI)

## 18. Client UI (TUI)

The Bubble Tea client (`internal/tui`, spec §15) is the primary local surface. It has
two top-level states today — **home menu** and **session view** — plus a modal
**settings overlay**. This section captures the interaction model.

### 18.1 Session input — multiline

The session input is a **`textarea`** (not single-line `textinput`). It grows
vertically as the user types and wraps long lines.

- **Enter** sends the buffer (prod / answer) and clears it.
- **Shift+Enter** inserts a newline.
- The textarea height is bounded (a few rows); beyond that it scrolls internally so it
  never crowds out the event stream.

The home-menu prompt can stay single-line (one-shot kickoff prompt) but the same
multiline rules are fine there too.

### 18.2 Settings overlay (esc — "video-game style")

**Esc** opens a modal settings overlay over whatever state the client is in. The
overlay is a small navigable menu; it does **not** immediately leave the session.
Leaving a session is now an explicit, intentional menu choice ("Back to home menu") so
the user can't fat-finger their way out of a running session.

Overlay contents:

- **Interaction level** — `interactive | judgement | autonomous`, changeable
  **mid-session** (see §11). Selecting a value issues `SetInteractionLevel(sessionID,
  level)`; the daemon updates the live policy and emits an event so the change is in the
  log (and reflected in any other subscribed client).
- **Thinking level** — `off | low | medium | high | xhigh | max`, changeable
  **mid-session**. A session-wide reasoning override that applies to **every** agent
  (coordinator + spawned implementer/reviewers), taking precedence over each model's
  per-config thinking (§13) until changed. Selecting a value issues
  `SetThinking(sessionID, level)`; the daemon updates the live coordinator loop and
  rebuilds the implementer/reviewer specs so the next spawn uses it, then emits a
  `thinking_level_changed` event. `off` disables reasoning; any effort level maps to
  adaptive thinking at that effort with summarized display.
- **Model / role configuration** — the headline feature. Per-role model selection:
  - **coordinator** — pick one model
  - **implementer** — pick one model
  - **reviewers** — pick *one or more* models (multi-select; reviewer fan-out, §13)
  - Choices are drawn from the configured logical models (§13). Changes apply to the
    current session (and optionally persist to config). Issued via
    `SetRoleConfig(sessionID, roles)`; the daemon rebuilds the relevant gollama clients
    so the next coordinator turn / next spawned subagent uses the new assignment.
- **UI preferences** — theme/style, follow/auto-scroll toggle, and similar client-only
  prefs. These never touch the daemon; they live in client state (and a small local
  client config).
- **Back to home menu** — leaves the session view (replaces the old "esc = back to
  menu" reflex).
- **Quit** — exit the client.

Esc closes the overlay (back to wherever you were) when no destructive choice is made.

### 18.3 Structured interactive questions (Claude-Code style)

When the coordinator calls `ask_user`, it may supply **options** in addition to the
free-text question (the `Asker.Ask(ctx, question, options)` interface and `ask_user`'s
`options?` param already anticipate this — see §8). The client renders these as a
**selectable list** (arrow-key/number navigable) rather than only a free-text box:

- If `options` are present: a highlighted picker with the listed choices, plus an
  "other…" affordance that drops into the multiline textarea for a free-text answer.
- If `options` are absent: the plain multiline textarea.

Wire path: `question_asked` events carry the options; `AnswerQuestion` carries either a
chosen option (index/value) or free text. This gives the agent the same crisp,
low-friction Q&A loop a good interactive coding assistant has, instead of forcing every
clarification into prose.

### 18.4 Reasoning (thinking) in the event stream

When a model turn returns a reasoning summary, the engine emits a `thinking` event (§7.4)
carrying the summary text. The TUI renders it like any other stream event — **collapsed by
default** with a one-line `(reasoning) …` preview, click/Enter to expand — so the agent's
"inner voice" is available without cluttering the stream. The expanded body is shown
**dimmed + italic** to read distinctly from the model's actual response. Empty summaries
produce no event. (The provider reasoning blocks themselves round-trip in conversation
history automatically and are not re-displayed.)

## 19. Onboarding flows

ycc has two onboarding moments. They are independent and triggered by different signals:
the **first-run** flow runs once per machine/user and configures *which models ycc talks
to*; the **per-project** flow runs the first time work begins in a given workspace and
configures *what ycc should know about that project* (its `spec.md` + backlog).

### 19.1 First-run setup (global — model providers & roles)

**Trigger.** The very first time a user runs `ycc` with **no usable model configuration**:
no `ycc.toml` is discoverable (`DiscoverConfig` returns "" — §`internal/daemon`) *and* no
fallback env key is set, so the daemon would otherwise fall back to a keyless Anthropic
config that 401s on the first turn (§13). Rather than failing the first session, the client
runs a guided setup.

**Where it runs.** This is a **client/TUI wizard, not an agent flow** — it must work before
any working model exists, and it is a structured form, not a conversation. It writes a
`ycc.toml` to the user config dir (`~/.config/ycc/ycc.toml`, via `os.UserConfigDir()`), the
second `DiscoverConfig` candidate, so every later run finds it.

**What it collects.**
1. **One or more model providers.** For each: a logical name (e.g. `claude`, `gpt`,
   `local`), a backend (`anthropic` | `openai` | `ollama` — the backends `config.Build`
   already supports), a base URL (sensible default per backend), a model id, and an API key.
   Keys are stored as a `key_env` reference (the var name) following the spec's
   "keys in env only" lean (§17 open question), *not* inlined into the TOML; the wizard can
   also offer to note which env vars must be exported. At least one provider is required.
2. **Role assignments** (§13): pick the `coordinator`, `implementer`, and one-or-more
   `reviewers` from the configured logical models. With a single provider, all three default
   to it (mirroring `DefaultAnthropic`) and the user can accept without choosing.

**Output.** A valid `config.Config` written as TOML. This requires a new `config.Save(path,
*Config)` (the package currently only `Load`s). After writing, the wizard hands the path to
the daemon resolution path (§3.1) exactly as a discovered config would be, so the first real
session uses it. Re-running setup later is available from the settings overlay (§18.2,
"Model / role configuration") — that overlay already edits role assignments live; first-run
setup is the bootstrap that creates the file those edits then mutate.

**Skipping.** If a usable config or env key already exists, first-run setup does not trigger;
plain `ycc` proceeds straight to the home menu. The wizard is also skippable on purpose (the
user may prefer to hand-author `ycc.toml`), in which case ycc proceeds with whatever fallback
exists and surfaces the existing keyless-401 warning.

### 19.2 Per-project onboarding (agent — scope spec & backlog)

**Trigger.** The first time a session begins in a workspace that has **not been onboarded**.
The signal is the absence of project docs: no `spec.md` at the workspace root (or a trivially
empty one) and no `backlog/`. The client detects this when a project is opened/selected and
offers the appropriate onboarding entry prominently in the home menu (it remains available as
a preset thereafter, since "onboard later" is valid).

This flow is **agent-driven**, and it is a `pm`-mode flow (planning/intake/docs — §9), so it
slots in as two new **pm presets** (opening-prompt + first message), alongside the existing
`feature` / `bug` / `spec` / `backlog` presets. The agent itself distinguishes new vs.
existing:

**New project (empty / greenfield).** Signal: the workspace is essentially empty of code
(e.g. no source files / no meaningful git history) in addition to lacking docs. The agent
runs a **full scoping** conversation: elicit the project's purpose, scope, constraints, and
shape; author an initial `spec.md` (the canonical sections — Vision, Goals, Architecture,
Components, Constraints, Open Questions — §6.1); and seed a starter backlog of well-scoped
tasks. This is the "spec the whole thing" path.

**Existing project (brownfield).** Signal: the workspace already has substantial code but no
ycc docs. Specing the *entire* existing codebase up front is wasteful and rarely what the
user wants. Instead the agent runs a **scoped intake**:
1. Ask the user **what they want to work on** (a feature, an area to refactor, a class of
   bugs, etc.) — the entry point, not a whole-project audit.
2. Explore **only the parts of the codebase relevant to that work** (Read + ripgrep),
   reading enough to understand the slice in question.
3. Write **only the spec slices that the work touches** — author/extend just the relevant
   `spec.md` section(s) (e.g. one Component + the Goals it serves), explicitly *not* a
   from-scratch full spec. Note in the spec that it is partial/seeded-as-needed.
4. Create the backlog tasks for the requested work, with a concrete plan (`propose_plan`),
   so the user can hand one to `work` (`switch_to_work`) immediately.

The guiding principle for brownfield: **spec the work, not the repo.** Coverage grows
incrementally as more work is done, rather than requiring a big-bang documentation effort
before ycc is useful. The agent should make the new-vs-existing determination itself from the
workspace contents (and may confirm with the user when ambiguous), so a single "Onboard this
project" entry can route to the right behaviour; the prompt encodes both branches.

**Relation to existing presets.** The brownfield path overlaps the `feature` preset (explore
→ propose) but differs in intent: it is the *first* time ycc sees the project and it also
establishes the initial spec slice + backlog conventions, whereas `feature` assumes those
already exist. Keeping it a distinct, prominently-surfaced preset is what makes onboarding
discoverable.
