---
id: "0612"
title: 'iOS: tapping the session header jumps to the opening prompt; long-press opens the focused task'
status: done
priority: 3
created: "2026-11-25"
updated: "2026-11-25"
depends_on:
    - "0607"
spec_refs:
    - docs/design/ios-client.md
---

## Description

Current state: todo. Requested by the user 2026-11-25 (session filing this task). It waits on 0607 (iOS project-home loop line and session-header task ids), which another session is building in the same header (`titleToolbar` in `clients/ios/App/SessionView.swift`).

## Acceptance criteria

- Tapping the header in a session whose opening prompt is beyond the loaded pages lands with that prompt row at the top. In a short, fully loaded session it scrolls there directly.
- Long-pressing in a session with focused task(s) offers one entry per task, each opening that task's detail. A session without a task offers only the prompt entry.
- Live streaming, follow-latest, and earlier-page anchor restoration still behave as before after a jump.
- Built and checked on the Mac simulator (light and dark, including a long paged session). YccKit `swift test` passes, with a test for any new model method, such as a load-to-start path.

## Outcome

Tapping the iOS session header jumps to the opening prompt. When the start isn't loaded, SessionViewModel.seekOpeningPrompt fetches every remaining page first and installs them together, so the reader stays put while it loads, under a progress banner. A failed or cancelled load installs nothing. The prompt is then pinned at the top as scrollback. A long press offers "Opening prompt" plus "Task NNNN · title" for each focused task, which pushes TaskDetailView. A SeekOwner token stops the search-hit seek, the header jump and Jump to latest from overriding one another. A held prompt is sought again if a cached transcript's revalidation drops it. A stale latestVisible on reopened short transcripts is fixed. Verified with YccKit swift test (701 pass, 3 new tests), a Mac sim build, and light/dark sim checks on a ~600-row paged session. sol accepted in round 3.

Commit: 0612: iOS session header — tap jumps to the opening prompt (loads the rest of the history first), long-press offers each focused task's detail
