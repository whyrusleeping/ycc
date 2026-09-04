# Project memory

> Agent-maintained operational notes. Advisory, not normative — verify before relying.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.

## Codebase gotchas

- Token classes are DISJOINT (engine/loop.go); pricing in internal/config/default_pricing.go.
- E2E TUI harness (internal/e2e): a goroutine MUST drain emu.Read back into the PTY or screen reads deadlock; skips without a PTY.
- Provider errors can arrive INSIDE an HTTP 200 stream (codex SSE `error` frame); apierror.go treats server_error/internal_error as retryable. Codex rejects max_output_tokens (spec §7).
- Anthropic refusals are STICKY (0238); OAuth tokens resolved PER TURN — refresh invalidates the prior token; retired-flow creds can show as HTTP 429.
- Models sometimes leak XML invoke syntax into JSON tool args; internal/tools/argrepair.go repairs.
- Work loop is daemon-owned; persisted per workspace (workloop_persist.go) — restart restores as finished/interrupted, never auto-resumes.
- Codex stateless replay: Responses items ride as ONE marked ThinkingBlock; engine.messagesForBackend strips them for non-openai backends (0197). Never emit user_input events for synthetic/subagent history (0175).
- iOS: question rows via openQuestionRowID; unread badges are client-side watermarks (SessionReadStore); nav via HomeRouter.open dedupe; manual keyboard avoidance (App/KeyboardObserver.swift, FB13296535); composers use UIKit-backed ComposerTextField (SwiftUI TextField can't paste images) with @State Bool focus (not @FocusState) and the autocorrect-pulse clear trick — don't revert to TextField.
- Manager goroutines running git must be joinable (ReclaimAll waits) or TempDir tests race; server.workstreamError maps errors BY STRING.
- Event-log failure is TERMINAL (0198): Record returns Seq==0; durable emit sites must check Emitter.Err()/ctx before mutating state.
- Backlog ids are daemon-allocated per project (docs.IDAllocator); duplicates self-heal on docs.Store scan (`ycc doctor` reports moves).
- Model turns are ctx-aware via TurnCtx/TurnStreamCtx (0204); gollama legacy Turn/TurnStream use context.Background — never use in inference paths.
- ycc spec-check symbol search matches untracked .ycc/ logs — can pass live yet fail on a clean checkout; verify via git archive temp dir (0301 open).
- GetUsage with empty project + multiple projects returns the ALL-projects rollup (0287); iOS drawer's Usage row opens .usage(project: "").

## Environment & tooling

- No Swift toolchain here — iOS builds/tests on the user's Mac: `swift test` in clients/ios/YccKit; `xcodegen generate && xcodebuild …`; keep ad-hoc signing or simulator Keychain fails (-34018).
- Tool-failure forensics: <workspace>/.ycc/sessions/\*/events.jsonl; Edit diagnostics in internal/tools/editdiag.go.
- buf in ~/go/bin; Swift proto regen uses REMOTE BSR plugins (network), Go local. After ANY proto change commit BOTH regens (0254).
- `go test ./...` has known flaky tests (internal/session, internal/setup, internal/tools background-bash); verify against HEAD first.
- The `commit` tool does `git add -A` — don't use it when unrelated work is in the tree (see selective-commit lesson).
- Live-model checks feasible: ANTHROPIC_API_KEY set; codex.New("", openaiauth.AccessToken) uses ycc's ChatGPT OAuth login.

## User preferences

- Home-menu action affordances must be ctrl-chords, never naked letter keys.
- iOS client: in-repo clients/ios (XcodeGen + YccKit SPM, iPhone-only iOS 17+, committed protos), ntfy + ycc:// deep links, work loop daemon-side.
- iOS `in_review` means "implemented, awaiting on-device use" — sweep during backlog audits; such work may sit UNCOMMITTED across sessions.
- iOS project rename/removal live only in drawer rows' long-press context menu — intentional.

## Lessons learned

- For user-reported TUI/session issues, check .ycc/sessions in ALL workspaces; filter events.jsonl for `session_error`.
- For verbatim code-move refactors, diff sorted go/ast decl dumps of HEAD vs new trees (0210).
- Selective commit when the tree holds other tasks' work: snapshot pre-task worktree (tar + `git diff HEAD`); apply diff(pre, worktree) onto `git show HEAD:` blobs; NEVER interdiff generated protos (buf generate in temp tree); adapt interdiffed tests to HEAD's API; gofmt replayed edits; verify assembled git-archive temp tree; commit via GIT_INDEX_FILE temp index + commit-tree; if staging clobbered the pre-task index, filter combined hunks per task and check the residual equals the other task's hunks (0207/0275/0289/0293/0303).
