---
id: "0592"
title: 'Generated outputs as derived files: never claimed, rebuilt from the sources being committed'
status: proposed
priority: 2
created: "2026-11-23"
updated: "2026-11-23"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description
Current state: proposed on 2026-11-22 from `docs/reports/shared-tree-concurrency-2026-11.md`. The user asked for it to be filed as a proposal in session s_fc31ffbed8395135 (event 164, 2026-11-23 02:07 UTC); it is not yet accepted. In the same answer the user chose to keep committing `internal/web/dist`, rebuilt automatically from the candidate tree, rather than building it at release.

Generated files collide between any two concurrent tasks in the same area:
- A `proto/ycc/v1/ycc.proto` change regenerates `ycc.pb.go`, `yccv1connect/ycc.connect.go`, `clients/web/src/gen/ycc/v1/ycc_pb.ts` and the Swift files under `clients/ios/YccKit/Sources/YccProto`. 27 of 158 commits since 2026-11-01 touched the proto.
- A web change rewrites `internal/web/dist/index.html` and `manifest.json` and renames the hashed `index-*.js`/`.css` assets. 44 of the 158 commits touched `dist`.

On 2026-11-22, 0461 (work-loop preflight) found its own regenerated bindings left out of its snapshot because another session's shell had claimed them. It made 29 no-op edits to claim them back. 0582 (the project sessions view) had three review rounds with stale `TestBundleFresh` findings.

Proposed direction: project config declares generated paths, the sources they derive from, and the command that regenerates them. Generated paths are never claimed or attributed. Whatever is reviewed or committed regenerates them from its own sources. That would be the candidate tree from 0593 (candidate tree for verification, review and commit, proposed), or the worktree when no candidate is used. Regeneration in a worktree must not run `npm ci` against a symlinked `node_modules`, because that wipes the main tree's copy (project memory note on `clients/web`).

## Acceptance criteria
- `.ycc/config.toml` (or equivalent) declares generated outputs with their sources and regenerate command; this repository declares the proto bindings and `internal/web/dist`.
- Writes to declared generated paths create no claims, overlap notices or commit refusals.
- A review snapshot or commit whose changeset touches a declared source includes outputs regenerated from exactly that changeset's sources. A failed regeneration stops the commit and reports the error.
- Two sessions changing different RPCs in `ycc.proto` each commit bindings that match their own committed proto, without hand-splitting generated hunks.
- spec §7.3 and the config reference describe the behaviour.

## Work log
- 2026-11-23 groom: added the event reference for the filing request and the dist decision, and named the memory note. Not implemented: no generated-path declaration exists. Still proposed.
