---
id: "0155"
title: Forge probe helper + ycc doctor check (gh/glab availability & auth)
status: todo
priority: 4
created: "2026-07-06"
updated: "2026-07-08"
depends_on: []
spec_refs:
    - docs/design/forge-integration.md#4. Auth strategy
---

## Description
From docs/design/forge-integration.md §3/§4 (design spike 0146). Foundation for all forge work.

Add a small `internal/forge` helper that:
- detects `gh`/`glab` availability + auth (`gh --version`, `gh auth status`; `glab` equivalents),
- infers forge + host from an issue URL or a git remote URL (github.com → gh, gitlab.com/self-hosted GitLab → glab; unrecognised host → clean "not a supported forge" error).

Wire a **non-fatal** forge check into `ycc doctor` (cmd/ycc/doctor.go): ✓ installed+authenticated / ⚠ installed but not authenticated (suggest `gh auth login`) / ⚠ not installed (forge features unavailable). Must be a warn — absence of forge CLIs must not affect doctor's exit code.

## Acceptance criteria
- [ ] `internal/forge` probe: installed/auth status + URL/remote → forge/host inference, unit-tested (exec stubbed).
- [ ] `ycc doctor` shows the forge check as a warn-only line; exit code unaffected when gh/glab absent.
- [ ] Errors are specific and actionable (name the missing CLI / the auth command).

## Work log
