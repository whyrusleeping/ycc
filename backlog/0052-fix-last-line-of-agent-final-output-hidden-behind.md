---
id: "0052"
title: 'Fix: last line of agent final output hidden behind input box'
status: todo
priority: 2
created: "2026-06-27"
updated: "2026-06-27"
depends_on: []
spec_refs: []
---

## Description
## Problem

The final line of the agent's "final" output in the session view gets obscured/hidden behind the user input text box. This appears to be a layout/scrolling issue where the chat-log viewport height doesn't fully account for the input box, so the bottom-most line is clipped.

## Acceptance Criteria

- The last line of the agent's final output is fully visible above the input text box.
- No content is clipped behind the input box at the bottom of the session view.
- Verify across cases where output ends exactly at the viewport boundary (single-line and multi-line final messages).
- Layout remains correct on terminal resize.

## Acceptance criteria

## Work log
