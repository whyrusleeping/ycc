---
id: "0342"
title: 'iOS model editor: test configured model with a real inference request'
status: in_review
priority: 2
created: "2026-09-04"
updated: "2026-09-04"
depends_on: []
spec_refs:
    - Models, credentials, and review tiers
    - Client behavior
    - docs/remote-api.md#DiscoverModels / TestModel
    - docs/design/ios-client.md#Settings
---

## Description
The existing **Discover models** action only proves that a provider's model-list endpoint is reachable. It does not validate the selected model id or the request shape ycc will actually use (authentication mode, reasoning parameters, generation endpoint, etc.). Add a **Test model** button to the iOS model editor.

## Acceptance criteria

- Add a daemon RPC that accepts a draft `ModelConfig` and tests it without saving or mutating the live registry. Secrets remain daemon-side: the request carries only `key_env`, and the daemon resolves the credential through the normal environment/secrets path.
- The probe uses the same backend construction and resolved thinking/effort settings as a real ycc turn, including the entered backend, base URL, auth mode, model id, and key reference. It sends one deliberately tiny real inference request with a bounded timeout/token cap; successful generation is sufficient and the UI makes clear that the provider may bill for it.
- The RPC returns a concise success result (optionally latency/model response metadata) or a sanitized, actionable provider error. It must never return/log the credential.
- In `ModelEditorView`, add a visible **Test model** button that tests the current unsaved form values. Disable it while required fields are missing or a test is in flight, show progress, and render inline success/failure. Editing a connection/model-affecting field clears a stale test result.
- The action works while adding, editing, or duplicating a model and does not require saving first. Testing a disabled draft is allowed because it is an explicit diagnostic action.
- Add Go coverage using local HTTP test providers for success, auth/model/request-shape validation, provider failure, timeout/cancellation, and no config persistence/mutation. Add headless YccKit tests for request mapping and success/error/in-flight state. Regenerate and commit both Go and Swift protobuf outputs.
- Update the spec/API/iOS settings documentation to distinguish **Discover models** (listing probe) from **Test model** (real inference probe).

## Work log

- 2026-09-04: Implemented `TestModel` across the daemon Connect API, generated Go/Swift bindings, YccKit, and the iOS model editor. The probe uses an isolated draft registry, normal context-aware backend construction and streaming request shape, resolved reasoning controls, a 64-token cap, and a 30-second daemon timeout; provider failures return categorical diagnostics without raw bodies or credentials. Save and Test share `ModelEditorDraft`; the UI tests unsaved/disabled drafts, shows progress/results, warns about billing, and cancels/invalidates stale probes. Added local-provider Go coverage (request/auth shape, empty stream, provider failure redaction, timeout/cancellation, no memory/disk mutation), headless Swift model/mapping tests, and docs/spec updates. `go test ./...`, focused race tests, repeated probe tests, `go vet`, `buf build`, `ycc spec-check`, and `git diff --check` pass. Read-only review accepted. Swift/Xcode execution remains for the user's Mac because this environment has no Swift toolchain.
