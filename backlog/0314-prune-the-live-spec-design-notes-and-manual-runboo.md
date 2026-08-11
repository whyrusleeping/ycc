---
id: "0314"
title: Prune the live spec, design notes, and manual runbooks
status: done
priority: 3
created: "2026-08-10"
updated: "2026-08-11"
depends_on:
    - "0310"
spec_refs:
    - CONTRIBUTING.md#Documentation and comments
    - docs/design/doc-style.md#Spec and design docs
    - Design document set
    - Plans and memory
---

## Description
Turn the live documentation into a concise source of durable behavior and rationale. Remove shipped chronology, stale proposal language, duplicated configuration/UI detail, obsolete candidate analysis, and feature-inventory smoke steps. Merge or delete design notes once their durable decisions are represented in the spec.

## Acceptance criteria
- The spec emphasizes architecture, public behavior, interfaces, and important invariants rather than package/symbol inventories or implementation chronology.
- Shipped milestones, obsolete open questions, and stale “proposed / what we will add” wording are removed or made current.
- Each surviving design note retains rationale not usefully captured elsewhere; duplicate material is merged or deleted.
- Manual runbooks contain only critical, repeatable checks not already covered by automation, and especially the iOS smoke plan is reduced to a practical release checklist.
- README, spec, design notes, and runbooks do not repeat the same operational guidance.
- `ycc spec-check` and relevant documentation checks pass.

## Plan

1. Audit README.md, spec.md, docs/design/*.md, and plans/*.md against the documentation roles in CONTRIBUTING.md and the accepted doc-style contract, while preserving unrelated dirty-tree changes.
2. Refocus spec.md on durable architecture, public behavior/interfaces, and invariants: remove shipped chronology, obsolete milestone/open-question material, stale proposal tense, package/symbol inventories, and duplicated low-level UI/configuration detail; retain anchors and concrete identifiers required by spec-check where they document real interfaces.
3. Prune the design-note set: delete notes that are only obsolete proposals/inventories, and substantially shorten surviving notes so they retain unique rationale, rejected-alternative reasoning, or cross-cutting decisions without repeating the spec or README.
4. Reduce manual plans to critical repeatable checks that automation cannot cover. Turn the iOS smoke plan into a practical release checklist and trim web/remote/build guidance where it duplicates README, automation, or another runbook.
5. Check cross-document links and wording for current tense and a single source of truth; update README only where needed to point readers to authoritative docs rather than restating them.
6. Run formatting/link/documentation sanity checks available in the repository, `go run ./cmd/ycc spec-check` (or the repository's equivalent), and relevant tests. Report any pre-existing dirty-tree or spec-check issue separately.

### Starting points
- CONTRIBUTING.md:31-42 defines the intended roles of README/spec/design notes/runbooks
- docs/design/doc-style.md:33-39 defines the normative register
- The working tree contains uncommitted changes from dependency/adjacent tasks; preserve them and do not reset or rewrite unrelated code/backlog files
- Current largest cleanup targets: spec.md §§15-17 and detailed UI inventories; docs/design/{ios-client,forge-integration,mcp,parallel-workstreams,web-client}.md; plans/ios-client-smoke.md

## Work log
- 2026-08-11 plan: 1. Audit README.md, spec.md, docs/design/*.md, and plans/*.md against the documentation roles in CONTRIBUTING.md and the accepted doc-style contract, while preserving unrelated dirty-tree changes. 2.
…[truncated]
- 2026-08-11 context hints: 4 recorded with plan
- 2026-08-11 context hints: CONTRIBUTING.md:31-42 defines the intended roles of README/spec/design notes/runbooks; docs/design/doc-style.md:33-39 defines the normative register; The working tree contains uncommitted changes from
…[truncated]
- 2026-08-11 preload: 4 file(s), ~26 KiB seeded into implementer context
- 2026-08-11 implementer report: Implemented task 0314’s documentation prune.  - Rewrote `spec.md` from 2,255 to ~560 lines, retaining durable architecture, public contracts, security/persistence/replay invariants, and existing sec
…[truncated]
- 2026-08-11 review tier: high-powered — reviewers: sol, claude
- 2026-08-11 review (sol): revise — The documentation is substantially leaner and the surviving design notes/runbooks are generally well focused. `go run ./cmd/ycc spec-check` and `go test ./...` both pass, and local Markdown links reso
…[truncated]
- 2026-08-11 review (claude): accept — Task 0314's documentation prune is complete and accurate. spec.md was rewritten from 2,255 to 554 lines and now states durable architecture, contracts, and invariants in current tense with no mileston
…[truncated]
- 2026-08-11 revision: Addressed all review findings while keeping the prune lean:  - Clarified spec §§11 and 14.1: gate/manual integration requires per-workstream acceptance; configured auto mode is pre-authorized only f
…[truncated]
- 2026-08-11 review (sol): accept — The revision addresses all prior findings: the spec now distinguishes gated/manual from pre-authorized automatic workstream integration, documents the all-project GetUsage exception, restores the Code
…[truncated]
- 2026-08-11 review (claude): accept — The revision cleanly resolves the prior findings: the stale "spec \u00a76.5" citation in docs/remote-api.md now points to \u00a76.3, and backlog 0314/0315 spec_refs were updated to the rewritten spec'
…[truncated]
- 2026-08-11 decision: accept — commit: Prune project documentation
