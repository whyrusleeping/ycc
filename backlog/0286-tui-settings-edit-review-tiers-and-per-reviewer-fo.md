---
id: "0286"
title: 'TUI settings: edit review tiers and per-reviewer focus prompts'
status: proposed
priority: 4
created: "2026-08-07"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - 13.1 Review tiers
    - 18.2 Settings overlay
---

## Description
Review tiers (spec §13.1) are now rich config — per-reviewer model, focus prompt, and
thinking level — but are only editable by hand in `ycc.toml`. The settings overlay already
manages models and role assignments; a tier editor would let a user create a tier, add
reviewer slots, pick a model per slot, and type a focus prompt without leaving the TUI.

Would likely need `ListReviewTiers` / `UpsertReviewTier` / `RemoveReviewTier` RPCs (persisted
via `config.Save` like `UpsertModel`), and would carry over to the iOS/web clients.

## Acceptance criteria
- [ ] Browse configured tiers with their reviewer line-up and focuses
- [ ] Add/edit/remove a tier and its reviewer slots (model + label + focus + thinking)
- [ ] Changes persist to ycc.toml and take effect without a restart


## Work log
