---
id: "0191"
title: 'iOS: fix keychain save failure on connect (KeychainError error 0 / unsigned-build entitlement)'
status: done
priority: 1
created: "2026-07-08"
updated: "2026-07-08"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#5. App architecture
---

## Description
Bug reported by the user: connecting to a daemon (valid URL + token, validation round-trip succeeds) fails with "The operation couldn't be completed: YccKit.KeychainError error 0" — i.e. `KeychainError.unexpectedStatus` thrown by `SystemKeychainStore.setToken` when persisting the token after validation.

Likely root cause: `clients/ios/project.yml` sets `CODE_SIGNING_ALLOWED: NO`; an unsigned app carries no application-identifier entitlement, so simulator keychain access fails with errSecMissingEntitlement (-34018).

## Acceptance criteria
- Connecting from the simulator succeeds and the token persists across app relaunch (keychain write works).
- `KeychainError` conforms to `LocalizedError` with a human-readable message including the OSStatus (no more "error 0").
- Headless agent verification still passes: `swift test` and `xcodegen generate && xcodebuild ... -destination 'generic/platform=iOS Simulator' build`.
- Regression coverage for the keychain round-trip on-simulator if practical (e.g. a small test target run via `xcodebuild test`), else documented manual verification.

## Plan

Fix the keychain save failure on connect, reported by the user (simulator, valid daemon + token; ListProjects validation succeeds; SystemKeychainStore.setToken throws "YccKit.KeychainError error 0").

1. Root cause: clients/ios/project.yml sets CODE_SIGNING_ALLOWED: NO. An unsigned simulator app has no application-identifier entitlement, so SecItemAdd fails with errSecMissingEntitlement (-34018). Fix: allow code signing for simulator builds (ad-hoc "Sign to Run Locally" needs no team/account). Verify empirically that the headless agent build (`xcodebuild -destination 'generic/platform=iOS Simulator' build`) still succeeds with signing enabled; if the generic destination misbehaves, scope the setting per-sdk (e.g. keep signing disabled only where it must be) — but prefer the simplest config that both builds headless AND produces a keychain-capable app.
2. Diagnosability: make KeychainError conform to LocalizedError with clear messages that include the OSStatus (e.g. "Keychain write failed (OSStatus -34018)"), so any future failure isn't an opaque "error 0". Consider mapping the specific -34018 case to a hint ("app is missing its signing entitlements").
3. Optionally add kSecAttrAccessible = kSecAttrAccessibleAfterFirstUnlock to stored items (sane default for a client that may refresh in background).
4. Regression verification: prove the fix on-simulator, e.g. boot a simulator (xcrun simctl boot), build+install the app, and exercise a keychain round-trip — the cleanest automated form is a small unit-test bundle target in project.yml (YccAppTests) with one SystemKeychainStore round-trip test run via `xcodebuild test -destination 'platform=iOS Simulator,name=...'`; if that's disproportionate, at minimum verify the app installs and a keychain probe works, and document the manual step.
5. Keep swift test green; don't commit generated Ycc.xcodeproj / App/Info.plist.

### Starting points
- clients/ios/project.yml:38 — CODE_SIGNING_ALLOWED: NO (suspected root cause: unsigned app → no application-identifier entitlement → SecItemAdd -34018 on simulator)
- clients/ios/YccKit/Sources/YccKit/KeychainStore.swift — SystemKeychainStore.setToken throws KeychainError.unexpectedStatus(OSStatus); KeychainError lacks LocalizedError
- clients/ios/App/ConnectView.swift:82 — falls through to error.localizedDescription, which prints 'YccKit.KeychainError error 0'
- clients/ios/App/AppModel.swift connect() → ConnectionStore.saveProfile → keychain.setToken
- verify: cd clients/ios/YccKit && swift test; cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build; simulator run via xcrun simctl if needed

## Work log
- 2026-07-08 plan: Fix the keychain save failure on connect, reported by the user (simulator, valid daemon + token; ListProjects validation succeeds; SystemKeychainStore.setToken throws "YccKit.KeychainError error 0"). 
…[truncated]
- 2026-07-08 context hints: 5 recorded with plan
- 2026-07-08 context hints: clients/ios/project.yml:38 — CODE_SIGNING_ALLOWED: NO (suspected root cause: unsigned app → no application-identifier entitlement → SecItemAdd -34018 on simulator); clients/ios/YccKit/Sources/Yc
…[truncated]
- 2026-07-08 implementer report: Fixed the iOS keychain save failure on connect ("KeychainError error 0" / OSStatus -34018 errSecMissingEntitlement).  ## Root cause `clients/ios/project.yml` set `CODE_SIGNING_ALLOWED: NO`. An unsigne
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: claude
- 2026-07-08 review (claude): accept — The change correctly fixes the iOS keychain-save-on-connect failure. Root cause (unsigned simulator build → no application-identifier entitlement → errSecMissingEntitlement -34018) is addressed by
…[truncated]
- 2026-07-08 decision: accept — commit: iOS: fix keychain save on connect — enable ad-hoc signing, readable KeychainError, on-simulator regression test (task 0191)
