---
id: "0122"
title: TUI needsOnboarding should respect the configured spec entry point
status: todo
priority: 4
created: "2026-07-02"
updated: "2026-07-02"
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

## Work log
