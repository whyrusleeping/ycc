---
id: "0177"
title: Doc-style contract + anti-drift grooming guidance (dialect / self-instruction drift)
status: todo
priority: 3
created: "2026-07-08"
updated: "2026-07-08"
depends_on: []
spec_refs:
    - Project memory — agent-learned, advisory
    - Spec doctor — drift & coverage checking
---

## Description
Model-authored docs drift toward the authoring model's dialect: emphasis inflation (bold IMPORTANT warnings), self-addressed imperatives ("always/never do X") that harden into pseudo-policy nobody decided, hedging boilerplate, and re-framing around the model's preferred abstractions. Correctness checks (spec-doctor, memory budget) don't catch this. Two mitigations:

1. **A short committed doc-style contract** (e.g. a section in spec §6 or a small docs/ file) stating what memory/spec entries are for and what they must not contain: terse dated bullets in memory; no self-exhortations or emphasis inflation; design facts not instructions-to-self; keep the project's existing register; prefer re-derivation from evidence over paraphrase. An explicit norm makes drift checkable by any model (or a human) instead of being a matter of taste.
2. **Fold dialect drift into the grooming prompts**: memoryGroomPresetPrompt (internal/orchestrator/prompts.go) should explicitly direct pruning of self-instructions/register drift and rewriting entries *from verified evidence* rather than paraphrasing; consider the same for spec-doctor phase 2 (flag framing drift, not just factual drift). Optionally: the groom pass may diff the doc against an older git revision to make register drift legible.

Pairs with the preset→model binding task (cross-model fresh eyes + explicit contract as the target).

## Acceptance criteria
- [ ] A concise doc-style contract exists in the docs set and is referenced by the pm/groom prompts.
- [ ] memory-groom prompt directs dedialecting: remove self-exhortations/emphasis inflation, re-derive wording from evidence, preserve project register.
- [ ] Spec notes the drift problem and the mitigation strategy (§6.5 and/or §6.4).

## Work log
