---
id: "0204"
title: Propagate session cancellation into in-flight model requests
status: done
priority: 2
created: "2026-07-15"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - Agent engine#The loop
    - Client UI (TUI)#Interrupt & steer (pause / correct / resume)
---

## Description
The engine's `Turner`/`StreamTurner` interfaces do not accept a context. The Codex transport consequently creates `context.Background()` for token refresh and HTTP requests, allowing a stopped session or daemon shutdown to leave an inference request running until the HTTP timeout. Other transports may have the same underlying limitation.

Make model turns context-aware end to end so hard Stop/shutdown promptly cancels network I/O, while a graceful Interrupt still waits for the documented safe checkpoint unless the optional immediate-turn cancellation behavior is deliberately implemented.

## Acceptance criteria
- [x] The engine passes its run context into both blocking and streaming backend turns.
- [x] gollama adapters, Codex, test fakes, and registry-built clients satisfy the context-aware interface without falling back to `context.Background()` for inference.
- [x] Codex token refresh and HTTP requests use the session context.
- [x] `StopSession` and daemon/in-process shutdown promptly cancel an in-flight model request and do not wait for the backend's long HTTP timeout.
- [x] Graceful Interrupt semantics remain as specified and are not accidentally converted into hard cancellation.
- [x] Cancellation does not emit a duplicate or misleading `session_error`.
- [x] Tests use a blocking HTTP/backend fake to prove cancellation and goroutine cleanup.
- [x] `go test ./...` passes.

## Plan

Goal: thread the session context end to end into model turns so hard Stop / daemon shutdown promptly cancels in-flight inference HTTP, without touching graceful Interrupt (pause-at-checkpoint) semantics.

KEY DESIGN DECISION (zero-adapter seam, same trick as task 0262): gollama grows ADDITIVE ctx-aware methods whose signatures exactly match a renamed ycc engine seam, so *gollama.Client keeps satisfying engine.Turner directly:

  gollama:  func (c *Client) TurnCtx(ctx context.Context, opts RequestOptions) (*ResponseMessageGenerate, error)
            func (c *Client) TurnStreamCtx(ctx context.Context, opts RequestOptions, onDelta func(text string)) (*ResponseMessageGenerate, error)
  (Turn/TurnStream stay unchanged, delegating with context.Background() — no breaking change for other gollama consumers.)

  ycc engine.Turner       → method TurnCtx(ctx, opts)
  ycc engine.StreamTurner → Turner + TurnStreamCtx(ctx, opts, onDelta)

WORKSPACE MECHANICS (gollama lives outside this workspace; file tools cannot write there — established pattern from task 0262):
1. git clone /home/why/code/gollama .gollama-work (inside the ycc workspace; add to .git/info/exclude if not already there). Edit ONLY in .gollama-work with normal file tools; build/test with `cd .gollama-work && go build ./... && go vet ./... && go test ./...`.
2. When done: commit in .gollama-work, then
   git -C /home/why/code/gollama pull --ff-only /home/why/code/ycc/.gollama-work main
   git -C /home/why/code/gollama push origin main
   Record the new sha.
3. ycc bump: GOPRIVATE=github.com/whyrusleeping go get github.com/whyrusleeping/gollama@<sha> && go mod tidy. (If the proxy hasn't seen the commit yet, use GOPROXY=direct for the go get.)
4. Cleanup at the very end: rm -rf .gollama-work.

PART A — gollama (context threading):
- http.go: add ctx-aware variants — doWithRetry/prepareRequest/prepareGet gain ctx (either add prepareRequestCtx/prepareGetCtx keeping thin Background wrappers, or thread ctx params through the private funcs; private funcs may just change signature). Use http.NewRequestWithContext, and make the retry backoff sleep ctx-aware (select on timer vs ctx.Done, returning ctx.Err()). Bail out before an attempt when ctx is already done.
- turn.go: TurnCtx / TurnStreamCtx as above; Turn/TurnStream delegate with context.Background(). Route ctx through the backend switch (Anthropic stream, OpenAI stream, bedrock fallback).
- openai.go: ChatCompletionCtx(ctx, opts) (public, since Turn delegates to it); ChatCompletion = ChatCompletionCtx(context.Background(), …).
- anthropic.go: ChatCompletionAnthropic gains a ctx path (public Ctx variant mirroring ChatCompletion, or thread through the internal call — mirror whatever ChatCompletion dispatch needs).
- anthropic_stream.go / openai_stream.go: chatCompletionAnthropicStream/chatCompletionOpenAIStream are private — just add ctx as first param and use the ctx-aware prepareRequest. NOTE: for streaming, the request ctx must stay live for the WHOLE body read (do not cancel after headers) — since the ctx is caller-owned this is automatic; just ensure ctx cancellation mid-stream surfaces as an error from the SSE read loop.
- bedrock.go: ChatCompletionBedrock ctx variant; http.NewRequestWithContext.
- batch.go / ollama-native (Generate/Chat) / ListModels: out of scope — leave on the Background wrappers.
- gollama tests: httptest server that blocks until the request context is canceled; assert TurnCtx and TurnStreamCtx (both OpenAI and Anthropic-mode clients) return promptly with a ctx error when the caller cancels, and that the retry sleep is interruptible. Keep existing tests green.

PART B — ycc adoption:
- internal/engine/loop.go: rename interface methods (Turner.TurnCtx, StreamTurner.TurnStreamCtx — keep the snapshot-contract doc); turnOnce takes ctx and passes it to client.TurnCtx / streamer.TurnStreamCtx; runTurn already has ctx — pass it down. Update the now-stale comment at the "final continuation barrier" (the API DOES carry ctx now). Keep behavior otherwise identical: after runTurn error, the existing `if ctx.Err() != nil { return nil, ctx.Err() }` guard means a canceled turn is returned as a plain ctx error with NO session_error emission (acceptance: no duplicate/misleading session_error). Verify ClassifyAPIError treats context.Canceled as non-retryable (or rely on the ctx.Err() check in the retry loop, which already short-circuits).
- internal/anthropicauth/turner.go: TurnCtx/TurnStreamCtx; run(ctx, …); token resolution/refresh use context.WithTimeout(ctx, tokenTimeout) derived from the TURN ctx instead of Background, so Stop also cancels a hanging token round trip.
- internal/codex/codex.go: Turn/TurnStream → TurnCtx(ctx…)/TurnStreamCtx(ctx…); drop the `ctx := context.Background()` at line ~409 — use the passed ctx for c.tokens(ctx) AND the HTTP request (it already uses NewRequestWithContext). parseStream reads resp.Body which is ctx-bound via the request — cancellation mid-stream then errors the read; make sure that surfaces cleanly.
- internal/config/config.go Registry.Build: unchanged logic (gollama.Client now satisfies the renamed seam). Add/keep a compile-time assertion var _ engine.StreamTurner = (*gollama.Client)(nil) somewhere sensible (config or engine test).
- Update ALL fakes/tests that implement Turner/TurnStream: internal/engine/{loop,retry,stream,context}_test.go, internal/session/{reopen,checkpoint_persistence,retry_resume,refusal,settings}_test.go, internal/orchestrator/{revise,background}_test.go, internal/codex/codex_test.go, internal/anthropicauth/turner_test.go, internal/config/live_oauth_smoke_test.go, internal/engine/stream_live_test.go. Mostly mechanical signature updates.
- session/orchestrator plumbing needs no change: Session.Stop/reap already cancel s.ctx which is the Run ctx; graceful Interrupt goes through Steer checkpoints and never cancels the run ctx — do NOT wire interrupt to cancellation.

PART C — cancellation proof tests (ycc):
- engine-level: a blocking fake StreamTurner whose TurnStreamCtx blocks on ctx.Done(); start Loop.Run in a goroutine, cancel the ctx, assert Run returns promptly (~<1s) with the ctx error, and that NO session_error event was emitted for the cancellation.
- transport-level (real HTTP): gollama client (SetMaxRetries(0)) against an httptest server that holds the request open until its own ctx is done; run a turn through engine.Loop (or directly through the client via the seam), cancel, assert prompt return + the server saw its request ctx canceled (proves the HTTP request itself was torn down, not just abandoned). Cover the codex client the same way in internal/codex (its test harness already fakes the SSE endpoint).
- session-level (if cheap with existing harness): Session with a blocking fake turner; Stop; assert run() exits promptly and the log ends with session_stopped and no session_error.

PART D — docs & verification:
- spec.md §7.1 "what we add in gollama" list: add ctx-aware turns (TurnCtx/TurnStreamCtx) as a bullet; touch §7.2/§18.7 only if they assert the old no-context limitation.
- go build ./... && go vet ./... && go test ./... in BOTH repos (memory: internal/session, internal/setup, internal/tools background-bash have known flakes — verify against HEAD before blaming new work).

Acceptance mapping: ctx into both blocking+streaming turns (B loop.go); adapters/codex/fakes/registry satisfy without Background fallback (B); codex token refresh + HTTP on session ctx (B codex); prompt Stop/shutdown cancellation (A ctx-aware HTTP + C tests); Interrupt untouched (no Steer changes); no duplicate session_error (existing ctx.Err() guard + C assertions); blocking-fake tests (C); go test ./... (D).

### Starting points
- ycc seam: internal/engine/loop.go:24-45 (Turner/StreamTurner), turnOnce ~:564, runTurn ~:616, 'final continuation barrier' comment ~:765
- internal/codex/codex.go:396-447 (Turn/TurnStream, ctx := context.Background() at :409; tokens(ctx) refresh)
- internal/anthropicauth/turner.go:99-152 (Turn/TurnStream/run; token ctx from Background at :131/:142)
- internal/config/config.go Registry.Build ~:1302 (returns *gollama.Client / anthropicauth wrappers / codex.New as engine.Turner)
- gollama (clone to .gollama-work): turn.go (Turn/TurnStream/Backend), http.go (doWithRetry/prepareRequest/prepareGet), openai.go ChatCompletion :192, anthropic.go ChatCompletionAnthropic :581, anthropic_stream.go :61, openai_stream.go :69, bedrock.go ChatCompletionBedrock :76 + NewRequest :107
- push/bump pattern from task 0262: commit in .gollama-work → git -C /home/why/code/gollama pull --ff-only <workspace>/.gollama-work main → push origin main → GOPRIVATE=github.com/whyrusleeping go get github.com/whyrusleeping/gollama@<sha> && go mod tidy
- session Stop path: internal/session/session.go Stop ~:2271, run() ~:1054 (TurnError dedupe + ctx.Err() guards)
- fakes to update: engine/{loop,retry,stream,context}_test.go, session/{reopen,checkpoint_persistence,retry_resume,refusal,settings}_test.go, orchestrator/{revise,background}_test.go, codex_test.go, anthropicauth/turner_test.go, config/live_oauth_smoke_test.go, engine/stream_live_test.go

## Work log
- 2026-08-07 plan: Goal: thread the session context end to end into model turns so hard Stop / daemon shutdown promptly cancels in-flight inference HTTP, without touching graceful Interrupt (pause-at-checkpoint) semanti
…[truncated]
- 2026-08-07 context hints: 8 recorded with plan
- 2026-08-07 context hints: internal/engine/loop.go:24-45 seam, turnOnce ~:564, runTurn ~:616, 'final continuation barrier' ~:765, ctx.Err() guard after runTurn ~:777; internal/codex/codex.go:396-447; internal/anthropicauth/turn
…[truncated]
- 2026-08-07 implementer report: Implemented task 0204 end to end.  Changes: - Added and pushed gollama context-aware `TurnCtx`/`TurnStreamCtx` APIs at commit `db217a6d41cef71c623597564e892ed2fa814f50`; retained background-context wr
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): revise — The model-turn seam, Codex/Anthropic auth paths, gollama transport, and cancellation/error tests are generally implemented correctly, and the ycc build, vet, tests, and targeted race tests pass. Howev
…[truncated]
- 2026-08-07 revision: Addressed both review findings.  Changes: - `buildHandler` now returns the constructed `session.Manager`; `InProcess` retains it. - Added idempotent `Manager.ReclaimAll`, which atomically drains live 
…[truncated]
- 2026-08-07 review (sol): accept — The revision addresses the daemon lifecycle gap: in-process shutdown now retains the session manager, reclaims all live sessions before/after HTTP shutdown, and has an end-to-end blocking HTTP test pr
…[truncated]
- 2026-08-07 decision: accept — commit: Propagate session cancellation into in-flight model requests (task 0204)  gollama db217a6 adds additive ctx-aware turns (TurnCtx/TurnStreamCtx) threading caller cancellation through retries, HTTP requ
…[truncated]
- 2026-08-07 usage: 14,445,787 tok (in 1,341,685, out 52,198, cache_r 15,228,362, cache_w 238,596) · cost n/a (unpriced)
  implementer: 12,085,730 tok (in 842,260, out 29,902, cache_r 11,213,568, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 2,347,349 tok (in 499,379, out 9,634, cache_r 1,838,336, cache_w 0) · cost n/a (unpriced)
  coordinator: 12,708 tok (in 46, out 12,662, cache_r 2,176,458, cache_w 238,596) · cost n/a (unpriced)
