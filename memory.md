# Project memory

> Agent-maintained operational notes. Advisory, not normative — verify before relying.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.

## Codebase gotchas

- 2026-07-06: Token classes are DISJOINT (normalized in engine/loop.go); pricing in internal/config/default_pricing.go.
- 2026-07-08: E2E TUI harness (internal/e2e): a goroutine MUST drain emu.Read back into the PTY or screen reads deadlock; skips without a PTY, no-ops under -short.
- 2026-07-28: Provider errors can arrive INSIDE an HTTP 200 stream (codex SSE `error` frame); apierror.go treats server_error/internal_error as retryable.
- 2026-08-05: Anthropic refusals are STICKY: refusal turns kept out of history, SendInput gated until Resume/model change (task 0238).
- 2026-08-05: Anthropic OAuth: tokens resolved PER TURN (anthropicauth.NewOAuthTurner) — any refresh invalidates the previous access token; retired-flow creds can show as HTTP 429.
- 2026-08-06: Duplicate backlog ids SELF-HEAL on any docs.Store scan (internal/docs/dedupe.go); `ycc doctor` reports moves.
- 2026-08-06: Models sometimes leak XML invoke syntax into JSON tool args; internal/tools/argrepair.go repairs — check tool_call `repaired` field.
- 2026-08-06: Work loop is daemon-owned and in-memory only; a daemon restart loses a running loop (task 0280 open).
- 2026-08-07: Codex stateless replay: Responses items ride as ONE marked ThinkingBlock ("codex-response-items-v1:…"); engine.messagesForBackend strips them for non-openai backends (task 0197).
- 2026-08-07: Never emit user_input events for synthetic/subagent history — engine.ReplayHistory folds ALL user_input into coordinator history regardless of actor (task 0175).
- iOS: question rows resolve via openQuestionRowID (0247); composer clear needs the autocorrect-pulse trick (0278); unread badges are client-side watermarks (SessionReadStore, 0283); all nav pushes go through HomeRouter.open with dedupe (0288).
- 2026-08-07: Manager goroutines running git after session events must be joinable (Manager.Stop waits on workstreamWatches) or TempDir tests race; server.workstreamError maps manager errors BY STRING — rewording breaks Connect codes.
- 2026-08-07: Event-log failure is TERMINAL (0198): Record returns Seq==0, OnFailure cancels the session; new durable emit sites must check Emitter.Err()/ctx before mutating history/workspace/config.
- 2026-08-07: Backlog ids are daemon-allocated per project (docs.IDAllocator, state in <state>/ycc/backlog-ids.json, floored against the primary tree each call); plain docs.NewStore without SetIDSource still scans only its own tree — wire daemon stores through Manager.backlogStore (task 0249).

## Environment & tooling

- 2026-08-05: No Swift toolchain here — iOS builds/tests on the user's Mac: `cd clients/ios/YccKit && swift test`; `xcodegen generate && xcodebuild …`. Keep ad-hoc signing or simulator Keychain fails (-34018).
- 2026-07-07: Tool-failure forensics: <workspace>/.ycc/sessions/\*/events.jsonl; Edit diagnostics in internal/tools/editdiag.go.
- 2026-07-08: buf lives in ~/go/bin; Swift proto regen uses REMOTE BSR plugins (network), Go regen local plugins.
- 2026-07-10: `go test ./...` has known flaky tests (internal/session, internal/setup, internal/tools background-bash); verify against HEAD before blaming new work.
- 2026-08-07: The `commit` tool does `git add -A` — stash unrelated work first; a pop conflict on a backlog file resolves with `git checkout HEAD -- backlog/<task>.md`.
- 2026-08-07: Live-model checks feasible: ANTHROPIC_API_KEY set (gated tests in internal/engine); codex.New("", openaiauth.AccessToken) uses ycc's own ChatGPT OAuth login.

## User preferences

- 2026-07-08: Home-menu action affordances must be ctrl-chords, never naked letter keys.
- 2026-07-08: iOS client: in-repo clients/ios (XcodeGen + YccKit SPM, iPhone-only iOS 17+, committed protos), ntfy + ycc:// deep links (no APNs), work loop daemon-side.
- 2026-08-06: iOS `in_review` means "implemented, awaiting the user's on-device use" — sweep during backlog audits.

## Lessons learned

- 2026-07-09: For user-reported TUI/session issues, check .ycc/sessions in ALL workspaces ycc runs in; filter events.jsonl for `session_error`.
- 2026-08-07: For verbatim code-move refactors, diff sorted go/ast decl dumps of HEAD vs new trees — made a 16k-line split reviewable in minutes (task 0210).
