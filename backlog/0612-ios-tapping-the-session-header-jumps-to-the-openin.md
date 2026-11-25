---
id: "0612"
title: 'iOS: tapping the session header jumps to the opening prompt; long-press opens the focused task'
status: todo
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

## Problem
The session view's two-line principal title (the title taken from the opening prompt, over "project · task · model · duration · state") does nothing when tapped. In a long session the opening prompt is many pages up, and the transcript is paged (`SessionViewModel.loadEarlierRows` / `earlierCursor`), so scrolling there by hand means repeated swipes and page loads. The header also names the focused task, but you have to reach its detail screen through Backlog.

## Chosen behavior (the user left the choice to the authoring agent; this is its inference)
- **Tap** the header to scroll to the opening user prompt, with the prompt row at the top. The title text comes from that prompt, so tapping the title takes you to its source, and this works for every session, including ones without a task. If earlier pages are not loaded, load them (or fetch the first page directly if the session-view API allows it) and show progress while this happens. The jump must not fight the existing earlier-page anchor restoration or follow-latest. The user can get back down with the existing "Jump to latest" control.
- **Long-press** the header to open a context menu: "Opening prompt" (the same action as tap) plus one "Task NNNN · <title>" entry for each focused task. An entry pushes the existing `TaskDetailView` for that task in the session's project. A session with no focused task shows only the prompt entry.
- Accessibility: the combined header element gets a button trait and a hint ("Jumps to the opening prompt"), and the task entries are exposed as custom actions.

Rejected: a single tap that opens the task when there is one and the prompt otherwise. The same gesture would do different things depending on state that isn't visible, and most sessions have no task. Also rejected: a separate tap target on the task id inside the 250 pt caption line, which is too small to hit reliably.

## Acceptance criteria
- Tapping the header in a session whose opening prompt is beyond the loaded pages lands with that prompt row at the top. In a short, fully loaded session it scrolls there directly.
- Long-pressing in a session with focused task(s) offers one entry per task, each opening that task's detail. A session without a task offers only the prompt entry.
- Live streaming, follow-latest, and earlier-page anchor restoration still behave as before after a jump.
- Built and checked on the Mac simulator (light and dark, including a long paged session). YccKit `swift test` passes, with a test for any new model method, such as a load-to-start path.

## Work log
