---
id: "0180"
title: 'iOS: app scaffold (clients/ios, XcodeGen + YccKit) with connect screen & Keychain auth'
status: todo
priority: 2
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0178"
spec_refs:
    - docs/design/ios-client.md#3. Location & toolchain (decision)
    - docs/design/ios-client.md#5. App architecture
---

## Description
Scaffold the SwiftUI iPhone app per `docs/design/ios-client.md` §3/§5 and build the connect screen (phase 1, step 1).

## Description
- `clients/ios/` layout: `project.yml` (XcodeGen manifest; iPhone-only, iOS 17 deployment target, app target `Ycc` depending on the local `YccKit` package), `YccKit/` SPM package (Sources/YccKit + the generated Sources/YccProto from 0178 + Tests/YccKitTests), `App/` thin SwiftUI shell. `.xcodeproj` is generated, git-ignored.
- `YccKit.YccClient`: wrapper over the generated connect-swift `SessionService` client — base URL + token, bearer-auth interceptor on every request (unary + streaming), typed async methods, ATS exception documented in project.yml/Info.plist (`http://` tailnet deployment).
- `YccKit.ConnectionStore`: saved server profiles (name + base URL) in UserDefaults; token in the Keychain (never UserDefaults); one active server.
- Connect screen (SwiftUI): base URL + token entry, validate via `ListProjects` (401 → "invalid token"), persist on success, and a placeholder landing view proving an authenticated round-trip (e.g. project names listed).

## Acceptance criteria
- `swift test` passes for YccKit on macOS (connection-store logic, request shaping with a stubbed transport).
- `xcodegen generate && xcodebuild build` for an iOS Simulator destination succeeds.
- Token lives in Keychain; a wrong token shows an error and does not persist; a mid-session 401 returns to the connect screen.
- No hand-edited/committed `.xcodeproj`.

## Work log
