---
id: "0121"
title: 'Generalize the spec surface: respect existing docs layouts, allow multi-file specs behind a single entry point'
status: todo
priority: 2
created: "2026-07-02"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - Document model
    - Per-project onboarding
---

## Description
## Description
The harness currently pushes agents to treat a single root-level `spec.md` as *the* design
document. That's wrong for two growing cases: (a) existing projects that already have a
reasonable docs setup (docs/ tree, ARCHITECTURE.md, ADRs) — the agent should adopt and
maintain those instead of imposing a parallel spec.md; and (b) large specs, which are better
split across multiple logically decomposed files. The durable invariants to preserve are:
a committed, agent-maintained design doc set, and a single well-known entry point for
orientation. The entry point may be (or become) an index into other docs.

Scope:
- Prompts (`internal/orchestrator/prompts.go`): reword chat/pm/onboard so spec.md is the
  *default* entry point, not the only legitimate spec surface. Onboarding STEP 0 must also
  inventory existing non-ycc docs and propose adopting them as the spec surface; "no
  spec.md" no longer implies "no docs".
- Config: optional `.ycc` setting for the spec entry point path + docs globs (default:
  `spec.md` at root). `doc_updated` in `modes.go` fires for any file in the docs set,
  not just `SpecPath()`.
- `spec_refs` in task frontmatter: allow `path#Section`; bare section titles keep meaning
  the entry point (backward compatible).
- Spec §6.1: rewrite to describe the entry-point + docs-set model and the "follow the
  project's existing docs convention" rule.
- `internal/docs/spec.go`: generalize (or prune) the section helpers accordingly —
  `SpecSections`/`UpdateSpecSection` are nearly unused today.

## Acceptance criteria
- [ ] Onboarding a repo with an existing docs/ layout adopts it (verified by prompt text and a manual run), rather than authoring a fresh root spec.md
- [ ] Spec entry point path is configurable via .ycc config, defaulting to spec.md; doc_updated fires for edits to any configured spec doc
- [ ] pm/chat prompts say "follow the project's existing docs layout; keep the entry point as an index when the spec is split"
- [ ] spec_refs accept path#Section without breaking existing bare-title refs
- [ ] Spec §6.1 updated to match

## Acceptance criteria

## Work log
