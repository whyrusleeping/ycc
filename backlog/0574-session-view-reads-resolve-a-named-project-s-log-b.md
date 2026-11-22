---
id: "0574"
title: 'Session view reads: resolve a named project''s log before a same-id live session elsewhere'
status: done
priority: 5
created: "2026-11-21"
updated: "2026-11-21"
depends_on: []
spec_refs: []
---

## Description
**Status:** done; fixed within 0571 (session search daemon), whose review required it on 2026-11-21. `Manager.SessionViewLogPath` now resolves a named project's log through `Manager.ProjectSessionLog`.

## Problem

`GetSessionView`, `GetSessionViewPage` and `GetSessionViewDetail` resolve a session's log through `Manager.SessionLogPath`. That function checks the global live-session map before the named project. If a live session in project A has the same id as a persisted log in project B, which happens only when a log is copied between projects, a view request naming B serves A's transcript. Session search resolves logs within the project (`Manager.ProjectSessionLog`), so a search hit in B can open on A's rows.

## Acceptance criteria

- [ ] When a request names a project, presentation reads resolve the log within that project and its in-flight worktrees. A live session elsewhere never stands in for it.
- [ ] Requests without a project, and live workstream sessions opened under their parent project, keep working.


## Work log
