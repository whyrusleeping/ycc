# iOS release smoke checklist

Run this before an iOS release on an iPhone simulator and, for notification/network checks, a
physical device. Swift package tests and the Xcode build cover reducers, models, and compilation;
this checklist covers Keychain, app lifecycle, native pickers/navigation, and real daemon traffic.

## Prerequisites

- A release-candidate app build installed on the target.
- A reachable token-protected daemon with a registered project, one live attended session, and a
  configured notification endpoint for the device check.
- A second client attached to the live session for cross-client/reconnect checks.

Use loopback for the simulator or a private-network address for a device. Follow
`plans/remote-access-smoke.md` only when validating the network path itself.

## Checklist

### 1. Authentication and secure persistence

1. On a clean install, connect once with a wrong token. Confirm the app stays on the connection
   screen and does not save an authenticated session.
2. Connect with the correct token, force-quit, and relaunch. Confirm the profile reopens without
   re-entering the token.
3. Restart the daemon with a different token and foreground/refresh the app. Confirm the 401
   returns to connection while preserving the saved endpoint/profile for repair.

Pass condition: authentication is proven by an RPC, the valid token survives only through
Keychain-backed state, and invalid credentials never leave the app looking connected.

### 2. Live stream and lifecycle recovery

1. Open a live session and watch transient model text resolve into one durable turn.
2. Scroll into history while events arrive. Confirm the viewport does not jump and the
   jump-to-latest control restores follow.
3. Drop the network or background the app during streaming, generate more events from the second
   client, then reconnect/foreground. Confirm no missing or duplicate durable rows and no stale
   live-tail bubble.
4. Force-quit and reopen the same session. Confirm it loads from durable history and continues
   normally.

Pass condition: app lifecycle and network interruption preserve the persisted sequence boundary.

### 3. Cross-client questions and controls

1. Answer one option question and one free-text or batched question from the app.
2. Leave another question sheet open and answer from the second client. Confirm it dismisses from
   the streamed durable answer.
3. Interrupt, send a steer, and Resume. Then Stop and cancel once before confirming the destructive
   action.

Pass condition: question ownership is daemon-wide, action errors remain non-fatal, interrupt is
resumable, and Stop is clearly destructive.

### 4. Native input and pictures

1. With the software keyboard visible, exercise a bottom composer and a sheet composer. Confirm
   the focused field and send controls remain above the keyboard through rotation/presentation.
2. Start a session with one photo and no prompt text, then send another photo in-session. Confirm
   thumbnails can be removed before send and the model receives the retained images on the first
   relevant turn.

Pass condition: keyboard avoidance works on real bottom chrome and PhotosPicker content reaches
the model without a text-only workaround.

### 5. Navigation, deep links, and notifications

1. Navigate between projects, recents, session history, backlog, usage, and a workstream, then
   background/foreground. Confirm the selected project and stack remain coherent and duplicate
   taps do not push duplicate destinations.
2. Open a `ycc://` session link while authenticated and while signed out. Confirm both routes land
   on the intended session after any required connection step.
3. Start unattended work, lock/background the device, and trigger a question or completion
   notification. Tap it and confirm the app opens the matching project/session without a token in
   the URL or notification payload.

Pass condition: in-app taps, deep links, and notifications share one authenticated routing path.

## Release result

Record device/simulator model, OS version, daemon commit, app commit, and any failed checklist
section. A release candidate passes when all sections complete without a crash, lost/duplicated
durable event, credential leak, or navigation dead end.
