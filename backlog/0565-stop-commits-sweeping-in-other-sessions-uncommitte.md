---
id: "0565"
title: Stop commits sweeping in other sessions' uncommitted changes and agents working around the commit tool
status: done
priority: 2
created: "2026-11-20"
updated: "2026-11-22"
depends_on:
    - "0566"
    - "0567"
    - "0568"
spec_refs:
    - spec.md#6.2
    - spec.md#7.3
---

## Description

Current state: accepted on 2026-11-20 (session s_18f059896f075e5a, event 430: "Yes, file a todo task for the commit sweep-in / git-plumbing problem"). Split on 2026-11-20 into 0566 (commit only a session's own lines of backlog and memory files, with watch runs committing their own bookkeeping; done in `9d0f0ed`), 0567 (refuse or narrow commits of code files another live session also changed; done in `f16ec93`) and 0568 (surface history rewrites and stashes on a shared HEAD; done in `f862cc7`). On 2026-11-22 all criteria were checked together and met (see the work log). This was found while reviewing the wheeler project's session logs for 0443 (experimental session awareness), but it is independent of that feature.

## Acceptance criteria

- A session committing its own work no longer includes another session's uncommitted backlog or `memory.md` edits by default; tests cover concurrent edits to one task file by two sessions.
- Bookkeeping written by watch runs does not remain as long-lived dirty state that later commits sweep in.
- A session has a supported way to commit only its own changes to a code file another live session also changed, or a clear refusal explaining the alternative (a workstream or waiting for the other commit).
- History-rewriting or stash operations on a shared HEAD while other live sessions exist are surfaced to the agent, not silently allowed.
- spec §6.2 and §7.3 describe the chosen behaviour.

## Outcome

Parent closure. All criteria are met by done children: 0566 (9d0f0ed) commits only a session's own lines of backlog/memory.md, with concurrent-edit tests, and watch runs commit their own bookkeeping. 0567 (f16ec93) refuses commits of code files another live session changed and names the alternatives (wait, workstream, or name the file in paths). 0568 (f862cc7) adds the Bash shared-HEAD rewrite/stash warning. Spec §6.2 and §7.3 describe all three. Re-ran go test for orchestrator, workspacelease, tools, session and docs on HEAD f862cc7 after later scoping changes (0580, 0585, 0461): all pass.

Commit: Close 0565: commit sweep-in and shared-HEAD safeguards verified across 0566-0568
