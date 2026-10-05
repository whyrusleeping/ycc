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
JPEG screenshot, one over 5 MiB, and a non-picture file.

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
14. Repeat a few steps with the system in dark mode.

## Pass condition

All checks pass without a JavaScript console error, a Content-Security-Policy violation, or a
full-page reload. (The token probe's 401 on first load and the deliberately rejected attachments'
400s are expected network errors.) Record the browser and version, daemon commit, and console output for failures.
