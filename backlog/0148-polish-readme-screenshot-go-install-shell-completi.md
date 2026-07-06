---
id: "0148"
title: 'Polish: README screenshot + go install, shell completions, cli.md audit'
status: todo
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - docs/cli.md#Commands
---

## Description
Small first-impression polish, bundled:

- **README**: the repo contains a TUI screenshot that the README never shows — a docs-driven agent harness with a rich TUI should lead with a picture. Capture a *current* screenshot (the snapshot package can even render one deterministically) and reference it; add a `go install` line if the module path supports it.
- **Shell completions**: urfave/cli ships completion support; expose `ycc completion bash|zsh|fish` and document it. Session ids and project names are the high-value completions (`ycc attach <tab>`).
- **cli.md drift**: docs/cli.md's command table should be double-checked against the actual command tree once the above lands (spec-check covers doc paths, not flags).

## Acceptance criteria
- [ ] README shows a current TUI screenshot near the top; stale root-level screenshot removed or replaced.
- [ ] `ycc completion <shell>` emits a working completion script; documented in README + cli.md.
- [ ] `ycc attach` / `--project` complete against live sessions/projects when a daemon is reachable (best-effort).

## Work log
