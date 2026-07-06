---
id: "0136"
title: 'First-run wizard: capture, store & verify API keys (close the post-wizard 401 pit)'
status: todo
priority: 2
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 19.1 First-run setup (global — model providers & roles)
---

## Description
The first-run wizard (internal/setup) collects only a `key_env` **name** — never the key value. A brand-new user finishes the wizard, starts their first session, and hits a 401, because nothing told them to `export ANTHROPIC_API_KEY` or run `ycc token set`. The machine-local secrets store (`internal/secrets`) already exists; the wizard just doesn't use it. This is the single biggest onboarding pit.

Also: `setup.NeedsSetup` checks only `os.Getenv("ANTHROPIC_API_KEY")`, not the secrets store, and the wizard's model-id defaults are stale/hand-rolled (`gpt-4o`) instead of reusing the curated defaults + `DiscoverModels` seeding the TUI backend manager already has (spec §18.2/§13).

## Acceptance criteria
- [ ] Per provider, the wizard offers an optional "paste the API key now" field (masked input); a pasted key is stored via `secrets.Set(key_env, value)`, never written into `ycc.toml`.
- [ ] A "verify" step tests the connection per provider (e.g. `DiscoverModels` or a minimal completion) and shows pass/fail inline before finishing; failures can be edited and retried, or accepted anyway.
- [ ] Model-id defaults are seeded from the same curated per-backend defaults the TUI backend manager uses, with discovery (ctrl+f equivalent) available once a key is entered.
- [ ] `NeedsSetup` treats a key present in the secrets store like an env key (no spurious wizard re-run).
- [ ] Skipping the wizard prints concrete next steps ("set ANTHROPIC_API_KEY, or run `ycc token set ANTHROPIC_API_KEY`, or re-run setup from esc → settings") instead of proceeding silently toward a keyless 401.

## Work log
