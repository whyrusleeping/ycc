---
id: "0284"
title: 'Live smoke: codex stateless reasoning replay against the real ChatGPT backend'
status: proposed
priority: 2
created: "2026-08-07"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - Backends & model registry
---

## Description
Task 0197 changed the Codex wire format (adds `include: ["reasoning.encrypted_content"]` and replays `reasoning` input items with provider ids ahead of message/function_call items). It is fully unit-tested against synthetic SSE, but never exercised against the real `chatgpt.com/backend-api/codex/responses` endpoint.

Run a real ChatGPT-subscription session (2+ consecutive tool calls, then reopen it) and confirm the backend accepts the new input shape.

## Acceptance criteria
- [ ] A live codex session runs at least two consecutive tool-call turns with no 400 (specifically no "Item 'rs_…' of type 'reasoning' was provided without its required following item" and no unknown-parameter error for `include`).
- [ ] Reopening that session (ResumeSession) and continuing works — the replayed reasoning items are accepted.
- [ ] A model switch mid-session (codex → another model → back) does not 400.
- [ ] Findings recorded in the task; if the backend rejects any part, capture the exact error body and open a fix task.

## Work log
