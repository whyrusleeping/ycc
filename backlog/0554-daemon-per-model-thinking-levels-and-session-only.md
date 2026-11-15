---
id: "0554"
title: 'Daemon: per-model thinking levels and session-only thinking overrides'
status: todo
priority: 2
created: "2026-11-15"
updated: "2026-11-15"
depends_on: []
spec_refs:
    - spec.md#7.4 Reasoning settings
---

## Description
Status: todo. This task is the daemon/proto part of the work. The web and iOS picker tasks depend on it.

## Why

User request, 2026-11-15: every model picker should also let you choose a thinking level, and the levels on offer should match that model. Today the daemon offers the same six levels (`off|low|medium|high|xhigh|max`) for every model, and providers quietly change some of them:
- OpenAI, OpenAI-compatible and Codex: `max` is clamped to `xhigh` (gollama `mapOpenAIEffort`, `internal/codex/codex.go`). `off` leaves the effort out, so the model uses its default effort. In practice `off` does not turn thinking off.
- Ollama: effort is ignored; only on/off applies.

Thinking scope is also inconsistent. `SetRoleConfig` with a session_id changes only that session. `SetThinking` with a session_id applies live **and** persists to `[models.X]` (`Session.SetThinking` → `reg.SetModelThinking`).

## User decisions (2026-11-15, this session)
1. A thinking level picked when starting a session (new session, workstream, iOS Start work) applies to that session only. The model's saved default does not change.
2. `SetThinking` on a live session becomes session-only. Saved defaults change only through Settings (empty session_id).
3. The daemon reports which levels each model supports. The list comes from the model's backend and can be overridden per model in `ycc.toml`. Clients show only those levels. When the model changes, they move to the nearest supported level.

## Work
- Config: add an optional `thinking_levels` list to `[models.X]` and validate it against the level set. Add a resolver that returns the model's override if set, otherwise the backend default. The backend lists are the implementer's call, but they must not offer a level the provider ignores or rewrites. For example, OpenAI/Codex should not offer `off` unless it really disables reasoning, nor `max`. Ollama offers on/off only. Document the mapping in spec §7.4.
- Proto: add `repeated string thinking_levels` to `ModelInfo`, and the override to `ModelConfig` so Settings can edit it. Add session-only thinking for the coordinator on `StartSessionRequest` and `SpawnWorkstreamRequest`, next to `coordinator_model`. Add an optional `thinking` to `WatchDefinition` (applies to watch runs). Update the `SetThinking` comment: with a live session_id it is session-only; with an empty session_id it persists the default. Regenerate Go, Swift and TS code.
- Session: the start-time override seeds `thinkLevels` for the coordinator model. `SetThinking` with a session stops calling `SetModelThinking`. The override survives reopen: rebuild it from the `thinking_level_changed` / start events in the log. Reject an unsupported level for a model with InvalidArgument, or clamp it in a documented way (implementer's choice; document it). Saved configs that hold an unsupported level keep working.
- `ListModels` with a session_id reports that session's resolved per-role levels, as it does now.
- Spec/docs: §7.4, §13 (config sample), §18.2, docs/remote-api.md, docs/design/watches.md.

## Acceptance criteria
- [ ] `ListModels` returns each model's supported levels: the backend default, or the `thinking_levels` override when set. The override round-trips through Upsert/GetModelConfig and `ycc.toml`.
- [ ] A session started with a coordinator thinking level runs its coordinator at that level and leaves `ycc.toml` unchanged. The same holds for `SpawnWorkstream`.
- [ ] `SetThinking` with a live session_id changes only that session. It still applies on reopen. `ycc.toml` is unchanged. With an empty session_id it persists as before.
- [ ] A watch with `thinking` set runs at that level. An empty value uses the model default.
- [ ] Tests cover the points above. `go build ./...` and the affected package tests pass. `ycc spec-check` passes.

## Work log
