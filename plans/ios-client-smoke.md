# iOS client smoke test (manual simulator/device runbook)

Verifies the SwiftUI iPhone app (docs/design/ios-client.md) against a real
daemon. The headless logic (`YccKit`) is covered by `swift test`; the build is
covered by `xcodegen generate && xcodebuild`. This runbook covers what those
can't: the app actually talking to a daemon from a simulator/device.

This covers the **phase-1** cut plus **phase-2 start/resume** and the **phase-2
backlog browser** — the connect screen, the projects/session list, a live
streaming transcript, answering `ask_user` questions, the interactive controls
(input bar, interrupt/resume/stop), starting/resuming sessions, and browsing the
backlog (list, detail, status updates, quick capture, start-work). It grows as
later phases land.

## Prerequisites

- A Mac with Xcode 17+ and `xcodegen` on `PATH` (`brew install xcodegen`).
- A reachable ycc daemon with a bearer token. Loopback is fine for the
  simulator (it shares the Mac's network):

  ```
  export YCC_TOKEN=$(head -c 32 /dev/urandom | base64)
  YCC_TOKEN=$YCC_TOKEN ycc daemon --addr 127.0.0.1:8790
  ```

  For a device, put both on the same tailnet and use machine A's tailnet IP
  (see `plans/remote-access-smoke.md`); the app's ATS config allows the
  `http://` tailnet address.
- At least one registered project so the landing view has something to show
  (`ycc project add <dir>` or start a session in a workspace).

## Build & run

```
cd clients/ios
xcodegen generate
open Ycc.xcodeproj      # then run the Ycc scheme on an iPhone simulator
# — or —
xcodebuild -project Ycc.xcodeproj -scheme Ycc \
  -destination 'platform=iOS Simulator,name=iPhone 16' build
```

## Steps

1. **Wrong token is rejected and not persisted.** Launch the app. Enter the
   base URL (`http://127.0.0.1:8790`) and a deliberately wrong token, tap
   **Connect**.
   - Expected: an "Invalid token." error under the form; you stay on the
     connect screen.
   - Kill and relaunch the app: it opens on the connect screen (nothing was
     saved).

2. **Right token connects and lists projects.** Enter the correct `$YCC_TOKEN`
   and tap **Connect**.
   - Expected: the app advances to the **Projects** landing view listing the
     daemon's project names/paths — proof of an authenticated round-trip. This
     is the phase-1 acceptance signal.

3. **Session persists across launch.** Force-quit and relaunch the app.
   - Expected: it lands authenticated directly on the Projects view (the
     profile + base URL came from `UserDefaults`, the token from the Keychain —
     the token is never written to `UserDefaults`).

4. **Mid-session 401 returns to connect.** With the app on the landing view,
   stop the daemon and restart it with a *different* token, then pull-to-refresh
   the Projects list (or foreground the app).
   - Expected: the now-invalid token yields a 401; the app clears the active
     session and drops back to the connect screen.

5. **Disconnect.** Tap **Disconnect** on the landing view.
   - Expected: back to the connect screen; the saved profile is retained (its
     base URL is still available to reconnect) but the app is no longer
     authenticated.

## Session interactions (phase-1 step 4)

These steps exercise the live session view: streaming, answering questions,
replay-from-seq reconnect, and the interrupt/resume/stop controls. Start a live
session in an *interactive* mode so `ask_user` actually blocks (autonomous mode
auto-answers). A quick way:

```
# In the daemon's workspace, start an interactive session that will ask something.
ycc start --mode build --level interactive "a task that needs a decision from you"
# You can also `ycc attach <session-id>` from another terminal and type to
# answer/steer — handy for the "answered elsewhere" race below. Then open the
# session from the app's session list.
```

6. **Watch a live stream.** From the session list, open the running session.
   - Expected: the transcript renders the replayed history then tails live —
     model turns stream in as a "streaming" live-tail bubble that resolves into
     a durable bubble; the toolbar shows the green **Live** indicator; tool
     calls appear as collapsed rows that flip to ✓/✗ on result.

7. **Answer an `ask_user` (single, option).** Drive the agent to a question that
   offers suggested options (e.g. a Yes/No confirm). An answer sheet presents.
   - Expected: tapping an option button answers immediately (`AnswerQuestion`
     with `option_index >= 0`); the sheet dismisses and the question row shows
     "Answered: …".

8. **Answer an `ask_user` (single, free text).** At another question, type a
   free-text answer and tap **Send**.
   - Expected: `AnswerQuestion` with `option_index = -1` and your text; row
     resolves.

9. **Answer a batch `ask_user`.** Drive the agent to a batch question (multiple
   questions in one `ask_user`). The sheet shows one section per question.
   - Expected: pick an option for some and type text for others, then **Submit**
     → `AnswerQuestions` with positional `answers[i]` (option picks send
     `option_index`, typed ones send `-1` + text). All questions resolve.

10. **Answered-elsewhere race.** Re-open a question sheet, then answer the same
    question from another client (`ycc attach <session-id>` and type the answer,
    or the web client).
    - Expected: the sheet auto-dismisses (the `question_answered` event clears
      the pending gate). If you tap an option in the split-second before it
      clears, you get a mild "no pending question" toast — **no crash**.

11. **Kill the network mid-stream → replay-from-seq reconnect.** While a turn is
    streaming, drop connectivity: toggle the Mac's Wi‑Fi, or stop and restart the
    daemon (`Ctrl-C` then relaunch with the *same* token and workspace so the
    session's event log persists). Then restore connectivity / foreground the app.
    - Expected: the toolbar shows the reconnecting spinner while down; on
      recovery the app re-`Subscribe`s from its last **persisted** seq. The feed
      has **no gap and no duplicate** rows — it resumes exactly where it left off
      (design §10). The stale streaming live-tail from before the drop is cleared
      rather than left dangling.

12. **Interrupt → steer → resume.** With the session running, open the overflow
    (**⋯**) menu and tap **Interrupt**.
    - Expected: an "interrupted" system row and a **Paused — send a steer or
      Resume** banner above the input bar. Type a steer in the input bar and
      send it (`SendInput`); then tap **Resume** (banner button or overflow
      menu). The agent continues, incorporating the steer; the banner clears once
      activity resumes.

13. **Stop with confirmation.** Open the overflow menu and tap **Stop…**.
    - Expected: a destructive confirmation dialog ("This hard-terminates the
      agent — there is no resume."). Confirm → `StopSession`; the stream ends
      cleanly (Live indicator clears) and the feed reflects the session stopping.

14. **Error toasts.** Try an action against a session the daemon has already
    dropped (e.g. stop it, then attempt Resume), or send while stopped.
    - Expected: a mild "Action failed" alert surfacing the daemon's message
      (`not_found` / `failed_precondition`), no crash.

## Start & resume sessions (phase-2 step 5)

These steps exercise starting a new session from the phone and resuming a
persisted one (docs/design/ios-client.md §6 phase 2 step 5). The view-model
logic (`NewSessionModel`) is covered by `swift test`; this covers the live
round-trip.

15. **Start a new session.** On the Sessions landing view, tap the **+** button
    in the toolbar.
    - Expected: the **New session** sheet presents with a **Mode** picker
      (work/pm/chat with a description under it), any **Presets** as tappable
      shortcuts, an **Interaction level** picker (Interactive/Judgement/
      Autonomous with a description), a **Project** picker (only when more than
      one project is registered; "Default" = daemon default), and a multiline
      **Prompt** composer. Mode/level/project default to your last-used choices.

16. **Preset seeds the composer.** Tap a preset (e.g. a pm framing).
    - Expected: the mode switches to the preset's mode and the prompt field is
      pre-filled with its opening prompt; you can edit before starting.

17. **Start lands in the live stream.** Pick `work` (or `pm`/`chat`), type a
    prompt, tap **Start**.
    - Expected: a brief progress spinner, then the sheet dismisses and the app
      navigates directly into the **live** session view streaming from seq 0 —
      the green **Live** indicator shows and the first turn streams in. Re-open
      the **+** sheet later: your mode/level/project are remembered.

18. **Start error is surfaced.** Open **+**, pick a project the daemon can't
    resolve (or stop the daemon), and tap **Start**.
    - Expected: an inline red error row in the sheet (the daemon's message, e.g.
      unknown project) — you stay in the composer, no crash. A 401 drops back to
      the connect screen.

19. **Resume a persisted session.** Back on the Sessions list, swipe **right**
    on a non-live (idle/finished/stopped) row (or long-press for the context
    menu) and tap **Resume**.
    - Expected: `ResumeSession` re-opens it on its existing log; the app
      navigates into the live view. The transcript is **continuous** — the same
      event log continues (seq continuity: no gap, no restart), and new activity
      appends. Resuming an already-live session is idempotent (still opens it).

20. **Resume error is surfaced.** Swipe-resume a session the daemon has dropped
    (or with the daemon stopped).
    - Expected: a **"Couldn't resume"** alert with the daemon's message
      (`not_found` / server error), no crash.

## Backlog browser (phase-2 step 6)

These steps exercise the backlog browser (docs/design/ios-client.md §6 phase 2
step 6, spec §18.5): listing, task detail, status updates, quick capture, and the
"start work on this task" action. The view-model logic (`BacklogModel`,
`TaskDetailModel`) is covered by `swift test`; this covers the live round-trip.
Use a workspace with a non-trivial `backlog/` (some todo, in_progress, blocked
and proposed tasks, and at least one task with a `depends_on` on a not-yet-done
task) so the sections and ready/blocked annotations have something to show.

21. **Open the backlog.** On the Sessions landing view, tap the **checklist**
    icon in the toolbar.
    - Expected: the **Backlog** view pushes, listing tasks grouped into status
      **sections** ordered active-first — In progress, In review, Todo, Blocked,
      Proposed, then Done last. Each row shows the task **id** (monospaced),
      title, a **priority** pill (P1–P5), and a readiness annotation: a green
      **Ready** for unblocked tasks or an orange **Blocked by 0173, …** listing
      the not-yet-done dependencies (matches `ListBacklog` semantics). A
      multi-project daemon shows a project filter in the leading toolbar.

22. **Empty backlog state.** Point the project filter (or the daemon) at a
    workspace with no backlog.
    - Expected: a sane empty state ("Backlog is empty") with a **Capture task**
      button — not an error.

23. **Open task detail.** Tap a task row.
    - Expected: the detail view shows the title, a **status pill**, priority,
      ready/blocked, **Depends on** / **Spec refs** / **Created** / **Updated**
      rows as present, and the markdown **Details** body rendered (headings,
      lists and paragraphs render; fenced code blocks show monospaced). Pull to
      refresh re-reads the task.

24. **Change status.** In the detail toolbar, open the **⋯** status menu and pick
    a new status (e.g. promote a **Proposed** task to **Todo**, or mark one
    **Blocked**). The current status has a checkmark.
    - Expected: `UpdateTask` runs (brief spinner), and the status pill reflects
      the daemon's response. Backing out to the list and refreshing shows the
      task in its new section. Picking the current status is a no-op.

25. **Quick capture.** Back on the Backlog list, tap **+** in the toolbar. Enter
    a title (and optional markdown description) and tap **Save**.
    - Expected: `CreateTask` runs, the sheet dismisses, and the new task appears
      in the list (default **Todo**, priority P3). A blank title disables
      **Save**. A create failure surfaces an inline red error row in the sheet.

26. **Start work on a task.** Open a task's detail and tap **Start work on this
    task**.
    - Expected: `StartSession` runs (mode `work`, level judgement, a prompt like
      "Work on task 0184: …"); the app navigates directly into the **live**
      session view streaming from seq 0 (green **Live** indicator). A failure
      surfaces a "Couldn't start work" alert; a 401 drops back to connect.

## Notes
- Transport security: the app allows insecure (`http://`) loads for tailnet
  deployment (spec §14). `https://` daemons work unchanged.
- Later phases extend this runbook beyond phase 1 (starting sessions from the
  home menu, notifications via ntfy + `ycc://` deep links).
