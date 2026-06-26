---
id: "0018"
title: Remove / raise / make-configurable the implementer turn limit
status: in_progress
priority: 2
created: "2026-06-26"
updated: "2026-06-26"
depends_on:
    - "0002"
spec_refs:
    - Agent engine
    - Backends & model registry
---

## Description
`engine.Loop` caps a run at `MaxTurns` iterations; `defaultMaxTurns = 40` (loop.go:54). When
the cap is hit the loop aborts with an error and `Result{Report: "(stopped: reached max
turns)"}` (loop.go:146) — the agent is cut off mid-task. Nothing sets `MaxTurns` anywhere, so
every agent (coordinator / implementer / reviewer / chat) shares the same 40.

The implementer is the one this bites: it does the heavy multi-step work (read → edit across
several files → build → test → fix), and a real task can easily exceed 40 tool-call turns.
Getting guillotined at 40 wastes the whole run and leaves a half-done diff.

Want: remove the limit, OR raise it a lot, OR (best) make it configurable — ideally per-role
in the model/role config, the way `max_tokens` already is.

Design notes / open questions:
- The cap exists as a runaway/cost guard (a model stuck in an infinite tool-call loop). So
  "configurable with a high default" is probably better than "remove entirely" — keep *some*
  backstop so a degenerate loop can't burn tokens forever.
- Per-role config: `config.Registry` already carries `MaxTokens`; add `MaxTurns` alongside it
  (per role: coordinator/implementer/reviewer) and plumb it engine ← session ← config, setting
  `Loop.MaxTurns` instead of relying on the default.
- Verify whether the 40 is per-`Run` or cumulative across revise rounds: `send_to_implementer`
  reuses the implementer's loop context, so confirm each revise `Run` resets the turn counter
  (loop.go starts `turn` at 1 per Run) vs. accumulating — that changes how tight 40 really is.
- Interacts with 0010 (context-window management): more turns → more accumulated context, so
  raising the cap without context handling may just trade a turn-limit abort for a context
  abort. Note the dependency.
- Behavior on hitting the cap could also degrade more gracefully (hand partial work back to the
  coordinator instead of erroring), but that's secondary to raising/configuring the limit.

## Acceptance criteria
- [ ] the implementer is no longer cut off at 40 turns on normal multi-step tasks
- [ ] decide the approach (remove vs. high default vs. configurable) with the user — recommend
      configurable, per-role, with a high default
- [ ] if configurable: a `max_turns` knob (per role, beside `max_tokens`) plumbed
      config → session → engine, with a sane high default
- [ ] a runaway backstop still exists in some form (no truly unbounded loop / cost)
- [ ] document the interaction with 0010 so raising turns doesn't silently hit context limits

## Work log
- 2026-06-26 plan: Approach (per task recommendation, proceeding on judgement): make MaxTurns configurable per-role with a high default, keeping a backstop so loops aren't unbounded.  1. Engine: raise `defaultMaxTurns` 
…[truncated]
- 2026-06-26 implementer report: Made the engine turn limit configurable per-config with a high default, keeping a runaway backstop.  **Default chosen:** 200 (raised from 40). High enough that the implementer's multi-step read→edit
…[truncated]
- 2026-06-26 review (claude): accept — Task 0018 is correctly and completely implemented. The engine turn cap is now configurable per-config via a new `max_turns` TOML knob, plumbed config → Registry.MaxTurns() → session (both orchestr
…[truncated]
- 2026-06-26 revision: Trimmed the staged changeset to ONLY the 0018 turn-limit work, removing scope leakage that had crept in from other tasks (0012/0019).  **Reverted out-of-scope changes:** - `backlog/0012-settings-overl
…[truncated]
- 2026-06-26 review (claude): accept — The revision cleanly addresses the prior scope-leakage finding: the staged changeset is now scoped to task 0018 only — backlog.md adds just the 0018 row, spec.md adds only the max_turns config knob 
…[truncated]
