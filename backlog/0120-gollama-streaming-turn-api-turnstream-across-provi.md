---
id: "0120"
title: 'gollama: streaming turn API (TurnStream) across providers'
status: done
priority: 3
created: "2026-07-02"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 7. Agent engine
    - 18.4 Reasoning (thinking) in the event stream
---

## Description
Delivers incremental model-output streaming in the session view end to end. Originally
the gollama-only prerequisite for ycc task 0114; per pm grooming 2026-07-08, 0114's small
remainder (the ycc adapter + live verification) was **folded into this task** and 0114
closed as merged. gollama work happens in the separate repo (/home/why/code/gollama);
this task tracks it from the ycc backlog, like earlier gollama thinking-levels work.

ycc-side groundwork is already done (tasks 0128/0129): the transient never-persisted
`turn_delta` broadcast path, the engine `StreamTurner` seam (throttled, snapshot
semantics), and the TUI live tail row. The engine loop type-asserts `StreamTurner`, so
once the gollama client implements TurnStream, adoption is a small adapter.

gollama's `Turn` is hard-wired non-streaming (`turn.go`: `opts.Stream = false`; no
SSE/delta handling in any provider path). Add a streaming variant — e.g.
`TurnStream(opts RequestOptions, onDelta func(Delta)) (*ResponseMessageGenerate, error)` —
that:
- streams partial assistant text (and ideally thinking deltas) via the callback/channel
  while the turn runs, then returns the same normalized final `ResponseMessageGenerate`
  as `Turn` (so callers can treat the final result identically);
- implements provider SSE/streaming for **Anthropic** first (the primary ycc backend),
  then OpenAI-compatible and Ollama as follow-ups;
- **falls back gracefully**: for providers without streaming support, performs a blocking
  turn and delivers the whole text as one delta — callers never need to branch;
- keeps tool-call turns correct (tool_use blocks assembled from the stream match the
  non-streaming shape; thinking block signatures round-trip intact).

## Acceptance criteria
- [x] TurnStream exists with the fallback behavior; `Turn` semantics unchanged
- [x] Anthropic SSE streaming implemented and covered by offline tests (recorded stream fixtures) + a live smoke test
- [x] final message from a streamed turn is byte-equivalent (content/tool calls/thinking blocks) to the non-streaming shape for the same response
- [x] ycc can adopt it by swapping the `engine.Turner` call site (no other API churn)
- [x] (folded from 0114) ycc adapter: gollama client implements the engine `StreamTurner` seam, wiring TurnStream deltas to the existing transient `turn_delta` path
- [x] (folded from 0114) live end-to-end verification: model text streams incrementally in the TUI session view; final persisted `model_turn` unchanged; events.jsonl/replay contain no deltas

## Plan

Goal: land streaming turns end to end — gollama grows TurnStream (Anthropic SSE first, graceful fallback elsewhere), ycc adopts it via a version bump (zero adapter code: the method signature is designed to satisfy ycc's existing engine.StreamTurner seam directly).

KEY DESIGN DECISION: gollama's TurnStream signature is exactly
  func (c *Client) TurnStream(opts RequestOptions, onDelta func(text string)) (*ResponseMessageGenerate, error)
with SNAPSHOT semantics: onDelta receives the full accumulated assistant text so far (not increments), invoked serially, possibly zero times (tool-only turns). This matches ycc's engine.StreamTurner (internal/engine/loop.go:42) verbatim, so *gollama.Client satisfies the seam with no ycc adapter — adoption is just the go.mod bump. Document the contract in gollama. Thinking-delta streaming and OpenAI/Ollama native SSE are explicitly follow-ups (separate backlog tasks).

WORKSPACE MECHANICS (gollama lives outside this workspace; file tools cannot write there):
1. git clone /home/why/code/gollama .gollama-work  (inside the ycc workspace; already listed in .git/info/exclude so it never enters ycc's git). Edit ONLY in .gollama-work with normal file tools; build/test with `cd .gollama-work && go build ./... && go vet ./... && go test ./...` (ANTHROPIC_API_KEY is set, so live tests run — good).
2. When gollama work is done: commit in .gollama-work (clear message), then
   git -C /home/why/code/gollama pull --ff-only /home/why/code/ycc/.gollama-work main
   git -C /home/why/code/gollama push origin main    (established pattern, tasks 0001/0025/0093/0134)
   Record the new sha.
3. ycc bump: GOPRIVATE=github.com/whyrusleeping go get github.com/whyrusleeping/gollama@<sha> && go mod tidy.
4. Cleanup at the very end: rm -rf .gollama-work.

PART A — gollama:
- turn.go: add TurnStream with the contract doc. Routing: Backend()==BackendAnthropic → new streaming path; every other backend (OpenAI, Ollama, Bedrock) → fallback: resp, err := c.Turn(opts); if err==nil and text non-empty, call onDelta once with the whole text; return resp. Callers never branch. Turn itself is untouched.
- anthropic.go: add `Stream bool `json:"stream,omitempty"`` to anthropicRequest. Extract the response→ResponseMessageGenerate conversion from parseAnthropicResponse into a shared func convertAnthropicResponse(*anthropicResponse) *ResponseMessageGenerate; parseAnthropicResponse = decode JSON + convert. New chatCompletionAnthropicStream builds the request (buildAnthropicRequest, Stream=true, same anthropic-version header default, via prepareRequest so pre-stream 429/503/529 handling is unchanged), then parses the SSE stream and ASSEMBLES an anthropicResponse, finally returning convertAnthropicResponse(assembled) — same converter as non-streaming guarantees the byte-equivalent final shape (criterion 3).
- SSE parsing (Anthropic Messages streaming): use bufio.Reader (not Scanner — deltas can exceed default buffer) reading `event:`/`data:` lines. Handle: message_start (seed id/model/role + usage.input_tokens/cache_creation_input_tokens/cache_read_input_tokens), content_block_start (per-index block: text | thinking | redacted_thinking | tool_use{id,name,input}), content_block_delta (text_delta.text → append + fire snapshot; thinking_delta.thinking → append; signature_delta.signature → append; input_json_delta.partial_json → accumulate string), content_block_stop (tool_use: unmarshal accumulated partial_json into Input; if empty accumulation keep the start block's input, typically {}), message_delta (stop_reason/stop_sequence + usage.output_tokens), message_stop (end), ping (ignore), error event (return an error carrying type/message). Unknown event types: ignore (forward-compatible). onDelta snapshot = concatenation of all text-block content so far.
- Tests, offline (anthropic_test.go style, httptest server emitting a scripted SSE body):
  a) rich stream: thinking block (thinking_delta + signature_delta), multiple text_deltas, tool_use with input split across several input_json_delta chunks, stop_reason tool_use → assert final result reflect.DeepEqual to parseAnthropicResponse fed the equivalent non-streaming JSON; assert snapshots are monotonically growing prefixes and the last equals final Content; assert usage fields merged from message_start + message_delta.
  b) fallback: non-anthropic client against a mock /chat/completions → exactly one delta equal to the full text; result identical to Turn.
  c) error event mid-stream → error returned (and turn.go fallback untouched).
- Live smoke (anthropic_live_test.go pattern, skips without ANTHROPIC_API_KEY): TurnStream with a prompt requesting a ~150-word answer; assert ≥2 delta callbacks, growing snapshots, final Content == last snapshot, StopReason set. Model: claude-opus-4-8 or a cheaper sonnet if listed in that file — keep consistent with existing live tests.

PART B — ycc (after bump):
- No adapter: verify the seam engages. Add a compile-time assertion `var _ engine.StreamTurner = (*gollama.Client)(nil)` (e.g. in internal/config/config_test.go or a small config.go comment+assert in a _test file) so a future gollama signature drift breaks the build loudly.
- Live e2e (folded 0114): add internal/engine/stream_live_test.go, guarded by ANTHROPIC_API_KEY (t.Skip otherwise), modeled on the emitter/broadcast wiring used by stream_test.go + subscribe_transient_test.go but with a real gollama anthropic client (NewClient("https://api.anthropic.com"), SetAnthropicMode(true), SetAPIKey, SetMaxRetries(0)): run a single no-tool turn through engine.Loop with a broadcast-capable emitter writing a real events.jsonl in t.TempDir(); assert (1) ≥2 transient turn_delta broadcasts with growing text then the clearing {"", done:true}, (2) a persisted model_turn with the final text, (3) the events.jsonl file contains no turn_delta lines. This proves the wire: TurnStream → engine seam → transient turn_delta path. (TUI rendering of turn_delta was delivered+tested in task 0129 — tui_stream_test.go — so the TUI criterion is covered by that plus this live engine-level check.)
- spec.md §7.1: extend the "what we add in gollama" list with item 3 — TurnStream (snapshot-semantics streaming turn; Anthropic SSE, blocking fallback elsewhere), keeping it one or two lines.
- Run the plans/build-and-test.md runbook: go build ./... && go vet ./... && go test ./... (live tests will exercise the key).

FOLLOW-UPS (coordinator will file, not this task): OpenAI-compatible + Ollama native streaming; thinking-delta streaming surface.

Acceptance mapping: TurnStream+fallback (A); Anthropic SSE + offline fixture tests + live smoke (A tests); byte-equivalence via shared converter + DeepEqual test (a); zero-churn ycc adoption via signature match + compile-time assert (B); folded-0114 adapter criterion satisfied by direct interface satisfaction; folded-0114 live verification via stream_live_test.go (deltas live, model_turn unchanged, no deltas persisted).

### Starting points
- ycc seam: internal/engine/loop.go:24-45 (Turner/StreamTurner, snapshot contract) and turnOnce at :392
- gollama repo readable via symlink .gollama-link/ (READ ONLY — write via a clone at .gollama-work/)
- gollama: turn.go (Turn/Backend), anthropic.go (buildAnthropicRequest, parseAnthropicResponse ~line 503, ChatCompletionAnthropic), http.go prepareRequest/doWithRetry, client.go anthropicEndpoint
- gollama live-test pattern: anthropic_live_test.go liveAnthropicClient()
- ycc bump pattern (task 0134): commit gollama, push origin main, then GOPRIVATE=github.com/whyrusleeping go get github.com/whyrusleeping/gollama@<sha> && go mod tidy
- ycc wiring refs: internal/config/config.go Registry.Build ~line 959 (returns *gollama.Client as engine.Turner); internal/server/subscribe_transient_test.go + internal/engine/stream_test.go for emitter/broadcast test wiring
- spec.md §7.1 (~line 400) 'what it lacks and we add' list — add TurnStream as item 3; §5.2 turn_delta contract ~line 212 already matches
- Anthropic SSE event types: message_start, content_block_start, content_block_delta (text_delta|thinking_delta|signature_delta|input_json_delta), content_block_stop, message_delta, message_stop, ping, error

## Work log
- 2026-07-08 pm grooming (with user): unblocked — the gollama working repo now exists at
  /home/why/code/gollama (HEAD d8e738f, still no TurnStream). Scope kept Anthropic-first.
  Folded in the remainder of 0114 (ycc StreamTurner adapter + live end-to-end
  verification) so one cross-repo session lands streaming completely; 0114 closed as
  merged into this task.
- 2026-07-05 blocked (coordinator): the work lives in the separate gollama repo at
  /home/why/code/gollama, which does not exist in this environment (gollama is only
  present as a read-only module-cache dependency). Cloning/forking gollama into the ycc
  workspace would be a hard-to-reverse structural decision the task explicitly avoids.
  Unblock when the gollama working repo is available alongside ycc.
- 2026-07-06 plan: Goal: land streaming turns end to end — gollama grows TurnStream (Anthropic SSE first, graceful fallback elsewhere), ycc adopts it via a version bump (zero adapter code: the method signature is desi
…[truncated]
- 2026-07-06 context hints: 8 recorded with plan
- 2026-07-06 context hints: ycc seam: internal/engine/loop.go:24-45 (Turner/StreamTurner snapshot contract), turnOnce at :392, turnDeltaInterval throttle; gollama sources readable now via .gollama-link/ symlink (turn.go, anthrop
…[truncated]
- 2026-07-06 implementer report: Landed streaming turns end to end. Both repos build/vet/test green (incl. live tests against the real Anthropic API via ANTHROPIC_API_KEY).  ## gollama (pushed to origin/main, sha 4140920e3029064cb478
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change lands streaming turns end to end. gollama gains TurnStream (snapshot semantics, Anthropic SSE via a shared converter guaranteeing byte-equivalent final messages, graceful single-delta fallb
…[truncated]
- 2026-07-06 decision: accept — commit: Streaming turns end to end: gollama TurnStream + ycc adoption (task 0120)  gollama 4140920 adds TurnStream(opts, onDelta) — Anthropic Messages SSE streaming with snapshot-semantics text deltas, asse
…[truncated]
- 2026-07-06 usage: 46,782 tok (in 152, out 46,630, cache_r 3,796,671, cache_w 202,952) · cost n/a (unpriced)
  implementer: 30,043 tok (in 94, out 29,949, cache_r 2,480,804, cache_w 79,722) · cost n/a (unpriced)
  coordinator: 12,157 tok (in 26, out 12,131, cache_r 1,019,131, cache_w 91,428) · cost n/a (unpriced)
  reviewer:Claude: 4,582 tok (in 32, out 4,550, cache_r 296,736, cache_w 31,802) · cost n/a (unpriced)
