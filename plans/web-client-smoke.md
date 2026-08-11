# Embedded web client release smoke

Run this in a real browser when releasing web assets, browser streaming, or daemon web serving.
Automated tests cover asset routing, authentication middleware, frame parsing, sequence cursors,
and transient reduction; this checklist covers browser lifecycle and phone-sized interaction.

## Prerequisites

Start the release-candidate daemon with web serving and a bearer token. Use loopback with desktop
device emulation or a private-network address with a real phone. Have one live attended session
and a second client available.

```
YCC_TOKEN=<token> ycc daemon --web --addr <reachable-address>:8791
```

## Checklist

1. Open the page in a narrow phone viewport. A wrong token must remain on connection with a mild
   error; the correct token must authenticate and survive a reload for that browser origin.
2. Open the live session. Confirm user/model turns are readable, tool and reasoning detail folds,
   and model/tool text containing markup displays literally rather than becoming HTML.
3. While output streams, confirm one live-tail bubble is replaced by the durable turn. Scroll into
   history and verify new events do not move the viewport until jump-to-latest is used.
4. Background the tab or briefly drop the network while the second client produces durable events.
   Return and confirm replay resumes with no missing/duplicate rows and no stale transient tail.
5. Answer an option question in the browser. Open another question and answer it from the second
   client; confirm the browser gate dismisses. Send input, interrupt/steer/resume, and cancel once
   before confirming Stop.
6. Open a persisted-only session. Confirm it renders as a finite read-only transcript rather than
   showing live controls or reconnect churn.
7. Close/reopen the tab and switch between two projects if available. Confirm selected session and
   authentication state recover without a navigation dead end.

## Pass condition

Token entry, phone layout, safe transcript rendering, transient-to-durable replacement,
replay-from-sequence, cross-client questions, controls, and persisted transcript behavior all work
without a JavaScript error or full-page reload. Record browser/device, daemon commit, and console
output for failures.
