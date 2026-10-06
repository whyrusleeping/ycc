---
id: "0416"
title: 'Desktop web: usage/cost dashboard and model, role, review-tier settings'
status: in_progress
priority: 3
created: "2026-10-02"
updated: "2026-10-06"
depends_on:
    - "0410"
spec_refs:
    - Usage, pricing, and budgets
    - Models, credentials, and review tiers
    - Settings
---

## Description
Seventh phase of docs/design/web-client.md. Reference iOS UsageView/UsageModel, GlobalSettingsView/GlobalSettingsModel, and ReviewTiersView/ReviewTiersModel, plus spec §13 and §20.

- Usage: GetUsage rollups (all projects or per project, by role/model/task, over time), GetSubscriptionUsage allowance, and GetBudget spend-guard state.
- Models: ListModels, UpsertModel, RemoveModel (confirmed), TestModel, and DiscoverModels. Credentials are referenced by key_env NAME only, never entered as secrets in the browser.
- Role config (SetRoleConfig), review tiers (ListReviewTiers, UpsertReviewTier, RemoveReviewTier, SetReviewDefault), and modes (ListModes).

## Acceptance criteria
- [ ] Usage totals match `ycc` CLI usage output for the same scope.
- [ ] Add, test, and remove a model and edit a review tier from the browser. No secret value is ever sent or displayed.

## Work log
