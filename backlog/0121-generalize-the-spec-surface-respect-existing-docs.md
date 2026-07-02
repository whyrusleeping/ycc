---
id: "0121"
title: 'Generalize the spec surface: respect existing docs layouts, allow multi-file specs behind a single entry point'
status: done
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

## Plan

Generalize the spec surface from "one root spec.md" to "a configurable entry point + docs set", preserving the invariants: committed agent-maintained design docs, one well-known entry point.

1) internal/docs — workspace docs config
- New file internal/docs/config.go (or extend spec.go): load optional per-workspace config from `<workspace>/.ycc/config.toml` with fields `spec_path` (string, relative to workspace root; default "spec.md") and `doc_globs` ([]string, slash-separated globs relative to root; default empty). Use github.com/pelletier/go-toml/v2 (already a dependency). Tolerate a missing/invalid file by falling back to defaults (never fail Store construction). Load lazily or at NewStore; cache on the Store.
- Store.SpecPath() returns the configured entry point joined to the workspace root (workspace root = filepath.Dir(s.dir)). Reject/ignore configs that escape the workspace (e.g. absolute paths or ".." after Clean) — fall back to default spec.md.
- New Store.IsDoc(absPath string) bool: true when absPath == SpecPath() or when the workspace-relative slash path matches any doc_glob. Implement a small dependency-free glob matcher supporting `*`, `?`, and `**` (matching across path separators); document its semantics. Never matches paths outside the workspace.
- ReadSpec stays (reads the entry point). PRUNE SpecSections and UpdateSpecSection (and sectionTitle if now unused) plus their tests — they are unused outside docs tests, and §6.1 no longer prescribes section-scoped editing via a tool.
- Tests (internal/docs/spec_test.go or new config_test.go): default SpecPath without config; configured spec_path honored; escaping spec_path ignored; IsDoc true for entry point + glob matches (incl. `**` across directories) and false otherwise; missing/garbage config.toml falls back cleanly.

2) internal/orchestrator/modes.go — doc_updated for the whole docs set
- In BuildMode's Workspace.OnWrite, replace `path == specPath` with `d.Docs.IsDoc(path)`; keep emitting event.DocUpdated with {"doc":"spec"} and add a "path" field with the workspace-relative path (TUI renders unknown fields harmlessly). Update the surrounding comments.
- Update createTask's spec_refs description (and the identical one in capture.go) to: bare section titles refer to the spec entry point; `path#Section` references a section of another doc in the docs set. No parsing/validation change needed — spec_refs are free-form strings end to end, so bare titles remain backward compatible by construction.

3) Prompts (internal/orchestrator/prompts.go, modes.go preset description)
- chatModeSystem: reword the "Project context lives in the docs" paragraph — the durable design docs are entered via the spec ENTRY POINT (spec.md at the workspace root by default; a project may configure a different entry point and split the spec across multiple files). Follow the project's existing docs layout; keep the entry point as an index when the spec is split.
- pmModeSystem: reword the "Maintain spec.md" bullet and the NO CODE EDITS paragraph likewise: maintain the project's design doc set behind the entry point; adopt/maintain an existing docs convention (docs/ tree, ARCHITECTURE.md, ADRs) rather than imposing a parallel spec.md; keep the entry point an index when split. Include the literal guidance "follow the project's existing docs layout; keep the entry point as an index when the spec is split" (acceptance criterion).
- onboardPresetPrompt: STEP 0 must additionally inventory existing NON-ycc docs (README with design content, docs/ tree, ARCHITECTURE.md, ADRs, CONTRIBUTING) and, when a reasonable docs layout exists, propose ADOPTING it as the spec surface (configuring/treating its natural root as the entry point, or writing a thin entry-point index that links into it) instead of authoring a parallel root spec.md. Make explicit that "no spec.md" no longer implies "no docs". Adjust the FIRST-TIME branch wording accordingly (brownfield with existing docs → adopt + extend).
- Presets() onboard description in modes.go: reword "any existing spec.md + backlog" to cover existing project docs too.
- Fix modes_test.go expectations if wording assertions break (it checks for "spec.md" etc. — spec.md remains mentioned as the default, so likely fine; verify).

4) spec.md §6.1 rewrite
- Retitle "### 6.1 spec.md" → the entry-point + docs-set model: a committed, agent-maintained design doc set with a single well-known entry point (default `spec.md` at the workspace root); the entry point may be or become an index into other docs; projects with an existing docs convention keep it (agents adopt and maintain it rather than imposing a parallel spec.md); optional `.ycc/config.toml` (`spec_path`, `doc_globs`) names the entry point and docs set; `doc_updated` fires for edits anywhere in the docs set. Drop the stale `update_spec` tool sentence. In §6.2, note spec_refs may be `path#Section` while bare titles mean the entry point.

5) Verify: go build ./... && go test ./... ; grep prompts for leftover absolutist "spec.md is the spec" phrasing.

Out of scope (deliberate): TUI needsOnboarding/specIsEmpty still checks root spec.md only — capture as a follow-on task if not trivially includable; a manual onboarding run on a docs/-layout repo is left as a prompt-text verification.

### Starting points
- internal/docs/spec.go — SpecPath/ReadSpec/SpecSections/UpdateSpecSection; workspace root = filepath.Dir(s.dir)
- internal/docs/docs.go — Store/NewStore(workspaceRoot) shape
- internal/orchestrator/modes.go:59-86 — BuildMode OnWrite hook emitting DocUpdated; createTask spec_refs description ~line 143
- internal/orchestrator/capture.go:94 — duplicate spec_refs description
- internal/orchestrator/prompts.go — chatModeSystem ~207, pmModeSystem ~214, onboardPresetPrompt ~253
- spec.md lines 194-240 — §6.1/§6.2
- github.com/pelletier/go-toml/v2 already in go.mod (see internal/config/config.go)
- internal/orchestrator/modes_test.go:86,208-230 — tests referencing spec.md wording/Write

## Work log
- 2026-07-02 plan: Generalize the spec surface from "one root spec.md" to "a configurable entry point + docs set", preserving the invariants: committed agent-maintained design docs, one well-known entry point.  1) inter
…[truncated]
- 2026-07-02 context hints: 8 recorded with plan
- 2026-07-02 context hints: internal/docs/spec.go — SpecPath/ReadSpec/SpecSections/UpdateSpecSection; workspace root = filepath.Dir(s.dir); internal/docs/docs.go — Store/NewStore(workspaceRoot) shape; Store has mu *sync.Mute
…[truncated]
- 2026-07-02 implementer report: Generalized the spec surface from "one root spec.md" to a configurable entry point + docs set, preserving the two invariants (a committed, agent-maintained design doc set; a single well-known entry po
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change generalizes the spec surface from a single root spec.md to a configurable entry point plus docs set, preserving the two invariants. All five acceptance criteria are met: onboarding prompt n
…[truncated]
- 2026-07-02 decision: accept — commit: Generalize spec surface: configurable entry point + docs set (task 0121)  - docs: optional .ycc/config.toml (spec_path, doc_globs); SpecPath honors it,   new IsDoc matches the whole docs set (dep-free
…[truncated]
