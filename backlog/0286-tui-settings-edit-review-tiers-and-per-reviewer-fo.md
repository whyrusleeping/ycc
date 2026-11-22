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
Current state (groom, 2026-11-22): the TUI was retired on 2026-11-10 (`6ce9e51`, 0481), so a TUI editor can no longer be built. The outcome exists in the other clients: `ListReviewTiers` / `UpsertReviewTier` / `RemoveReviewTier` RPCs, the web tier editor (`clients/web/src/features/settings/TierEditor.tsx`, from `257af00`, 0416) and the iOS `ReviewTiersView` (`b38f56d`). Each edits slots with model, label, focus prompt and thinking. Closing it needs the user, because it is proposed; see 0550 (backlog groom decisions).

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
- 2026-11-22 groom: added a current state: the TUI is retired and web and iOS already edit review tiers through the RPCs the task proposed. Flagged in 0550 for closing.
