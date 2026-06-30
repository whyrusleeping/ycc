---
id: "0070"
title: 'Improve ctrl+n quick-add backlog UI: echo user message in log, wrap log lines, reuse interactive question UI'
status: todo
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
