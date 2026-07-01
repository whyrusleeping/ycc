---
id: "0096"
title: 'Connection-centric model config: multi model-id entry + backend model discovery'
status: done
priority: 2
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 13. Backends & model registry
    - "18.2"
---

## Description
Make the backend manager connection-centric so a single connection (backend + base_url + key_env) can produce multiple selectable logical models (siblings), instead of forcing one logical model per entry.

Motivation: user wants to configure the anthropic connection once and then pick opus/sonnet/fable per role. Same for ollama (enter several tags) and openai (auto-populate from the /models endpoint).

Scope:
- config: `CuratedModelIDs(backend)` curated defaults + `DiscoverModels(ctx, backend, base_url, key_env)` that queries the backend's model-listing endpoint (openai/glm `/models`, anthropic `/v1/models`, ollama `/api/tags`).
- proto+server: `DiscoverModels` RPC returning available model ids (network) with curated fallback.
- TUI backend manager: the model field accepts multiple space/comma-separated ids; a "fetch available models" key populates it from the backend; submitting with >1 id creates one sibling logical model per id sharing the connection. Anthropic add form prefills opus/sonnet/fable curated ids.
- spec §13 / §18.2 updates.

Acceptance:
- Adding an anthropic connection yields opus/sonnet/fable(/haiku) selectable models in the role pickers.
- Ollama connection accepts multiple tags → multiple models.
- OpenAI connection can fetch available model ids from /models.
- Existing single-model add/edit/duplicate still works; build + tests green.

## Acceptance criteria

## Work log
- 2026-07-01 decision: accept — commit: backlog: mark 0094 + 0096 done (implemented and committed in eaa34e5, statuses were stale)
