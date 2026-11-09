---
id: "0501"
title: Edit model suitability notes from the settings editors
status: in_review
priority: 3
created: "2026-11-04"
updated: "2026-11-04"
depends_on: []
spec_refs:
    - §13 Models, credentials, and review tiers
---

## Description
Operator `notes` on a logical model feed the spawn_agent model catalog (0358) but are TOML-only: ModelConfig doesn't carry them and UpsertModel preserves the stored value. Make them editable in the apps so coordinators get guidance for picking subagent models.

## Acceptance criteria
- ModelConfig gains `optional string notes`; GetModelConfig returns it, UpsertModel writes it when present and preserves the stored value when omitted (older clients).
- Web, iOS and TUI model editors show and edit a short description/notes field and send it on save.
- Catalog rendering unchanged (bounded, labeled operator note).
- Go, web and Swift protobuf regenerated; spec §13 updated; tests cover round-trip and omission preservation.

## Work log
