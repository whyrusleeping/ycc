---
id: "0400"
title: 'gollama SSE assemblers: reject streams that end without a terminal event'
status: done
priority: 2
created: "2026-09-25"
updated: "2026-09-25"
depends_on:
    - "0363"
spec_refs: []
---

## Description
Found in 0363 review: gollama's OpenAI chat-completions (openai_stream.go assembleOpenAIStream) and Anthropic (anthropic_stream.go assembleAnthropicStream) SSE assemblers return a successful response on a clean EOF after partial deltas, without a terminal marker (`[DONE]`/finish_reason for OpenAI, `message_stop` for Anthropic). A proxy disconnect can therefore commit truncated output as a successful turn. ycc's in-repo Codex parser was fixed in 0363 (requires response.completed, wraps io.ErrUnexpectedEOF).

## Acceptance criteria
- gollama OpenAI and Anthropic stream assemblers return an error wrapping io.ErrUnexpectedEOF when the body ends before a terminal event (OpenAI: `[DONE]` or a chunk with non-null finish_reason; Anthropic: `message_stop`).
- gollama tests cover truncated and complete streams for both; push to gollama main (sibling ../gollama) and bump ycc go.mod.
- ycc: an engine/config test via Registry.Build against an httptest SSE server shows a truncated stream is a retryable failure, not a committed turn.

## Work log
