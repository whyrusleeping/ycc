---
id: "0561"
title: 'iOS: publish a viewed file to the share server and manage its link'
status: in_review
priority: 2
created: "2026-11-18"
updated: "2026-11-18"
depends_on:
    - "0560"
spec_refs:
    - docs/design/publishing.md
---

## Description
Status: implemented and checked in the simulator; awaiting on-device use before it is done.

This is the iOS half of the user's file-publishing request (2026-11-18, this session). It has its own task because iOS changes are verified separately: a Mac build plus on-device use. 0560 (share server, daemon RPCs, web) supplies the RPCs.

## Acceptance criteria
- When the daemon reports a publish target, the iOS file viewer menu offers "Publish to <name>".
- After publishing, the link can be copied and shared through the system share sheet, and the viewer shows whether the current contents are already published.
- Published links for the file can be unpublished from the viewer.
- The change builds and its UI is checked in a simulator on the configured Mac.


## Work log
- 2026-11-18: YccClient gained `getPublications`, `publishFile` (on the 120 s bulk client because uploads can take up to 2 minutes) and `unpublishFile`. The new `FilePublicationsModel` (YccKit, 7 tests) holds the publication state, and the file viewer menu has the publish, copy, share, open, unpublish and earlier-versions items plus a bottom banner after publishing. `swift test` (647 passing), `xcodegen generate` and `xcodebuild build`/`test` pass on the Mac. The flows were checked in simulator ycc-review-C against a scratch daemon and a real ycc-share, including share sheet, unpublish confirmation, an error alert and dark mode.
