---
id: "0511"
title: Protect live session state from working-tree git operations (.ycc inside the repo)
status: proposed
priority: 3
created: "2026-11-09"
updated: "2026-11-09"
depends_on: []
spec_refs: []
---

## Description
Follow-up from PR #1 (session log holes). Live session state (`events.jsonl`, attachments, the sessionview inputs) lives under `<workspace>/.ycc/sessions`, inside the project's working tree. In the incident behind PR #1, a session's own agent ran `git filter-branch` to strip `.ycc` transcripts that had been committed. That rolled the live `events.jsonl` back under a running daemon. PR #1 now makes the writer fail-stop and the reader tolerate the resulting gaps. But any checkout, reset, stash, clean or history rewrite that touches `.ycc` can still destroy or roll back session state. This happens wherever `.ycc` is tracked or not ignored, in the project's current or past history.

This is a design decision, so it needs the user's acceptance first. Options to evaluate:
- Move live session state out of the working tree (e.g. a daemon-owned state dir keyed by project), keeping or migrating the existing `.ycc/sessions` layout. This touches history, the reaper, workstream preservation, usage scanning, and the docs/prompts that point agents at `.ycc/sessions/<id>/events.jsonl`.
- Keep the layout but harden it: make sure `.ycc/` is ignored when a project is onboarded, warn when `.ycc/sessions` is tracked in git, and have agents/tools refuse git operations that would rewrite `.ycc` paths.

## Acceptance criteria (if accepted)
- A git operation in a project working tree, including restoring an old commit that tracks `.ycc/`, cannot silently roll back or delete a live session's log. It is either impossible or detected and surfaced before damage.
- Existing sessions stay readable and resumable after any migration.
- The spec records where session state lives and why.

## Acceptance criteria

## Work log
