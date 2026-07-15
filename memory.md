# Project memory

> Agent-maintained operational notes. Advisory, not normative — verify before relying.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.

## Codebase gotchas

- 2026-07-06: Usage accounting: OpenAI reports cached tokens as a SUBSET of prompt*tokens while Anthropic reports cache reads/writes disjoint from input_tokens; engine/loop.go normalizes to disjoint classes at emit time; default pricing in internal/config/default_pricing.go (config price*\* always overrides).
- 2026-07-07: File-access policy: Read tool is unrestricted (any path); Write/Edit confined to workspace + config write_roots (tools.Workspace.WriteRoots, symlink-aware); old read_roots key removed, silently ignored.
- 2026-07-08: iOS app must keep ad-hoc code signing (CODE_SIGN_IDENTITY "-" in clients/ios/project.yml): unsigned simulator apps fail all Keychain calls with errSecMissingEntitlement (-34018); YccAppTests keychain round-trip guards this.
- 2026-07-08: E2E TUI harness (internal/e2e): a goroutine MUST drain emu.Read back into the PTY (vt.SafeEmulator answers terminal queries under its lock) or screen reads deadlock; harness skips without a PTY and no-ops under -short.

## Environment & tooling

- 2026-07-08: iOS app (clients/ios) builds cleanly: `cd clients/ios/YccKit && swift test` (macOS, headless) for YccKit logic; `cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build`. connect-swift resolves to 1.2.3; its interceptor API is UnaryInterceptor/StreamInterceptor (request-side hooks handleUnaryRequest/handleStreamStart mutate HTTPRequest headers) wrapped in an InterceptorFactory passed to ProtocolClientConfig(host:networkProtocol:.connect,codec:JSONCodec(),interceptors:). Generated .xcodeproj AND App/Info.plist (xcodegen emits it from project.yml `info:`) are git-ignored; do not commit them.
- 2026-07-07: Tool-failure forensics: agent session transcripts live in <workspace>/.ycc/sessions/\*/events.jsonl (tool_call args + tool_result pairs keyed by id) and can be replayed to diagnose tool UX issues; Edit not-found diagnostics live in internal/tools/editdiag.go.
- 2026-07-08: buf went missing from PATH (2026-07-08); reinstalled via `go install github.com/bufbuild/buf/cmd/buf@latest` into ~/go/bin — Swift proto regen uses REMOTE BSR plugins (network required), Go regen uses local protoc-gen-go/protoc-gen-connect-go.
- 2026-07-08: In clients/ios/YccKit, `swift test` output ends with a swift-testing "0 tests" summary (the suite is XCTest); check for "Test Suite 'All tests' passed" (e.g. via rg) instead of tailing the last lines.
- 2026-07-10: On this machine (2026-07-10) `go test ./...` fails at HEAD in three pre-existing spots: internal/session TestReconcileWorkstreams (stale-vs-active), internal/setup TestConfigPath (real XDG path leaks through), internal/tools TestBackgroundBashWaitReturnsExitAndOutput (no job_finished) — unrelated to most changes; verify against HEAD before blaming new work.
- 2026-07-08: iOS builds: `cd clients/ios/YccKit && swift test` (XCTest — check for "Test Suite 'All tests' passed", not the trailing swift-testing "0 tests" line); app: `xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build`. connect-swift 1.2.3 interceptors: UnaryInterceptor/StreamInterceptor in an InterceptorFactory passed to ProtocolClientConfig. Generated .xcodeproj and App/Info.plist are git-ignored; do not commit.
- 2026-07-07: Tool-failure forensics: session transcripts live in <workspace>/.ycc/sessions/\*/events.jsonl (tool_call/tool_result pairs by id); Edit not-found diagnostics in internal/tools/editdiag.go.
- 2026-07-08: buf lives in ~/go/bin (`go install github.com/bufbuild/buf/cmd/buf@latest`); Swift proto regen uses REMOTE BSR plugins (network required), Go regen uses local protoc-gen-go/protoc-gen-connect-go.

## User preferences

- 2026-07-08: Home-menu action affordances must be ctrl-chords, never naked letter keys; keep new menu shortcuts consistent with this.
- 2026-07-08: iOS client decisions (2026-07): in-repo at clients/ios (XcodeGen + YccKit SPM, iPhone-only iOS 17+, committed generated proto code), notifications stay ntfy + ycc:// deep links (no APNs), work loop is daemon-side (task 0179).

## Lessons learned

- 2026-07-09: When debugging user-reported TUI/session issues, check .ycc/sessions in ALL workspaces the user runs ycc in (~/code/vals, ~/code/ychat, …); running ycc processes' cwds (ps / readlink /proc/PID/cwd) identify candidates.
- 2026-07-15: Anthropic rolled back third-party Claude subscription (Pro/Max) restrictions; verified LIVE end-to-end 2026-07-15: `ycc login anthropic` + auth="oauth" completed a real /v1/messages turn with NO system-prompt spoofing (bearer + anthropic-beta: oauth-2025-04-20). Implementation: internal/anthropicauth + config.Registry.Build; access tokens ~8h, auto-refresh from stored refresh token.
- 2026-07-15: ChatGPT subscription (codex backend) verified live 2026-07-15 via internal/codex against chatgpt.com/backend-api/codex/responses (headers: bearer + chatgpt-account-id + originator + OpenAI-Beta responses=experimental): July-2026 model catalog is gpt-5.6-sol / gpt-5.5 / gpt-5.4 / gpt-5.4-mini (older gpt-5.x-codex ids all rejected); the official codex CLI's ~/.codex/auth.json tokens work read-only for testing but ycc uses its own OPENAI_OAUTH login to avoid refresh-token rotation conflicts.
