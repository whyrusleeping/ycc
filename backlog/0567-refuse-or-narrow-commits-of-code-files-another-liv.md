---
id: "0567"
title: Refuse or narrow commits of code files another live session also changed
status: todo
priority: 2
created: "2026-11-20"
updated: "2026-11-20"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description
Current state: split from 0565 (stop commits sweeping in other sessions' uncommitted changes) on 2026-11-20; not started.

This delivers 0565's shared-code outcome. It is separate because it changes the commit tool's contract for shared paths, independently of the bookkeeping line attribution.

## Why
A path both scopes claim is included whole and only reported as shared. In wheeler (2026-11-15 to 2026-11-20) about 12 commits carried another session's code, script or YAML changes, and agents used git plumbing, amends and stashes to avoid it. A hunk-only commit the user requested on 2026-11-16 still took a whole file (`9bab6cb`).

## Acceptance criteria
- A session has a supported way to commit only its own changes to a code file another live session also changed, or a clear refusal explaining the alternative (a workstream, waiting for the other commit, or deliberately taking the file whole and naming the other work).
- spec §7.3 describes the behaviour.

## Work log
