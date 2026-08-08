# Design: Document style contract

> Status: accepted. This contract applies to model-authored project memory and design docs.

## 1. Purpose

Model-authored docs can drift toward the authoring model's dialect without becoming
factually incorrect. Common symptoms are emphasis inflation, self-addressed imperatives
that harden into undecided policy, hedging boilerplate, and reframing around the model's
preferred abstractions. An explicit contract makes that drift reviewable by another model
or a human rather than leaving it as a matter of taste.

## 2. Shared register

Preserve the project's existing register, terminology, and level of abstraction. State the
fact, decision, or observation directly. Do not add bold `IMPORTANT` / `CRITICAL` warnings,
routine hedging, motivational language, or instructions addressed to a future author.

When wording is suspect, re-derive it from verified evidence such as code, git history, or
events. Do not merely paraphrase prior model output; that reinforces its framing and can
change meaning while appearing to clean it up.

## 3. Memory entries

`memory.md` contains terse, dated empirical observations under Environment & tooling,
Codebase gotchas, User preferences, and Lessons learned. Entries describe what was
observed and enough evidence or context to verify it. They are advisory, not design policy.

Memory entries do not contain `always` / `never` self-exhortations, emphasis inflation, or
hedging boilerplate. If an observation has become a design constraint, promote it through
the deliberate spec path instead of rewriting it as an instruction to future agents.

## 4. Spec and design docs

The spec and its design docs state normative design facts, decisions, invariants, and
interfaces in the project's established register. They do not manufacture pseudo-policy
from an author's habits or recast an accepted design around a preferred abstraction.
Changes in framing that alter meaning require the same evidence and deliberation as other
design changes.

## 5. Enforcement

The `memory-groom` preset deduplicates and dedialects memory against this contract. The
`spec-doctor` comparison pass may surface framing or register drift as cleanup suggestions,
separate from factual drift. Optional cross-model preset bindings (`[roles.presets]`, spec
§9 and §13) provide fresh wording and reduce single-model reinforcement; this contract is
the stable target those passes check.
