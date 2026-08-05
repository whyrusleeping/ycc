---
id: "0243"
title: 'iOS: fix silently dropped picture attachments in the session composer'
status: in_review
priority: 1
created: "2026-08-05"
updated: "2026-08-05"
depends_on: []
spec_refs: []
---

## Description

Pictures picked in the iOS session composer were silently discarded: the thumbnails never appeared and `SendInput` went out with text only, no error shown. Reported from the field — two attempts to send a screenshot through the app arrived as text-only messages.

**Root cause.** `SessionView.loadPictures` cleared the Photos-picker selection in a `defer`:

```swift
.onChange(of: photoItems) { _, items in Task { await loadPictures(items) } }

private func loadPictures(_ items: [PhotosPickerItem]) async {
    loadingPictures = true
    defer { loadingPictures = false; photoItems = [] }   // re-fires onChange
    ...
    pictures = loaded                                    // 2nd pass: loaded == []
}
```

Clearing `photoItems` re-fired `onChange` with an empty selection, and the second pass assigned `pictures = []` — wiping the round that had just loaded. Deterministic: every attachment was destroyed between picking and sending.

A second latent defect on the same path: pictures were encoded with a single `jpegData(compressionQuality: 0.85)` at full resolution and rejected outright above 5 MiB, so a modern high-megapixel camera-roll original could fail even when a perfectly good downscaled version would fit.

The daemon side was verified correct (`validateInputImages` + `engine.UserMessage.Images` → native multimodal blocks); this was purely a client bug.

## Acceptance criteria

- [x] Picking pictures leaves them attached until sent or explicitly removed; an empty picker round never clears the draft.
- [x] Merge/capacity rules extracted to `PictureAttachments` (YccKit) with headless tests, including the empty-round regression.
- [x] Successive picks accumulate up to the 4-picture cap instead of replacing the previous round.
- [x] Pictures are downscaled (2048px long edge, orientation baked in) and quality-stepped to fit the 5 MiB cap rather than failing.
- [x] A failed round keeps previously attached pictures instead of clearing them.
- [x] The composer button shows attachment state (badge + count) and disables at capacity.
- [ ] Verified on device: attach a screenshot, send it, and confirm the model receives the image.

## Work log
- Root-caused the self-cancelling `onChange`/`defer` cycle in `SessionView.loadPictures`; the daemon path (`internal/server.validateInputImages`, `engine.UserMessage.Images`) was confirmed correct and untouched.
- Added `PictureAttachments` (YccKit): `room` / `isFull` / `merged`, where an empty round is a no-op by construction, plus `PictureAttachmentsTests` covering the regression, accumulation, and the cap.
- Rewrote `loadPictures` to guard empty rounds, merge instead of replace, preserve the draft on error, and normalize via a new `normalizedJPEG` (downscale to 2048px through `UIGraphicsImageRenderer`, stepping quality 0.85 → 0.4 until under the cap).
- Composer affordance: `photo.badge.checkmark` in green with an attached count, an inline spinner while loading, and the picker disabled at capacity.
- NOT YET VERIFIED on device — no Swift toolchain on the workspace machine.
