# Desktop web client release smoke

Run this in a real desktop browser when releasing web client changes, browser streaming, or daemon
web serving. Automated tests cover asset routing, history fallback, cache headers, authentication
boundaries, bundle freshness (`go test ./internal/web ./internal/daemon`), and the session reducer,
controller, and shared event contract (`npm test` in `clients/web`). This checklist covers browser
lifecycle, layout, and interaction.

## Prerequisites

Build or install the release-candidate binary and start it with web serving and a bearer token.
Use a desktop browser window about 1280px wide. Have at least two registered projects, one live
attended session that can be made to ask a single and a batch question, one persisted (finished)
session, a second client (TUI or iOS) on the same daemon, and a few pictures: an ordinary PNG or
JPEG screenshot, one over 5 MiB, and a non-picture file. For the rich-transcript checks, also have
a chat session whose reply contains markdown (a table, a task list, fenced Go code, a `diff` fence,
an `https:` link, a `javascript:` link, and raw `<script>`/`<img onerror>` HTML), a session that
ran Bash (one failing command), Read, Edit, and Write, and a long work session (several hundred
rows, so it has unloaded earlier pages) with a commit, a review verdict, and a `finish` report.
For the backlog checks, have a scratch project whose backlog holds proposed, todo, in-progress,
blocked, and done tasks with dependencies (one task blocked by an unfinished dependency, one task
body with a work log and a relative link to a sibling task file such as `0002-slug.md`), and a
session whose task focus names one of them. For the work-loop and workstream checks, have a scratch
git project whose backlog holds two or three ready tasks (one the agent will block on), a project
configured for automatic integration (`[integration] mode = "auto"` with a `verify` command you can
make fail, e.g. `test ! -e /tmp/verify-red`), and a model that can finish small tasks.

```
YCC_TOKEN=<token> ycc daemon --web --addr 127.0.0.1:8791
```

## Checklist

1. Open `http://127.0.0.1:8791/`. A wrong token stays on token entry with "Invalid token"; the
   correct token opens the shell. Reload: the token survives for that origin. The address bar and
   the daemon log never show the token.
2. The sidebar shows the Recent feed across all projects: a "Needs answer" section first when a
   session is waiting, then most recent first, each row with title, project name, status badge,
   live marker, background-jobs state, turn count, and relative time. "Load older sessions" pages
   further back. The project switcher scopes the list to one project and back to Recent. A project
   with no sessions offers "Start a session".
3. Open the live session. User and model turns are prominent; tool, reasoning, review, and
   system detail fold; a question and its answer render as one exchange. Model or tool text that
   contains markup (for example `<b>x</b>` or `<script>`) displays literally.
4. While output streams, the live tail updates in place and is replaced by the durable turn.
   Scroll up: new output does not move the viewport and a "Jump to latest" pill appears; it
   returns to the live edge. Scroll to the top of a long session: earlier rows load without the
   viewport jumping.
5. Expand an abbreviated row ("Show full text" or a folded tool row) and open a row in the
   inspector; the inspector resizes by dragging its edge and closes with ×. Open Working changes
   and, on a review row, View working changes.
6. Send a multiline message (Shift+Enter for a newline, Enter to send). It shows as sending/sent,
   then as the durable turn (queued until delivered while the agent is mid-turn).
7. Answer a single question with an option, and a batch question with a mix of options and free
   text; the panel dismisses only when the session reports the question closed. Ask another
   question and answer it from the second client; the browser panel dismisses.
8. Interrupt a running turn (Pausing… then Paused), send a steer, then Resume. Cancel a pause
   once. Open More → Stop session, cancel once, then confirm; the view becomes read-only.
9. Drop the network or sleep the machine briefly while the second client produces output. On
   return the view reconnects and shows every durable row once, with no stale live tail.
10. Open the persisted session: a finite read-only transcript with no composer, controls, or
    reconnect churn. Resume session re-opens it in place (history stays, then the composer and
    controls return and a "Session reopened" row appears). Hovering a non-live row in the session
    list shows Resume, which navigates to the session and re-opens it the same way.
11. From the unscoped Recent feed, New session asks "Which project?" with the last-viewed project
    first; from a scoped project it preselects that project. Mode cards show descriptions; a
    suggestion card adopts its mode and seeds the prompt (changing the mode drops the preset). Work
    mode starts with an empty prompt; other modes need text or a picture. Pick a non-default model,
    attach a picture by paperclip, paste, and drag-and-drop (thumbnails, sizes, ×), and start with
    Enter: the browser opens the live session, the picture shows as a transcript thumbnail that
    opens full size in the inspector, the header shows the chosen model, and the session is already
    in the sidebar. A non-picture file is refused before sending; a corrupt picture or an oversized
    GIF shows the daemon's error without creating a session; an oversized PNG/JPEG is downscaled
    before sending.
12. In a live session, paste or drop a picture into the composer and send it. Open Settings: change
    the reasoning level (a "Thinking … → level" row appears and the level stays selected), switch the
    coordinator model (a "Roles: …" row appears and the header model changes), and check the context
    tokens, Roll over context, and the per-model usage table. Settings on a non-live session are
    read-only.
13. Reload on a session deep link (`/p/<project>/s/<session>`) and on a placeholder route; the same
    view returns. Back/forward navigate between sessions. A second tab works independently.
14. Rich transcript (markdown and code). In the markdown chat session the agent's reply reads as a
    normal agent turn (not a "Finished" card): headings, the GFM table, task-list checkboxes, nested
    lists, and quotes render; the `https:` link opens in a new tab; the `javascript:` link is inert
    dotted text; raw `<script>`/`<img onerror>` shows as literal text and no image is fetched.
    Fenced code is syntax highlighted with a language label; its Copy button puts exactly the fenced
    source on the clipboard (tabs, `<`, trailing spaces intact), and a `diff` fence renders as a
    tinted diff whose Copy copies the raw diff. A path-like code span (`internal/a.go:12`) copies the
    path when clicked.
15. Tool rows. Collapsed rows read at a glance: Bash shows the command and `exit N` (red when
    non-zero, "timed out", or "background"); Read, Write, and Edit show the file path (click copies
    it) plus line count or the Edit's `+N −M`. Expanding shows the highlighted command and output,
    the Edit as a highlighted diff, Write content highlighted by file type, and Read output with a
    line-number gutter, each with copy buttons; "Open in inspector" shows the same detail in full.
16. Commits, reviews, and reports. In the long work session the `finish` report keeps the green
    "Finished" card (a `report_blocked` one reads "Blocked"). Clicking the commit row opens its diff
    in the inspector: commit metadata de-emphasized, `+`/`−` lines tinted, code highlighted per
    file, a truncation notice for a capped diff, and Copy matching `git show`. On a review row the
    verdict (ACCEPT/REVISE) shows; View working changes says whether the tree changed since the
    reviewed snapshot and lists the session's verdicts, marking one that covers the current changes.
17. Search. Ctrl/Cmd-F opens the find bar instead of the browser's (also the header's Search).
    Typing highlights matches in loaded rows and selects the newest; a term that only appears in an
    unloaded earlier page says "No loaded matches"; Enter then pages earlier history until it finds
    the match, scrolls it into view, and opens a folded row it lands in. Enter steps to older
    matches, Shift+Enter to newer ones, wrapping once the whole history is loaded; Esc closes it and
    clears the highlights.
18. Backlog browsing. With "All projects" selected, the sidebar's Backlog asks which project (last
    viewed first); a scoped project opens `/p/<project>/backlog` directly, and the project switcher
    follows to another project's backlog. The table hides done tasks until "Show done"; status
    chips, "Actionable only" (todo or in-progress tasks whose dependencies are done), and the text
    filter (id, title, status, or a dependency id) narrow it; the count reads "N of M tasks".
    Column headers sort both ways (status order is active work first, done last). Blocked rows say
    which dependencies block them; proposed rows read "needs promotion".
19. Backlog keyboard. `j`/`k` or the arrow keys move the row cursor, Enter opens the task at
    `/p/<project>/backlog/<id>`, `/` focuses the filter, Esc clears the filter or closes the task.
    Typing `j` in the filter (or any text field) only types.
20. Task detail. The pane shows status, priority, actionable/blocked state, dependencies as links
    (with their titles and statuses), spec refs, dates, the file path (Copy), sessions focused on
    the task, the markdown body with raw HTML shown literally, and the work log as its own
    section. Dependency links and a relative link to a sibling task file open that task. Reload on
    a task URL returns to it.
21. Status changes. Promote a proposed task from its table row and another from the detail pane
    ("Promote to todo"), and change a status with the status menu while a session in the same
    project is running: nothing waits on the session, and `ycc task show <id> --project <p>` shows
    the new status.
22. Editing. Edit a task's title, priority, dependencies, spec refs, and body; Save (or
    Ctrl/Cmd+Enter) shows "Saved task …", the pane shows the daemon's canonical task, and
    `ycc task show` matches. Clear the title and save: "Title is required." and the body draft is
    intact. While editing the body, hand-edit the same task's body on disk and save: a warning says
    the task changed (body) and the draft is kept; switch to another task and back, and reload —
    the draft is still there; "Overwrite with my draft" saves it ("Discard my draft, load theirs"
    drops it instead). Edit only the title while the file's body changes on disk: the save merges
    (your title, their body).
23. New task and quick capture. "+ New task" refuses a blank title, then creates the task and opens
    it; `ycc task list --project <p>` lists it. Quick capture (button, or Alt+N / ⌥N from any page)
    streams the capture agent's progress and offers "Open task" for the created task; a vague
    description gets one clarifying question whose answer creates the task. Running sessions are
    not disturbed.
24. Session links. A session row's focus-task chip opens that task in the backlog while the rest of
    the row still opens the session; in a session, the header's "task N" chip opens the task in the
    inspector beside the transcript, and a dependency link there swaps the inspector to that task.
25. Work loop. With "All projects" selected, the sidebar's Work loop asks which project; each card
    shows that project's loop state. A project with no loop explains the loop and lists its ready
    tasks. Start loop… shows the ready tasks (or warns that none are ready), the work implementation
    (delegate/direct, the current one marked; changing it is daemon-wide), and the budget caps the
    loop captures. While it runs: the state badge, summary, and token/cost totals update; the
    sidebar's Work loop item shows a pulsing "running" badge; "Follow live session →" opens the
    current loop session live, and the loop's sessions carry a "loop" tag in session lists.
26. Stop loop asks first (the current session finishes; a waiting loop stops at once), then reads
    Stopping… and Finished with "loop stopped: requested", announced by a toast even when another
    page is open. The finished loop lists every session run (tokens, cost, duration, attempts, a
    failure line, clamped evidence) linking to its transcript, the digest (completed, blocked with
    its reason, in review, unfinished, created) with task links and commit shas that open the
    commit in the inspector, and the captured resource envelope. A loop that stops on a failure
    (a provider rejection, or a startup failure such as an expired login) shows the outcome as an
    error, raises an error toast, and keeps the reason in the backlog's loop banner.
27. Workstreams. Spawn workstream (optional ready task, which seeds the prompt; prompt; optional
    base branch or commit) lists the new stream as Working with its branch, commits, session state,
    Open session, and the integration mode. When it finishes with commits it is Queued/Integrating,
    then merged automatically, or reads Needs attention with the daemon's reason (red verify,
    conflicts) and an Integration log link that opens the integration agent's transcript; the
    sidebar's Workstreams item shows the in-flight count, flagged when one needs attention.
28. Preview & merge… opens the integrated diff in the inspector ("Merges cleanly onto <base>") with
    Accept & merge, which advances the base and links the merge commit; a conflicting stream lists
    its conflicted files instead and the base is untouched. Fix the cause (e.g. make verify pass)
    and Retry integration: the stream re-queues and merges. Discard… asks first, then removes the
    worktree and branch; merged and discarded streams stay under "Merged and discarded" with their
    session transcripts. In gate mode, ready streams read Gated and "Merge all ready" merges them in
    order, stopping at the first failure.
29. Repeat a few steps with the system in dark mode.

## Pass condition

All checks pass without a JavaScript console error, a Content-Security-Policy violation, or a
full-page reload. (The token probe's 401 on first load and the deliberately rejected attachments'
400s are expected network errors.) Record the browser and version, daemon commit, and console output for failures.
