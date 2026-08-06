---
id: "0257"
title: 'Pictures on session start: StartSessionRequest.images + iOS new-session picker'
status: in_review
priority: 3
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs: []
---

## Description
The iOS new-session composer has no picture attachment button, because `StartSessionRequest` cannot carry images — only `SendInputRequest` can (spec §12 / proto `SendInputRequest.images`). Starting a session whose whole point is a screenshot therefore requires starting a blank session first and sending the picture as a second turn, which wastes the first agent turn.

## Scope

- proto: `StartSessionRequest` gains `repeated ImageAttachment images = 7;` (same message type as SendInput); regenerate Go + Swift.
- daemon: `Server.StartSession` validates the attachments with the SAME rules as `SendInput` (≤4, 1 byte..5 MiB each, jpeg/png/gif/webp with sniffed content matching the declared media type) → `InvalidArgument` on violation; `session.Config` gains `Images []engine.Image`.
- session: the initial `user_input` event records image METADATA only (media_type/filename), like `SendInputMessage`, so events.jsonl never stores bytes and `replayUserText` already annotates a reopened session; the seed posts a multimodal message (`Loop.PostMessage`) instead of `Loop.Seed` when images are present.
- iOS: extract the picker/thumbnail strip from `SessionView` into a shared composer component and use it in `NewSessionView`; `NewSessionModel.start()` sends the images with `StartSession`.
- docs: spec §12 (RPC surface) + docs/design/ios-client.md new-session composer.

## Acceptance criteria

- [ ] `StartSession` with 1–4 valid pictures starts a session whose first model message carries native image blocks
- [ ] invalid attachments (too many, oversize, wrong/mismatched media type, empty) fail with `InvalidArgument` and no session is created
- [ ] the initial `user_input` event carries `images: [{media_type, filename}]` and NO base64 bytes
- [ ] reopening such a session replays the prompt with the "picture attachments unavailable" note (existing `replayUserText`)
- [ ] iOS new-session composer shows the photo button, previews/removes up to 4 pictures, and starts the session with them
- [ ] `go test ./...` passes; YccKit tests pass on the Mac

## Work log
