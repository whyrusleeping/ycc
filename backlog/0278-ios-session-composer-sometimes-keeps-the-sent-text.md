---
id: "0278"
title: 'iOS: session composer sometimes keeps the sent text (autocorrect write-back)'
status: in_review
priority: 2
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs: []
---

## Description
User report: sending a message in an existing session on the iOS app sometimes leaves the typed text in the composer (the message *is* sent).

Root cause: `SessionView.send()` sets the `draft` @State to `""` synchronously, but when the keyboard is holding an uncommitted autocorrection (send tapped without accepting/dismissing the inline suggestion) UIKit commits that correction *after* the action returns and writes the corrected string back into the `TextField(axis: .vertical)` binding — repopulating the just-cleared composer. Known SwiftUI behaviour (same fix shape as GetStream/stream-chat-swiftui#955).

Fix (clients/ios/App/SessionView.swift): pulse `.autocorrectionDisabled(suppressAutocorrect)` true for one runloop turn on send so UIKit discards the pending correction, plus a deferred `draft = ""` on the next MainActor hop to mop up a write-back that lands first.

## Acceptance criteria
- Type a word that triggers autocorrect (e.g. `teh`), tap send without accepting the suggestion: the message sends AND the composer is empty.
- Same via the return key / hardware keyboard (`onSubmit`).
- Normal typing still gets autocorrection (the disable is only a one-turn pulse) and the keyboard/focus is not dismissed by sending.

Not verifiable on the Linux workspace (no Swift toolchain) — needs an on-device/simulator check.

## Work log
