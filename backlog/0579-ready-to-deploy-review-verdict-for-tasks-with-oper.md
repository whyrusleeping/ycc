---
id: "0579"
title: Ready-to-deploy review verdict for tasks with operational acceptance criteria
status: done
priority: 2
created: "2026-11-22"
updated: "2026-11-22"
depends_on: []
spec_refs: []
---

## Description

Found by 0576 (review-cycle investigation, `docs/reports/review-cycles-2026-11.md` cause 5, from the session logs of Wheeler, another project run with ycc).

Tasks with operational acceptance criteria (deploy, run a load, measure, report) need their code reviewed before deployment, but a review can only accept or revise. Reviewers revise every round while the operational evidence is missing, even when they find no code defect. Wheeler 0175 (fast load of archived history) went through six rounds within a day, all revised, and never committed; in rounds 4 and 6 neither reviewer reported a major code defect, and only operational acceptance criteria remained (the load and its counts, the operations procedure, measurements, live-lag evidence). A keyword pass found unfinished-criteria or missing-evidence findings in 34 of Wheeler's 185 revise rounds (18%). Some reviewers improvised a staged acceptance ("accepted for the pre-rollout stage"), others did not.

## Acceptance criteria

- A reviewer can accept a snapshot as ready to deploy or run while naming the acceptance criteria that still need operational evidence, as an outcome distinct from accept and revise.
- After such a verdict the coordinator is told it may deploy or run that snapshot, and that the remaining criteria need evidence and a further review before commit.
- A later review checks the new evidence against the named criteria. It skips re-reviewing the code only when the delta since the ready-to-deploy snapshot is evidence alone (reports, work logs, documentation of results); any change to source, configuration, deployment files, migrations, or run scripts is reviewed as usual.
- The coordinator prompt adds, in substance: "Before requesting review, verify the criteria you can check before deployment or a run. Name the rest in the handoff."

## Outcome

submit_review gains ready_to_deploy with required pending_criteria. A round counts as ready only when every review came through submit_review for one assigned snapshot, none revised, and at least one is ready. The coordinator is then told it may deploy or run that snapshot, sees the named criteria, and must gather evidence and re_review before commit. Later reviews of the task (spawn, retained or fresh re_review, context recovery) get the criteria plus the paths changed since that snapshot, submodule commits included. The code-review shortcut is offered only for document, log, CSV, or backlog/memory changes that no other snapshot file names, and the reviewer must confirm them. Plain-text execution claims are shown as unverified. The coordinator and reviewer prompts and spec §7.3 are updated. Tests are in readytodeploy_test.go; go test ./... passes.

Commit: Ready-to-deploy review verdict for tasks with operational acceptance criteria (0579)
