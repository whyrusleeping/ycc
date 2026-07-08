# iOS client smoke test (manual simulator/device runbook)

Verifies the SwiftUI iPhone app (docs/design/ios-client.md) against a real
daemon. The headless logic (`YccKit`) is covered by `swift test`; the build is
covered by `xcodegen generate && xcodebuild`. This runbook covers what those
can't: the app actually talking to a daemon from a simulator/device.

This is the **phase-1 step 1** cut — the connect screen + an authenticated
landing view listing projects. It grows as later phases (session list, live
stream, answering questions, replay-from-seq reconnect) land.

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

## Notes

- Transport security: the app allows insecure (`http://`) loads for tailnet
  deployment (spec §14). `https://` daemons work unchanged.
- Later phases extend this runbook with the session list, the live event stream
  (`Subscribe`), answering a live `ask_user`, and killing the network mid-stream
  to verify replay-from-seq reconnect (design §10).
