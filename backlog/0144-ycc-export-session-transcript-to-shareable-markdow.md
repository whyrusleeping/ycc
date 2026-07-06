---
id: "0144"
title: ycc export — session transcript to shareable markdown
status: todo
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 18.6 Session history browser & reopen
---

## Description
Sessions are durable and browsable in the TUI, but can't leave the machine in readable form: no way to share "here's what the agent did and why" in a PR description, an issue comment, or a chat. The transcript renderer already folds tool plumbing and merges ask_user exchanges — reuse those semantics for a markdown export.

## Acceptance criteria
- [ ] `ycc export <session-id> [--out FILE]` writes a markdown transcript: turns, collapsed tool-call summaries, questions+answers folded per §18.3's one-block rule, review verdicts, commits (sha + message), final report, and a usage/cost footer.
- [ ] `--full` includes tool call payloads/results; default keeps the folded, human-readable view.
- [ ] Works for live and persisted sessions (same source as GetSessionTranscript).

## Work log
