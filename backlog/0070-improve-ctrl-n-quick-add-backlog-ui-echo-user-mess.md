---
id: "0070"
title: 'Improve ctrl+n quick-add backlog UI: echo user message in log, wrap log lines, reuse interactive question UI'
status: done
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
The ctrl+n "quick add" backlog capture UI has a few UX problems that make it hard to follow the conversation with the capture agent.

## Problems
1. **User message disappears after Enter.** When the user submits a message in the quick-add UI, the typed text is cleared and is NOT shown in the log/transcript. The user's own messages should be appended to the log so the conversation history is visible.
2. **Log lines don't wrap.** Long lines in the log overflow / get truncated instead of wrapping to the available width, making them hard to read.
3. **Clarifying questions use a different UI.** When the capture agent calls `ask_clarification`, the quick-add UI should reuse the same "interactive question UI" component that the main agents already use, rather than a bespoke prompt.

## Acceptance criteria
- After sending a message in the ctrl+n quick-add UI, the submitted user text is echoed/appended into the log shown in that view.
- Log lines wrap to the available width instead of being truncated or overflowing.
- Clarifying questions from the capture agent are rendered using the same interactive question UI component used by the main agents.
- Existing quick-add capture flow still works end-to-end (message → agent response → task created).

## Acceptance criteria

## Work log
- 2026-06-30 plan: Fix three UX issues in the ctrl+n quick-add capture overlay (all in internal/tui/tui.go), plus tests.  1) Echo user messages into the log (updateCapture enter handler, ~lines 1315-1335):    - Before l
…[truncated]
- 2026-06-30 implementer report: All changes complete and verified. Here's the summary:  ## Changes to `internal/tui/tui.go`  **Problem 1 — echo user messages:** - Added `userInputEvent(text)` helper that builds a synthetic `{Actor
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change addresses all three UX problems in the ctrl+n quick-add capture overlay. (1) User messages are echoed via a synthetic `userInputEvent` appended to `captureLog` on both stage-0 and stage-1 E
…[truncated]
- 2026-06-30 decision: accept — commit: Improve ctrl+n quick-add capture overlay: echo user messages, wrap log lines, reuse shared question UI  - Echo the user's submitted text into the capture transcript (synthetic   user_input event) and 
…[truncated]
