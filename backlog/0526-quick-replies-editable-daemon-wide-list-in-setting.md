---
id: "0526"
title: 'Quick replies: editable daemon-wide list in Settings (web + iOS), project extras on top'
status: done
priority: 2
created: "2026-11-10"
updated: "2026-11-27"
depends_on: []
spec_refs:
    - spec.md
---

## Description

Owner request after 0513/0514: quick replies should be editable from Settings. Decisions (owner): the built-ins become editable and removable defaults; there is one daemon-wide list plus optional per-project extras; the editor is on both iOS and web.

**Acceptance:** web Settings and iOS Settings edit the daemon-wide list: add, edit, remove, reorder, save. An edited default keeps its analytics id. Sessions offer the daemon-wide list and then the project's extras. The list persists in the daemon state dir, and an emptied list stays empty.

## Outcome

The global reply list persists edits, ordering and an intentionally empty list; project replies follow it. Browser and simulator save/reopen checks covered the original scope, and the owner confirmed the quick reply editor on device.

Commit: Close review of global and project quick replies
