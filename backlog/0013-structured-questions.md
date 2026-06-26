---
id: "0013"
title: Structured interactive ask_user questions (option pickers)
status: done
priority: 2
created: "2026-06-26"
updated: "2026-06-26"
depends_on:
    - "0006"
spec_refs:
    - Client UI (TUI)
    - Tools
    - RPC protocol
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
- 2026-06-26 plan: Implement structured ask_user with option pickers end-to-end:  1. Tool schema: extend the `ask_user` tool definition to expose an optional `options` array (list of suggested string answers) alongside 
…[truncated]
- 2026-06-26 revision: Implemented structured interactive `ask_user` questions (option pickers) end-to-end. All code compiles, `go test ./...` passes, and proto was regenerated with `buf generate`.  ## Changes by file  - **
…[truncated]
- 2026-06-26 review (claude): accept — The change implements structured interactive ask_user questions end-to-end and satisfies all acceptance criteria. The tool schema exposes an optional `options` array (StrArrProp), `question_asked` eve
…[truncated]
- 2026-06-26 revision: Removed the out-of-scope work and confirmed the build remains green.  ## Cleanup performed - Deleted `backlog/0018-save-rerun-plans.md` (via `git rm`) — it was an unrelated backlog task not part of 
…[truncated]
- 2026-06-26 decision: accept — commit 0e36d4c: feat: structured ask_user with option pickers (0013)  Add optional `options` to the ask_user tool schema, propagate them through question_asked events, and let AnswerQuestion accept a chosen option in
…[truncated]
