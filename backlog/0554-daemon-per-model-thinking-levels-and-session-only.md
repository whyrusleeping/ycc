---
id: "0554"
title: 'Daemon: per-model thinking levels and session-only thinking overrides'
status: done
priority: 2
created: "2026-11-15"
updated: "2026-11-15"
depends_on: []
spec_refs:
    - spec.md#7.4 Reasoning settings
---

## Description

Status: todo. This task is the daemon/proto part of the work. The web and iOS picker tasks depend on it.

## Acceptance criteria

- [ ] `ListModels` returns each model's supported levels: the backend default, or the `thinking_levels` override when set. The override round-trips through Upsert/GetModelConfig and `ycc.toml`.
- [ ] A session started with a coordinator thinking level runs its coordinator at that level and leaves `ycc.toml` unchanged. The same holds for `SpawnWorkstream`.
- [ ] `SetThinking` with a live session_id changes only that session. It still applies on reopen. `ycc.toml` is unchanged. With an empty session_id it persists as before.
- [ ] A watch with `thinking` set runs at that level. An empty value uses the model default.
- [ ] Tests cover the points above. `go build ./...` and the affected package tests pass. `ycc spec-check` passes.

## Outcome

ModelInfo.thinking_levels reports each model's supported levels: the [models.X] thinking_levels override if set (ModelConfig.thinking_levels, which round-trips), otherwise the backend default. Defaults: anthropic all six; openai/codex low-xhigh; openai-compatible/glm low-high; ollama off/high. Unsupported levels are clamped to the nearest supported one, rules in spec §7.4. StartSession and SpawnWorkstream accept coordinator_thinking, which applies to that session only. SetThinking with a live session id no longer writes ycc.toml; reopen rebuilds overrides from session_started and thinking_level_changed. Watches gain an optional thinking level. Go, Swift and TS proto regenerated; web bundle rebuilt. Go tests pass apart from TestGetBacklogGroom, which already failed (0548); spec-check passes; web typecheck and vitest pass.

Commit: Per-model thinking levels and session-only thinking overrides (StartSession, SpawnWorkstream, SetThinking, watches)
