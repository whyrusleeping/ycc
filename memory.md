# Project memory

> Agent-maintained operational notes. Advisory, not normative — verify before relying.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.

## Codebase gotchas
- 2026-07-06: Usage accounting: OpenAI reports cached tokens as a SUBSET of prompt_tokens while Anthropic reports cache reads/writes disjoint from input_tokens; engine/loop.go normalizes to disjoint classes at emit time and built-in default pricing lives in internal/config/default_pricing.go (config price_* always overrides).
- 2026-07-07: File-access policy (since write_roots change): Read tool is unrestricted (any path); Write/Edit are confined to the workspace plus config write_roots (tools.Workspace.WriteRoots, symlink-aware); the old read_roots config/machinery was removed and the key is silently ignored.
- 2026-07-08: iOS app builds must keep code signing enabled (ad-hoc CODE_SIGN_IDENTITY "-" in clients/ios/project.yml): an unsigned simulator app lacks the application-identifier entitlement and all Keychain calls fail with errSecMissingEntitlement (-34018); the hosted YccAppTests keychain round-trip (`xcodebuild test`) guards this.

## Environment & tooling
- 2026-07-08: iOS app (clients/ios) builds cleanly: `cd clients/ios/YccKit && swift test` (macOS, headless) for YccKit logic; `cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build`. connect-swift resolves to 1.2.3; its interceptor API is UnaryInterceptor/StreamInterceptor (request-side hooks handleUnaryRequest/handleStreamStart mutate HTTPRequest headers) wrapped in an InterceptorFactory passed to ProtocolClientConfig(host:networkProtocol:.connect,codec:JSONCodec(),interceptors:). Generated .xcodeproj AND App/Info.plist (xcodegen emits it from project.yml `info:`) are git-ignored; do not commit them.
- 2026-07-07: Tool-failure forensics: agent session transcripts live in <workspace>/.ycc/sessions/*/events.jsonl (tool_call args + tool_result pairs keyed by id) and can be replayed to diagnose tool UX issues; Edit not-found diagnostics live in internal/tools/editdiag.go.
- 2026-07-08: buf went missing from PATH (2026-07-08); reinstalled via `go install github.com/bufbuild/buf/cmd/buf@latest` into ~/go/bin — Swift proto regen uses REMOTE BSR plugins (network required), Go regen uses local protoc-gen-go/protoc-gen-connect-go.

## User preferences
- 2026-07-08: Home-menu action affordances must be ctrl-chords, never naked letter keys (user rule; w/s/c became ctrl+w/ctrl+s/ctrl+l in 2026-07); keep new menu shortcuts consistent with this.
- 2026-07-08: iOS client decisions (2026-07): app lives in-repo at clients/ios (XcodeGen + YccKit SPM package, iPhone-only iOS 17+, connect-swift with committed generated code), notifications stay ntfy + ycc:// deep links (no APNs), and the work loop moves daemon-side (task 0179) rather than being client-driven.
