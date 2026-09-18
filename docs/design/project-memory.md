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
context, with legacy notes marked as having unverified provenance. Memory is neither approved design
nor authorization, including for destructive actions.

The spec checker excludes memory. Treating an inline path or symbol in an empirical note as a
normative claim would create false drift and encourage agents to rewrite history rather than
verify it.

## Write and grooming policy

Coordinator-level agents can append terse notes. Implementers and reviewers report useful findings
upward rather than independently mutating shared memory. The soft and hard size budgets measure only
the active notes rendered into prompts, not superseded audit records retained on disk. Crossing the
soft budget accepts the note but requests grooming. Once active memory is at the hard ceiling,
ordinary growth is refused, while a correction or consolidation that reduces active memory remains
permitted. Prompt injection also has an independent truncation bound.

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
