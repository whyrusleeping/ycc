---
id: "0194"
title: 'Subscription auth UX: setup-wizard login option + TUI auth picker'
status: done
priority: 3
created: "2026-07-15"
updated: "2026-07-15"
depends_on:
    - "0193"
spec_refs:
    - 13. Backends & model registry
---

## Description
Follow-up to 0193 (Anthropic subscription OAuth). Two UX gaps were deliberately left out of the first cut:

- The first-run setup wizard (`internal/setup`) only offers API-key entry; it could offer "log in with a Claude subscription" as an alternative (driving the same PKCE flow as `ycc login anthropic`).
- The TUI backend form has no auth field: editing preserves a model's `auth` value, but switching a model between api-key and oauth requires editing ycc.toml by hand. Add an auth picker (api-key | oauth, anthropic backend only) to the connection form.

Acceptance:
- [ ] wizard offers subscription login for the anthropic backend
- [ ] TUI connection form can set/clear `auth = "oauth"` with validation matching config.Model.Validate

## Acceptance criteria

## Work log
