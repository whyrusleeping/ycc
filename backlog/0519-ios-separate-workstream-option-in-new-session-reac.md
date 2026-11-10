---
id: "0519"
title: 'iOS: "Separate workstream" option in New Session; reach workstreams from All'
status: in_review
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on:
    - "0518"
spec_refs:
    - spec.md#14.1 Parallel workstreams
    - docs/design/ios-client.md
---

## Description
iOS counterpart of 0518. Today iOS can list and merge workstreams but not start one, and the list is reachable only from the home **…** menu when a single project is selected.

**Change:**
- The iOS New Session sheet gets the same **Separate workstream** toggle, using 0518's extended spawn RPC (prompt, mode, model, preset, pictures, optional task).
- The session list marks workstream sessions the same way as web.
- Workstreams is reachable from the home **…** menu even in the All scope. Ask for a project if needed, as Backlog does.
- `new_session.submit` analytics carry `workstream=true|false`.

**Acceptance:** with the toggle on, a session starts in a new workstream with the chosen options. With it off, nothing changes. Workstreams is reachable from All. Built and tested on the configured Mac simulator. Left in_review for on-device use.

## Acceptance criteria

## Work log
- 2026-11-10: Implemented, now in review for on-device use. New Session has an off-by-default **Separate workstream** chip (accent-filled when on) with a one-line explanation and an optional **Task** chip listing the project's ready todo/in_progress tasks by priority. With it on, `NewSessionModel.start()` calls `SpawnWorkstream` (new `YccClient+Workstreams.swift`) with the same mode, model override, applied preset (while its mode is selected) and pictures; a chosen task stands in for an unwritten prompt ("Work on backlog task <id>: <title>"), and a task selection never carries across projects. Off, it still calls `StartSession`. Session rows of workstream sessions show a branch mark, and their context menu opens Workstreams. The home **…** menu offers Workstreams in the All scope too (all projects, with Workstreams' own project filter), so it is never disabled. `new_session.submit` carries `workstream=true|false`; the toggle records `new_session.workstream`. docs/design/ios-client.md updated. Verified on the configured Mac: `swift test` (563, 10 new NewSessionModel tests) and `xcodebuild test` pass. Simulator (iPhone 18 Pro Max, scratch daemon + mock model, two projects): Workstreams opens from Recent; the chip, explanation and task menu render in light and dark; starting chat-mode with task 0001 and no prompt created ws_…-0001 and opened its live session with the task prompt; the alpha list showed the branch mark. One `swift test` run crashed in the existing `BacklogOptimisticTests.testStatusChangeMovesTheRowAtOnceAndOnlyBusiesThatRow` mock (`GatedBacklogSource` array race); five reruns passed.
