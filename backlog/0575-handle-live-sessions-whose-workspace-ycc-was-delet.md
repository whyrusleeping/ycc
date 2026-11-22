---
id: "0575"
title: Handle live sessions whose workspace .ycc was deleted
status: proposed
priority: 4
created: "2026-11-21"
updated: "2026-11-21"
depends_on: []
spec_refs: []
---

## Description
Observed 2026-11-21: running `rm -rf .ycc` in a project while the daemon held one of its sessions live left a phantom row. `ListSessionHistory`'s "live session with no on-disk snapshot" fallback stamps `StartedAt`/`LastActivity` with `time.Now()`, so the row always reads "now" and sorts first. Opening the row fails because `GetSessionView` indexes from the missing `events.jsonl` (`os.ErrNotExist` → NotFound → "Session not found."). The daemon also keeps the deleted `session-view.sqlite` open in `viewStores`. The workaround is `ycc stop <id>`.

## Acceptance criteria
- A live session whose log file has vanished lists with its in-memory event timestamps, not the current time.
- Opening it either works from the in-memory log or offers a clear way to dismiss/stop it, not a bare "Session not found."
- A cached view store whose file was unlinked is reopened.

## Work log
- 2026-11-22 groom: moved the acceptance list under the Acceptance criteria heading, which was empty. Checked that `ListSessionHistory` (internal/session/history.go) still stamps the no-snapshot fallback with `time.Now()`; still proposed.
