---
id: "0138"
title: ycc doctor — one-shot environment/config health check with remedies
status: todo
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 19. Onboarding flows
---

## Description
When something is misconfigured today the user finds out mid-session (401s, unreachable daemon, silently-degraded reviewer sandbox). A single `ycc doctor` command that checks the whole stack and says what's wrong — and how to fix it — collapses most support/onboarding friction into one command. It's also the natural thing to tell people to run in a bug report.

## Acceptance criteria
- [ ] `ycc doctor` reports, with ✓/✗/⚠ per line: config file discovered (and which path), each configured model's key resolution (env / secrets store / MISSING), persistent daemon reachability + whether one is running, sandbox mechanism available (landlock / bwrap / none), git repo present + clean/dirty, docs entry point + backlog found, EXA_API_KEY presence (web tools degrade without it).
- [ ] Each ✗ comes with a one-line remedy ("run `ycc token set OPENAI_API_KEY`", "run `ycc daemon` or use --background", …).
- [ ] Exit code non-zero when any hard failure (unresolvable model key, malformed config) is found, so it's scriptable.
- [ ] Works daemon-free (like `ycc spec-check`); daemon checks are best-effort probes.

## Work log
