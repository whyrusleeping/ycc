---
id: "0023"
title: First-run setup wizard — configure model providers & roles (write ycc.toml)
status: done
priority: 2
created: "2026-06-26"
updated: "2026-06-27"
depends_on:
    - "0022"
spec_refs:
    - Onboarding flows
    - Backends & model registry
    - Client UI (TUI)
    - Daemon lifecycle & projects
---

## Description
## Description
Implement the **first-run setup wizard** (spec §19.1): the first time a user runs `ycc`
with no usable model configuration, guide them through configuring model provider(s) and
role assignments instead of falling back to a keyless config that 401s on the first turn.

**Trigger.** On `ycc` startup (TUI path), if `daemon.DiscoverConfig(ws) == ""` AND no
fallback env key is set (e.g. `ANTHROPIC_API_KEY` empty), run the wizard before/at the
home menu. If a config or env key exists, skip straight to the home menu.

**Where.** A client/TUI form (Bubble Tea), NOT an agent flow — it must work before any
working model exists. It writes `~/.config/ycc/ycc.toml` (via `os.UserConfigDir()`, the
2nd `DiscoverConfig` candidate) using `config.Save` (task 0022), then feeds that path into
daemon resolution (§3.1) so the first real session uses it.

**Collects** (spec §19.1):
- One or more providers: logical name, backend (anthropic|openai|ollama), base URL
  (defaulted per backend), model id, and the API key env-var name (`key_env`). Store keys
  as env references, not inline. At least one provider required.
- Role assignments: coordinator / implementer / reviewers (multi-select) from the
  configured models. With a single provider, default all three to it and allow accepting
  without choosing (mirrors `config.DefaultAnthropic`).

**Skippable** on purpose (user may hand-author `ycc.toml`); skipping proceeds with the
existing fallback + keyless-401 warning.

Wire the resolved/written config path into `resolveDaemon` (cmd/ycc/main.go) so the
in-process one-shot daemon picks it up. Coordinate with the settings overlay (§18.2) which
edits roles live — first-run setup is the bootstrap that creates the file the overlay then
mutates; re-running setup can be offered from that overlay later (not required here).

## Acceptance criteria
- [ ] On startup with no discoverable config and no fallback env key, the wizard runs;
      with a config/env key present it does not (home menu directly).
- [ ] User can add 1+ providers (name/backend/base-url/model/key_env) with per-backend
      base-URL defaults; at least one is enforced.
- [ ] User assigns coordinator/implementer/reviewers from configured models; single-provider
      case auto-defaults and is acceptable without manual selection.
- [ ] On completion a valid `ycc.toml` is written to the user config dir via `config.Save`
      and the subsequent session uses it (no keyless-401).
- [ ] The wizard is skippable, after which ycc proceeds with the prior fallback behaviour.
- [ ] Tests cover the trigger decision (when to show the wizard) and that completion yields
      a loadable config; TUI form interaction covered to the extent the existing tui tests do.

## Work log


## Acceptance criteria

## Work log
- 2026-06-27 plan: Create a self-contained first-run setup wizard in a new package `internal/setup`, invoked from cmd/ycc/main.go on the TUI path before daemon resolution.  1. New package `internal/setup` (setup.go):   
…[truncated]
- 2026-06-27 implementer report: Implemented the first-run setup wizard (spec §19.1) as a new self-contained package `internal/setup`, wired into the TUI startup path.  ## Changes  **New `internal/setup/setup.go`** (logic, pure/test
…[truncated]
- 2026-06-27 review (claude): revise — The first-run setup wizard for task 0023 is implemented correctly and completely: the new internal/setup package provides NeedsSetup (DiscoverConfig=="" && ANTHROPIC_API_KEY=="", consistent with the a
…[truncated]
- 2026-06-27 review (claude): accept — The implementer resolved the prior major scope-hygiene finding: the unrelated settings-overlay thinking-UI rework in internal/tui/tui.go is now unstaged and excluded from the commit, task 0036's statu
…[truncated]
- 2026-06-27 decision: accept — commit 701e1e4: First-run setup wizard: configure providers & roles, write ycc.toml (§19.1)  Add internal/setup: a Bubble Tea first-run wizard that, when ycc launches the TUI with no discoverable config and no fallb
…[truncated]
