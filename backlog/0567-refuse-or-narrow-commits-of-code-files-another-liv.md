---
id: "0567"
title: Refuse or narrow commits of code files another live session also changed
status: done
priority: 2
created: "2026-11-20"
updated: "2026-11-22"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description

Current state: split from 0565 (stop commits sweeping in other sessions' uncommitted changes) on 2026-11-20; not started.

This delivers 0565's shared-code outcome. It is separate because it changes the commit tool's contract for shared paths, independently of the bookkeeping line attribution.

## Acceptance criteria

- A session has a supported way to commit only its own changes to a code file another live session also changed, or a clear refusal explaining the alternative (a workstream, waiting for the other commit, or deliberately taking the file whole and naming the other work).
- spec §7.3 describes the behaviour.

## Outcome

The work and chat commit tools now refuse to commit a file when another live session also claims it. The refusal names the files and the alternatives: wait for that session's commit, use a workstream, or name the file in paths to take it whole and name the other work. Files a retried finalization adopts from its earlier scope do not count as named. A shell edit of a file another scope claimed gets an overlap claim (persisted in claims.json) when every other claim on the file predates the command. The overlap claim keeps the file out of the shell session's changeset but makes the claimant's commit refuse it. Claims of sessions that have ended do not refuse. Spec §6.2 and §7.3, the tool descriptions and the manifest note are updated, with tests.

Commit: Refuse commits of files another live session also changed unless named in paths
