---
id: "0504"
title: 'First-run daemon/CLI: no $HOME project, startup guidance, clear "no model yet" state, browser login sets up a model'
status: in_review
priority: 2
created: "2026-11-09"
updated: "2026-11-09"
depends_on: []
spec_refs:
    - spec.md#3.1 Daemon lifecycle and projects
    - spec.md#13. Models, credentials, and review tiers
    - spec.md#19. Onboarding
---

## Description
A newcomer following the README runs `ycc daemon --web`, opens the browser, and should land on an obvious next step instead of a failing hardcoded model and their home directory registered as a project.

Acceptance:
- `ycc daemon` registers its starting directory as a project only when it is a git work tree other than $HOME, or when `--workspace` is given explicitly; otherwise it logs how to add projects.
- Startup output says where the web app is (with `--web`), where the access token comes from (generated token file vs YCC_TOKEN), and, when no model is usable or no projects exist, what to do next.
- A model whose credential is absent (missing API key, or OAuth model with no stored login) reports a health check that says so plainly ("not set up"), without a provider probe that surfaces as "Authentication failed (HTTP 401)".
- Completing the in-browser Anthropic subscription login while the daemon is on the built-in fallback without a credential installs an `auth = "oauth"` model for all roles and persists it (the same result as CLI `ycc login`); with any other config it still leaves model settings alone.
- `ycc doctor` remedies for a missing config/key mention `ycc login` and the web app, not only the TUI wizard.
- Curated/default model ids are current (a fresh subscription setup does not get a stale model id).

## Acceptance criteria

## Work log

- 2026-11-09: Implemented. Startup dir registered only with explicit `--workspace` or a git work tree other than $HOME (symlink-safe); startup prints web URL, token source, missing model/projects; health reports "Not set up" for absent credentials without probing; browser Anthropic login adopts the untouched keyless fallback (never a config edited in Settings or written since startup by `ycc login`); `ycc login` says to restart a running daemon; doctor remedies point at `ycc login`; curated ids now lead with claude-opus-5-5 / gpt-6.1-sol. Spec §3.1, §13, §14.2, §19.1, README, docs/cli.md updated. One reviewer (sol): three defects found and fixed (adoption clobbering a later `ycc login`, adopting an edited fallback, symlinked $HOME). Verified end to end with an isolated scratch daemon (.ycc/tmp/onboarding). Uncommitted.
