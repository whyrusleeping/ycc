---
id: "0001"
title: Add unified Turn dispatch to gollama
status: done
priority: 1
created: 2026-06-25
updated: 2026-06-25
depends_on: []
spec_refs: ["Agent engine"]
---

## Description
gollama exposes per-backend completion methods (`ChatCompletion`,
`ChatCompletionAnthropic`, `ChatCompletionBedrock`, `Chat`). The agent loop should not
branch per provider. Add a single dispatch method that routes to the right backend based
on the client's configured mode and normalizes the tool-call + usage shapes.

## Acceptance criteria
- [ ] `Client.Turn(ctx, RequestOptions) (*ResponseMessageGenerate, error)` (name TBD)
      routes to anthropic/openai/bedrock/ollama correctly
- [ ] tool calls and usage are returned in one normalized shape regardless of backend
- [ ] a `Backend` accessor lets the registry introspect a client
- [ ] existing per-backend methods still work (no breaking changes)

## Work log
- 2026-06-25 implemented in `gollama/turn.go`: `Backend` enum + `Client.Backend()`
  introspection and `Client.Turn(opts)` as the canonical non-streaming entry point.
  Finding: `ChatCompletion` already dispatches Anthropic/Bedrock/OpenAI and normalizes
  tool calls into `Choices[].Message.ToolCalls`, so `Turn` is a thin, stable wrapper
  rather than a new dispatcher. `go build ./...` + `go vet ./...` pass. No breaking changes.
