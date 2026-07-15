---
id: "0204"
title: Propagate session cancellation into in-flight model requests
status: todo
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - Agent engine#The loop
    - Client UI (TUI)#Interrupt & steer (pause / correct / resume)
---

## Description
The engine's `Turner`/`StreamTurner` interfaces do not accept a context. The Codex transport consequently creates `context.Background()` for token refresh and HTTP requests, allowing a stopped session or daemon shutdown to leave an inference request running until the HTTP timeout. Other transports may have the same underlying limitation.

Make model turns context-aware end to end so hard Stop/shutdown promptly cancels network I/O, while a graceful Interrupt still waits for the documented safe checkpoint unless the optional immediate-turn cancellation behavior is deliberately implemented.

## Acceptance criteria
- [ ] The engine passes its run context into both blocking and streaming backend turns.
- [ ] gollama adapters, Codex, test fakes, and registry-built clients satisfy the context-aware interface without falling back to `context.Background()` for inference.
- [ ] Codex token refresh and HTTP requests use the session context.
- [ ] `StopSession` and daemon/in-process shutdown promptly cancel an in-flight model request and do not wait for the backend's long HTTP timeout.
- [ ] Graceful Interrupt semantics remain as specified and are not accidentally converted into hard cancellation.
- [ ] Cancellation does not emit a duplicate or misleading `session_error`.
- [ ] Tests use a blocking HTTP/backend fake to prove cancellation and goroutine cleanup.
- [ ] `go test ./...` passes.

## Work log
