---
id: "0526"
title: 'Quick replies: editable daemon-wide list in Settings (web + iOS), project extras on top'
status: in_review
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - spec.md
---

## Description
Owner request after 0513/0514: quick replies should be editable from Settings. Decisions (owner): the built-ins become editable and removable defaults; there is one daemon-wide list plus optional per-project extras; the editor is on both iOS and web.

**Acceptance:** web Settings and iOS Settings edit the daemon-wide list: add, edit, remove, reorder, save. An edited default keeps its analytics id. Sessions offer the daemon-wide list and then the project's extras. The list persists in the daemon state dir, and an emptied list stays empty.

## Work log
- 2026-11-10: `GetQuickRepliesResponse.global` replaces `builtin`. `SetQuickRepliesRequest.global` replaces the daemon-wide list, stored at `<state>/ycc/quick-replies.json`; only default ids are kept. Web has a Settings › Quick replies section using a shared `QuickRepliesForm`, which the project dialog also uses. iOS has Settings › Quick replies (`QuickRepliesSettingsView` + YccKit `QuickRepliesEditorModel`), built by a subagent. Analytics kind is the default's id, else `global` or `project`. Tests pass: Go RPC test, web suite (435), YccKit (571), and xcodebuild test. Checked web save → chips in the browser, and the iOS editor plus swipe menu in the simulator. Left in_review for on-device use.

## Acceptance criteria
