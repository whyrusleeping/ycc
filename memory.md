# Project memory

> Agent-maintained operational notes. Advisory, not normative — verify before relying.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.

## Codebase gotchas

- Token classes are DISJOINT (engine/loop.go); pricing in internal/config/default_pricing.go.
- E2E TUI harness (internal/e2e): a goroutine MUST drain emu.Read back into the PTY or screen reads deadlock; skips without a PTY.
- Provider errors can arrive INSIDE an HTTP 200 stream (codex SSE `error` frame); apierror.go treats server_error/internal_error as retryable.
- Anthropic refusals are STICKY: refusal turns kept out of history, SendInput gated until Resume/model change (0238).
- Anthropic OAuth tokens resolved PER TURN (anthropicauth.NewOAuthTurner) — a refresh invalidates the previous access token; retired-flow creds can show as HTTP 429.
- Duplicate backlog ids SELF-HEAL on docs.Store scan; `ycc doctor` reports moves.
- Models sometimes leak XML invoke syntax into JSON tool args; internal/tools/argrepair.go repairs (see tool_call `repaired`).
- Work loop is daemon-owned, in-memory only; daemon restart loses a running loop (0280 open).
- Codex stateless replay: Responses items ride as ONE marked ThinkingBlock; engine.messagesForBackend strips them for non-openai backends (0197).
- Never emit user_input events for synthetic/subagent history — ReplayHistory folds ALL user_input into coordinator history (0175).
- iOS: question rows resolve via openQuestionRowID (0247); composer clear needs the autocorrect-pulse trick (0278); unread badges are client-side watermarks (SessionReadStore); nav pushes go through HomeRouter.open with dedupe.
- iOS: automatic keyboard safe area under bottom safeAreaInset can wedge (FB13296535) — use App/KeyboardObserver.swift manual avoidance for any bottom-chrome screen.
- Manager goroutines running git after session events must be joinable (Manager.Stop waits) or TempDir tests race; server.workstreamError maps errors BY STRING.
- Event-log failure is TERMINAL (0198): Record returns Seq==0; durable emit sites must check Emitter.Err()/ctx before mutating state.
- Backlog ids are daemon-allocated per project (docs.IDAllocator, <state>/ycc/backlog-ids.json); wire daemon stores through Manager.backlogStore (0249).
- Model turns are ctx-aware via TurnCtx/TurnStreamCtx (0204); gollama's legacy Turn/TurnStream use context.Background — never use in inference paths.

## Environment & tooling

- No Swift toolchain here — iOS builds/tests on the user's Mac: `swift test` in clients/ios/YccKit; `xcodegen generate && xcodebuild …`; keep ad-hoc signing or simulator Keychain fails (-34018).
- Tool-failure forensics: <workspace>/.ycc/sessions/\*/events.jsonl; Edit diagnostics in internal/tools/editdiag.go.
- buf in ~/go/bin; Swift proto regen uses REMOTE BSR plugins (network), Go regen local.
- `go test ./...` has known flaky tests (internal/session, internal/setup, internal/tools background-bash); verify against HEAD first.
- The `commit` tool does `git add -A` — stash unrelated work first.
- Live-model checks feasible: ANTHROPIC_API_KEY set; codex.New("", openaiauth.AccessToken) uses ycc's ChatGPT OAuth login.

## User preferences

- Home-menu action affordances must be ctrl-chords, never naked letter keys.
- iOS client: in-repo clients/ios (XcodeGen + YccKit SPM, iPhone-only iOS 17+, committed protos), ntfy + ycc:// deep links, work loop daemon-side.
- iOS `in_review` means "implemented, awaiting on-device use" — sweep during backlog audits.
- 2026-08-08: iOS: project removal lives only in a long-press context menu on drawer project rows (WorkspaceDrawer) since the drawer refactor — user confirmed this is intentional, no visible affordance wanted.

## Lessons learned

- For user-reported TUI/session issues, check .ycc/sessions in ALL workspaces; filter events.jsonl for `session_error`.
- For verbatim code-move refactors, diff sorted go/ast decl dumps of HEAD vs new trees (0210).
