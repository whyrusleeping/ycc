---
id: "0528"
title: Extend the doc-style contract to backlog tasks
status: done
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - docs/design/doc-style.md
    - Project memory — agent-learned, advisory
---

## Description

## Description
## Why
A November 2026 review covered about 2,700 agent-written task files across ycc, valstore, wheeler, oldgrowth, ipfs-nu, lamia, sage and ychat-dev. It found little invented jargon. It did find patterns that make tasks hard for a fresh reader to use:
- **Inherited authority.** 71 wheeler tasks (created Oct 17–20) open with "Necessary … slice of accepted 0068", but 0068 no longer exists. valstore's September tasks repeat authorization disclaimers ("not an external-release authorization").
- **Unsourced user claims.** ycc 0521 states "**Owner direction:** chat is the interactive default" with no date or session reference.
- **Insider references.** Bare task ids and run codenames: valstore went from about 6 to 16 bare ids per 1,000 words between July and October ("(0404, 0369.2/11/12, 0412, …)", "SOAK5B-V3 / V3-R2"). ycc examples: "per the 0289 lesson", "since the 0312/0316 prune".
- **Stale status.** valstore 0248 has `status: done` while its Description still says "remains P1 and blocked". ycc 0283 is done with "- [ ] Verified on device" unchecked.
- **Debris.** Clipped tool output (`…[truncated]` in about 790 files), duplicate or empty Acceptance criteria headings, and stock reviewer praise ("The change fully implements task 0016") instead of findings.

`docs/design/doc-style.md` (added by task 0177) covers memory and the spec, but not backlog tasks.

## Scope
Add a backlog-task section to `docs/design/doc-style.md` and reference it wherever agents create or update tasks: the pm, chat and work prompts and the create_task tool description. Keep it short. Rules:
- A task is readable on its own. The first time it mentions another task id or a codename, it adds a few-word gloss, e.g. "0312 (August test prune)".
- The current state goes at the top of the task: status, what remains, and what blocks it. A status change updates any contradicting text.
- A claimed user or owner decision carries a date and session reference, or is marked as the agent's inference.
- Authority isn't inherited by citing a parent. A split task says which outcome of the parent it delivers and why it needs its own task.
- Work-log entries are outcomes and evidence (what changed, how it was verified, the commit), not pasted tool output or verdict boilerplate.

## Acceptance criteria
- doc-style.md has a backlog section covering the rules above, written in the existing register.
- The prompts that create or update tasks reference it in one sentence each. No duplicated rule text.
- The spec notes that the contract covers backlog tasks.
- `go test ./internal/orchestrator` passes.

## Acceptance criteria

- doc-style.md has a backlog section covering the rules above, written in the existing register.
- The prompts that create or update tasks reference it in one sentence each. No duplicated rule text.
- The spec notes that the contract covers backlog tasks.
- `go test ./internal/orchestrator` passes.

## Outcome

docs/design/doc-style.md §5 "Backlog tasks" now has rules for references, current state, decisions, authority and work logs. One shared sentence in projectDocsGuidance points the pm, chat and work prompts at doc-style.md, replacing pm's old separate line. Both create_task tool descriptions (modes and capture) also reference it. Spec §6.1 says the contract covers backlog tasks. go test ./internal/orchestrator passes, and the sol reviewer accepted after re-running it.

Commit: doc-style: add backlog-task rules; reference them from task-writing prompts and create_task (0528)
