---
id: "0013"
title: Structured interactive ask_user questions (option pickers)
status: todo
priority: 2
created: 2026-06-26
updated: 2026-06-26
depends_on: ["0006"]
spec_refs: ["Client UI (TUI)", "Tools", "RPC protocol"]
---

## Description
Give the agent a Claude-Code-style Q&A loop: `ask_user` can offer selectable options,
and the client renders a picker instead of forcing every clarification into prose. The
`Asker.Ask(ctx, question, options)` interface already carries options; the tool schema,
events, and answer RPC need to surface them end-to-end. See spec §18.3 and §8.

## Acceptance criteria
- [ ] `ask_user` tool schema exposes an optional `options` (list of suggested answers)
- [ ] `question_asked` events carry `options`
- [ ] `AnswerQuestion` accepts a chosen option (index/value) or free text
- [ ] TUI renders a navigable picker when options are present, with an "other…" escape
      to the multiline textarea; plain textarea when absent
- [ ] coordinator prompt nudges using options for crisp choices

## Work log
