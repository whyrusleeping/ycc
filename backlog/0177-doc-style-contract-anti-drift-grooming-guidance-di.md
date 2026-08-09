---
id: "0177"
title: Doc-style contract + anti-drift grooming guidance (dialect / self-instruction drift)
status: done
priority: 3
created: "2026-07-08"
updated: "2026-08-08"
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

## Plan

Goal: make model-authored doc drift (dialect drift: emphasis inflation, self-addressed imperatives hardening into pseudo-policy, hedging boilerplate, re-framing into the author model's preferred abstractions) an explicit, checkable norm, and wire the grooming/doctor prompts to counter it.

1. New committed doc `docs/design/doc-style.md` — the concise doc-style contract (~1 page):
   - Purpose: an explicit norm makes drift checkable by any model or human instead of a matter of taste.
   - Name the failure mode (dialect / self-instruction drift) with the concrete symptoms above.
   - Rules per doc kind:
     * memory.md: terse dated bullets under the four categories; empirical observations, not instructions-to-self; no "always/never" self-exhortations; no emphasis inflation (bold IMPORTANT/CRITICAL warnings); no hedging boilerplate.
     * spec/docs set: normative design facts and decisions in the project's existing register; no pseudo-policy nobody decided; re-derive wording from verified evidence (code, git history, events) rather than paraphrasing prior model output.
   - State that grooming flows (memory-groom, spec-doctor phase 2) enforce this contract, and that cross-model preset bindings (spec §9/§13 [roles.presets]) exist precisely to break the single-model reinforcement loop.

2. Prompt updates (internal/orchestrator/prompts.go):
   - memoryGroomPresetPrompt: add a DEDIALECT step (between PRUNE and PROMOTE, or fold into the rewrite step): remove self-exhortations and emphasis inflation, rewrite entries from verified evidence rather than paraphrase, preserve the project's register; reference docs/design/doc-style.md as the contract to check against (Read it if present).
   - pmModeSystem MEMORY paragraph: one sentence referencing the doc-style contract (docs/design/doc-style.md) as the norm for memory/spec entries.
   - specDoctorPresetPrompt phase 2: add a brief third-class note — flag FRAMING/REGISTER drift (self-instructions, emphasis inflation, abstraction re-framing that changed meaning) as candidates for cleanup, distinct from factual drift, keeping the false-positive discipline (style flags are suggestions, not confirmed drift).

3. Spec updates (spec.md):
   - §6.5: add a bullet on doc-style / dialect drift: the failure mode, the committed contract at docs/design/doc-style.md, grooming as the enforcement point, cross-model presets as the structural mitigation (cross-ref §9).
   - §6.4: one or two sentences noting phase 2 may also surface framing/register drift per the contract.

4. Verify: go build ./... && go vet ./internal/orchestrator; go test ./internal/orchestrator (prompt strings are consts, so mostly compile-level); check spec-check still passes if it scans the touched sections (`go run ./cmd/ycc spec-check`).

Keep the contract doc itself an exemplar of the rules: terse, no bold warnings, project register.

### Starting points
- internal/orchestrator/prompts.go — memoryGroomPresetPrompt (~line 531), specDoctorPresetPrompt (~line 494), pmModeSystem MEMORY paragraph (~line 411)
- spec.md §6.4 (~line 365) and §6.5 (~line 400); §9 cross-model preset rationale ~lines 759-767
- docs/design/project-memory.md — sibling design doc, follow its register
- Note: this workspace has no .ycc/config.toml, so docs/design/* is not in the spec-check docs set (task 0206 covers that separately) — reference the doc from spec.md like project-memory.md is

## Work log
- 2026-08-08 plan: Goal: make model-authored doc drift (dialect drift: emphasis inflation, self-addressed imperatives hardening into pseudo-policy, hedging boilerplate, re-framing into the author model's preferred abstr
…[truncated]
- 2026-08-08 context hints: 4 recorded with plan
- 2026-08-08 context hints: internal/orchestrator/prompts.go: memoryGroomPresetPrompt ~line 531, specDoctorPresetPrompt ~line 494, pmModeSystem MEMORY paragraph ~line 411; spec.md §6.4 ~line 365, §6.5 ~line 400, §9 cross-mode
…[truncated]
- 2026-08-08 preload: 4 file(s), ~26 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0177.  Changed: - Added `docs/design/doc-style.md`, defining the concise dialect/self-instruction drift contract for memory and design docs, evidence-first rewriting, project-register
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — The change fully satisfies task 0177. It adds a concise, evidence-first document-style contract; references it from both PM and grooming prompts; explicitly directs memory grooming to remove self-exho
…[truncated]
- 2026-08-08 usage: 852,303 tok (in 395,171, out 19,628, cache_r 1,180,454, cache_w 31,712) · cost n/a (unpriced)
  implementer: 729,294 tok (in 311,381, out 6,521, cache_r 411,392, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 111,299 tok (in 83,762, out 1,425, cache_r 26,112, cache_w 0) · cost n/a (unpriced)
  coordinator: 11,710 tok (in 28, out 11,682, cache_r 742,950, cache_w 31,712) · cost n/a (unpriced)
