# Project memory

> Agent-maintained operational notes. Advisory, not normative — verify before relying.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.

## Codebase gotchas

- 2026-07-06: Usage accounting: OpenAI cached tokens are a SUBSET of prompt_tokens, Anthropic cache reads/writes are disjoint from input_tokens; engine/loop.go normalizes to disjoint classes; default pricing in internal/config/default_pricing.go.
- 2026-07-08: E2E TUI harness (internal/e2e): a goroutine MUST drain emu.Read back into the PTY or screen reads deadlock; skips without a PTY, no-ops under -short.
- 2026-07-28: Provider errors can arrive INSIDE an HTTP 200 stream (codex SSE `error` frame); apierror.go matches server_error/internal_error signatures as retryable `server`.
- 2026-08-05: Anthropic subscription OAuth (2026 flow): claude.com/cai/oauth/authorize + platform.claude.com token/callback, extra scopes, beta headers claude-code-20250219,oauth-2025-04-20 + x-app: cli; retired-flow credentials can misleadingly get HTTP 429.
- 2026-08-05: Anthropic refusals (stop_reason "refusal") are STICKY: engine drops refusal turns from history (Result.Refused), replay skips them, session gates SendInput until Resume/model change (task 0238; stop_details unparsed, task 0239).
- 2026-08-06: Duplicate backlog ids SELF-HEAL on any docs.Store scan (internal/docs/dedupe.go, oldest `created` keeps the id); `ycc doctor` reports moves.
- 2026-08-06: iOS SessionProjection: the question row resolves via openQuestionRowID (kept past an optimistic gate close), NOT pendingQuestion (task 0247).
- 2026-08-06: Models sometimes leak XML invoke syntax into JSON string tool args; internal/tools/argrepair.go repairs schema-aware — check tool_call event's `repaired` field when a tool looks like it lost arguments.
- 2026-08-06: Anthropic OAuth tokens are resolved PER TURN (anthropicauth.NewOAuthTurner); a token frozen at Build dies when any other process refreshes, since each refresh invalidates the previous access token.
- 2026-08-06: iOS composers: clearing a TextField binding on send is not enough — UIKit re-commits a pending autocorrection; fix is a one-runloop `.autocorrectionDisabled` pulse + deferred re-clear (SessionView.send, task 0278).
- 2026-08-06: Work loop is daemon-owned (Start/Stop/GetWorkLoop + polling from TUI/iOS); loop state is in-memory only, a daemon restart loses a running loop (task 0280).
- 2026-08-06: iOS unread is client-side only: YccKit/SessionReadStore keeps per-session watermarks in UserDefaults from DAEMON timestamps; never badges a still-running session; marks read on disappear/background (task 0283).
- 2026-08-07: Codex stateless replay (task 0197): Responses output items ride as ONE marked gollama.ThinkingBlock ("codex-response-items-v1:…") on model_turn.thinking_blocks; engine.messagesForBackend strips them for non-openai backends (marker duplicated in loop.go); buildInput drops items from another model.
- 2026-08-07: iOS navigation (task 0288): all stack pushes go through HomeRouter.open (environment-injected; screen-identity dedupe pops back instead of pushing copies) or value-based NavigationLink over HomeDestination; per-screen navigationDestination(item:) only for true leaves (DiffView).

## Environment & tooling

- 2026-08-05: No Swift toolchain on this Linux box — iOS builds/tests run on the user's Mac: `cd clients/ios/YccKit && swift test`; `cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build`. Keep ad-hoc signing (CODE_SIGN_IDENTITY "-") or simulator Keychain fails (-34018).
- 2026-07-07: Tool-failure forensics: session transcripts in <workspace>/.ycc/sessions/\*/events.jsonl; Edit not-found diagnostics in internal/tools/editdiag.go.
- 2026-07-08: buf lives in ~/go/bin; Swift proto regen uses REMOTE BSR plugins (network), Go regen uses local protoc-gen-go/protoc-gen-connect-go.
- 2026-07-10: `go test ./...` has known flaky tests (internal/session, internal/setup, internal/tools background-bash); verify against HEAD before blaming new work.
- 2026-08-07: The `commit` tool does `git add -A` — stash unrelated uncommitted work first; a pop conflict on the task's backlog file resolves with `git checkout HEAD -- backlog/<task>.md`.

## User preferences

- 2026-07-08: Home-menu action affordances must be ctrl-chords, never naked letter keys.
- 2026-07-08: iOS client: in-repo clients/ios (XcodeGen + YccKit SPM, iPhone-only iOS 17+, committed proto code), ntfy + ycc:// deep links (no APNs), work loop daemon-side.
- 2026-08-06: iOS `in_review` means "implemented, awaiting the user's on-device use" — sweep during backlog audits.

## Lessons learned

- 2026-07-09: For user-reported TUI/session issues, check .ycc/sessions in ALL workspaces ycc runs in; filter events.jsonl for `session_error`.
- 2026-07-15: Anthropic subscription OAuth and ChatGPT subscription (codex backend) both verified live; ycc uses its own OPENAI_OAUTH login (don't share ~/.codex/auth.json).
- 2026-08-07: For verbatim code-move refactors, diff sorted go/ast decl dumps of old (git worktree of HEAD) vs new trees — made the 16k-line TUI split reviewable in minutes (task 0210).
