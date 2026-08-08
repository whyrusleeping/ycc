---
id: "0208"
title: Harden permissions for session logs and credential-bearing config
status: done
priority: 2
created: "2026-07-15"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - Session & event log#Storage
    - Backends & model registry
    - Persistence & remote sync
---

## Description
Session directories/logs currently default to `0755`/`0644` even though transcripts can contain prompts, source excerpts, tool output, and sensitive material. `config.Save` writes `0644` even though `Config.Notify.Auth` may contain an authorization credential. The ignored root `ant-key` also illustrates how easy it is for local plaintext credentials to remain broadly readable, though code must not inspect or migrate arbitrary user files without consent.

Define and enforce a private-by-default permissions policy for ycc-owned sensitive state, and move credential-bearing config values toward environment/secrets-store references where practical.

## Acceptance criteria
- [ ] Newly created `.ycc` session-state directories and `events.jsonl` files use owner-only permissions by default on Unix.
- [ ] Existing log files opened by ycc have overly broad permissions repaired where safe and documented.
- [ ] User-global config containing `notify.auth` is not left world-readable; either config files are private or notify credentials are referenced through the secrets store/environment.
- [ ] Project-local committed config behavior is addressed explicitly so tightening permissions does not pretend a committed bearer secret is safe.
- [ ] Daemon logs and other ycc-owned files are audited and assigned an intentional sensitivity/permission policy.
- [ ] Tests assert Unix modes and skip appropriately on platforms without Unix permission semantics.
- [ ] Documentation tells users to migrate loose plaintext key files into `ycc token` and never prints/reads such values during migration guidance.
- [ ] `go test ./...` passes.

## Plan

Define and enforce a private-by-default permissions policy for ycc-owned sensitive state, on Unix; document it; keep everything else intentionally at standard modes.

POLICY (to be documented in spec.md and enforced in code):
- Owner-only (dirs 0700, files 0600), created private and repaired best-effort on open:
  1. Workspace session state: `<workspace>/.ycc/` tree created by the event log — session dirs and `events.jsonl` (transcripts hold prompts/source/tool output).
  2. User config dir files written by ycc: `~/.config/ycc/ycc.toml` (may hold `notify.auth`) — `config.Save` writes 0600 and creates its dir 0700; `secrets.json` already 0600/0700 (unchanged).
  3. Daemon log `~/.cache/ycc/daemon.log` (stderr can echo prompts/errors): file 0600, `~/.cache/ycc` dir 0700, chmod-repair existing log best-effort.
- Intentionally standard (0755/0644), documented as such: committed docs (backlog/, plans/, memory.md, spec exports via `ycc export`), state-dir registries (`~/.local/state/ycc/{projects,workstreams,backlog-ids}.json` — paths/ids only, no secrets), clientconfig UI prefs, worktree bootstrap copies and tool Write/Edit outputs (normal working-tree content).

CODE CHANGES:
1. internal/event/log.go OpenLog: MkdirAll(dir, 0o700); OpenFile with 0o600; after opening an EXISTING file, best-effort os.Chmod file→0600 and session dir→0700 (ignore errors — repair only where we can, e.g. not the owner ⇒ no-op). Comment documents the repair.
2. internal/session/workstream_merge.go preserve-log copy (~line 352-370): create dst dirs 0o700 and copied files 0o600 (session state stays private when relocated to the primary tree).
3. internal/config/config.go Save: MkdirAll 0o700, WriteFile 0o600, plus best-effort chmod of a pre-existing file to 0600 (WriteFile keeps old mode). Note in the Save doc comment: this protects the LOCAL file only; a project-local ycc.toml committed to git is public regardless — credentials must not live there.
4. Notify env indirection: add `AuthEnv string toml:"auth_env,omitempty"` to config.Notify. notify.New resolves effective auth: cfg.Auth if non-empty, else os.Getenv(cfg.AuthEnv) when AuthEnv set. Document in spec §14 notify TOML sample. (Keeps auth out of config files entirely when preferred.)
5. internal/daemon/client.go: daemonLogPath MkdirAll 0o700; open log with 0o600 and best-effort chmod existing to 0600.
6. `ycc doctor`: add a permissions/credential-hygiene check: WARN when a workspace-local `<workspace>/ycc.toml` sets notify.auth inline (remedy: move to user config `~/.config/ycc/ycc.toml` or use notify.auth_env, since workspace config is typically committed); WARN when the user-global ycc.toml or an existing daemon.log/secrets.json is group/other-readable (remedy: chmod 600). Skip mode checks on windows. Doctor must never print credential VALUES and never read/parse arbitrary user key files (e.g. ant-key) — at most mention by policy in docs.

TESTS (assert modes on Unix; skip on windows via runtime.GOOS check, and mask-compare `mode.Perm()&0o077 == 0` after explicit chmods so umask can't flake):
- event: OpenLog creates dir 0700/file 0600; opening a pre-existing 0644 log repairs it to 0600.
- config: Save writes 0600 and repairs an existing 0644 file; dir created 0700.
- notify: New resolves auth from AuthEnv (t.Setenv) with Auth taking precedence.
- daemon or doctor tests where practical (doctor: workspace ycc.toml with inline notify.auth ⇒ warn line present; use existing doctor test harness patterns in cmd/ycc/doctor_test.go).

DOCS:
- spec.md §5.1 Storage: session state is private-by-default (0700/0600, repaired on open).
- spec.md §14: add a short "File permissions & credential hygiene" note with the policy table above, the notify `auth_env` option in the TOML sample, and the explicit statement that tightening modes does NOT make a committed workspace ycc.toml secret-safe — committed config must use `auth_env` or keep [notify] in the user-global config.
- spec.md §13 (or README): guidance to migrate loose plaintext key files (e.g. an ignored `ant-key`) into the secrets store via `ycc token set <KEY_ENV>`; ycc never reads or prints such files.

VERIFY: gofmt, go build ./..., go test ./... (known flaky: internal/session, internal/setup, internal/tools background-bash — compare against HEAD before blaming).

### Starting points
- internal/event/log.go OpenLog (MkdirAll 0755 / OpenFile 0644 at ~lines 77-94)
- internal/config/config.go Save (~line 658), Notify struct (~line 598), NotifyEventKinds
- internal/notify/notify.go New (auth field)
- internal/daemon/client.go daemonLogPath + log OpenFile (~lines 83-87, 146-154)
- internal/session/workstream_merge.go preserve-session-log copy (~lines 333-370)
- cmd/ycc/doctor.go check struct + runDoctor pattern; cmd/ycc/doctor_test.go harness
- internal/secrets/secrets.go — existing 0600/0700 pattern incl. Chmod-after-CreateTemp
- spec.md §5.1 (line ~153), §13 (~975), §14 (~1231, notify TOML sample ~1274)

## Work log
- 2026-08-07 plan: Define and enforce a private-by-default permissions policy for ycc-owned sensitive state, on Unix; document it; keep everything else intentionally at standard modes.  POLICY (to be documented in spec.
…[truncated]
- 2026-08-07 context hints: 8 recorded with plan
- 2026-08-07 context hints: internal/event/log.go OpenLog ~lines 76-94; internal/config/config.go Save ~line 658; Notify struct ~line 598; internal/notify/notify.go New ~line 50; internal/daemon/client.go log open ~lines 83-87, 
…[truncated]
- 2026-08-07 preload: 4 file(s), ~10 KiB seeded into implementer context
- 2026-08-07 implementer report: Implemented task 0208’s private-state policy.  Changes: - Session logs now create `.ycc` session directories as 0700 and `events.jsonl` as 0600; opening legacy logs best-effort repairs the log and s
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): revise — The permission enforcement, mode-repair paths, notify environment indirection, documentation policy table, and Unix-mode tests are otherwise well implemented, and `go test ./...` plus `go build ./...`
…[truncated]
- 2026-08-07 revision: Addressed both review findings:  - Changed `credentialHygieneChecks` to leniently read workspace `ycc.toml` with `toml.Unmarshal` into a notify-only wrapper, independent of full runtime config validat
…[truncated]
- 2026-08-07 review (sol): accept — The revision addresses both prior issues: doctor now leniently inspects the workspace `[notify]` table independently of full config validation, with coverage for partial project configs, and all guida
…[truncated]
- 2026-08-07 decision: accept — commit: Private-by-default permissions for session logs, config, and daemon log (task 0208)  Session .ycc dirs/events.jsonl now 0700/0600 with best-effort repair of legacy modes on open; config.Save writes 06
…[truncated]
- 2026-08-07 usage: 5,565,041 tok (in 865,901, out 44,036, cache_r 5,863,191, cache_w 128,964) · cost n/a (unpriced)
  implementer: 4,501,152 tok (in 513,184, out 22,528, cache_r 3,965,440, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 1,049,881 tok (in 352,675, out 7,542, cache_r 689,664, cache_w 0) · cost n/a (unpriced)
  coordinator: 14,008 tok (in 42, out 13,966, cache_r 1,208,087, cache_w 128,964) · cost n/a (unpriced)
