---
id: "0146"
title: 'Design spike: git-forge integration (issues → tasks, workstreams → PRs)'
status: todo
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 14.1 Parallel workstreams (git worktrees)
    - 6.2 Backlog — structured items, markdown-rendered
---

## Description
Design spike. The harness's units map naturally onto forge primitives, and the seams already exist:

- **Issues → backlog**: import a GitHub/GitLab issue as a task (`spec_refs`/link back to the issue URL; sync status on done). Killer for teams whose intake lives in issues.
- **Workstream → PR**: a workstream is already a branch (`ycc/ws/…`) with a review-gated merge; "open a PR instead of merging to base" is a natural alternative terminal state, with the reviewers' findings + session summary (see ycc export, 0144) as the PR body.
- **Work session → PR** for non-workstream tasks: optional `commit → push → gh pr create` flow behind a Confirm gate.

Decide: shell out to `gh`/`glab` (zero deps, user's existing auth) vs. API clients; how much is agent-prompt (a plan/runbook) vs. first-class tooling. A minimal first cut might be a coordinator tool + prompt guidance using `gh` via Bash, plus `ycc task import <issue-url>`.

## Acceptance criteria
- [ ] Design doc (docs/design/forge-integration.md) covering the three flows, auth strategy, failure modes (no gh, no remote), and what stays prompt-level vs. becomes tools/RPCs.
- [ ] Follow-on implementation tasks filed from the doc.

## Work log
