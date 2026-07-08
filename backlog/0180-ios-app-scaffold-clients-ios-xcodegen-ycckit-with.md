---
id: "0180"
title: 'iOS: app scaffold (clients/ios, XcodeGen + YccKit) with connect screen & Keychain auth'
status: done
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

## Plan

Scaffold the SwiftUI iPhone app per docs/design/ios-client.md §3/§5 (phase-1 step 1: connect screen).

1. YccKit SPM package (clients/ios/YccKit):
   - Package.swift: swift-tools 5.9+, platforms [.iOS(.v17), .macOS(.v14)] (macOS so `swift test` runs headless), targets YccProto (existing generated sources; deps: connect-swift's Connect + swift-protobuf) and YccKit (depends on YccProto), plus YccKitTests. Pin connect-swift (latest, ~> 1.x) and swift-protobuf.
   - Sources/YccKit/YccClient.swift: wrapper over Ycc_V1_SessionServiceClient — init(baseURL:token:), ProtocolClientConfig on URLSession with .connect protocol + JSON (or binary) codec, and an auth Interceptor attaching `Authorization: Bearer <token>` to unary AND stream requests. Expose typed async methods needed now (listProjects for validation) plus a generic accessor to the underlying generated client for later tasks. Map Connect .unauthenticated errors to a typed YccError.unauthorized so UI can distinguish 401.
   - Sources/YccKit/ConnectionStore.swift: @Observable (or plain class + protocol for testability). ServerProfile {id, name, baseURL}; profiles + activeProfileID persisted in UserDefaults (injectable UserDefaults for tests); token per profile in Keychain via a small KeychainStore protocol (real SecItem impl + in-memory fake for tests — Keychain isn't available in plain swift test reliably, so tests use the fake).
   - Tests/YccKitTests: connection-store logic (add/save/select/delete profile, token round-trip with fake keychain, no token in UserDefaults), request shaping: auth interceptor injects the bearer header (test the interceptor directly or via a stubbed transport/HTTPClientInterface capturing the HTTPRequest).
2. App shell (clients/ios/App): YccApp.swift (@main), RootView switching on connection state: ConnectView (base URL + token fields, Connect button → YccClient.listProjects; success → persist profile+token, show landing; Connect error .unauthorized → "invalid token", don't persist), LandingView listing project names as proof of authenticated round-trip, with a sign-out/change-server affordance. A mid-session unauthenticated error from the client surfaces via an AppModel that clears auth state → back to ConnectView.
3. project.yml (clients/ios): XcodeGen manifest — app target Ycc, iPhone-only (TARGETED_DEVICE_FAMILY=1), iOS 17.0 deployment, sources App/, package dependency on local YccKit package, Info.plist properties generated inline including NSAppTransportSecurity/NSAllowsArbitraryLoads=true with a comment documenting the tailnet-http rationale. Bundle id something like dev.ycc.ios (personal tool).
4. .gitignore: ignore clients/ios/*.xcodeproj (+ xcuserdata, .DS_Store, YccKit/.build).
5. Verify: `cd clients/ios/YccKit && swift test` (macOS); `xcodegen generate` then `xcodebuild -project clients/ios/Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build`.
6. Add plans/ios-client-smoke.md? — design says the smoke runbook lands with the first cut; include a minimal version covering connect-screen validation (can grow in task 0183).

Notes: xcodegen is being installed via brew. Network access is available for SPM to resolve connect-swift.

### Starting points
- docs/design/ios-client.md §3–§5 (layout, generated client, architecture)
- clients/ios/YccKit/Sources/YccProto/ycc/v1/ycc.connect.swift — generated Ycc_V1_SessionServiceClient (async methods incl. listProjects)
- buf.gen.swift.yaml at repo root (generation posture; do NOT regenerate)
- connect-swift: ProtocolClient(httpClient: URLSessionHTTPClient, config: ProtocolClientConfig(host:networkProtocol:codec:interceptors:)); interceptors are InterceptorFactory closures; UnaryInterceptor/StreamInterceptor protocols
- docs/remote-api.md — wire contract, bearer auth, 401 semantics
- Keychain: kSecClassGenericPassword, service e.g. 'dev.ycc.ios', account = profile id

## Work log
- 2026-07-08 plan: Scaffold the SwiftUI iPhone app per docs/design/ios-client.md §3/§5 (phase-1 step 1: connect screen).  1. YccKit SPM package (clients/ios/YccKit):    - Package.swift: swift-tools 5.9+, platforms [.i
…[truncated]
- 2026-07-08 context hints: 6 recorded with plan
- 2026-07-08 context hints: clients/ios/YccKit/Sources/YccProto/ycc/v1/ycc.connect.swift — generated Ycc_V1_SessionServiceClient (async methods incl. listProjects(request:headers:)); buf.gen.swift.yaml at repo root shows gener
…[truncated]
- 2026-07-08 implementer report: Scaffolded the SwiftUI iPhone app at clients/ios/ (phase-1 step 1: connect screen + authenticated landing) per docs/design/ios-client.md §3/§5.  ## What I added  **YccKit SPM package** (clients/ios/
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: claude
- 2026-07-08 review (claude): accept — The change fully scaffolds the SwiftUI iPhone app per docs/design/ios-client.md §3/§5. I verified all acceptance criteria empirically: `swift test` passes (11 tests), `xcodegen generate && xcodebuil
…[truncated]
- 2026-07-08 decision: accept — commit: iOS: app scaffold (clients/ios, XcodeGen + YccKit) with connect screen & Keychain auth (task 0180)
