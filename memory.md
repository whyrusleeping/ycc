# Project memory

> Agent-maintained operational notes. Advisory, not normative — verify before relying.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.

## Codebase gotchas

- 2026-07-06: Usage accounting: OpenAI reports cached tokens as a SUBSET of prompt_tokens while Anthropic reports cache reads/writes disjoint from input_tokens; engine/loop.go normalizes to disjoint classes at emit time; default pricing in internal/config/default_pricing.go (config price_\* always overrides).
- 2026-07-07: File-access policy: Read is unrestricted (any path); Write/Edit confined to workspace + config write_roots (tools.Workspace.WriteRoots, symlink-aware); old read_roots key removed, silently ignored.
- 2026-07-08: iOS app must keep ad-hoc code signing (CODE_SIGN_IDENTITY "-" in clients/ios/project.yml): unsigned simulator apps fail all Keychain calls with errSecMissingEntitlement (-34018); YccAppTests keychain round-trip guards this.
- 2026-07-08: E2E TUI harness (internal/e2e): a goroutine MUST drain emu.Read back into the PTY (vt.SafeEmulator answers terminal queries under its lock) or screen reads deadlock; harness skips without a PTY and no-ops under -short.
- 2026-07-28: Provider errors can arrive INSIDE an HTTP 200 stream (codex sends an SSE `error` frame with code `server_error`), so engine.ClassifyAPIError cannot key on a parsed status alone — adapters must keep the provider error code in the message, and apierror.go matches providerServerSignatures (server_error/internal_error) as retryable `server`.

## Environment & tooling

- 2026-07-08: iOS builds: `cd clients/ios/YccKit && swift test` for YccKit logic (XCTest — grep for "Test Suite 'All tests' passed"; the trailing swift-testing "0 tests" summary is misleading); app: `cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build`. connect-swift 1.2.3 interceptors: Unary/StreamInterceptor in an InterceptorFactory passed to ProtocolClientConfig. Generated .xcodeproj AND App/Info.plist are git-ignored; never commit them.
- 2026-07-07: Tool-failure forensics: agent session transcripts live in <workspace>/.ycc/sessions/\*/events.jsonl (tool_call args + tool_result pairs keyed by id) and can be replayed to diagnose tool UX issues; Edit not-found diagnostics live in internal/tools/editdiag.go.
- 2026-07-08: buf lives in ~/go/bin (`go install github.com/bufbuild/buf/cmd/buf@latest`; it has gone missing from PATH before) — Swift proto regen uses REMOTE BSR plugins (network required), Go regen uses local protoc-gen-go/protoc-gen-connect-go.
- 2026-07-10: `go test ./...` has intermittently failed at HEAD in three spots: internal/session TestReconcileWorkstreams, internal/setup TestConfigPath (real XDG path leaks through), internal/tools TestBackgroundBashWaitReturnsExitAndOutput. All three PASSED on 2026-07-28 — treat as flaky, and verify against HEAD before blaming new work.

## User preferences

- 2026-07-08: Home-menu action affordances must be ctrl-chords, never naked letter keys; keep new menu shortcuts consistent with this.
- 2026-07-08: iOS client decisions (2026-07): in-repo at clients/ios (XcodeGen + YccKit SPM, iPhone-only iOS 17+, committed generated proto code), notifications stay ntfy + ycc:// deep links (no APNs), work loop is daemon-side (task 0179).

## Lessons learned

- 2026-07-09: When debugging user-reported TUI/session issues, check .ycc/sessions in ALL workspaces the user runs ycc in (~/code/oldgrowth, ~/code/ychat, …); running ycc processes' cwds (ps / readlink /proc/PID/cwd) identify candidates. Filtering events.jsonl for `session_error` surfaces real provider failures with their classification.
- 2026-07-15: Anthropic rolled back third-party Claude subscription (Pro/Max) restrictions; verified live: `ycc login anthropic` + auth="oauth" completed a real /v1/messages turn with NO system-prompt spoofing (bearer + anthropic-beta: oauth-2025-04-20). Implementation: internal/anthropicauth + config.Registry.Build; access tokens ~8h, auto-refresh from stored refresh token.
- 2026-07-15: ChatGPT subscription (codex backend) verified live against chatgpt.com/backend-api/codex/responses: July-2026 model catalog is gpt-5.6-sol / gpt-5.5 / gpt-5.4 / gpt-5.4-mini (older gpt-5.x-codex ids rejected); the codex CLI's ~/.codex/auth.json tokens work read-only for testing, but ycc uses its own OPENAI_OAUTH login to avoid refresh-token rotation conflicts.
