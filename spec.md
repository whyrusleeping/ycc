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
| `interrupted` / `resumed` | agent paused to steer / continued (§18.7) |
| `user_input` / `user_input_delivered` | user message accepted (queued mid-run carries `queued:true`) / delivered at a safe checkpoint (§18.7) |
| `plan_proposed` / `plan_accepted` | coordinator plan checkpoints |
| `review_tier_selected` | which review tier the coordinator chose for a change (§13.1) |
| `review_submitted` | one reviewer's findings |
| `decision_made` | accept / revise, with rationale |
| `doc_updated` | spec or task file changed (with diff) |
| `commit_made` | git sha + message |
| `session_idle` / `session_error` | terminal-ish states |
| `log` | free-text narration for the UI |

Subagents get their own session-scoped event substreams (`subagent_spawned` carries a
child stream id); the client can drill into an implementer/reviewer's transcript.

#### Transient (broadcast-only) events

Some events are **ephemeral UI hints** rather than durable facts. A transient event is
marked `transient: true`, carries **`seq: 0`** (it is *not* assigned a sequence number),
and is **broadcast to live subscribers only**: it is never written to `events.jsonl`,
never appended to the in-memory replay slice, and is invisible to `Snapshot` / `ReadLog`
/ transcripts / late subscribers. Because it never gets a seq, it never advances a
subscriber's `from_seq` resume cursor — a reconnect resumes strictly from persisted seqs,
so transient delivery cannot corrupt or reorder the append-only log/replay. Delivery is
**best-effort and lossy under backpressure** (a slow subscriber may drop transients),
whereas persisted events stay lossless and ordered. The `Subscribe` RPC forwards
transient events unchanged; every subscriber (including the TUI) must tolerate seq-less
events safely.

The motivating use is streaming a model's partial output: `turn_delta` events tail the
in-progress turn text to live clients while the durable `model_turn` event (written on
turn completion) remains the source of truth for the turn (see §18.4, task 0114).

**`turn_delta` payload contract (snapshot semantics).** Each `turn_delta` carries
`{"text": <full-accumulated-turn-text-so-far>}` — a **snapshot**, not an increment. A
client replaces its live tail row with the latest snapshot, so lossy transient delivery
and mid-turn retries are harmless (a retried attempt simply restarts from a short
snapshot). The engine throttles snapshots to ~10/s. A turn's tail is cleared by a
terminating delta `{"text": "", "done": true}` broadcast on turn end (success **or**
error, so no stale tail survives a failed turn) and, redundantly, by the arrival of the
turn's persisted `model_turn`. The engine emits deltas only when the backend client
implements a streaming capability *and* the recorder supports broadcast; otherwise the
turn runs non-streaming with identical semantics and no deltas.

**`retry` (transient).** When an LLM API call fails transiently and the engine loop is
backing off before another attempt (§7.2), it broadcasts a transient `retry` event with
`{attempt, max_attempts, delay_ms, kind, status, msg}` so live clients can show the wait
(the TUI renders a per-actor "retrying…" note under the live tails). Like `turn_delta`
it is never persisted — the durable log stays quiet unless the turn ultimately fails,
which records a `session_error`.

## 6. Document model

### 6.1 Design docs — entry point + docs set

The project keeps a committed, agent-maintained set of design documents reached through a
single well-known **entry point** — the one file an agent reads first to orient. By default
the entry point is `spec.md` at the workspace root (alongside `backlog/`). The entry point may
be, or grow into, an **index** into other docs: a large spec is better split across several
logically decomposed files, with the entry point linking them together.

Projects that already keep a reasonable documentation convention (a `docs/` tree,
`ARCHITECTURE.md`, ADRs) keep it: agents **adopt and maintain the existing docs** — treating
their natural root as the entry point, or writing a thin index that links into them — rather
than imposing a parallel `spec.md`. The durable invariants are only these two: a committed,
agent-maintained design doc set, and a single well-known entry point for orientation.

An optional per-workspace `.ycc/config.toml` names the surface:

```toml
spec_path = "docs/index.md"          # entry point, relative to the workspace root (default "spec.md")
doc_globs = ["docs/**", "adr/*.md"]  # the rest of the docs set (slash globs, relative to root)
```

Missing or malformed config falls back to the default (`spec.md`, no extra globs); a
`spec_path` that escapes the workspace is ignored. A `doc_updated` event fires for an edit
anywhere in the docs set (the entry point or any file matching a `doc_globs` pattern), not
just the entry point. Canonical spec sections (others allowed): Vision, Goals, Architecture,
Components, Constraints, Open Questions. Docs are plain markdown edited with the ordinary
Read/Edit/Write tools — there is no dedicated spec tool. The spec docs are committed with code.

### 6.2 Backlog — structured items, markdown-rendered

Canonical store: **one markdown file per task** with YAML frontmatter, under
`backlog/`. Per-file storage means git diffs are per-task, the agent edits one task
without rewriting the whole backlog, and a UI can manipulate items reliably.

Task file: `backlog/0007-add-token-auth.md`

```markdown
---
id: "0007"
title: Add token auth to the daemon
status: todo            # proposed | todo | in_progress | in_review | done | blocked
priority: 2             # 1 highest
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0003"]
spec_refs: ["Architecture", "docs/rpc.md#Protocol"]
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

`docs` package responsibilities: parse/write task files, validate frontmatter,
append to a task's work log, and provide `list/get/create/update` used by
the coordinator tools.

`proposed` sits before `todo` in the lifecycle: it marks an idea captured during
ideation (typically by the agent) that the user has not accepted as real scope. Proposed
tasks are durable backlog entries but are never *ready* — `list_backlog` doesn't mark them
`[READY]` and the work pipeline never picks them up. Promotion to `todo` (via
`update_task` or the backlog browser) is the explicit acceptance act. `create_task`
takes an optional initial status (`todo`, the default, or `proposed`); agent prompts
direct un-endorsed ideas to `proposed`.

`spec_refs` are free-form strings: a bare section title (e.g. `"Architecture"`) refers to the
spec entry point, while `path#Section` (e.g. `"docs/rpc.md#Protocol"`) references a section of
another doc in the docs set. Bare titles keep their meaning, so existing refs stay valid.

### 6.3 Reusable plans (runbooks)

Distinct from the backlog: a **task** is one-off work to do; a **plan** is *how* to do
something, repeatably. Reusable plans live in-repo at `plans/*.md` — committed,
version-controlled markdown procedures (matches the docs-driven philosophy). The
motivating case is a **testing/verification plan**: a repeatable procedure you replay
instead of re-describing. A plan is free markdown with a `#` title, concrete steps, and an
expected outcome.

There are no dedicated plan tools: plans are plain committed files, so agents browse the
library with `Bash`/`Read`, execute a plan by reading it and following its steps, and save
one with `Write` — the format convention (kebab-case name, `#` title, steps, expected
outcome) lives in the coordinator/pm prompts. The `docs` package provides
`ListPlans/ReadPlan/SavePlan/PlansDir`, used by the TUI/RPC plan-browsing surface (§16).

Separately, `propose_plan` now persists the FULL coordinator plan to the task's `## Plan`
section (a durable, human-browsable artifact) in addition to the dated one-line work-log
breadcrumb — the complete plan lives next to its task, not just buried in a session event.

### 6.4 Spec doctor — drift & coverage checking

The founding principle is that the durable state of a project lives in documents and **a
drifted spec is a bug** (§1). The **spec doctor** actively detects that drift instead of
merely trusting agents to keep the docs true. It is an **on-demand `pm` preset** (`spec-doctor`,
§9) — there is no scheduling or auto-trigger — and it runs in two phases:

1. **Deterministic pre-pass** (`internal/specdoctor`, surfaced as the `ycc spec-check` subcommand). It
   extracts the concrete file paths, package directories, and code symbols the docs set
   mentions in *inline code spans* (fenced code blocks are skipped — they hold illustrative
   examples) and verifies each still exists: paths via `os.Stat` (a file or a directory both
   resolve, covering package dirs like `internal/docs`); symbols via a word-boundary search
   across the workspace source, excluding the docs set itself and `backlog/` so a reference
   never resolves against its own mention. It holds a strict **zero-false-positive**
   discipline — any span that is ambiguous (a glob pattern, a multi-word command, a bare
   lowercase or ALL-CAPS word) is *skipped, never flagged*. Its markdown report of stale
   references is confirmed drift and seeds/grounds phase 2. Because it is fully deterministic
   and daemon-free, it is a plain CLI subcommand rather than an agent tool: the spec-doctor
   preset drives phase 1 by running `ycc spec-check` via `Bash` (it exits non-zero when it finds
   stale references), and the same command is directly usable by humans and as a pre-commit / CI
   gate.
2. **LLM comparison pass.** Grounded by the pre-pass, the agent walks the spec section by
   section, reads the relevant code, and flags only two things: **drift** (the spec states
   behavior, an interface, a name, or a flow the code now *contradicts*) and **coverage gaps**
   (a significant `internal/*` package, RPC, or user-facing tool with no spec section). The
   spec is *intentionally* higher-level than the code, so the pass flags genuine
   contradictions and undocumented significant surface only — never mere missing implementation
   detail.

**Output** is all three: a consolidated **report** (stale refs + drift + coverage gaps), a
**proposed backlog task** per actionable finding (`create_task`), and **suggested spec edits**
drafted for the user — applied only with explicit approval (the flow reuses `pm`'s existing
`create_task` and Edit/Write tools; it drafts, the user approves). Cost is intentionally
unbounded for now; sampling or git-driven targeting can be added later if it proves expensive.

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

#### API failure handling (classification, retry, session_error)

All LLM API failures flow through one classifier (`engine.ClassifyAPIError`), which maps
an error to a **kind** — `rate_limit` (429), `overloaded` (503/529), `server` (other
5xx), `timeout` (408 / transport timeout), `network` (transport failure), `auth`
(401/403), `invalid_request` (other 4xx), `context_length` (a 400 whose body matches the
providers' context-window phrasings), or `unknown` — plus the parsed HTTP status and a
**retryable** verdict. Retry decisions, context-window detection
(`IsContextLengthError`), and error events all use this one taxonomy.

**Retry lives in the loop** (`Loop.runTurn`, policy `Loop.Retry`; zero value = 3 total
attempts, exponential backoff 500ms→30s with equal jitter). The loop is where the run
ctx (a stopped session cancels a pending backoff instead of sleeping it out), the
emitter (each backoff broadcasts a transient `retry` event, §5), and the classification
meet. Non-retryable failures surface immediately; retries exhausted surface the
original error. (Layering note: gollama's transport additionally retries 429/503/529
internally before its error ever reaches the loop.)

**A failed turn records exactly one `session_error`**, emitted by the loop with
structured data: `{msg, kind, status, retryable, attempts, duration_ms, turn}`. The
returned error is an `engine.TurnError` marking the failure as already recorded — outer
layers (`Session.run`) check for it with `errors.As` and must not emit a duplicate. A
cancelled run (session stopped) records nothing. `context_length` failures replace the
opaque provider 400 with an actionable message (start fresh / narrow scope) since
retrying can never succeed.

### 7.3 Subagents

A subagent is just another `Loop` with its own client/model/system/tools and an event
substream. The coordinator spawns one via a control tool; the spawn is synchronous from
the coordinator's perspective (it awaits a structured report) but reviewers fan out
**concurrently** (goroutines + a barrier). Reviewer contexts are *retained* so a revise
round can reuse them (`re-review` sends the new diff into the existing reviewer history).

**Async jobs (planned).** Spawns and long shell commands will also be runnable in the
background under a unified **job** abstraction (`background: true` on spawn tools /
`run_in_background` on Bash → a job id; `wait`/`job_output`/`kill_job`; completed-job
reports pushed into the conversation at loop checkpoints so the model never polls).
Design: `docs/design/async-jobs.md`; backlog 0131/0132.

### 7.4 Reasoning (extended/adaptive thinking + effort)

Every agent's request carries reasoning settings (Anthropic extended/adaptive
thinking). The engine `Loop` holds `Thinking` / `Effort` / `ThinkingDisplay` fields beside
`MaxTok` and sets them on the `gollama.RequestOptions` for every turn; these reach the
coordinator loop, the implementer, and each reviewer, resolved **per role** (not just per
model) so the coordinator, implementer, and reviewers can each reason at a different depth
even when they share a backend (§13). `Thinking=""` disables reasoning; `"adaptive"` enables it;
`Effort` (`low`..`max`) tunes depth/spend; `ThinkingDisplay="summarized"` opts into reasoning
summaries. The provider's reasoning blocks round-trip automatically because the engine
appends the returned assistant `Message` (which carries `ThinkingBlocks`) to history. When a
turn returns a reasoning summary, the loop emits a dedicated `thinking` event (before the
`model_turn`) for the UI (§18). Non-Anthropic backends ignore these fields.

## 8. Tools

**Worker tools** (implementer; read/write the workspace):
`Read`, `Write`, `Edit`, `Bash`, `web_search`/`fetch_page` (Exa-backed; no-op without a
key), `finish(report)` — the control tool that ends the run and returns the report to
the coordinator — and `report_blocked(reason)` — a structured escalation control tool the
implementer calls INSTEAD of `finish` when it cannot responsibly proceed without a decision
that isn't its to make; it ends the run with a distinct BLOCKED outcome (the reason lands in
the task work log) rather than a normal report. There are no separate `list_dir`/`grep`/`glob`
tools: `Read` on a directory lists it, and searching goes through `Bash` + ripgrep.

**Multimodal `Read`.** The `Read` tool is multimodal, mirroring Claude Code: there is **no
separate "view image" tool**. When `Read` is given an image (PNG, JPEG, GIF, WebP) or a PDF
it returns the bytes as a **native content block** (an image block / an Anthropic document
block) in the tool result rather than `cat -n` text, so the model perceives the file through
the provider's native vision/PDF support. gollama already carries this end-to-end —
`ToolResult.Images`/`Documents` round-trip into a `tool_result`'s content blocks (Anthropic
native path). The engine loop attaches that media to the tool message for Anthropic; for
OpenAI-compatible backends (which don't allow media in a tool-role message) it instead sends
images as a follow-up user message, and PDFs degrade to a text note. A size cap keeps oversize
files from being inlined (the model is told to use `Bash` for those instead).

**Coordinator tools** (orchestrate; delegate real coding to the implementer):
the editing set (`Read`/`Write`/`Edit`/`Bash` — for verifying state and reviewing diffs
first-hand; the prompt confines its own edits to tiny touch-ups),
`list_backlog`, `get_task`, `create_task`, `update_task`,
`propose_plan(task_id, plan, context_hints?)`,
`spawn_implementer(task_id, plan, context_hints?)`,
`spawn_reviewers(task_id, review_tier?)` (§13.1),
`send_to_implementer(task_id, instructions)` (revise; reuses implementer ctx),
`re_review(task_id)` (reuses reviewer ctx), `commit(task_id, message)`,
`ask_user(question, options?)`, `finish()`. There is no `update_spec` tool: `spec.md` is a
plain file edited with `Edit`/`Write` (a write to it is surfaced as a `doc_updated` event).

**Shared prompt assembly.** Every agent's system prompt is assembled through one path
(`sys`/`inspectSys` in `internal/orchestrator`): the role's base prompt + the shared tooling
guidance (Read-over-cat, ripgrep, fresh-shell/no-`cd` rules; read-only roles get it without
the editing sentence) + a workspace note + (coordinator/pm/chat only) the interaction-level
policy. Subagents don't receive interaction-level guidance — they have no `ask_user`.

`ask_user(question, options?)` is the structured-question control tool. The optional
`options` parameter is a list of suggested answers; when present, the client renders a
selectable picker (Claude-Code style) with an "other…" escape to free text. When
absent, the user answers with free (multiline) text. See §18.3 for the UI side. The
`Asker.Ask(ctx, question, options)` interface already carries `options` end-to-end;
exposing it on the tool schema + answering by option is the remaining wiring.

`ask_user` can also pose **several questions in one call**: pass `questions`, a list
where each item has its own `question` text and its own optional `options` set. The
client presents a short questionnaire (the user answers each question — picker or free
text — before a single final submit) and the answers are returned mapped to their
questions (`Q1/A1`, `Q2/A2`, …). This is wired end-to-end via `Asker.AskMany` and the
`AnswerQuestions` RPC; the single-question form (above) is unchanged.

Tools are gollama `Tool` values (`Params` is JSON schema, `Call` does the work + emits
events). Worker and orchestration tools are the same kind of object.

**Reviewer bash sandbox.** Reviewers get `Read`, `Bash`, and `submit_review` and are
told not to mutate the change under review. Their `Bash` is hard-enforced read-only via
`internal/sandbox` where the host supports it. Mechanism order (probed once, cached):
**Landlock** (preferred — Linux ≥ 5.13, no external dependency, symlink-proof because the
kernel evaluates the real inode), then **bubblewrap** (`bwrap`, re-binding the workspace
read-only), else **none**. The Landlock path re-execs the `ycc` binary as a hidden helper
(`__ycc-sandbox-exec`, dispatched at the very top of `main` before CLI parsing) that
installs a ruleset denying all filesystem writes by default and re-allowing writes only
under a small allowlist (temp dirs, `/dev`, `/run`, the Go build/module caches) that
**excludes** the workspace; reads and execs are permitted everywhere, so `git diff`, `cat`,
`grep`, `ls`, and builds still work. It **fails closed**: if the policy cannot be applied
the helper exits non-zero rather than running unsandboxed. When no mechanism is available
(non-Linux, or kernel/tool support missing) it degrades to prompt-only enforcement and the
orchestrator emits a one-off `log` (Narration) warning per `spawn_reviewers`. Relatedly,
`Workspace.resolve` (the Write/Edit path confinement) is now **symlink-aware**: after the
textual `../` check it resolves symlinks and rejects a path that lands outside the root
through an in-workspace symlink.

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
  `ask_user`, `finish`. The spec-doctor drift check is no longer a pm tool: it is the
  daemon-free `ycc spec-check` subcommand (§6.4) the preset runs via `Bash`. This **replaces** the former `spec`, `backlog`, `feature`, and `bug` modes —
  they were one capability set under four prompt framings, and are now simply ordinary
  `pm` work rather than distinct menu entries. The home menu no longer lists those framings
  as separate presets (they added redundant clutter for what is all planning/intake work);
  `pm`'s own description signals it covers spec authoring, backlog grooming, new features,
  and bug intake. The remaining opening-prompt presets are **`onboard`** (§19.2), the
  distinct first-run flow, and **`spec-doctor`** (§6.4), the on-demand spec/code drift &
  coverage check. A prompt typed alongside a selected preset **composes** with it —
  the preset supplies the framing and the typed text is appended as the user's upfront
  context — rather than replacing it.
- **`chat`** — open-ended assistant that *can* edit code directly, with no fixed workflow.
  Kept as the freeform "just do it" counterpart to `pm`'s "just plan it."
- **`work`** — the orchestrated implementation pipeline (§10): pick/accept a task, plan,
  spawn implementer, multi-model review, revise, commit, update backlog.

**`work (loop)` — unattended backlog drain.** On the home menu, pressing **tab** with the
`work` entry selected toggles it to `work (loop)`. Starting it runs `work` repeatedly: each
session drives one task to a committed (or blocked/in_review) state, and when it ends the
client immediately starts a fresh `work` session (new context, no carried prompt) for the
next ready task. It keeps going until nothing is actionable — every remaining task is `done`,
`blocked`, `in_review`, or not yet `ready` (dependencies unmet). A guard stops the loop if a
finished session left its expected task unchanged (so it would re-pick the same task forever),
and **shift+tab** in the running session toggles the loop — halting is *graceful* (the current
task finishes and commits; the loop just doesn't pick up the next one), and it can likewise
roll a single `work` session into a loop. This pairs with the coordinator's
ability to mark a task `blocked` when it needs user feedback (§10): the loop simply skips such
tasks rather than stalling. The loop is a **client** concern (the daemon just runs each
session); the running session view shows a `⟳ loop` indicator.

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
        if acceptable → update_task(status=done) + commit (captures final backlog state) + finish
        else          → send_to_implementer(consolidated instructions)
                        → re_review()   (reuse reviewer contexts)
                        → back to 6
  7. on finish: emit session_idle, return to user
```

Fresh context in step 0 is important: each `work` session starts clean so the
coordinator reasons from the durable docs, not from stale conversation.

**Blocked implementer (step 4).** Instead of a normal report, the implementer can end its
run BLOCKED (via `report_blocked`) with a reason — a decision that isn't its to make. The
coordinator then resolves the decision itself (an ordinary judgement call), asks the user
per the interaction level, or marks the task `blocked`; the reason is recorded in the task's
work log, and a subsequent `send_to_implementer` resumes the same context with the answer.

A `work` session drives **one** task to a committed state, but the coordinator may
**grow the backlog** while doing so via `create_task` (the same tool `pm` uses):
- **Split** — if the task is too big, break out the scope that doesn't belong in this
  commit into new tasks (optionally `depends_on` the current one) rather than cramming
  everything into one change.
- **Follow-on** — capture worthwhile follow-up it discovers (refactors, hardening,
  tests, latent bugs) as new tasks instead of dropping it or expanding the active task's
  scope.
This keeps the active task focused; new tasks get clear titles, acceptance criteria, and
appropriate dependencies. It is the mechanism, not an invitation to scope-creep.

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
  rpc Interrupt(InterruptRequest) returns (InterruptResponse);          // pause a running agent to steer (§18.7, task 0040)
  rpc Resume(ResumeRequest) returns (ResumeResponse);                   // continue after a pause, unchanged (§18.7, task 0040)
  rpc Stop(StopRequest) returns (StopResponse);                         // terminate a session (task 0009)

  // Projects — persistent multi-project daemon (§3.1).
  rpc ListProjects(ListProjectsRequest) returns (ListProjectsResponse);
  rpc AddProject(AddProjectRequest) returns (AddProjectResponse);
  rpc RemoveProject(RemoveProjectRequest) returns (RemoveProjectResponse);

  // Parallel workstreams — git worktrees, child of a project (§14.1).
  rpc SpawnWorkstream(SpawnWorkstreamRequest) returns (SpawnWorkstreamResponse);
  rpc ListWorkstreams(ListWorkstreamsRequest) returns (ListWorkstreamsResponse);
  rpc PreviewMerge(PreviewMergeRequest) returns (PreviewMergeResponse);   // non-mutating trial merge
  rpc MergeWorkstream(MergeWorkstreamRequest) returns (MergeWorkstreamResponse);
  rpc DiscardWorkstream(DiscardWorkstreamRequest) returns (DiscardWorkstreamResponse);

  // Settings overlay (§18.2) — change session config mid-flight.
  rpc ListModels(ListModelsRequest) returns (ListModelsResponse);       // available logical models
  rpc UpsertModel(UpsertModelRequest) returns (UpsertModelResponse);    // add/edit a model backend (§18.2, task 0041)
  rpc RemoveModel(RemoveModelRequest) returns (RemoveModelResponse);    // delete a model backend (§18.2, task 0041)
  rpc DiscoverModels(DiscoverModelsRequest) returns (DiscoverModelsResponse); // list a connection's model ids (§13, §18.2)
  rpc SetInteractionLevel(SetInteractionLevelRequest) returns (SetInteractionLevelResponse);
  rpc SetRoleConfig(SetRoleConfigRequest) returns (SetRoleConfigResponse);
  rpc SetThinking(SetThinkingRequest) returns (SetThinkingResponse);    // per-role reasoning level
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
- `SetThinkingRequest { session_id; string role; string level }` — set a reasoning level
  (`off | low | medium | high | xhigh | max`) for one **role** (`coordinator | implementer |
  reviewers`); an empty `role` applies the level to **every** role at once. The override takes
  precedence over that role's config thinking until changed (§13). `off` disables reasoning;
  any effort level maps to adaptive thinking at that effort with summarized display. (The
  prior shape was session-wide with no `role` — adding `role` makes thinking independently
  configurable per agent.)
- `ListModelsResponse { repeated ModelInfo models }` where `ModelInfo` carries the
  logical name + backend + model id, so the client can populate the role pickers. For
  *editing* backends the client needs the full record: a `ModelConfig` message mirrors a
  `[models.X]` block (name, backend, base_url, model, key_env, thinking/effort/display,
  pricing) and is returned by an extended `ListModels` (or a `GetModelConfig`).
  `UpsertModelRequest { ModelConfig model; bool persist }` adds or replaces a logical model
  by name; `RemoveModelRequest { string name; bool persist }` deletes one. The daemon
  **always** writes the change back to `ycc.toml` via `config.Save` (§19.1) so it survives
  restart; the `persist` field is retained for wire compatibility but ignored (a settings
  edit is never runtime-only). The daemon rebuilds backends on the next `Build`, so changes
  take effect without a restart (§13, §18.2, task 0041).
- `InterruptRequest { session_id }` / `ResumeRequest { session_id }` — pause a running
  agent at the next safe checkpoint, then continue (§18.7). A correction is steered in by
  `SendInput` while paused; `Resume` continues with no change. `Interrupt` is a *graceful
  pause to steer*, distinct from `Stop` (terminate, task 0009).

`Subscribe` takes a `from_seq` so a reconnecting client replays from an offset. Auth: a
bearer token (config/env) — **required** for any non-loopback bind (the daemon refuses to
start without one). The TUI talks to the loopback daemon; remote clients dial in over a
private network (Tailscale/VPN), with TLS optional (`TLSCert`/`TLSKey`) for direct
exposure (§14).

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

[roles.thinking]               # optional per-role reasoning override (see below)
coordinator = "xhigh"          # off | low | medium | high | xhigh | max
implementer = "low"
reviewers   = "high"           # one level for the whole reviewer fan-out

max_tokens  = 32000  # per-turn output token cap (0 => backend default)
max_turns   = 1000   # per-Run tool-call turn cap; runaway/cost backstop (0 => engine default, 1000)
```

The registry hands the engine a configured gollama `Client` + model string for any
logical name. Reviewer fan-out iterates `roles.reviewers`.

**Logical model = credentials/endpoint + model id.** A `[models.X]` block bundles a
backend's *credentials/endpoint* (`backend`, `base_url`, `key_env`) with a specific
*model id* (`model`). These are conceptually separable: several logical models may share
one backend's credentials/endpoint while pointing at **different model ids** — e.g.
`claude-opus`, `claude-sonnet`, and `claude-haiku` all using the same `anthropic` backend,
`base_url`, and `ANTHROPIC_API_KEY`, differing only in `model` (and possibly pricing). This
is how "the same backing token, a different model" is expressed: define sibling logical
models that reuse the credential. The TUI's backend manager (§18.2) makes this ergonomic in
two ways: **adding a connection** captures the credential/endpoint once plus a *set* of
model ids (one space/comma-separated field), creating one sibling logical model per id
(named after the id); and **duplicate** clones an existing model, changing only the name +
model id. The set of ids can be **discovered** from the live backend via `DiscoverModels`
(the OpenAI-compatible `/models`, Anthropic `/v1/models`, or Ollama `/api/tags` endpoint)
or seeded from curated per-backend defaults. The underlying config stays the flat
per-logical-model map (a dedicated `[providers.X]` credential table that models reference is
a possible future normalization, not required for this).

**Per-model reasoning** (`thinking` / `effort` / `thinking_display`) is configured on each
`[models.X]` block and resolved by the registry (`ThinkingFor(name)`), paralleling
`max_tokens` / `MaxTokens()`. These map to Anthropic extended/adaptive thinking + effort
(see §7.4); they are honored by the anthropic backend and ignored harmlessly by others.
**Defaults are reasoning-on** (`thinking="adaptive"`, `effort="high"`,
`thinking_display="summarized"`) — this is an agentic coding harness, so reasoning is
desired by default, including on the no-config single-backend path. Set `thinking="off"`
(or `""`) on a model to disable reasoning.

**Per-role reasoning** lets each agent reason at a different depth even when roles share a
backend (e.g. coordinator `xhigh`, implementer `low`, reviewers `high`). An optional
`[roles.thinking]` sub-table assigns a single-knob level (`off | low | medium | high | xhigh
| max`) per role; `reviewers` takes one level applied to the whole fan-out. The same levels
are settable mid-session per role via `SetThinking(role, level)` (§12, §18.2). Reasoning for
an agent is resolved with this precedence (highest wins):

1. **per-role session override** (settings overlay / `SetThinking`),
2. **per-role config** (`[roles.thinking]`),
3. **per-model config** (`[models.X]` thinking/effort/display),
4. **package defaults** (adaptive / high / summarized).

A level maps to adaptive thinking at that effort with summarized display; `off` disables
reasoning. The resolved settings are applied per **role** to every agent: coordinator,
implementer, and each reviewer (a reviewer resolves its model fallback independently, but the
`reviewers` role override/config applies to all reviewers uniformly).

`max_turns` bounds how many tool-call turns a single engine `Run` may take. It is a
**runaway backstop**, not a normal stopping condition: the high default (1000) keeps the
implementer's read → edit → build → test → fix cycles from being cut off mid-task while
still stopping a degenerate infinite tool-call loop. The cap is **per `Run`**, so each
`send_to_implementer` revise round gets a fresh budget rather than accumulating across
rounds. Interaction with context-window management (§ task 0010): a higher turn cap lets a
run accumulate more conversation history, so until context budgeting lands a very high
`max_turns` can trade a turn-limit abort for a context-window-limit abort on long runs.

### 13.1 Review tiers

Review intensity is **tiered** and the work coordinator picks a tier per change based on its
size/risk, rather than every change getting the same fixed review. Tiers are named and
configurable under an optional `[reviews]` table:

```toml
[reviews]
default = "single-opus"           # tier used when the coordinator doesn't pick one
[reviews.tiers.high-powered]
models = ["claude", "gpt"]        # parallel multi-model review
[reviews.tiers.simple]
strategy = "coordinator"          # coordinator self-reviews; no reviewer agent
```

Three **built-in tiers** always exist (and may be overridden by a same-named `[reviews.tiers.X]`):

- **simple** — `strategy = "coordinator"`: the coordinator reviews the change itself; **no
  reviewer agent is spawned**. Intended only for tiny, low-risk changes.
- **single-opus** — one reviewer; reproduces the current default reviewer behaviour (the
  configured `roles.reviewers`).
- **high-powered** — multiple reviewers running **in parallel**, results aggregated; for
  large, risky, security-sensitive, or hard-to-reverse changes. Out of the box the built-in
  `high-powered` tier resolves to the **same reviewer set as `single-opus`** (the configured
  `roles.reviewers`); it only runs a genuinely parallel multi-model review once
  `[reviews.tiers.high-powered]` is configured with more than one model (e.g.
  `models = ["claude", "gpt"]`).

Each tier maps to a **strategy** plus a model/agent set: `strategy = "agents"` (the default
when empty) spawns a reviewer subagent for each logical model in `models`; `strategy =
"coordinator"` (aliases `self` / `self-review`) means the coordinator self-reviews with no
separate reviewer agent. The coordinator selects a tier per change via the `spawn_reviewers`
tool's optional `review_tier` parameter; omitting it uses `reviews.default` (which itself
defaults to `single-opus` — the sensible default for ordinary changes).

Selection is **auditable**: every `spawn_reviewers` call emits a `review_tier_selected` event
(`{ task, tier, requested, self_review, fallback, reviewers }`) and writes a `review tier: …`
line to the task's work log. An **unknown or missing tier degrades gracefully** — an unknown
`review_tier` falls back to the default (recorded with `fallback=true`), an `agents` tier whose
models don't resolve falls back to the session's current reviewer assignment, and a tier that
resolves to no reviewer at all degrades to coordinator self-review. The explicitly configured
tiers are validated at load (unknown strategy, an `agents` tier referencing an unknown model,
or a `reviews.default` naming no tier are rejected); the built-ins are always valid.

## 14. Persistence & remote sync

- Per-session JSONL event log is the unit of persistence and of sync.
- `.ycc/` holds sessions, snapshots, and config; the `spec.md` + `backlog/` live in the
  repo proper (committed with code).
- A persistent daemon's **project registry** (name → path) is durable state in the
  daemon's state dir (`~/.local/state/ycc/projects.json`), separate from each project's
  per-workspace `.ycc/`. One-shot daemons keep no registry (one implicit project = cwd).
- **Remote access (M5) — direct dial, no log replication.** *Decided:* the earlier
  daemon-to-daemon push/pull replication idea is **dropped**. Remote observation and
  prodding happen by dialing the workspace daemon's Connect endpoint directly — another
  machine runs `ycc -addr <url>`, and a phone app speaks the same Connect HTTP/JSON
  protocol (via connect-swift / connect-kotlin / connect-es; it is also curl-able). No
  separate REST/SSE facade. `Subscribe(from_seq)` already *is* "ship events after seq N",
  and the single-writer invariant holds trivially: remote clients only issue RPCs
  (input/commands), which the workspace daemon serializes; nothing else ever writes the
  log. Deployment model: a private network (Tailscale/VPN) with the **bearer token
  required on any non-loopback bind** (the daemon refuses to bind otherwise); TLS is
  optional (`TLSCert`/`TLSKey` flags exist) since the tailnet provides transport
  encryption — the daemon logs a cleartext warning when bound non-loopback without TLS.
  The phone-facing surface is the **documented** HTTP/JSON endpoint set — see
  [`docs/remote-api.md`](docs/remote-api.md) (connection & auth, protocol primer,
  endpoint catalog, and the event model for client authors).

### 14.1 Parallel workstreams (git worktrees)

To run multiple agent tasks at once without them clobbering each other's working tree,
ycc adopts **git worktrees** (design spike task 0078; full rationale/alternatives in
`docs/design/parallel-workstreams.md`).

- **Concept.** A **workstream** = a linked git worktree + a branch
  `ycc/ws/<workstream-id>[-<task>]` + a `work` session scoped to the worktree's absolute
  path. A workstream is a **child of a project**, never a top-level project entry — this
  keeps the project picker free of ephemeral worktrees.
- **Worktree location.** Out of the primary tree, under the daemon state dir:
  `<state>/ycc/worktrees/<project>/<workstream-id>`. Each worktree keeps its own
  `.ycc/sessions/<id>/events.jsonl` (git-ignored, so it never travels into a merge).
- **Single-writer, per tree.** The single-writer invariant (§14) holds verbatim: exactly
  one daemon/coordinator writes each worktree. Parallelism is *across* trees, each
  internally single-writer — the invariant itself is unchanged.
- **Workstream registry.** The daemon owns a serialized `workstreams.json` in the state
  dir (beside `projects.json`), mapping id → `{ project, base commit, branch, worktree
  path, session id, status }`. This is metadata, not workspace mutation, so it doesn't
  violate single-writer. Startup recovery reconciles stale worktrees via `git worktree
  list`/`prune`.
- **Merge flow — conflict-aware, sequential, review-gated.** A **non-mutating trial
  merge** detects conflicts first. **Clean** → auto-merged under the *autonomous*
  interaction level, otherwise gated behind an explicit acceptance with the integrated
  diff shown. **Conflicted** → emit `workstream_conflict` listing the conflicted paths and
  stop; the base branch is **never** left conflicted, and the worktree is kept for
  resolution. On merge or discard, cleanup runs `git worktree remove`, deletes the branch,
  and `git worktree prune`.
- **Lifecycle events** on the workstream's own session stream: `workstream_created`,
  `workstream_merged`, `workstream_conflict`, `workstream_discarded` — clients render them
  like any other session event.
- **RPC surface** (§12): `SpawnWorkstream`, `ListWorkstreams`, `PreviewMerge`,
  `MergeWorkstream`, `DiscardWorkstream`; `Subscribe` is reused verbatim for the
  workstream's session stream.

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
    specdoctor/      # deterministic spec/code reference checker (drift pre-pass, §6.4)
    event/           # event types, JSONL store, reducer/projection
    session/         # session lifecycle + state
    server/          # connect handlers, auth
    daemon/          # serve + lifecycle (one-shot in-process vs persistent) + project registry + client dialing
    tui/             # Bubble Tea home menu + session view
    git/             # diff/commit helpers
```

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
- **M5 — Remote access.** Direct-dial remote clients over a private network
  (Tailscale/VPN): bearer token required on non-loopback binds, TLS optional; verified
  end-to-end remote Subscribe/prod path + a documented Connect HTTP/JSON surface for
  phone clients. Daemon-to-daemon log sync/replication is **dropped** (§14).
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
  workspace, and their `Bash` is now **sandboxed read-only on Linux** (Landlock,
  falling back to bubblewrap; see §8), which hard-enforces non-mutation while keeping
  read-only inspection working. Where no sandbox mechanism is available (non-Linux, or
  missing kernel/tool support) it degrades gracefully to prompt-only enforcement with a
  logged warning. Implemented in task 0008.
- **Implementer isolation:** *Decided.* A single-task implementer works **directly on the
  primary codebase**. For **parallel** work, git worktrees are **adopted**: each parallel
  workstream gets its own linked worktree so the single-writer invariant holds *per tree*
  (design spike task 0078; `docs/design/parallel-workstreams.md`). The previously deferred
  revisit is now resolved — see §14.1 for the workstream concept, registry, and merge flow.
- **Commit granularity:** one commit per accepted task vs. checkpoints during work.
- **TUI framework:** Bubble Tea is the obvious Go choice for the client.
- **Session GC / retention** of `.ycc/sessions`.
- **Secrets:** keep API keys in env only, or a daemon-side keyring?

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
  log (and reflected in any other subscribed client). This is deliberately **per-session
  and not persisted** — a persisted global `autonomous` default (no human approval gates)
  would be a safety footgun, so each session starts at its configured/default level.
- **Thinking level** — `off | low | medium | high | xhigh | max`, changeable
  **mid-session**, set **per role** (coordinator / implementer / reviewers). Each role has
  its own reasoning override that applies to that agent (and the reviewer fan-out for
  `reviewers`), taking precedence over that role's config thinking (§13) until changed.
  Selecting a value issues `SetThinking(sessionID, role, level)`; the daemon updates the live
  coordinator loop (when the coordinator role changes) and rebuilds the implementer/reviewer
  specs so the next spawn uses it, emits a `thinking_level_changed` event carrying the role,
  and **persists** the new level as the default (`roles.thinking.*` in `ycc.toml`) so it
  survives a restart. With no live session (changed from the home menu) an empty `session_id`
  just persists the default. `off` disables reasoning; any effort level maps to adaptive
  thinking at that effort with summarized display.
- **Model / role configuration** — the headline feature. Per-role model selection:
  - **coordinator** — pick one model
  - **implementer** — pick one model
  - **reviewers** — pick *one or more* models (multi-select; reviewer fan-out, §13)
  - Choices are drawn from the configured logical models (§13). Cycling a role
    picker (←/→ for coordinator/implementer, space to toggle reviewers) **applies
    immediately** — there is no separate "apply" step. The change updates the
    current session **and is persisted** as the default role assignment (`roles` in
    `ycc.toml`) so the selection survives a restart and applies to future sessions.
    Issued via `SetRoleConfig(sessionID, roles)`; with a live session the daemon
    rebuilds the relevant gollama clients so the next coordinator turn / next spawned
    subagent uses the new assignment, then writes `ycc.toml` via `config.Save`. Opened
    from the home menu with **no** session, an empty `session_id` just persists the new
    default (which the next session picks up). The overlay seeds its pickers from the
    daemon's current default assignment (returned by `ListModels`) so it always shows
    the real current selection rather than a guess.
- **Model backends (add / edit / remove)** — beyond *choosing* among configured models,
  the overlay can **manage the model backends themselves**, so the user can configure
  everything about a provider from the TUI without hand-editing `ycc.toml` or re-running
  first-run setup (§19.1). A backend manager screen lists the configured logical models and
  lets the user **add** a new one, **edit** an existing one, **duplicate** one, and
  **remove** one. Adding is **connection-centric** (§13): the form captures one connection
  (backend `anthropic|openai|openai-compatible|glm|ollama`, base URL, `key_env`, shared
  reasoning/pricing) plus a **set of model ids** in a single space/comma-separated field.
  Submitting creates **one sibling logical model per id**, each named after its model id, so
  a single anthropic connection yields selectable `claude-opus-4-8` / `claude-sonnet-4-5` /
  `claude-fable-5` models the role pickers can assign independently. The model-id field is
  seeded with the backend's curated defaults (opus/sonnet/fable for anthropic) and can be
  populated from the live backend with **`ctrl+f`** (`DiscoverModels`, §13) — the
  OpenAI-compatible `/models`, Anthropic `/v1/models`, or Ollama `/api/tags` endpoint —
  falling back to curated defaults when discovery is unavailable. **Edit** operates on a
  single model id; **duplicate** clones a connection and changes only the name + model id.
  This reuses the first-run wizard's provider form (task 0023). Edits issue
  `UpsertModel` / `RemoveModel` (§12); the daemon updates the live config (so the next
  `Build` uses it) and **always** writes `ycc.toml` via `config.Save` so a settings
  change survives a restart — persistence is unconditional, not an opt-in toggle. (The
  RPCs keep a `persist` field for wire compatibility but the daemon ignores it and
  always persists; when no config path can be resolved the edit still applies in-memory.)
  A removed or renamed model still referenced by a role is rejected (validation) so the
  session never points at a missing backend.
- **UI preferences** — theme/style, follow/auto-scroll toggle, and similar client-only
  prefs. These never touch the daemon; they live in client state (and a small local
  client config).
- **Interrupt agent** — gracefully pause the running agent at its next safe checkpoint
  (§18.7); while paused the same row reads **Resume agent**. This is the overlay route to
  interrupt, and the reliable fallback on terminals where `ctrl+i` cannot be
  distinguished from tab (no kitty keyboard protocol).
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

`ask_user` may also pose **multiple questions at once** (via the `questions` list — see
§8): each question has its own optional options set. The client then drives a short
**questionnaire wizard** — it shows an overview of every question and answers them one at
a time (picker or free text per question), then submits all answers together. Wire path:
one `question_asked` event carries the `questions` list; the `AnswerQuestions` RPC carries
the positional answers (per-question option index or free text); one `question_answered`
event carries the `answers` list, returned to the model mapped to each question. The
single-question wire path above is unchanged.

**Transcript rendering (one block per exchange).** One ask_user round-trip produces four
events on the wire — the engine's `tool_call`, the gate's `question_asked` +
`question_answered`, and the engine's `tool_result` (whose payload repeats the answer) —
but the TUI renders the exchange **once**: the `question_asked` row is the canonical
block, the answer folds into it (`→ answer`, per-question for batches, options dropped
once answered), and the tool plumbing rows plus the `question_answered` row are hidden.
An ask_user call that errored without asking (or whose result is an error, e.g. cancelled
mid-question) keeps its error row visible. While the footer picker/wizard is collecting
the answer, the question row's body collapses to an "answer below ↓" pointer so the
prompt never shows twice on screen at once. Autonomous auto-answers render as a single
dim "auto-answered (autonomous mode)" line instead of the canned no-human paragraph the
model receives.

### 18.4 Reasoning (thinking) in the event stream

When a model turn returns a reasoning summary, the engine emits a `thinking` event (§7.4)
carrying the summary text. The TUI renders it like any other stream event — **collapsed by
default** with a one-line `(reasoning) …` preview, click/Enter to expand — so the agent's
"inner voice" is available without cluttering the stream. The expanded body is shown
**dimmed + italic** to read distinctly from the model's actual response. Empty summaries
produce no event. (The provider reasoning blocks themselves round-trip in conversation
history automatically and are not re-displayed.)

Partial model output is streamed incrementally to live clients as **transient
`turn_delta` events** (broadcast-only, seq-less, never persisted — see §5.2) and the
durable `model_turn` event written on completion remains the turn's source of truth
(task 0114). Each delta's `text` is the **full accumulated turn text so far** (a
snapshot), so the TUI renders a single dim, in-progress "streaming…" tail row per
streaming actor that is *replaced* by every fresh snapshot and *removed* by a
terminating `{"text":"","done":true}` delta or the actor's persisted `model_turn`
(whichever comes first) — leaving no stale tail even if a turn errors. Backends without
a streaming capability run the turn non-streaming with identical semantics and no
deltas.

### 18.5 Backlog browser

The backlog is durable project state (§6), but until now a client could only see it
indirectly through the agent. A **backlog browser** lets the human open and inspect the
backlog directly from the TUI — independent of any session.

- **Open.** A key/menu entry opens a modal backlog view (over the home menu or a session,
  like the settings overlay). It lists tasks with id, status, priority, title, and a
  readiness/blocked annotation (the same data `list_backlog` projects).
- **Inspect.** Selecting a task drills into its full detail — description, acceptance
  criteria, dependencies, and work log — rendered read-only.
- **Filtering/sorting** (status, priority, ready-only) is a nice-to-have, not required for
  a first cut.
- **Read-only first.** The browser only *views*; mutation (quick-add, status changes) is
  separate work — see the capture overlay (task 0016).

This needs the backlog exposed to clients over RPC. The daemon gains read RPCs —
`ListBacklog` (summary rows) and `GetTask` (full task) — backed by `docs.Store`
(`List`/`Get`); the TUI renders the list + detail views by calling them. Because clients
are thin event/RPC consumers (§5), the same surface is reusable by the future phone client.

### 18.6 Session history browser & reopen

Every session is already durable: its event log is the source of truth on disk at
`<workspace>/.ycc/sessions/<id>/events.jsonl` (§5.1). But today a client can only see
*live, in-memory* sessions (`ListSessions` reflects the manager's map). Once the daemon
restarts or an idle session is GC'd (§ task 0009), the on-disk logs are orphaned: nothing
lists them, nothing lets the human re-read a finished session, and the promised "the
session persists and can be **resumed or re-entered**" (§4.5) is unimplemented. This
section closes that gap.

**Durable session index.** The daemon can enumerate *all* sessions for a project —
live and persisted — by scanning `.ycc/sessions/*/events.jsonl` and reducing each log to
a summary (`event.Reduce`, §5/§20.3): id, mode, status (running/idle/error), started-at
and last-activity timestamps, focused task(s), a short title (derived from the first user
prompt / kickoff), and — once usage lands (§20) — token/cost totals. Live sessions in the
manager's map take precedence over their on-disk snapshot so a running session shows live
status. A new read RPC (`ListSessionHistory`, project-scoped) returns these summary rows;
the existing `ListSessions` continues to mean "live only".

**Browser UI.** A **session browser** is a modal list+detail view, opened from the home
menu or settings overlay exactly like the backlog browser (§18.5). The list shows the
summary rows (most-recent first); selecting one drills into a **read-only transcript** —
the reduced/replayed event stream rendered with the same components the live session view
uses, so reasoning, tool calls, and results display identically. The transcript is served
by a read RPC (`GetSessionTranscript`, project + session-id scoped) that returns a session's
full event log — the live in-memory snapshot for a running session, otherwise the persisted
`events.jsonl` read from disk. From a selected session (in the list or the transcript) the
human can **Reopen** it.

**Reopen / re-enter (resume = replay).** Reopening a persisted session re-instantiates its
coordinator on the *existing* log rather than starting a fresh one: the daemon loads the
log, reconstructs the agent loop's conversation `history` from the events (model turns
with their thinking/tool-call content, tool results, and user inputs — the same data the
live loop appends, §7), restores mode and focus from the projection, and registers it in
the manager so `Subscribe`/`SendInput`/`AnswerQuestion` work again. New activity appends to
the same `events.jsonl` — one continuous log. This depends on the event log capturing
enough to rebuild model history losslessly; where it does not yet, that is a bug to fix
(the log is meant to be the whole state, §5.1). To that end `model_turn` events now carry
`thinking_blocks` — the signed/redacted reasoning blocks — so the model history rebuilds
losslessly (Anthropic verifies these signatures, §7), and reopen emits a `session_reopened`
marker into the continuous log. Two replay-fidelity details matter at a mid-Run truncation
boundary: when a turn is cut off at the output-token cap the live loop appends a sanitized
assistant stub plus an internal user "nudge" message, but that nudge is posted via
`Loop.Post` and is **not** recorded in the event log; so replay *synthesizes* the nudge
(a user message) whenever it detects a truncated coordinator turn immediately followed by
another coordinator assistant turn, keeping strict user/assistant alternation (some
backends reject two consecutive assistant turns). One known limitation remains explicit and
unsupported: multimodal tool-result content (images/PDFs) is **not** round-tripped on replay
— only the counts are recorded on `tool_result` events, so the reconstructed history carries
the text result only. Reopen is exposed as a `ResumeSession`
(a.k.a. reopen) RPC and interacts with session lifecycle/GC (§ task 0009) and
context-window management (§ task 0010), since a resumed long session may need budgeting
before its first new turn.

**Shared modal "browser" surface.** The settings overlay (§18.2), backlog browser
(§18.5), this session browser, and the cost view (§20.5, task 0029) are all the same
shape: a modal, navigable list with a drill-in detail pane, opened over the home menu or a
session and dismissed with Esc. These share one reusable TUI component (a generic
list+detail modal, `browser`/`browserRow`/`browserCard` in `internal/tui`) reused by the
backlog and session browsers, plus a small "browse" selector (ctrl+o) that routes to
backlog / sessions today and is ready to add cost (§20.5, task 0029) as a third row. The
same read RPCs back the future phone client (§5).

### 18.7 Interrupt & steer (pause / correct / resume)

A running agent should be **interruptible** so the human can grab its attention and either
*let it carry on* or *correct it before it acts further* — the same affordance a good pair
of hands gives you when you say "wait, hold on." This is distinct from a hard **Stop**
(terminate the session, task 0009): interrupt is a *graceful pause to steer*, after which
the agent keeps going on the **same** loop and conversation.

**Why it's needed.** A user watching the agent head down the wrong path needs to redirect it
*mid-flight*, not only after the wrong work is already done. There are two shapes of this:

- **Steer-by-default (deliver at the next checkpoint).** Typing a message and pressing Enter
  while a run is in flight does **not** wait for `Run` to complete. The session queues the text
  as a correction and the engine `Loop` appends it to the conversation at the **next safe
  checkpoint** (between turns / after a tool result) — so the model sees "no, wrong file"
  before its next turn without any pause/resume ceremony. The echo is honest: a mid-run
  `user_input` carries `queued: true` (rendered distinctly, e.g. "(queued)") and a
  `user_input_delivered` event marks the checkpoint where it actually entered the conversation,
  so the transcript never claims delivery before it happens.
- **Interrupt (stop and hold to steer).** When the human wants the agent to *stop and wait*
  rather than finish the current stretch of work, `Interrupt` pauses it at the next checkpoint;
  corrections then buffer and are drained only on an explicit `Resume`. Behavior while paused
  is unchanged by steer-by-default.

An idle session's input is unchanged: it is enqueued and picked up as the next prod.

**Model (graceful pause at a safe checkpoint).**
1. **Interrupt.** The client calls `Interrupt(session_id)`. The session marks a pause request
   and the engine `Loop` honors it at the next **safe checkpoint** — between turns and after
   each tool result (it does not abort a tool mid-write). The loop emits an `interrupted`
   event and the session status becomes `paused`; the loop blocks, holding its place.
2. **Choose.** While paused the human either:
   - **Resume** (`Resume(session_id)`) — continue exactly as before, *as if nothing happened*;
     or
   - **Correct** (`SendInput(session_id, text)`) — the text is appended to the loop's
     conversation as a user message and the loop resumes, so the agent's *next* turn sees the
     correction "before it moves on." Multiple messages before resuming all land in order.
3. **Resume.** The loop drains any steered-in messages, emits a `resumed` event, returns the
   status to `running`, and continues the same `Run` from where it paused.

**Engine seam.** The `Loop` gains a `Steer` hook checked at each checkpoint
(`Checkpoint(ctx) ([]string, error)`): if a pause is pending it blocks until resume (or
`ctx` cancellation, which propagates as a normal stop); otherwise, when mid-run corrections
have queued up, it drains and returns them immediately (steer-by-default) — no pause. Either
way it returns any correction messages to `Post` into history before the next turn and emits a
`user_input_delivered` event per delivered message. The session implements `Steer`,
coordinating the pause flag, a resume signal, a `running` flag, and the buffered corrections;
`SendInput`/`Resume` feed it. When not paused and nothing is queued, `Checkpoint` is a cheap
no-op, so the hot loop is unaffected.

*Optional immediacy (enhancement).* For responsiveness during a long in-flight **model turn**,
the turn may run under a child context that `Interrupt` cancels, discarding that turn's output
(wasted tokens, no history append) and dropping straight to the checkpoint. The baseline
(pause at the next checkpoint without aborting in-flight work) is acceptable for a first cut;
checkpoints between tool calls already make the common "agent is grinding through tool calls"
case feel responsive.

**TUI.** A key in the session view (e.g. `ctrl+i`, or `esc` → "Interrupt agent" in the
settings overlay) issues `Interrupt`. The paused state is shown distinctly ("⏸ paused — type a
correction and Enter to steer, or Resume to continue"); Enter on a non-empty buffer steers,
an explicit Resume action (empty buffer / a key) continues. Because state lives in the event
log (`interrupted` / `resumed` events), any subscribed client — including a future phone
client — sees and can drive the pause.

**Relation to `ask_user` and Stop.** When the agent is *blocked on a question* it is already
suspended at a clean point (§4, interaction layer); steer-interrupt targets the *running*
case. Hard termination is the separate `Stop` RPC (task 0009); naming is split so `Interrupt`
= pause-to-steer and `Stop` = terminate.

### 18.8 Snapshot rendering for debugging (dev/test aid)

Debugging TUI layout/styling is hard from text alone: tests can assert on
`stripANSI(model.View())` substrings, but neither a human nor the agent can *see* what the
screen looks like — colors, alignment, borders, wrapping. The `internal/tui/snapshot` package
rasterizes a TUI ANSI frame (the output of `model.render()` / `View()`) plus a `(cols, rows)`
size into a PNG: `snapshot.RenderANSI(ansi, cols, rows) (image.Image, error)` and
`snapshot.WritePNG(path, ansi, cols, rows) error`. It parses the frame into a cell grid with
`github.com/charmbracelet/ultraviolet` and draws a fixed monospace grid (embedded Go Mono /
Go Mono Bold via `golang.org/x/image`), honoring per-cell foreground/background colors plus
the bold, faint and reverse SGR attributes; cell alignment follows each cell's terminal width.
It is self-contained — no external terminal emulator, no network.

This is purely additive dev/test tooling; it does not change runtime behaviour. Tests
construct a `model`, size it via `tea.WindowSizeMsg`, and render it to an in-memory image for
assertions (valid dimensions, color survived to pixels). PNG files are written to disk only
when the `YCC_TUI_SNAPSHOT_DIR` env var is set, so ordinary `go test ./...` never litters the
tree; with the var set, a maintainer or the agent (via the multimodal `Read` tool, §8) can
open the PNG to visually inspect the rendered screen.

### 18.9 Transcript rendering is incremental (render caches)

The session view redraws by `rebuild()`, which concatenates every event's rendered block
into the viewport. Rendering a block is expensive (JSON re-parsing of event payloads, diff
generation, syntax highlighting, glamour markdown, lipgloss framing) and the fold logic
(`hiddenRow`: merged tool results, ask_user plumbing, echoed idles) does backward/forward
scans — so naively re-rendering every row on every keypress/event is O(N²)-ish and made
long sessions visibly slow. The invariant: **rebuild must be O(changed rows), not O(all
rows)**. Three caches enforce it, all owned by the model:

- `bodyCache` (seq → rendered body) — expanded prose/markdown bodies.
- `blockCache` (index → fully rendered block) — the whole row as concatenated by
  `rebuild()`. Rows rendered in their *selected* state are never stored, so the cursor can
  move without invalidation.
- `hiddenCache` (index → bool) — memoized `hiddenRow` fold decisions.

Invalidation is surgical: appending an event invalidates only the rows whose rendering can
depend on it (the previous visible row's └─/├─ connector and in-flight tool glyph; the
ask_user `tool_call` that folds away when its `question_asked` arrives, plus that row's
rendered neighbors; the queued `user_input` echo when its `user_input_delivered` marker
lands). Toggling a row's expansion invalidates that row. Anything that changes a global
rendering input — width, theme, auto-expand pref, picker/wizard state, swapping the event
log — clears all three via `invalidateRender()`. New code that makes an *earlier* row's
rendering depend on a *later* event must add a matching invalidation in `appendEvent`.
`BenchmarkRebuildWarm`/`Cold` (internal/tui) track the win (~ms vs ~seconds at 1500 events).

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
is exposed as a single **pm preset** (opening-prompt + first message): `onboard`, now the
**only** home-menu preset (the former `feature` / `bug` / `spec` / `backlog` presets have
been dropped — they were just ordinary `pm` work; see §9). The agent itself distinguishes
new vs. existing:

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

**Onboarding vs. ordinary pm work.** The brownfield path resembles ordinary `pm`
feature/bug intake (explore → propose) but differs in intent: it is the *first* time ycc
sees the project and it also establishes the initial spec slice + backlog conventions,
whereas ordinary `pm` work assumes those already exist. Keeping `onboard` a distinct,
prominently-surfaced preset — one of the two pm presets, alongside `spec-doctor` (§6.4) —
is what makes onboarding discoverable.

## 20. Token usage & cost accounting

ycc tracks token usage on every model turn and rolls it up into **cost breakdowns by
backlog task over time**. The design follows the spec's core principle (§5): the event
log is the source of truth, and usage/cost are **projections** over it — no separate
ledger to keep in sync.

### 20.1 Capture (per turn)

gollama's `Turn` already returns a `Usage` struct (prompt, completion, total, plus
`CacheCreationInputTokens` / `CacheReadInputTokens` and `PromptTokensDetails.CachedTokens`).
The engine currently discards it. Instead, every `model_turn` event carries a `usage`
object **and the model identity** that produced it:

```jsonc
{ "type": "model_turn",
  "actor": "reviewer:gpt",          // role is already the actor (§5.2)
  "data": {
    "text": "…", "tool_calls": 1,
    "model_name": "gpt",            // logical name (§13)
    "backend": "openai", "model_id": "gpt-5.5",
    "usage": { "input": 1820, "output": 412, "cache_read": 1536,
               "cache_write": 0, "total": 2232 }
  } }
```

So the engine `Loop` must know its **logical model name** (not only the resolved model
id) — added beside `Model`, set from the role's `AgentSpec.Name` for subagents and the
coordinator's role name. The actor already distinguishes coordinator / implementer /
reviewer:<name>, so usage is attributable per **role and per model** with no extra
plumbing. Empty/zero usage (e.g. backends that don't report it) records zeros and is
harmless.

### 20.2 Attribution to a backlog task (session → task focus)

A `work` session is essentially "do one task," but today nothing **durably** records
*which* task a session worked on (the task lives only in the kickoff prompt). To attribute
cost by task we record the linkage as an event: a new `task_focus` event
(`data: { task: "0007" }`) is emitted when the focus is established —

- carried in by the `pm → work` hand-off (`switch_to_work` already knows the target task),
  and/or
- emitted by the `work` coordinator when it picks/accepts a task (it already calls
  `update_task`→`in_progress` and `spawn_implementer(task_id=…)`).

A session may touch more than one task; attribution uses the **active focus** at the time
of each `model_turn` (turns before any focus are attributed to "unattributed"). The
projection (§20.3) folds usage into the currently-focused task. This keeps task linkage
in the log (replayable, syncable) rather than as out-of-band session metadata.

### 20.3 Aggregation & projection

The usage projection (an extension of `event.Reduce`, §5) folds a session's `model_turn`
usage into totals **by model and by focused task**. A cross-session aggregator
(`internal/usage`) scans a workspace's `.ycc/sessions/*/events.jsonl`, reduces each, and
produces a breakdown grouped by **task × model × agent × time** (e.g. per-day buckets), plus
per-session and project totals. The **agent** dimension is the event actor that spent the
tokens, collapsed to its role — `coordinator`, `implementer`, or `reviewer` (the per-model
reviewer actors `reviewer:<model>` collapse to one `reviewer` group; pair `agent` with
`model` to split them back out). This separates the cost of the coordinator's orchestration
from the implementer's work and the reviewers' passes. Because raw events are the source,
the breakdown is always recomputable and never drifts.

### 20.4 Pricing & cost

Each `[models.X]` config block (§13) gains optional **pricing** in US dollars per
million tokens, since rates differ by model and by token class:

```toml
[models.claude]
# … existing fields …
price_input        = 3.00   # $/Mtok for fresh input
price_output       = 15.00  # $/Mtok for output
price_cache_read   = 0.30   # $/Mtok for cache-read (cheaper) input
price_cache_write  = 3.75   # $/Mtok for cache-creation input
```

Cost for a turn = Σ(tokens_class × rate_class). The registry exposes pricing per logical
model; the aggregator joins usage with pricing to produce dollar costs. Models with **no
pricing configured report token counts only** (cost shown as "—"), so the feature degrades
gracefully and never invents numbers. Pricing is config, not code, so it can be updated as
vendor prices change without touching the event log.

### 20.5 Surfacing

- **Per-session, live.** The usage projection feeds the session view / `SessionIdle` so a
  running session shows accumulated tokens (and cost when priced).
- **By task, durable.** On `work` completion, a one-line **usage/cost summary** is appended
  to the task's work log (§6.2), so the cost of a task accrues in the backlog itself across
  the multiple sessions that may touch it.
- **Project rollup.** A `GetUsage` RPC + a `ycc cost` CLI view render the cross-session
  breakdown by task / model / agent / time from the aggregator (§20.3). This is the "detailed
  cost breakdown by backlog task over time" surface. In the TUI this cost view is a modal that
  shares the generic list+detail "browser" surface with the session history browser
  (§18.6) and the backlog browser (§18.5).

Relation to §10 task 0010 (context-window management): that task surfaces *context size*
to avoid window overflow; this section tracks *spend*. They share the per-turn usage signal
but answer different questions.
