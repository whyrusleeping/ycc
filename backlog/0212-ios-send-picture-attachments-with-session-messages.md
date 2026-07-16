---
id: "0212"
title: 'iOS: send picture attachments with session messages'
status: in_review
priority: 2
created: "2026-07-16"
updated: "2026-07-16"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#6. Screens & feature phases
    - spec.md#12. RPC protocol
---

## Description
Add end-to-end image attachments for iOS session messages.

## Acceptance criteria
- [x] The iOS session composer can select one or more pictures from Photos, preview them, remove them, and send them with optional text.
- [x] SendInput carries bounded, validated image data and MIME type through the daemon into the agent conversation as native multimodal user content.
- [x] User-input events record attachment metadata without embedding image bytes in transcript JSON, and the iOS transcript visibly indicates attached pictures.
- [x] Unsupported/oversized images fail with a clear user-facing error; existing text-only clients remain compatible.
- [ ] Go tests, YccKit Swift tests, and the iOS simulator build cover/pass the new path.

## Work log
- 2026-07-16: Implemented Photos picker + previews/removal, bounded protobuf attachments, daemon signature validation, multimodal engine/provider plumbing, metadata-only transcript events, replay marker, docs, and Go/Swift unit coverage. `go test ./...` passes. Swift/Xcode verification is pending because this workspace has no `swift`/Xcode toolchain.
