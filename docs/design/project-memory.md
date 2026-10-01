# Design: project memory

> Status: accepted and implemented.

## Context

Agents repeatedly discover useful facts that are neither product design nor work items: a flaky
command, a repository-specific trap, a maintainer preference, or an environment limitation.
Without a durable place these facts are rediscovered each session. Putting them in the spec makes
empirical and possibly stale observations look normative.

## Decision: separate intent from experience

The project uses distinct stores with distinct authority:

| Store | Meaning | Lifecycle |
|---|---|---|
| design docs | approved behavior, architecture, interfaces, invariants | deliberately maintained and drift-checked |
| backlog | accepted/proposed work and its execution history | explicit task statuses |
| plans | repeatable operational procedures | retained only while useful and correct |
| `memory.md` | typed operational notes about working in the repository | advisory, bounded, groomed |

Memory is committed Markdown so changes are reviewable and available across sessions. New entries
are model-classified as user-stated guidance, measured observation, model inference, or proposed
policy. The runtime—not the model—attaches a structurally selected candidate source event, its date,
and workspace scope. That reference is evidence to verify, not proof that the event supports the
note; the model-chosen classification is not verified authority. Categories remain Environment &
tooling, Codebase gotchas, User preferences, and Lessons learned. Agents receive only active notes as
context. Each note renders as one compact line tagged `[kind; recorded date; session#event[/actor];
id]` (the actor is omitted for the coordinator and the scope for the default workspace scope);
legacy bullets render as `[legacy-…]`. The caveats — model-chosen kinds are not verified authority,
evidence references are candidates rather than proof, legacy notes have unverified provenance — are
stated once in the prompt header instead of on every line, so a note's metadata costs less than its
content. Memory is neither approved design nor authorization, including for destructive actions.

The spec checker excludes memory. Treating an inline path or symbol in an empirical note as a
normative claim would create false drift and encourage agents to rewrite history rather than
verify it.

## Write and grooming policy

Coordinator-level agents can append terse notes with `remember` and retire notes with `forget`.
Implementers and reviewers report useful findings upward rather than independently mutating shared
memory. The soft (4 KB) and hard (16 KB) budgets measure only the active notes rendered into
prompts, not superseded or retired audit records retained on disk. Crossing the soft budget accepts
the note. A write that would leave active memory above the hard backstop is refused unless it
reduces active memory (a consolidating supersession); the refusal states the bytes to free and lists
the largest active notes by id. Retiring is always permitted because it only shrinks active memory.
The hard backstop equals the prompt-injection cap, so every accepted note is delivered.

`forget` appends an audit-only retraction record (kind `retraction`, in a trailing "Retired notes"
section) that supersedes the named ids and never renders into prompts. It exists because removing
an obsolete fact is the cheapest cleanup and should not require writing a replacement.

### Automatic grooming

The agent that trips the budget is mid-task with a full context and is badly placed to groom, and a
nudge to "run the memory-groom flow" names something only a user can start. The daemon therefore
owns grooming: when a `remember` write leaves active memory at or over the soft budget, or a session
starts in a project already over it, the daemon starts an unattended memory-groom pm session in the
project's primary tree. It uses the memory-groom preset binding, else the default coordinator —
deliberately not a cheaper model, since judging which notes still matter is the whole job. At most
one runs per project; a persisted cooldown (6 h) and a regrowth threshold (1 KB past the last
*finished* groom's result, recorded in `.ycc/memory-groom.json`; a failed groom sets no baseline, and
the threshold never applies at the hard backstop) keep a project that legitimately needs more memory
from being re-groomed on every session start. Linked workstream worktrees carry their own
`memory.md` copy and are not auto-groomed; their writers get the ordinary retire/merge advice. Writers are told grooming is handled and to
continue their task; the groom's own writes are not re-reported. The unattended groom retires,
merges, and tightens notes, and files design promotions as proposed tasks rather than editing the
spec. `memory.auto_groom = false` in `ycc.toml` disables scheduling. Clients show active size
against the budget, any running groom, and a one-tap "groom now" (the memory-groom preset started by
name). Prompt injection also has an independent truncation bound.

A correction names the contradicted entry ID. Prompt rendering excludes the superseded entry while
both records remain in the committed file as a concise audit trail; raw `memory.md` may therefore
exceed the active-memory ceiling without blocking later writes. It never contains or injects source
transcripts. Grooming deduplicates active notes through the same supersession mechanism and enforces
the document-style contract. Legacy bullets are single-line records with content-derived IDs; once
superseded, leave them immutable and record another correction rather than hand-editing them.
Confirmed information changes stores according to meaning:

- a durable design constraint moves to the design docs;
- a repeatable procedure moves to `plans/`;
- an actionable problem becomes a backlog task;
- incidental or obsolete detail is deleted.

Promotion is deliberate because repeated observation alone does not make a preference or local
limitation a product requirement.

## Rejected alternatives

- Adding observations directly to the spec conflates evidence with intent and makes drift checks
  noisy.
- An uncommitted machine-local database is invisible to review and other checkouts.
- Unlimited append-only memory eventually dominates prompts and preserves contradictions.
- Automatic promotion lets an agent manufacture policy from its own habits.
- Refusing writes at a hard ceiling and asking the writer to consolidate: observed in practice to
  stall — writers were mid-task, per-note metadata made one-for-one replacements grow memory, and
  there was no way to drop a note without writing another.
- Grooming with a cheaper model: grooming is judgement about which facts still matter, and a
  poor groom silently loses operational knowledge every agent depends on.
