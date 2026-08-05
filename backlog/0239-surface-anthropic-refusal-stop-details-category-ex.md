---
id: "0239"
title: Surface Anthropic refusal stop_details (category/explanation, fallback credit) via gollama
status: proposed
priority: 3
created: "2026-08-05"
updated: "2026-08-05"
depends_on: []
spec_refs: []
---

## Description
Anthropic refusals carry a `stop_details` object (`{type, category, explanation}`, arriving on the `message_delta` stream event) plus a `fallback_credit_token` that makes a manual retry on another model not re-pay prompt-cache cost. gollama (local checkout at ~/code/gollama) does not parse `stop_details` at all, so ycc's kind:"refusal" session_error cannot say WHY the classifier fired. Add StopDetails to gollama's response/stream assembly, plumb it into engine Result / the model_turn + session_error event data, and (optionally) redeem the fallback credit when SetRoleConfig auto-retries on a new model.

Acceptance criteria:
- gollama parses `stop_details` from both non-streaming responses and `message_delta` stream events.
- ycc includes the category/explanation in the refusal `session_error` msg when present.


## Acceptance criteria

## Work log
