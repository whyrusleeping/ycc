---
id: "0301"
title: 'spec-check: exclude runtime state from symbol search and verify clean-checkout parity'
status: done
priority: 3
created: "2026-08-08"
updated: "2026-09-18"
depends_on: []
spec_refs: []
---

## Description

`ycc spec-check` resolves doc-mentioned symbols by word-boundary search across the workspace. It does not exclude the untracked `.ycc/` state dir, so session event logs containing historical symbol names satisfy the search — the check passes in a live workspace but fails on a clean checkout of the same commit. Observed while committing 0177: HEAD fails on a clean tree with 4 stale spec.md symbol refs (`Think`, `list_dir`, `update_spec`, `ChooseMode`) yet passes in the dev workspace.

Two parts:
1. `internal/specdoctor`: exclude `.ycc/` (and ideally anything gitignored) from the symbol-resolution scan so results match a clean checkout.
2. Recheck the current spec after completed 0207/0314 and fix any remaining stale references so a clean-tree `ycc spec-check` passes. The four symbols above are historical evidence, not an assertion that all four remain stale or need changing.

## Acceptance criteria

- [ ] `ycc spec-check` produces the same result on the live workspace and on a `git archive` clean checkout of the same commit.
- [ ] `.ycc/` contents never satisfy a symbol reference.
- [ ] Clean-tree `ycc spec-check` passes at HEAD (stale refs fixed).

## Outcome

Excluded .ycc directories from the symbol-search corpus and added a regression test proving session logs cannot resolve historical symbols. Self-reviewed the scoped diff; go test ./internal/specdoctor ./cmd/ycc and git diff --check pass. Coordinator verified identical successful live and scoped git-archive CLI output: 67 references across 9 docs resolve. No spec changes needed; unrelated working changes preserved.

Commit: Exclude runtime state from spec-check symbol resolution
