---
id: "0022"
title: config.Save — write ycc.toml from a Config
status: in_progress
priority: 2
created: "2026-06-26"
updated: "2026-06-26"
depends_on: []
spec_refs:
    - Backends & model registry
    - Onboarding flows
---

## Description
## Description
The `config` package can `Load` a TOML config but cannot write one. The first-run
onboarding wizard (task for §19.1) needs to persist the providers + roles the user
chooses to `~/.config/ycc/ycc.toml`. Add a `Save(path string, c *Config) error` that
serialises a `*Config` to TOML (round-trips with `Load`), creating parent directories as
needed, and validating before writing so we never persist an invalid config.

This is a small, self-contained enabler that the wizard (and a future settings-overlay
"persist to config" action) builds on.

## Acceptance criteria
- [ ] `config.Save(path, *Config)` writes valid TOML that `config.Load` reads back to an
      equal `*Config` (round-trip test, including multiple models + reviewers list).
- [ ] `Save` calls `validate()` and returns an error (writing nothing) for an invalid
      config (e.g. role referencing an unknown model, empty reviewers).
- [ ] Parent directories are created (`MkdirAll`) so writing to a fresh
      `~/.config/ycc/ycc.toml` works.
- [ ] Keys are stored as `key_env` references only (never inline secret values), matching
      the loaded shape.
- [ ] Unit tests cover round-trip + the invalid-config rejection.

## Work log


## Acceptance criteria

## Work log
- 2026-06-26 plan: Add `config.Save(path string, c *Config) error` to the config package, mirroring `Load`:  1. Inspect the existing `config` package (Config struct, Load, validate, TOML tags, how keys/key_env are model
…[truncated]
- 2026-06-26 implementer report: Implemented `config.Save(path string, c *Config) error` in `internal/config/config.go`.  Changes: - Added `Save` which: returns an error for a nil config; calls `c.validate()` first and returns its er
…[truncated]
- 2026-06-26 review (claude): accept — The change adds config.Save cleanly mirroring Load: nil guard, validate() before any write, toml.Marshal, MkdirAll for parent dirs, and 0644 file write. It satisfies all acceptance criteria — round-
…[truncated]
