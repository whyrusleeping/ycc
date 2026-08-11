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
| `memory.md` | empirical observations about working in the repository | advisory, bounded, groomed |

Memory is committed Markdown so changes are reviewable and available across sessions. Its header
states that entries may be stale and must be verified. Categories are Environment & tooling,
Codebase gotchas, User preferences, and Lessons learned. Agents receive the contents as context,
not instructions.

The spec checker excludes memory. Treating an inline path or symbol in an empirical note as a
normative claim would create false drift and encourage agents to rewrite history rather than
verify it.

## Write and grooming policy

Coordinator-level agents can append terse observations. Implementers and reviewers report useful
findings upward rather than independently mutating shared memory. A soft size budget accepts a
boundary-crossing note but requests grooming; a hard ceiling refuses further append until entries
are consolidated. This avoids losing a just-discovered fact while keeping whole-file prompt
injection bounded.

Grooming deduplicates, removes invalid observations, and enforces the document-style contract.
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
