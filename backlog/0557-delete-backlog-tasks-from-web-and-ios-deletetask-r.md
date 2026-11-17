---
id: "0557"
title: Delete backlog tasks from web and iOS (DeleteTask RPC)
status: done
priority: 2
created: "2026-11-17"
updated: "2026-11-17"
depends_on: []
spec_refs:
    - spec.md#18.5 Backlog browser
---

## Description

Neither client can delete a backlog task; the only way is removing the file by hand. Add a daemon `DeleteTask` RPC and a confirmed Delete action on the web and iOS task detail screens.

## Acceptance criteria

- `DeleteTask(project, id)` removes the task file under the backlog store lock and removes the id from the `depends_on` of every task that is not done, returning the ids it edited. Unknown ids are NotFound.
- Web task detail offers Delete with a confirm dialog naming the open dependents that lose the dependency; after deletion the backlog list refreshes and the detail closes.
- iOS task detail offers Delete (destructive, confirmed) in its toolbar menu; on success the screen pops and the cached backlog drops the row.
- Go, Swift and web generated code regenerated; web dist rebuilt; iOS built on the Mac.

## Outcome

New DeleteTask RPC removes the task file under the backlog store lock and drops its id from the depends_on of tasks that are not done (done tasks keep theirs); it returns the edited ids. Web task detail has a Delete button with a confirm dialog that names affected dependents. iOS task detail's toolbar menu ("More", formerly "Status") has a destructive Delete Task with confirmation that pops back and updates the cache. Go/Swift/web code regenerated and web dist rebuilt. Tests: go docs/server, web suite, YccKit swift test, app build. Both flows checked end-to-end against a scratch daemon (Playwright and iOS simulator).

Commit: Delete backlog tasks from web and iOS (DeleteTask RPC)
