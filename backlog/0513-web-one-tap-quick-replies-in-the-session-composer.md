---
id: "0513"
title: 'Web: one-tap quick replies in the session composer (go ahead, continue, commit & push, project actions)'
status: done
priority: 2
created: "2026-11-09"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - docs/design/web-client.md
---

## Description
**Evidence (2026-11-09 usage analysis):** the owner's most common follow-ups are short and repeated: "continue" (18), "commit and push" (≈25 variants), "yes"/"yep"/"go ahead" (≈20), and per-project actions such as "deploy to prod please" (36 deploy requests in oldgrowth alone). Typing them is the main cost of a check-in, especially on mobile.

**Change:**
- When an idle session's composer is empty, show a small row of quick-reply chips. Tapping one sends that text as a normal user message. Built-in chips: Go ahead, Continue, Commit & push.
- Projects can define extra chips (label + message text, e.g. Deploy → "Deploy to prod following the deploy runbook") in project config. They're served by the daemon so web and iOS show the same set, and editable from project settings.
- Chips are hidden while the session is running, when a pending ask_user question has its own sheet, and once the user starts typing.
- Record `composer.quick_reply` analytics with the chip kind (built-in id or `project`), never the text.

**Acceptance:** chips render and send on an idle session. Project-defined chips come from the daemon and are editable in settings. The hide conditions above are covered by component tests. The light/dark visual check passes. internal/web/dist is rebuilt and committed. The iOS follow-up task reuses the same daemon data.

## Acceptance criteria

## Work log
- 2026-11-10: Done. The daemon has `GetQuickReplies` (returns the built-ins with stable ids, then the project's list) and `SetQuickReplies` (replaces the project's list). Project replies are stored in the root workspace at `.ycc/quick-replies.json`: personal state, not committed project config. Web shows chips above the composer. They hide while running, while the answer panel is up, once text is typed, and right after a click. The composer has an Edit… link, and the Projects page ⋯ menu has a Quick replies… editor. Component tests cover the hide rules. Light/dark check done with the sidebar-v2 harness, and a live send was checked on a mock session. One reviewer found two issues, both fixed: a double-click could send twice, and the sole-project cache alias went stale.
- Commit: quick replies: daemon-served built-in + project replies, web composer chips and editor (0513)
