---
id: "0207"
title: Refresh stale implementation-status claims across the spec
status: done
priority: 3
created: "2026-07-15"
updated: "2026-08-08"
depends_on: []
spec_refs:
    - Vision & philosophy
    - Build plan / milestones
    - Onboarding flows
---

## Description
The living spec contains semantic drift that deterministic reference checking cannot catch. Examples include the top-level `design (pre-implementation)` status, descriptions of the substantial iOS client as merely planned, and wording that says implemented wiring is still remaining or currently absent.

Run a focused spec-doctor comparison pass against the current code and recent completed backlog, updating factual implementation-status claims while preserving genuine future design and known limitations.

## Acceptance criteria
- [ ] The top-level project status accurately reflects the implemented, actively developed system.
- [ ] iOS, onboarding, structured questions, config saving, session reopen, and other affected sections distinguish shipped behavior from remaining work accurately.
- [ ] Milestone/task references are checked against current backlog status and code rather than mechanically changed.
- [ ] Contradictory duplicate statements across `spec.md` and linked design docs are reconciled.
- [ ] No aspirational design is falsely labeled implemented; remaining limitations stay explicit.
- [ ] `ycc spec-check` passes after the edits.
- [ ] The change is documentation-only unless a newly discovered code defect is filed separately.

## Plan

Docs-only spec-doctor pass fixing stale implementation-status claims, verified against code + backlog before each edit.

Verified stale claims to fix:
1. spec.md:3 — top-level "Status: **design** (pre-implementation)" → the system is implemented and actively developed (daemon, TUI, web client, iOS app, work loop, workstreams all exist); keep "living spec" framing.
2. spec.md ~707 (§8 ask_user) — "exposing it on the tool schema + answering by option is the remaining wiring" is stale: options/questions are on the tool schema (internal/orchestrator/orchestrator.go ~line 836) and wired end-to-end. Reword as shipped.
3. spec.md ~1338 (§14) — iOS app "(**planned**; …)" → shipped/actively developed: clients/ios has ~80 tracked files (App + YccKit SPM + tests), tasks 0178–0190 largely done/in_review.
4. spec.md §16 milestones — M5 and M6 lack "— done" markers though their tasks are done (0007/0130 for M5; 0011/0041/0106 etc. for M6). Mark done after confirming against backlog.
5. spec.md §18.6 ~1717–1726 — "the promised … resume … is unimplemented. This section closes that gap" → implemented: ListSessionHistory/ResumeSession/GetSessionTranscript exist (internal/session/history.go, internal/server/server.go). Rewrite the intro in present tense describing shipped behavior; keep the explicit multimodal-replay limitation.
6. spec.md §18.5 ~1715 / §18.7 ~1791 / §14 etc. — "future phone client" phrasing where the phone client now exists; adjust where factually wrong (light touch, don't churn every "future").
7. spec.md §19.1 — "This requires a new config.Save(path, *Config) (the package currently only Loads)" → config.Save exists (internal/config/config.go:672); the wizard exists (internal/setup). Reword design-future phrasing to describe shipped flow.
8. spec.md §19.2 — confirm onboard preset shipped (internal/orchestrator/prompts.go onboardPresetPrompt) and reword any "will/planned" phrasing.
9. docs/design/ios-client.md:3 — "Status: accepted (planned; …)" → implemented / actively developed.
10. docs/design/web-client.md:3 — "proposal … No code lands with this doc" → shipped (tasks 0151–0153; internal/web, daemon --web flag).
11. docs/design/parallel-workstreams.md:3 — "proposal … No code lands" → implemented (spec §14.1 adopted; internal/workstream).
12. docs/design/async-jobs.md:3 — "pre-implementation" → verify background jobs (background spawn_implementer/spawn_reviewers, Bash run_in_background, wait/job_output) are implemented in internal/orchestrator; update status accordingly.
13. docs/design/forge-integration.md — probe helper (0155) landed but import/publish (0156+) remain proposed; adjust status line to reflect the partial landing while keeping the rest future.
14. docs/design/mcp.md — verify nothing landed; if so leave status as proposal.

Preserve genuine future design: workstream-integration.md stays "partially implemented" (0252 done, 0254 todo — verify wording), forge import/publish stays future, remaining limitations stay explicit. No aspirational feature gets labeled implemented.

CRITICAL working-tree constraint: the tree holds UNCOMMITTED work from other in_review tasks in spec.md, docs/design/ios-client.md, docs/remote-api.md and many backlog/ files. Do not revert or rework those pending hunks (spec.md hunks near lines 121, 421–425, 914–1013, 1274–1310, 2126–2160; ios-client.md hunks near lines 163–175, 244–250, 386–400). Keep 0207's edits outside those regions where at all possible so the coordinator can selectively commit. Touch no code, no backlog files.

Verification: `go run ./cmd/ycc spec-check` passes after the edits.

Commit (coordinator): selective commit per the 0289 lesson — replay 0207's edits onto `git show HEAD:<file>` blobs for the shared dirty files, stage via hash-object/update-index, stage clean files normally, verify a `git archive` overlay tree passes spec-check, commit without `git add -A`.

### Starting points
- spec.md:3 (status line), ~707 (ask_user wiring), ~1338 (iOS planned), §16 milestones ~1468, §18.6 ~1717, §19 ~1954
- internal/orchestrator/orchestrator.go:836 (ask_user questions schema)
- internal/config/config.go:672 func Save
- internal/session/history.go (ListSessionHistory/Resume)
- internal/setup/wizard.go (first-run wizard)
- docs/design/*.md status lines (line 3 of each)
- backlog statuses: 0007/0130 done (M5), 0011/0041/0106 done (M6), 0151-0153 done (web), 0155 done / 0156+ proposed (forge), 0252 done / 0254 todo (integration)

## Work log
- 2026-08-08 plan: Docs-only spec-doctor pass fixing stale implementation-status claims, verified against code + backlog before each edit.  Verified stale claims to fix: 1. spec.md:3 — top-level "Status: **design** (p
…[truncated]
- 2026-08-08 context hints: 7 recorded with plan
- 2026-08-08 context hints: internal/orchestrator/orchestrator.go:836 — ask_user questions/options schema (proof options are wired); internal/config/config.go:672 — func Save; internal/session/history.go + internal/server/se
…[truncated]
- 2026-08-08 preload: 6 file(s), ~16 KiB seeded into implementer context
- 2026-08-08 implementer report: Updated implementation-status documentation only across `spec.md` and six design docs. The spec now identifies ycc as implemented/actively developed; records structured questions, iOS, M5/M6, backlog/
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): revise — The main spec status updates are factual and `go run ./cmd/ycc spec-check` passes, but the linked design-doc reconciliation is incomplete. In particular, the iOS and parallel-workstreams docs still co
…[truncated]
- 2026-08-08 revision: Fixed the two internally stale design-doc sections. In `docs/design/ios-client.md`, the smoke runbook is now documented as shipped, the shared daemon-side work loop and its `StartWorkLoop`/`GetWorkLoo
…[truncated]
- 2026-08-08 review (sol): accept — The revision resolves the prior contradictions. The iOS design now accurately describes the shipped daemon-side work loop, persisted/interrupted restart behavior, iOS validation status, and existing s
…[truncated]
- 2026-08-08 decision: accept — selective commit (0207 doc edits only; other tasks' uncommitted in_review work left in tree)
- 2026-08-08 usage: 7,224,487 tok (in 1,896,480, out 51,847, cache_r 7,828,094, cache_w 131,201) · cost n/a (unpriced)
  implementer: 5,655,514 tok (in 1,136,863, out 18,427, cache_r 4,500,224, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 1,543,211 tok (in 759,535, out 7,740, cache_r 775,936, cache_w 0) · cost n/a (unpriced)
  coordinator: 25,762 tok (in 82, out 25,680, cache_r 2,551,934, cache_w 131,201) · cost n/a (unpriced)
