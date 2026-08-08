---
id: "0301"
title: 'spec-check: exclude .ycc/ (and untracked state) from symbol search; fix 4 stale spec.md symbols'
status: proposed
priority: 3
created: "2026-08-08"
updated: "2026-08-08"
depends_on: []
spec_refs: []
---

## Description
`ycc spec-check` resolves doc-mentioned symbols by word-boundary search across the workspace. It does not exclude the untracked `.ycc/` state dir, so session event logs containing historical symbol names satisfy the search — the check passes in a live workspace but fails on a clean checkout of the same commit. Observed while committing 0177: HEAD fails on a clean tree with 4 stale spec.md symbol refs (`Think`, `list_dir`, `update_spec`, `ChooseMode`) yet passes in the dev workspace.

Two parts:
1. `internal/specdoctor`: exclude `.ycc/` (and ideally anything gitignored) from the symbol-resolution scan so results match a clean checkout.
2. Fix or reword the 4 stale spec.md references so a clean-tree `ycc spec-check` passes (overlaps task 0207's stale-claims refresh — coordinate/dedupe).

## Acceptance criteria
- [ ] `ycc spec-check` produces the same result on the live workspace and on a `git archive` clean checkout of the same commit.
- [ ] `.ycc/` contents never satisfy a symbol reference.
- [ ] Clean-tree `ycc spec-check` passes at HEAD (stale refs fixed).

## Work log
