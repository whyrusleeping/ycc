---
id: "0170"
title: 'gollama: native SSE streaming for OpenAI-compatible + Ollama backends (TurnStream parity)'
status: done
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 7. Agent engine
    - 18.4 Reasoning (thinking) in the event stream
---

## Description
Task 0120 landed `TurnStream` (gollama 4140920) with native SSE streaming for Anthropic only; OpenAI-compatible and Ollama backends currently use the graceful fallback (blocking Turn + one whole-text snapshot delta). The task 0120 description explicitly scoped these as follow-ups.

Implement native streaming for the OpenAI-compatible `/chat/completions` path (`stream: true` chunked deltas, incl. tool-call argument assembly) and Ollama, preserving TurnStream's contract: snapshot-semantics onDelta, final message byte-equivalent to the non-streaming shape, Turn untouched. Work happens in /home/why/code/gollama (clone-into-workspace pattern from task 0120's plan), then bump ycc's go.mod.

## Acceptance criteria
- [ ] OpenAI-compatible TurnStream streams natively (offline recorded-SSE tests: text deltas, tool-call assembly, usage; DeepEqual to non-streaming shape)
- [ ] Ollama path streams (native or via its OpenAI-compatible endpoint — document the choice)
- [ ] Fallback still covers Bedrock; no caller-visible API change
- [ ] ycc go.mod bumped; ycc build/vet/test green

## Plan

Goal: native SSE streaming for the OpenAI-compatible /chat/completions path in gollama's TurnStream, which also covers Ollama (its /v1 endpoint IS the OpenAI-compatible path — Turn already routes Ollama through it; document that choice). Bedrock keeps the graceful fallback. Then bump ycc's gollama dep and touch spec §7.1.

WORKSPACE MECHANICS (established pattern from task 0120):
1. git clone /home/why/code/gollama .gollama-work (already in .git/info/exclude). Edit ONLY in .gollama-work; .gollama-link is the read-only view. Build/test with `cd .gollama-work && go build ./... && go vet ./... && go test ./...` (ANTHROPIC_API_KEY is set, so Anthropic live tests run — they should stay green).
2. When done: commit in .gollama-work, then `git -C /home/why/code/gollama pull --ff-only /home/why/code/ycc/.gollama-work main` and `git -C /home/why/code/gollama push origin main`. Record the sha.
3. ycc bump: `GOPRIVATE=github.com/whyrusleeping go get github.com/whyrusleeping/gollama@<sha> && go mod tidy`.
4. Cleanup: rm -rf .gollama-work at the very end.

PART A — gollama:

A1. Shared request builder (byte-identical requests streaming vs not): extract the OpenAI-path request construction from ChatCompletion (openai.go:78-180 — system-message injection, tool-param normalization, top-level option promotion, Ollama think vs OpenAI reasoning_effort translation, ExtraBody merge) into `buildOpenAIRequest(c *Client, opts RequestOptions) (any, error)` returning the final body (post-ExtraBody merge). ChatCompletion calls it and keeps its current decode+normalize; behavior unchanged (drop the "streaming not yet supported" error — TurnStream is the streaming entry point; ChatCompletion may keep rejecting opts.Stream or force it false, keep Turn semantics untouched).

A2. openai_stream.go: `chatCompletionOpenAIStream(opts, onDelta)`:
- Build request via buildOpenAIRequest with Stream=true and add `stream_options: {"include_usage": true}` (new `StreamOptions *openaiStreamOptions json:"stream_options,omitempty"` field on openaiRequest, set only when streaming; supported by OpenAI, Ollama /v1, llama.cpp, vLLM — needed to get the final usage chunk).
- POST via prepareRequest (same retry-on-429/503 pre-stream behavior as Anthropic streaming), read SSE with bufio.Reader (data lines can be long): `data: {chunk}` lines, terminate on `data: [DONE]` or EOF.
- Chunk shape (chat.completion.chunk): choices[0].delta{role, content, reasoning, reasoning_content, tool_calls[{index, id, type, function{name, arguments}}]}, choices[0].finish_reason, top-level model, usage (final chunk, choices may be empty). Assemble:
  * content deltas → append; fire onDelta with the full accumulated text SNAPSHOT each time (contract parity with Anthropic path).
  * reasoning / reasoning_content deltas → accumulate separately; do NOT include in onDelta snapshots (matches Anthropic: thinking not streamed to onDelta).
  * tool_calls: keyed by delta index — id/type/function.name arrive on the first fragment, function.arguments concatenates across fragments (it's a raw string in gollama's ToolCallFunction.Arguments, no unmarshal needed).
  * finish_reason → GenChoice.FinishReason; usage chunk → Usage.
  * Also tolerate a mid-stream `data: {"error": {...}}` object → return an error.
- Final ResponseMessageGenerate must match the non-streaming shape: Model, Choices[0]{Index, Message{Role, Content, Thinking, ToolCalls}, FinishReason}, Usage; apply the same reasoning normalization as ChatCompletion (fold reasoning into Thinking when Thinking empty, clear Reasoning).

A3. turn.go routing: BackendAnthropic → anthropic stream (unchanged); BackendBedrock → fallback (extract the existing blocking-Turn-one-snapshot fallback into a small helper `turnStreamFallback`); default (BackendOpenAI, BackendOllama) → chatCompletionOpenAIStream. Update the TurnStream doc comment: OpenAI-compatible + Ollama now stream natively (Ollama via its OpenAI-compatible /v1 endpoint — record the choice here per the acceptance criteria); Bedrock falls back.

A4. Offline tests (openai_stream_test.go, httptest server emitting scripted SSE; capture request body):
 a) rich stream: multi-chunk text + reasoning deltas + a tool call whose arguments are split across ≥3 fragments + a second tool call at index 1 + finish_reason + usage chunk + [DONE] → assert reflect.DeepEqual of the streamed result vs ChatCompletion decoding the equivalent non-streaming JSON (byte-equivalent final shape); assert snapshots are monotonically growing prefixes and the last equals final Content; assert request body had stream:true and stream_options.include_usage:true.
 b) Ollama backend (client base URL containing :11434/v1): streams natively; request body carries think:true (when opts.Think) and NO reasoning_effort; reasoning folded into Thinking.
 c) tool-only turn: onDelta never called; tool calls assembled correctly.
 d) mid-stream error object → error returned.
 e) fallback: existing TestTurnStreamFallback in anthropic_stream_test.go currently uses a non-Anthropic client to prove one-whole-text-delta fallback — that client now streams natively, so repurpose it: test turnStreamFallback (the helper) directly against a mock /chat/completions to preserve the one-snapshot contract, and keep TurnStream routing for Bedrock pointing at the helper (Bedrock itself needs AWS creds, not mockable offline — routing is verified by code inspection/unit of the helper).
 f) go build/vet/test green including existing Anthropic offline+live tests.

A5. Optional live smoke: if ollama_live_test.go's guard env is satisfied locally, extend with a TurnStream case; otherwise add the guarded test anyway (skips cleanly).

PART B — ycc:
- Bump go.mod to the new gollama sha (currently c92bd8b), go mod tidy.
- spec.md §7.1 item 3 (~line 412-417): update the backend-support sentence — Anthropic (Messages SSE), OpenAI-compatible and Ollama (chat-completions chunk stream over Ollama's /v1 endpoint) stream natively; Bedrock falls back to a blocking turn delivered as one snapshot.
- Run go build ./... && go vet ./... && go test ./... in ycc; all green. No ycc code change expected (TurnStream signature unchanged; the compile-time StreamTurner assertion still holds).

Acceptance mapping: A2+A4a/c/d → OpenAI native streaming w/ recorded-SSE tests + DeepEqual; A3+A4b → Ollama path documented + tested; A3+A4e → Bedrock fallback preserved, no API change; Part B → go.mod bump + green build.

### Starting points
- gollama sources readable via .gollama-link/ (READ-ONLY); write via clone at .gollama-work/
- gollama turn.go:83-101 TurnStream routing + fallback; anthropic_stream.go is the model to mirror (assembler + shared converter pattern)
- gollama openai.go:67-210 ChatCompletion OpenAI path — extract request building; note reasoning→Thinking normalization at :201-207 must be replicated for streamed result
- gollama tools.go:34-43 ToolCall/ToolCallFunction (Arguments is a raw string)
- gollama anthropic_stream_test.go — offline SSE test style (httptest + scripted body); TestTurnStreamFallback at :167 needs repurposing
- gollama http.go prepareRequest — pre-stream retry handling, reuse as-is
- ycc spec.md:412-417 — §7.1 item 3 wording to update
- push pattern: commit in .gollama-work; git -C /home/why/code/gollama pull --ff-only /home/why/code/ycc/.gollama-work main; push origin main; then GOPRIVATE=github.com/whyrusleeping go get github.com/whyrusleeping/gollama@<sha>

## Work log
- 2026-07-06 plan: Goal: native SSE streaming for the OpenAI-compatible /chat/completions path in gollama's TurnStream, which also covers Ollama (its /v1 endpoint IS the OpenAI-compatible path — Turn already routes Ol
…[truncated]
- 2026-07-06 context hints: 8 recorded with plan
- 2026-07-06 context hints: gollama sources readable via .gollama-link/ (READ-ONLY); write via clone at .gollama-work/; gollama turn.go:83-101 TurnStream routing + fallback; anthropic_stream.go is the model to mirror (SSE assemb
…[truncated]
- 2026-07-06 implementer report: Implemented native SSE streaming for the OpenAI-compatible /chat/completions path (covering both OpenAI and Ollama) in gollama's TurnStream, achieving TurnStream parity with the Anthropic path. Bedroc
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change implements native SSE streaming for the OpenAI-compatible /chat/completions path (covering both OpenAI and Ollama via its /v1 endpoint) in gollama's TurnStream, matching the Anthropic path'
…[truncated]
- 2026-07-06 decision: accept — commit: gollama native SSE streaming for OpenAI-compatible + Ollama TurnStream (task 0170)  gollama 8653ccb adds native /chat/completions chunk streaming to TurnStream for OpenAI-compatible endpoints and Olla
…[truncated]
- 2026-07-06 usage: 56,861 tok (in 180, out 56,681, cache_r 3,953,257, cache_w 205,540) · cost n/a (unpriced)
  implementer: 34,620 tok (in 100, out 34,520, cache_r 2,648,101, cache_w 75,656) · cost n/a (unpriced)
  coordinator: 14,289 tok (in 44, out 14,245, cache_r 890,291, cache_w 87,106) · cost n/a (unpriced)
  reviewer:Claude: 7,952 tok (in 36, out 7,916, cache_r 414,865, cache_w 42,778) · cost n/a (unpriced)
- 2026-07-06 usage: 56,978 tok (in 182, out 56,796, cache_r 3,953,257, cache_w 272,414) · cost n/a (unpriced)
  implementer: 34,620 tok (in 100, out 34,520, cache_r 2,648,101, cache_w 75,656) · cost n/a (unpriced)
  coordinator: 14,406 tok (in 46, out 14,360, cache_r 890,291, cache_w 153,980) · cost n/a (unpriced)
  reviewer:Claude: 7,952 tok (in 36, out 7,916, cache_r 414,865, cache_w 42,778) · cost n/a (unpriced)
