---
id: "0569"
title: Commit publication can reset an index entry staged while the commit's hooks run
status: proposed
priority: 4
created: "2026-11-20"
updated: "2026-11-20"
depends_on: []
spec_refs: []
---

## Description
Current state: proposed on 2026-11-20 by the agent working on 0566 (line attribution for backlog and memory files). An independent reviewer reproduced it, and it predates 0566. Not accepted.

## Why
`Repo.Commit` resets each committed path's index entry to the committed version after HEAD advances. It checks staged content only when it inspects the changeset, before hooks run. If another session runs `git add` on one of those paths while the commit's hooks run and then changes the worktree again, publication silently drops that staged version. 0566 keeps an entry that differs from the old HEAD for partially committed bookkeeping files whose staging is known at inspection, but staging done during the hook can still be lost for any path.

## Acceptance criteria
- Publishing a commit never drops an index entry staged after the changeset was inspected; the entry is kept or the publication reports it.

## Work log
