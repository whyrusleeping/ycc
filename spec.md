# ycc — a docs-driven coding harness

> Status: implemented and actively developed. This document records the current design.

## 1. Vision and principles

`ycc` is a personal coding harness whose durable project state lives in committed design
documents and a structured backlog. A coordinator agent transforms that state by delegating
implementation and review work, while an append-only session log makes the process observable
and resumable.

The design follows these principles:

- The spec is maintained with the code. A contradiction between them is a defect.
- Planning, testing, documentation, and review effort are proportional to the risk they address.
  The repository policy is in `CONTRIBUTING.md`.
- Human involvement is available at any point, but unattended work does not wait forever for a
  client.
- Sessions are portable projections of an event log. Local, CLI, web, and iOS clients consume
  the same daemon API rather than owning agent state.
- Git remains the source of code history and diffs; ycc does not replace it.

## 2. Core concepts

- **Workspace** — a git repository operated on by ycc. It contains the design docs, backlog,
  plans, memory, and code.
- **Project** — a daemon registry entry mapping a stable name to a workspace path.
- **Session** — a continuous interaction with an id, a mode, and an append-only event log.
- **Mode** — a coordinator prompt, tool set, and state machine.
- **Coordinator** — the top-level agent. In delegated work it assigns mutations to an
  implementer; in direct work it edits through the worker tools itself.
- **Subagent** — an agent with its own model, prompt, tools, history, and actor-tagged event
  stream. Implementers mutate; reviewers inspect.
- **Workstream** — an isolated git worktree, branch, and work session belonging to a project.

## 3. Architecture

```
 ┌──────────────────────── workspace machine ──────────────────────────┐
 │  ycc daemon                                                         │
 │    project/session managers ─ event logs ─ projections              │
 │    mode coordinator ─ implementer/reviewer loops ─ workspace + git  │
 │    model registry ─ docs store ─ workstream integration queue       │
 │                  Connect RPC over HTTP                              │
 └──────────────────────────────▲───────────────────────────────────────┘
                                │
             ┌──────────────────┼──────────────────┐
             │                  │                  │
          TUI / CLI        embedded web       native iOS
```

The daemon owns model execution, filesystem mutation, event persistence, project state, and
workstream integration. Clients are replaceable projections: they subscribe to events and issue
commands. This boundary lets sessions continue when a client disconnects and prevents client
suspension from becoming session suspension. Client-specific rationale is retained in
`docs/design/web-client.md` and `docs/design/ios-client.md`.

Connect RPC is used because one protobuf service supports Go, Swift, browser-compatible HTTP,
unary commands, and server streaming. `proto/ycc/v1/ycc.proto` is the authoritative wire schema;
`docs/remote-api.md` documents the HTTP surface for client authors.

### 3.1 Daemon lifecycle and projects

Persistence is opt-in:

- Plain `ycc` attaches to a reachable persistent local daemon when one exists; otherwise it runs
  an in-process daemon tied to that client's lifetime. The current directory is its sole ordinary
  named project.
- `ycc --background` starts a detached persistent daemon and attaches to it.
- `ycc daemon` runs a persistent multi-project service explicitly.
- `ycc --addr <URL>` attaches to the specified daemon.

A persistent daemon stores its project name-to-path registry in its state directory. Projects may
be added, renamed, and removed without changing workspace contents. Starting a session in an
unknown workspace registers it. A request may omit the project only when exactly one project is
registered; ambiguity is an error. The exception is `GetUsage`: an omitted project requests the
all-project rollup even when several projects are registered.

Project status is computed from local workspace state without blocking on the network. Project
metadata reports whether onboarding is still needed (no substantive configured spec entry point and
no backlog tasks), allowing clients to omit the onboarding suggestion for established projects. Git
status comes from local refs; a daemon-owned poller caches fetch-dependent metadata. No upstream,
offline operation, authentication failure, and non-git directories remain non-fatal and appear as
unavailable or stale status.

## 4. Session flow

1. A client starts a session with a project, mode, optional preset/model override, prompt, and
   optional images.
2. The daemon creates the event log and coordinator, then returns the session id.
3. Clients subscribe from a persisted sequence number. Replay is followed by live events.
4. The coordinator runs model turns and tools. Tool use, user input, subagent activity, decisions,
   document updates, and commits are events.
5. A structured question suspends attended work until an answer RPC records the response.
6. Completion records an idle report. The log remains browsable and may be reopened on the same
   history.

No session depends on an attached subscriber. A disconnected client can resume from its last
persisted sequence without asking the daemon to replicate or transfer ownership of the log.

## 5. Event log

### 5.1 Storage and durability

Each session's source of truth is append-only JSONL at:

```
<workspace>/.ycc/sessions/<session-id>/events.jsonl
```

A reduced snapshot may accelerate startup, but it never replaces the log. On Unix, session-state
directories are owner-only and event logs are owner-readable/writable only because transcripts
may contain prompts, source excerpts, tool output, and credentials echoed by external programs.
Opening legacy state repairs these modes best-effort.

Durable emission is fail-stop: once appending the event log fails, the session must not continue
mutating state that can no longer be represented. Reopening replays model turns, tool calls and
results, user input, reasoning/provider state, focus, and lifecycle markers into a valid model
history, then appends to the same log. Multimodal bytes are never embedded in events. User-sent
pictures are retained as bounded, owner-only files beside the session log for authenticated client
display; events carry opaque
attachment references plus metadata. Model replay remains text-only because the retained payloads
are presentation data rather than reconstructed provider history.

A persisted session reported as running without a matching in-memory session is treated as
stopped after daemon restart. Optional GC settings can reclaim idle in-memory sessions and old
on-disk logs; retention is disabled unless configured.

### 5.2 Event contract

A durable event has a monotonically increasing per-session `seq`, timestamp, session id, actor,
type, and type-specific data. Important event families are:

- lifecycle: `session_started`, `session_idle`, `session_error`, interruption/resume, reopen;
- conversation: user input, model turns, reasoning summaries, questions and answers;
- execution: tool calls/results, jobs, subagent lifecycle, decisions, reviews, commits;
- durable project effects: document updates, task focus, workstream lifecycle, budget state.

Events from subagents use distinct actors and may interleave, but replay reconstructs each agent's
history independently. A final `session_idle` report is the canonical completion message; clients
coalesce an immediately repeated final model turn rather than displaying it twice.

### 5.3 Transient events

Transient events are live hints, not durable facts. They have `seq: 0`, are never written or
replayed, and may be dropped under backpressure. They therefore never advance a reconnect cursor.

`turn_delta` carries the full accumulated text snapshot for an in-progress model turn. Optional
append hints are valid only when the client's current UTF-8 length matches the supplied base;
otherwise the full snapshot wins. Clients keep an independent transient tail per actor so concurrent
subagent turns do not replace one another; a terminal delta or durable model turn clears only its
actor's tail. `retry` reports a live backoff. Durable completion or failure remains authoritative in
both cases.

## 6. Project documents

### 6.1 Design document set

A project has one well-known design entry point, `spec.md` by default, and may include an
existing documentation tree. Agents adopt the repository's established convention rather than
creating a parallel one. `.ycc/config.toml` may set a workspace-relative `spec_path` and
`doc_globs`; escaping paths are ignored. Design files are plain committed Markdown edited with
the ordinary file tools.

The spec states durable behavior, architecture, interfaces, and invariants. Design notes retain
rationale and rejected alternatives that would be noisy in the spec. `docs/design/doc-style.md`
defines their register. Operational instructions belong in the README or a reusable runbook, not
in the design set.

### 6.2 Backlog

The backlog stores one Markdown file per task under `backlog/`, with YAML frontmatter for id,
title, status, priority, dates, dependencies, and spec references. Active-task bodies carry a
description, acceptance criteria, an optional plan, and a work log. On accepted completion the body
is compacted in place to the original intent, acceptance criteria when present, a bounded outcome,
and commit subject. Session event logs are the source of detailed execution and usage history; git
retains the prior task body and is the reversible migration/rollback path. Backlog summaries and
dependency checks read frontmatter only, while lookup by id loads the selected body in full.

Statuses are `proposed`, `todo`, `in_progress`, `in_review`, `done`, and `blocked`. A proposed task
is captured but not accepted scope and is never ready; promotion to todo is the acceptance act.
Ready tasks are accepted active work whose dependencies are done.

Ids are daemon-allocated per project. Because independent branches can still create duplicate
ids, every store scan deterministically preserves the oldest claimant and moves later claimants
to fresh ids, renaming their files and recording the repair. Dependency references to the shared
old id remain attached to its oldest claimant because any other interpretation would be a guess.
`ycc doctor` reports repairs so they can be committed.

A bare spec reference names a section in the entry point; `path#Section` names another design
document. Work sessions record their active task focus in the event log, making usage and session
history attributable without out-of-band metadata.

### 6.3 Plans and memory

A plan under `plans/` is a repeatable procedure that automation cannot adequately replace. It has
concrete prerequisites, steps, and an observable outcome. One-off implementation plans live with
their backlog task when complexity warrants a durable plan.

`memory.md` stores bounded, categorized empirical observations about working on the project. It is
committed and available to agents, but is explicitly advisory, may be stale, and is excluded from
spec checking. Confirmed design constraints move into the spec; reusable procedures move into
plans; implied work becomes backlog tasks. `docs/design/project-memory.md` retains the rationale
for this normative/empirical split.

### 6.4 Spec drift checking

`ycc spec-check` is a daemon-free deterministic pre-pass. It checks concrete paths, package
directories, and code symbols mentioned in inline code spans across the configured design set,
while skipping fenced examples and ambiguous tokens. It excludes design files and backlog items
from the source search so a reference cannot validate itself. Confirmed stale references produce
a non-zero exit.

The `spec-doctor` pm preset combines that evidence with a section-by-section comparison against
code. It reports contradictions, significant undocumented interfaces, and register drift; it
does not demand code-level detail from a deliberately higher-level spec. Suggested edits and
backlog items still require the ordinary user-approved document workflow.

## 7. Agent engine

### 7.1 Provider boundary and history

Provider clients expose context-aware blocking and streaming turns with normalized messages,
tool calls, reasoning blocks, usage, stop reasons, and errors. The engine owns orchestration and
provider-neutral history; provider libraries remain transports. Provider request builders forward
engine limits only when the target accepts them; the Codex output-cap exception is in §13.
Cancellation reaches retry backoff, token refresh, HTTP requests, and streaming reads so hard stop
and daemon shutdown do not strand inference.

Opaque provider state needed for stateless continuation is recorded with the model turn and
replayed only to the same compatible backend. Non-compatible backends do not receive it.
Reasoning summaries may be rendered, but private or encrypted reasoning state is never presented
as transcript text.

### 7.2 Loop, repair, and failure handling

For each turn the loop calls the selected model, records the final message, dispatches tool calls,
records each result, and continues until the model yields or a control tool ends/suspends the run.
A per-run turn cap is a runaway backstop, not a normal stopping condition.

When a new work-session prompt contains exactly one existing backlog task id, the daemon executes
and seeds the routine `list_backlog` and `get_task` exchange before the first model turn. The calls
and their real results are recorded as synthetic coordinator events after the opening user input so
replay reconstructs the same history. Missing, stale, or ambiguous ids use the ordinary model-driven
selection flow.

Some providers leak XML-like parameter markup inside otherwise valid JSON tool arguments. The
engine repairs only a declared parameter that was left unset and only when the closing/parameter
pattern is unambiguous. Repair is recorded with the tool call and reported to the model.

LLM failures share one taxonomy: rate limit, overload, server, timeout, network, auth, invalid
request, context length, refusal, and unknown. Transient classes retry with bounded exponential
backoff and live retry events; explicit retry configuration overrides defaults. A failed turn
records exactly one durable `session_error` and parks with the unanswered turn intact. `Resume`
can retry a parked retryable failure without injecting dummy input.

Provider refusals are not replayed into model history because they can poison continuation. They
remain visible as model/error events, reject additional input while parked, and may be retried by
Resume or after changing the coordinator model.

### 7.3 Subagents and asynchronous jobs

A subagent is another engine loop with isolated history and an actor-tagged event stream.
Implementer and reviewer contexts may be retained for a small, localized revision, or explicitly
replaced with fresh loops when a revision is broad, the approach changed, or accumulated history is
obsolete or log-heavy. Before a retained revision or re-review, the orchestrator compares its coarse
input-context estimate with the resolved logical model's context window and safe fraction and
replaces a near-limit loop automatically. A context-length failure gets one fresh recovery only at a
safe boundary: before any implementer tool executed, or before a reviewer submission. A fresh
implementer receives the full task, current bounded diff, unresolved instructions, and latest
implementation/verification report. Fresh reviewers preserve the previously resolved slots, models,
focuses, reasoning settings, and read-only policy; they receive the current bounded diff, current
verification handoff, and unresolved blocker/major findings without old tool logs or tier
re-resolution. Subagent lifecycle events expose context mode, round, rollover reason, and old/new
advisory context estimates. Reviewer fan-out runs concurrently.

Background shell commands and subagents share session-owned job ids and the `job_output`, `wait`,
and `kill_job` controls. Final reports are delivered exactly once, either by a covering wait or
checkpoint injection. Progress reads never consume them. Jobs do not survive daemon restart;
replay closes an unfinished job with a lost-on-restart report so conversation history stays valid.

Normal chat can spawn general-purpose subagents with an explicit prompt, any configured logical
model, and an access level that defaults to read-only inspection but may explicitly allow workspace
mutation for delegated coding. Each spawn returns a stable agent id and a background job id. Once
a turn finishes, chat may send another prompt to that agent id; the same model loop, access level,
tools, and isolated history are retained, while the follow-up receives a new job id. Read-only
generic agents expose file reads and a read-only shell: supported hosts enforce workspace
non-mutation with the reviewer sandbox, and unsupported hosts visibly degrade to prompt-only
enforcement and conservatively count the agent as a mutating job. Explicitly mutating generic
agents use worker tools and always participate in single-writer scheduling. Generic agent handles
are live session state and are not reconstructed after daemon restart.

Only one mutating agent/job may operate in a worktree. Read-only work can fan out; parallel
mutation requires separate workstreams. `docs/design/async-jobs.md` explains the single-delivery
and single-writer rationale.

### 7.4 Reasoning settings

Thinking and effort resolve per logical model, with a live per-model override taking precedence
over persisted model config and then defaults. Roles that share a model share its default depth;
a reviewer slot may override depth for that review only. Unsupported settings degrade without
failing the turn and produce at most one visible warning per session/role.

Provider mappings preserve a common user contract: Anthropic supports adaptive thinking and the
full effort range; OpenAI-compatible backends map supported reasoning-effort levels; Ollama
supports only on/off; other backends may ignore the setting. Reported reasoning tokens are a
subset of output tokens, not a separate billable class.

## 8. Tools and access policy

Worker tools provide file read/write/edit, shell execution, optional Exa web search/fetch,
multimodal reads, and structured finish/block outcomes. Coordinator tools add backlog management,
planning, implementation/review delegation, revision, commit, questions, memory, and session
control. Tools are model-callable JSON-schema interfaces; control tools can suspend, resume, spawn,
or end loops.

Read access is unrestricted because shell access already is. Write and Edit are confined by
resolved filesystem paths to the workspace plus configured trusted write roots. This is an
accident guardrail, not a security boundary. Images and PDFs read through the file tool become
native model content when supported; size limits and backend degradation prevent accidental
unbounded embedding.

Reviewers receive read and shell inspection but no mutation tools. On supported Linux hosts their
shell is filesystem-write-restricted with Landlock or bubblewrap; inability to establish an
available sandbox fails closed. Hosts without either mechanism visibly degrade to prompt-only
read-only enforcement.

Questions support one or several prompts, each with optional choices and a free-text alternative.
They must contain enough context to answer without reading the transcript. Positional batch
answers and single answers are distinct wire forms but reduce to the same durable question
exchange.

Forge access through operator-installed official CLIs is intentionally outside the public tool
surface; `docs/design/forge-integration.md` records that trust-boundary decision.

## 9. Modes

- **pm** manages design docs, backlog, investigation, onboarding, and grooming. It may inspect the
  workspace but is prompt-constrained not to implement code. Presets provide opening context for
  onboarding, spec checking, and memory grooming; an optional preset-to-model binding supplies an
  independent editorial perspective without changing role defaults.
- **chat** is free-form assistance with direct worker tools and no fixed workflow.
- **work** drives one accepted task through implementation, proportional review, backlog update,
  and commit.

A prompt entered with a preset composes with the preset rather than replacing it. A pm-to-work
handoff requires explicit approval and carries the selected task and planning context. Mode
transitions are recorded; clients start sessions and otherwise project the daemon-owned state.

### 9.1 Unattended work loop

The daemon can repeatedly start fresh work sessions for ready tasks. It skips proposed, blocked,
in-review, dependency-blocked, and done tasks; no backlog progress halts the loop rather than
reselecting forever. Stop is graceful: the current task finishes, then no next task starts.

The loop survives client disconnects. Retryable provider outages enter a bounded waiting state
with escalating delays; non-retryable failures stop. Daemon restart restores an active/waiting
loop as interrupted and never silently resumes it. The daemon owns budget enforcement and the
end-of-batch digest.

## 10. Work orchestration

A work session starts with fresh coordinator context, reads the task and relevant design, chooses
an approach, and persists a plan only when complexity warrants one. Depending on
`work.implementation`, either a retained implementer subagent performs mutations (`delegate`) or
the coordinator uses worker tools itself (`direct`). The setting is fixed for a session.

Review intensity is proportional to risk. The coordinator may self-review a tiny low-risk change,
use one focused reviewer for ordinary work, or fan out independent reviewers for high-risk work.
Findings are judged against the task rather than accepted mechanically. On revision, the
coordinator chooses context retention or replacement: retained context avoids rediscovery for a
small changeset, while fresh context avoids obsolete-history cost and anchoring for broad or
approach-changing work. The model context budget additionally makes a near-limit retained subagent
roll over automatically, and a safe context-length failure gets one fresh recovery without replaying
mutation or submitting a duplicate review. Acceptance updates the task and creates one coherent task
commit.

An implementer can return a structured blocked outcome when progress requires a decision outside
its authority. The coordinator resolves ordinary implementation judgement, asks the user when
intent is needed, or marks the task blocked. Discovered adjacent work is captured as a follow-up
rather than silently expanding the active task.

## 11. Questions, unattended work, and confirmation

Attended agents ask only when input is useful and wait for the answer. Unattended sessions receive
an explicit execution context: ordinary questions are auto-answered with guidance to choose a
reversible assumption or block the task, so background work cannot wait forever.

High-impact operations use dedicated confirmation gates rather than ordinary questions. Starting
a pm-to-work handoff requires explicit acceptance. Gate/manual workstream integration requires
per-workstream acceptance; configured auto integration is pre-authorized to use only its
rebase/verify/fast-forward path. Unattended status never bypasses a required gate.

## 12. RPC protocol

`SessionService` is the sole public daemon API. Its protobuf schema groups RPCs around:

- session discovery, start/reopen, transcript/diff reads, event subscription, input, questions,
  interrupt/resume, and hard stop;
- project registration and directory discovery;
- model, role, reasoning, work-strategy, and review-tier settings;
- backlog, plans, memory, usage, allowance, budgets, notifications, and work loops;
- workstream spawn/list/preview/integrate/discard/retry.

`Subscribe` accepts `from_seq`; only durable sequences advance this cursor. The same service is
available through generated Connect clients and Connect HTTP/JSON. Bearer authentication applies
to RPCs. A non-loopback daemon bind is refused without a token; TLS is optional because private
network transport may provide encryption, but the daemon warns when its own transport is clear.
Static embedded-web assets may be public while their RPC calls remain authenticated.

Opening and in-session user input accept bounded JPEG, PNG, GIF, or WebP attachments. Bytes enter
the current model history and are retained in owner-only files beside the session log so clients
can fetch transcript thumbnails; events contain only opaque references and metadata. Invalid
opening attachments are rejected before the session/log is created.

## 13. Models, credentials, and review tiers

A TOML config maps logical model names to backend, endpoint, model id, auth, reasoning, optional
pricing, optional `context_window` and `context_safe_fraction`, and an enabled/disabled availability
flag. The context window is an input-history budget distinct from the global per-turn output cap;
the safe fraction defaults to 0.8, and known Claude/OpenAI model families have built-in windows when
none is configured. Roles select a coordinator, implementer, and reviewer models. Several logical
models may share one endpoint/credential while selecting different model ids. Config is discovered workspace-first and otherwise from the user config directory; the
active files are not merged.

API-key values resolve from the environment first and then the machine-local secrets store
managed by `ycc token`; committed config stores only the key name. Anthropic and OpenAI also
support subscription OAuth through `ycc login`. Tokens are stored machine-locally, refreshed as
needed, never included in RPC responses, and re-resolved per turn where provider refresh semantics
can invalidate an earlier access token. OpenAI subscription inference uses its compatible Codex
Responses transport rather than the platform API. The engine may enforce its configured output
cap, but this transport must not send `max_output_tokens`: the ChatGPT Codex backend rejects that
parameter. Subscription models are unpriced unless the user supplies rates.

Runtime settings changes persist to the active `ycc.toml` and affect the next model construction;
role and thinking changes may also update a live session's next turn. A model may be temporarily
disabled without deleting its connection, credential reference, pricing, reasoning, or role/tier
references. Disabled models remain visible in backend settings but are omitted from new model and
role choices, and explicit attempts to construct or select one for new inference fail clearly;
already-constructed live clients are not torn down by the toggle. Removing a model still referenced
by a role is rejected. Provider model discovery is only a listing probe; settings clients separately
offer a real model test that sends one small, bounded, potentially billed inference request using the
current unsaved model draft. Draft tests resolve credentials daemon-side, do not install or persist
the draft, and report provider authentication/request failures as diagnostics rather than confusing
them with daemon authentication. A first-run client wizard creates a usable config when no model
configuration or fallback credential exists.

### 13.1 Review tiers

Named review tiers choose either coordinator self-review or a set of reviewer slots. A slot binds
a logical model, optional label, focus prompt, and reasoning override. A focus is a lens rather
than a prohibition: every reviewer still checks the acceptance criteria and reports major defects
outside its assigned specialty.

The built-in tiers are `self-review`, `standard`, and `comprehensive`. Self-review spawns no
reviewer agent, standard uses the first configured reviewer and is the default, and comprehensive
fans out to all configured reviewers. Projects may override them or add named tiers. The legacy
names `simple`, `single-opus`, and `high-powered` remain accepted as aliases for those respective
built-ins, but effective listings and new persisted edits use canonical names. An invalid configured
tier is rejected; a stale tier name at runtime falls back visibly to the configured default, then to
available session reviewers, then to coordinator self-review. Selection and resolved reviewer/model
identities are recorded in session events rather than copied into task work logs. Tier edits persist
and apply to the next review spawn.

## 14. Persistence, remote access, and workstreams

Workspace state and session logs remain on the daemon host. Remote clients dial that daemon
directly over Connect; there is no daemon-to-daemon log replication. This preserves one writer for
each event log and makes reconnect equivalent to subscribe-from-sequence. The embedded web client
and native iOS app use the same boundary as the TUI and CLI.

Best-effort daemon notifications can report questions, idle completion, errors, blocked work, and
work-loop digests through an ntfy-compatible webhook. Delivery never blocks or fails a session.
Committed config references notification credentials through an environment variable; inline
credentials are only appropriate in a private user-global active config.

Sensitive ycc-owned state uses restrictive Unix permissions: session logs, generated user config,
secrets, and daemon logs are owner-only. Committed documents, registries that contain only
path/id/preference metadata, and normal tool output retain ordinary project modes. `ycc doctor`
reports broad modes and inline workspace notification credentials without printing secrets or
inspecting arbitrary key files.

### 14.1 Parallel workstreams

Parallel mutation uses linked git worktrees rather than branch switching in a shared tree or full
clones. Each workstream has a ycc branch, out-of-tree worktree, work session, base commit, and
daemon registry record. It is a child of its project, not a project-picker entry. The per-tree
single-writer invariant remains unchanged.

Integration is serialized per project. Gate/manual integration uses a non-mutating preview to
detect conflicts and show the integrated diff before per-workstream acceptance. Configured auto
integration is pre-authorized and does not add that acceptance step; it can only follow the
rebase/verify/fast-forward path below. The base tree is never left conflicted. Discard and
successful integration clean up the linked worktree and branch.

Automatic integration rebases inside the workstream, verifies there, and advances the base only
by fast-forward. A conflict or failed verification may start a bounded unattended
integration agent in that worktree; the daemon independently rebases and verifies again before it
moves base. Exhaustion leaves base untouched and the worktree available with a needs-attention
state. Unsupported automatic strategy/configuration degrades to the review gate rather than
performing a different history operation. Rationale is retained in
`docs/design/parallel-workstreams.md` and `docs/design/workstream-integration.md`.

## 18. Client interaction model

All clients project the same session/backlog/workstream state and may differ in layout. The TUI is
the primary local surface; the web client is a small embedded remote surface; the iOS client adds
native persistence, notifications, and phone navigation. Its backlog task detail can edit the task's
user-maintained frontmatter and Markdown body through the daemon, retaining failed drafts and
replacing its projection with the canonical saved response. Detailed command usage belongs in
`docs/cli.md`, and the TUI ownership map is in `docs/tui-components.md`.

Durable events render as a transcript with model/user turns prominent and tool, reasoning,
review, and system detail foldable. Question plumbing is coalesced into one exchange. Scrolling
away from the live edge disables follow and exposes a jump-to-latest action; new events must not
move the reader's viewport.

### 18.1 Session input

Live sessions provide multiline input and controls. Input sent during a run is queued and inserted
at the next safe checkpoint, with separate accepted and delivered events so the log does not claim
premature delivery. User image attachments are shown as fetched thumbnails when retained bytes are
available, with a metadata fallback for legacy or missing payloads; model-history replay does not
reinject the pixels.

### 18.2 Settings

Settings expose logical models, role assignment, reasoning, work implementation, review tiers,
and client-local presentation preferences. Daemon settings persist; local preferences do not.
The model editor distinguishes model-list discovery from testing the exact unsaved configuration
with a small real inference request, showing progress and an inline success/provider error while
warning that the probe may be billed. Changes whose prompt/tool shape is fixed at construction are
clearly marked as applying to the next session.

Native clients keep bearer tokens in platform credential storage rather than preferences. A 401
clears authenticated state without deleting the saved endpoint/profile. Deep links identify a
project/session and route through the same authenticated navigation path as in-app selection.

### 18.3 Structured questions

Clients render single and batched questions with option and free-text answers. A question answered
by another client resolves everywhere from the durable answer event. Stale answers surface as
ordinary non-fatal action errors.

### 18.4 Reasoning and streaming

Reasoning summaries are foldable transcript content; opaque provider reasoning state is not.
Streaming snapshots appear as stable, replaceable live tails keyed by actor; concurrent subagents
remain independently visible, and each tail disappears on that actor's durable completion or error.
A reconnect discards all stale transient presentation before replay.

### 18.5 Backlog browser

Clients can list, inspect, capture, and update backlog tasks through the daemon. Readiness and
status use the same store semantics as coordinator tools. Mutation remains explicit and validated;
clients do not maintain a second backlog representation.

### 18.6 Session history and reopen

Projects expose session history, transcripts, commit diffs, plans, memory, usage, allowance, work
loops, and workstreams through read RPCs. Reopening a session reconstructs model history and
continues its existing event log. A persisted-only transcript is finite and read-only until the
session is explicitly resumed.

### 18.7 Interrupt and steer

Interrupt requests a graceful pause at a safe checkpoint; it does not cancel a tool mid-write.
While paused, input queues as steering and Resume drains it before the next model turn. Hard Stop
terminates instead and requires destructive confirmation in interactive clients. Resume also
retries a parked retryable model failure.

## 19. Onboarding

### 19.1 Machine setup

Machine onboarding is a client-side form because it must work before a model does. When no usable
configuration or fallback credential exists, it collects one or more providers and role
assignments, performs subscription login when selected, and writes user config. Existing usable
configuration skips the wizard.

### 19.2 Project onboarding

Project onboarding is a pm preset. For a greenfield workspace it establishes purpose,
constraints, an initial design, and starter backlog. For an existing codebase it asks what the
user intends to change, inspects only that slice, records only the design needed for that work,
and creates actionable tasks. The invariant is to spec the work rather than inventory the entire
repository before ycc becomes useful.

## 20. Usage, pricing, and budgets

### 20.1 Capture

Every durable model turn records logical model identity, backend model id, actor, and normalized
disjoint token classes. Cached tokens are removed from fresh input before recording; reasoning
tokens remain a diagnostic subset of output.

### 20.2 Task attribution

Task-focus events attribute subsequent usage to the active backlog task. A session may change
focus; turns before a focus remain unattributed rather than guessed.

### 20.3 Aggregation

Cross-session/project summaries are recomputed from logs rather than kept in a separate ledger.
They can group by project, session, task, model, actor, and time.

### 20.4 Pricing

Optional per-million-token rates produce estimated cost. Explicit model rates win over built-in
rates for known API models; unknown and subscription models remain token-only unless explicitly
priced.

### 20.5 Surfaces

CLI and RPC views expose local usage/cost summaries. Session usage surfaces distinguish cumulative
spend from active context size and prominently show the latest completed coordinator turn's coarse
prompt-token estimate when the event log provides it; subagent contexts do not replace the
coordinator readout. Session history rows carry the same per-session context readout (rather than
cumulative spend) so list surfaces can show how full each session's conversation is. Provider
allowance is separate best-effort telemetry, cached and sanitized, and never blocks inference.

### 20.6 Spend guard

Optional session and work-loop token/cost caps turn telemetry into a guardrail. Enforcement occurs
at safe checkpoints, never during a filesystem mutation. A warning is emitted near a cap.
Attended sessions may explicitly continue; unattended sessions receive a wrap-up instruction and
halt at the nearest safe task state. Loop caps are checked between sessions. Unpriced models count
toward token caps but never invent dollars for cost caps.
