---
id: "0284"
title: 'Live smoke: codex stateless reasoning replay against the real ChatGPT backend'
status: done
priority: 2
created: "2026-08-07"
updated: "2026-09-17"
depends_on: []
spec_refs:
    - Backends & model registry
---

## Description

Task 0197 changed the Codex wire format (adds `include: ["reasoning.encrypted_content"]` and replays `reasoning` input items with provider ids ahead of message/function_call items). Synthetic SSE tests cover the wire format, and 0175/0291 record narrower live checks, but those do not establish the complete consecutive-tool-turn, durable-reopen and model-switch matrix below against `chatgpt.com/backend-api/codex/responses`. Use existing authorized credentials and a bounded scratch session; record missing prerequisites rather than inventing live evidence. Sanitize captured errors so credentials and opaque provider state are not copied into the backlog.

Run a real ChatGPT-subscription session (2+ consecutive tool calls, then reopen it) and confirm the backend accepts the new input shape.

## Acceptance criteria

- [x] A live codex session runs at least two consecutive tool-call turns with no 400 (specifically no "Item 'rs_…' of type 'reasoning' was provided without its required following item" and no unknown-parameter error for `include`).
- [x] Reopening that session (ResumeSession) and continuing works — the replayed reasoning items are accepted.
- [x] A model switch mid-session (codex → another model → back) does not 400.
- [x] Findings recorded in the task; if the backend rejects any part, capture the exact error body and open a fix task.

## Outcome

Real ChatGPT Codex smoke passed: two sequential single-Read tool turns with durable opaque response-item state; stopped and reopened through ResumeSession with successful continuation; sol→claude→sol completed without session errors or HTTP 400. Bounded temporary harness (8192 tokens/turn, five-turn cap, four-minute stage deadlines) used configured subscription backends and was removed afterward. No rejection or fix task. Evidence: session s_b69f78ef18a6c299 events 233 (harness) and 237 (all stages PASS). Focused Codex replay/block-safety and engine cross-backend/context-selection tests passed; independent review accepted.

Commit: Verify live Codex tool-turn replay, durable resume, and model switching
