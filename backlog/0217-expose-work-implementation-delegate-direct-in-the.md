---
id: "0217"
title: Expose work.implementation (delegate/direct) in the settings overlay
status: todo
priority: 3
created: "2026-07-16"
updated: "2026-07-16"
depends_on: []
spec_refs:
    - The `work` orchestration (in detail)
    - Client UI (TUI)
---

## Description
The `work.implementation` config setting (spec §10, `delegate` vs `direct`) is currently only
configurable via `ycc.toml` and resolves at session start. Expose it as a live, persisted
setting from the settings overlay (§18.2), matching the pattern of `SetInteractionLevel` /
`SetThinking` / `SetRoleConfig`.

Scope:
- Add a `SetWorkImplementation` RPC (proto + regen) and server handler that calls
  `config.Registry.SetWorkImplementation` (already implemented — persists to ycc.toml).
- Surface the current value via `ListModels`/settings seed so the overlay shows the real state.
- Add an overlay row (a two-choice toggle: delegate | direct).
- Mid-session application is the tricky part: changing the strategy changes the work
  coordinator's TOOLSET and system prompt, so the live coordinator loop must be rebuilt
  (unlike SetThinking which only tweaks reasoning). Either rebuild the loop at the next safe
  checkpoint, or document that the change applies to the next session only. Decide and
  implement; update spec §10 to drop the "follow-up" note once done.

## Acceptance criteria
- [ ] `SetWorkImplementation` RPC exists and persists the choice to ycc.toml.
- [ ] Settings overlay shows and can change delegate/direct.
- [ ] Mid-session semantics are implemented and documented (rebuild-now or next-session).
- [ ] spec §10 updated to reflect the shipped behaviour.

## Work log
