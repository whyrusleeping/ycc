---
id: "0340"
title: 'iOS: paste images directly into the composer message box'
status: in_review
priority: 3
created: "2026-08-16"
updated: "2026-08-16"
depends_on: []
spec_refs: []
---

## Description
Pasting a copied image (screenshot, web image) into the message box attaches it as a picture, instead of requiring the Photos picker.

## Implementation

- `clients/ios/App/ComposerTextField.swift` (new): UIKit-backed composer field. `ComposerPasteTextView: UITextView` overrides `canPerformAction`/`paste` so the system Paste menu (and hardware ⌘V) offers itself whenever `UIPasteboard.general.hasImages`, and hands pasted images to the composer instead of splatting the companion URL string into the draft. User-initiated paste → no permission alert. The SwiftUI wrapper reimplements what `TextField` provided: placeholder, 1→maxLines growth then internal scroll (`sizeThatFits`), Bool focus binding (with deferred first-responder + `didMoveToWindow` retry), return-to-send via delegate, autocorrect-suppression pulse (`autocorrectionType` + `reloadInputViews`), `.disabled` via environment, `.roundedBorder`-style chrome.
- `PictureComposer.load(pasted:current:onError:)`: pasted images go through the same `normalizedJPEG` + `PictureAttachments.merged` pipeline as the picker (`pasted-N.jpg` filenames).
- `SessionView` + `NewSessionView`: `TextField` → `ComposerTextField`; `@FocusState` → `@State` Bool.
- Design doc note in docs/design/ios-client.md (composer attach paths share one normalize/merge pipeline).

## Acceptance (on device)

- [ ] Copy a screenshot → long-press in message box → Paste appears → image lands in the thumbnail strip (both live session and new-session composers)
- [ ] Copy an image from Safari → paste attaches the picture and does NOT insert its URL as text
- [ ] Text paste still works; return still sends (live session); return inserts newline in new-session composer
- [ ] No regressions: focus on appear (new session), keyboard avoidance, autocorrect-clear-after-send, field growth to 5/6 lines, disabled while starting
- [ ] Run `xcodegen generate` before building (new file in App/)

## Acceptance criteria

## Work log
