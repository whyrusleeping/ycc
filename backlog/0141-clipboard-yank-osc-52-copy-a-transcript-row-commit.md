---
id: "0141"
title: 'Clipboard yank (OSC 52): copy a transcript row / commit sha out of the TUI'
status: todo
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 18. Client UI (TUI)
---

## Description
There is no way to get text *out* of the TUI: a commit sha, an error message, a model's answer, a file path. Terminal-native selection fights the alt-screen and mouse handling. OSC 52 clipboard writes work locally and over SSH (which matters for the remote-daemon story) and Bubble Tea supports them.

## Acceptance criteria
- [ ] `y` on a selected transcript row copies its body text (the expanded content, stripped of styling) to the clipboard via OSC 52.
- [ ] On a `commit_made` row, `y` copies the sha; on an error row, the error text.
- [ ] A brief flash confirms the copy ("copied ✓"); documented in the help modal per the maintenance contract in help.go.
- [ ] Gated like other bare-letter keys: never intercepts while the input has content.

## Work log
