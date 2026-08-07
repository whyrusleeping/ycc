# Project memory

> Agent-maintained operational notes. Advisory, not normative — verify before relying.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.

## Codebase gotchas

- 2026-07-06: Usage accounting: OpenAI reports cached tokens as a SUBSET of prompt_tokens; Anthropic reports cache reads/writes disjoint from input_tokens; engine/loop.go normalizes to disjoint classes at emit; default pricing in internal/config/default_pricing.go (config price_\* overrides).
- 2026-07-08: E2E TUI harness (internal/e2e): a goroutine MUST drain emu.Read back into the PTY (vt.SafeEmulator answers terminal queries under its lock) or screen reads deadlock; skips without a PTY, no-ops under -short.
- 2026-07-28: Provider errors can arrive INSIDE an HTTP 200 stream (codex sends an SSE `error` frame, code `server_error`); adapters keep the provider error code in the message and apierror.go matches providerServerSignatures (server_error/internal_error) as retryable `server`.
- 2026-08-05: Anthropic subscription OAuth (2026 flow): claude.com/cai/oauth/authorize + platform.claude.com token/callback, extra user:sessions:claude_code/user:mcp_servers/user:file_upload scopes, beta headers claude-code-20250219,oauth-2025-04-20 + x-app: cli; credentials from the retired flow can misleadingly get HTTP 429.
- 2026-08-05: Anthropic refusals (stop_reason "refusal", HTTP 200) are STICKY per docs — recovery is drop the turn or switch model. Handled since task 0238: engine keeps refusal turns out of history (Result.Refused), replay skips them, session gates SendInput until Resume/model change; stop_details still unparsed (task 0239).
- 2026-08-05: StartSession takes an optional per-session coordinator_model override (task 0240): validated in Manager.start (ErrUnknownModel → InvalidArgument), recorded in session_started and folded into event.Projection.Coordinator so Reopen replays on the same model; ListModels still reports only the GLOBAL role defaults (see proposed task 0241).
- 2026-08-06: Duplicate backlog ids now SELF-HEAL: any docs.Store scan renumbers the younger claimant (internal/docs/dedupe.go, oldest `created` keeps the id) and `ycc doctor` reports the moves; the 14 historical collisions (0120/0175/0192-0195/0211-0218) were healed into 0262-0275 on 2026-08-06.
- 2026-08-06: iOS SessionProjection: the question row is resolved via openQuestionRowID (kept past an optimistic gate close), NOT via pendingQuestion — resolving through the gate was the bug that left answered cards reading "Waiting for an answer" (task 0247).
- 2026-08-06: Models sometimes leak XML invoke syntax into a JSON string tool argument (question swallowing options, description swallowing priority); internal/tools/argrepair.go repairs it schema-aware in Registry.Repair (engine loop pre-emit) + Dispatch — check the tool_call event's `repaired` field when a tool looks like it lost arguments.
- 2026-08-06: Anthropic OAuth tokens are resolved PER TURN by anthropicauth.NewOAuthTurner (config.Build passes TokenSource{AccessToken, ForceRefresh, c.SetBearerToken}); a token frozen at Build dies whenever any other process refreshes, because Anthropic invalidates the previous access token on every refresh-token redemption.
- 2026-08-06: iOS composers: clearing a SwiftUI TextField binding on send is not enough — UIKit commits a pending autocorrection afterwards and writes the text back; fix is a one-runloop `.autocorrectionDisabled` pulse + deferred re-clear (done in SessionView.send, task 0278).
- 2026-08-06: The work loop is daemon-owned end to end since tasks 0179/0190/0267: TUI and iOS both only call StartWorkLoop/StopWorkLoop/GetWorkLoop and poll for state (TUI guards poll responses with a loopSeq generation and attaches to WorkLoopInfo.CurrentSessionId via reopenSession); loop state is still in-memory only, so a daemon restart loses a running loop and its digest (task 0280).
- 2026-08-06: iOS unread ("new agent messages") is client-side only: YccKit/SessionReadStore keeps a per-session watermark in UserDefaults from DAEMON timestamps (SessionProjection.lastEventTimestamp), baselines first sightings as read, never badges a still-running session, and SessionView marks read on disappear/background (task 0283).

## Environment & tooling

- 2026-08-05: The workspace machine (cloverleaf, Linux) has NO Swift toolchain and connect-swift is Apple-only — iOS builds/tests must run on the user's Mac: `cd clients/ios/YccKit && swift test` (XCTest; grep "Test Suite 'All tests' passed") and `cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build`. Generated .xcodeproj and App/Info.plist are git-ignored; keep ad-hoc signing (CODE_SIGN_IDENTITY "-") or simulator Keychain calls fail (-34018).
- 2026-07-07: Tool-failure forensics: session transcripts in <workspace>/.ycc/sessions/\*/events.jsonl (tool_call/tool_result pairs by id); Edit not-found diagnostics in internal/tools/editdiag.go.
- 2026-07-08: buf lives in ~/go/bin (has vanished from PATH before) — Swift proto regen uses REMOTE BSR plugins (network required), Go regen uses local protoc-gen-go/protoc-gen-connect-go.
- 2026-07-10: `go test ./...` flakes seen in internal/session TestReconcileWorkstreams, internal/setup TestConfigPath, internal/tools TestBackgroundBashWaitReturnsExitAndOutput (all passed 2026-07-28); verify against HEAD before blaming new work.

## User preferences

- 2026-07-08: Home-menu action affordances must be ctrl-chords, never naked letter keys.
- 2026-07-08: iOS client decisions: in-repo at clients/ios (XcodeGen + YccKit SPM, iPhone-only iOS 17+, committed generated proto code), notifications via ntfy + ycc:// deep links (no APNs), work loop daemon-side (task 0179).
- 2026-08-06: iOS tasks pile up in `in_review` because Swift/xcodebuild can't run on the Linux workspace — treat that status as "implemented, awaiting the user's on-device use", and sweep it during backlog audits rather than letting it grow.

## Lessons learned

- 2026-07-09: For user-reported TUI/session issues, check .ycc/sessions in ALL workspaces the user runs ycc in (find candidates via running ycc processes' cwds); filter events.jsonl for `session_error` to surface real provider failures.
- 2026-07-15: Anthropic subscription OAuth verified live (bearer + anthropic-beta: oauth-2025-04-20, no system-prompt spoofing); internal/anthropicauth + config.Registry.Build; ~8h access tokens auto-refresh.
- 2026-07-15: ChatGPT subscription (codex backend) verified live; July-2026 catalog gpt-5.6-sol/gpt-5.5/gpt-5.4/gpt-5.4-mini; ycc uses its own OPENAI_OAUTH login (don't share ~/.codex/auth.json refresh tokens).
