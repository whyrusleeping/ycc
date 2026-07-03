---
id: "0122"
title: TUI needsOnboarding should respect the configured spec entry point
status: done
priority: 4
created: "2026-07-02"
updated: "2026-07-03"
depends_on:
    - "0121"
spec_refs:
    - Design docs — entry point + docs set
    - Per-project onboarding
---

## Description
## Description
Task 0121 made the spec entry point configurable via `<workspace>/.ycc/config.toml` (`spec_path`, default `spec.md`), but the TUI's onboarding heuristic (`needsOnboarding`/`specIsEmpty` in internal/tui/tui.go, ~line 7228) still reads the hardcoded root `spec.md`. A project with a configured non-default entry point (e.g. `docs/index.md`) and no root spec.md would spuriously be flagged as needing onboarding.

Route the check through the configured entry point (e.g. `docs.NewStore(ws).SpecPath()`), keeping the conservative on-error behavior.

## Acceptance criteria
- [ ] `specIsEmpty` checks the configured spec entry point, not a hardcoded `spec.md`
- [ ] Default behavior (no `.ycc/config.toml`) is unchanged
- [ ] Existing needsOnboarding tests pass; a test covers the configured-entry-point case

## Acceptance criteria

## Plan

1. In internal/tui/tui.go, change `specIsEmpty` to resolve the spec entry point via `docs.NewStore(workspace).SpecPath()` instead of the hardcoded `filepath.Join(workspace, "spec.md")` (add the `internal/docs` import). Keep behavior otherwise identical: missing file → empty (true), unexpected read error → not-empty (false), headings/blank-only content → empty.
2. Default behavior is unchanged because `SpecPath()` falls back to `<ws>/spec.md` when there is no `.ycc/config.toml` (or the config is invalid / escapes the workspace).
3. Tests in internal/tui/tui_test.go: keep existing needsOnboarding tests passing; add a case where `.ycc/config.toml` sets `spec_path = "docs/index.md"`, root spec.md is absent, and docs/index.md has real content → specIsEmpty must be false (and needsOnboarding false). Also a case where the configured path is missing/empty → true.
4. Run `go build ./... && go test ./internal/tui/ ./internal/docs/`.

### Starting points
- internal/tui/tui.go:7312 specIsEmpty — hardcoded spec.md read
- internal/docs — Store.SpecPath() resolves .ycc/config.toml spec_path with safe fallback (see internal/docs/spec_test.go)
- existing tests: internal/tui/tui_test.go (needsOnboarding/specIsEmpty)

## Work log
- 2026-07-03 plan: 1. In internal/tui/tui.go, change `specIsEmpty` to resolve the spec entry point via `docs.NewStore(workspace).SpecPath()` instead of the hardcoded `filepath.Join(workspace, "spec.md")` (add the `inter
…[truncated]
- 2026-07-03 context hints: 3 recorded with plan
- 2026-07-03 context hints: internal/tui/tui.go:7312 specIsEmpty — hardcoded spec.md read; internal/docs — Store.SpecPath() resolves .ycc/config.toml spec_path with safe fallback (see internal/docs/spec_test.go); existing te
…[truncated]
- 2026-07-03 implementer report: Routed the TUI's onboarding heuristic through the configured spec entry point.  Changes: - internal/tui/tui.go: `specIsEmpty` now reads `docs.NewStore(workspace).SpecPath()` instead of the hardcoded `
…[truncated]
- 2026-07-03 review tier: simple (coordinator self-review)
- 2026-07-03 decision: accept — commit: tui: route needsOnboarding/specIsEmpty through the configured spec entry point (task 0122)
- 2026-07-03 usage: 5,890 tok (in 38, out 5,852, cache_r 227,909, cache_w 17,759) · cost n/a (unpriced)
  coordinator: 3,126 tok (in 18, out 3,108, cache_r 150,016, cache_w 9,262) · cost n/a (unpriced)
  implementer: 2,764 tok (in 20, out 2,744, cache_r 77,893, cache_w 8,497) · cost n/a (unpriced)
