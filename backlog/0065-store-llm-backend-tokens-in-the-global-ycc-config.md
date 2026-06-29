---
id: "0065"
title: Store LLM backend tokens in the global ycc config dir instead of env-only
status: done
priority: 3
created: "2026-06-29"
updated: "2026-06-29"
depends_on:
    - "0041"
spec_refs: []
---

## Description
## Description

Today a model backend's credential is referenced only by `key_env` (an env-var name) — see
done task 0041 ("keys-in-env lean", no secret values written to `ycc.toml`). This means the
relevant API token must be present in the environment for every session, which is awkward for
day-to-day use.

We want to be able to **save the LLM backend token once** in the global ycc config directory
(e.g. `~/.config/ycc`) and have the daemon pick it up from there each session, falling back to
the env var when no stored token exists. Secrets should live in a dedicated, restricted-perms
store separate from the per-project `ycc.toml` (which is checked into repos), not inline in
project config.

### Likely scope
- A global secrets/credentials store under the ycc config dir (separate file, mode `0600`),
  keyed by backend/model name or `key_env`.
- Credential resolution order at `Registry.Build` time: explicit env var → stored token →
  error. Keep `key_env` working for backward compat.
- A way to set/update a token (CLI command and/or the settings-overlay "Model backends" form,
  task 0044) that writes to the global store rather than `ycc.toml`.

## Acceptance criteria
- [ ] LLM backend tokens can be persisted to the global ycc config directory and are read on
      daemon start / per session without requiring the env var to be set.
- [ ] Stored secrets live in a separate file with restrictive permissions (0600), never in the
      project `ycc.toml`.
- [ ] Credential resolution falls back gracefully: env var takes precedence (or documented
      order), then the stored token; a clear error is surfaced when neither is present.
- [ ] Existing `key_env`-based configs keep working unchanged (backward compatible).
- [ ] A user-facing way to set/update a stored token (CLI and/or settings overlay) exists.
- [ ] Unit tests cover the store read/write, file permissions, and resolution precedence.

## Notes
- Relates to done tasks 0041/0044 (model-backend management, keys-in-env) and 0022/0023
  (config.Save, first-run wizard / config discovery).

## Acceptance criteria

## Work log
- 2026-06-29 plan: Add a global, restricted-perms secrets store for LLM backend tokens, keyed by the existing `key_env` name, and wire credential resolution to fall back to it. Keep `key_env`-based env resolution workin
…[truncated]
- 2026-06-29 implementer report: Implemented a global, restricted-perms secrets store for LLM backend tokens with env→stored-token resolution precedence. Backward compatible.  Changes: - New package internal/secrets (internal/secre
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change adds a clean, machine-local secrets store (internal/secrets) writing to ~/.config/ycc/secrets.json with dir mode 0700 and file mode 0600, never touching the project ycc.toml. Credential res
…[truncated]
- 2026-06-29 decision: accept — commit: Store LLM backend tokens in a global 0600 secrets store with env→stored fallback (0065)  Add internal/secrets: a machine-local secrets.json under the user config dir (dir 0700, file 0600) keyed by k
…[truncated]
