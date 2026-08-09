---
id: "0297"
title: Review-tier editing from iOS global settings (RPCs + UI)
status: in_review
priority: 3
created: "2026-08-08"
updated: "2026-08-08"
depends_on: []
spec_refs:
    - 13.1 Review tiers
---

## Description
Expose full review-tier configuration (spec §13.1) to clients, and build the iOS global-settings UI on top.

## Daemon side
- Proto: `ListReviewTiers` (effective tiers incl. built-ins, + default tier), `UpsertReviewTier`, `RemoveReviewTier` (drops the configured entry; built-ins revert to built-in behaviour), `SetReviewDefault`.
- Registry mutators with the same validate → apply → persist-to-ycc.toml → revert-on-failure pattern as UpsertModel/SetRoleThinking; validation shared with config load.
- Manager passthroughs + server RPC handlers; Go proto regen (local buf plugins).

## iOS side
- Swift proto regen (remote BSR plugins; network).
- YccClient methods + `ReviewTiersModel` in YccKit with headless unit tests.
- GlobalSettingsView: "Review tiers" section → tier list (default picker, built-in/custom badges) → tier editor: strategy (agents/self-review), description, tier-wide prompt, reviewer slots (name/model/prompt/thinking each, same model reusable), add/remove slots.

## Acceptance
- Tiers editable and default settable from the phone; changes persist to ycc.toml and take effect on the next spawn_reviewers (tiers resolve per call via Registry.ReviewTier).
- Invalid combos (models+reviewers both set, unknown model/level/strategy, removing/defaulting inconsistently) rejected with daemon errors surfaced verbatim in the UI.
- Go tests for registry mutators + RPCs; Swift model tests headless (run on user's Mac).

## Acceptance criteria

## Work log
