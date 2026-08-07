---
id: "0203"
title: Keep daemon bearer tokens out of spawned process arguments
status: done
priority: 2
created: "2026-07-15"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - Daemon lifecycle & projects
    - RPC protocol (Connect)
---

## Description
The background-daemon startup path currently appends the bearer token as `-token <value>` to the detached child's argv. Even when the caller supplied `YCC_TOKEN`, this exposes the token through process listings and `/proc/.../cmdline` on applicable systems.

Pass the token through the child's environment or another non-argv mechanism while retaining authenticated readiness probing and remote-client behavior.

## Acceptance criteria
- [ ] `EnsureBackgroundDaemon` never places the bearer token in child argv.
- [ ] A newly spawned daemon still receives the token via `YCC_TOKEN` or an equivalently private mechanism and enforces it.
- [ ] An existing token-protected local daemon is probed and attached using the caller's token.
- [ ] Tests inspect the constructed child command/environment without exposing a real token in failure logs.
- [ ] CLI/help documentation recommends `YCC_TOKEN` for secrets and does not encourage process-argument exposure.
- [ ] Token values are not printed in daemon startup errors or logs.
- [ ] `go test ./...` passes.

## Plan

Goal: `EnsureBackgroundDaemon` (internal/daemon/client.go) must never put the bearer token in the spawned child's argv; pass it via the child's environment instead. The `ycc daemon` subcommand's `-token` flag already has `Sources: cli.EnvVars("YCC_TOKEN")` (cmd/ycc/main.go ~line 573), so an env-passed token is picked up and enforced by the child with no server-side change.

Steps:
1. In internal/daemon/client.go:
   - Extract a small pure helper, e.g. `backgroundDaemonCmdline(self, workspace, configPath, token string) (args []string, env []string)`, that builds the child argv (no `-token`) and the child env: start from `os.Environ()` with any existing `YCC_TOKEN=` entries stripped, then append `YCC_TOKEN=<token>` when token != "" (strip-then-add keeps behavior deterministic if the parent env already had a stale YCC_TOKEN). Making it pure (or taking a base env slice) makes it unit-testable.
   - `EnsureBackgroundDaemon` uses the helper for `cmd.Args`-equivalent and `cmd.Env`. Everything else (detach, stdio redirection, readiness probing with the caller's token via `Reachable(LocalAddr, token)`) stays as is — an already-running token-protected daemon is still probed/attached with the caller's token, unchanged.
   - Update the function's doc comment to explain the env-based token handoff and why argv is avoided (process listings, /proc/*/cmdline).
2. Add internal/daemon/client_test.go unit tests for the helper:
   - argv never contains the token value or a `-token` flag (with and without a token supplied);
   - env contains exactly one `YCC_TOKEN=<value>` entry when a token is given, none when empty, and pre-existing YCC_TOKEN entries in the base env are replaced/stripped;
   - use an obviously fake token (e.g. "fake-test-token") and write assertions that don't dump full env/argv into failure messages beyond the fake value.
3. Verify no token leakage in startup errors/logs: EnsureBackgroundDaemon's error strings must not embed the token (they currently don't — keep it that way); scan `internal/daemon/serve.go` for any token logging just in case.
4. CLI help text (cmd/ycc/main.go): adjust the daemon `-token` flag usage and the daemon command description to recommend `YCC_TOKEN` for supplying secrets rather than a literal `--token <t>` on the command line (e.g. show `YCC_TOKEN=… ycc --addr … list` style in the description). Keep the flag itself for compatibility.
5. Run `go build ./...`, `go test ./internal/daemon/...`, then `go test ./...` (memory notes some pre-existing flakes in internal/session, internal/setup, internal/tools — compare against HEAD before blaming).

### Starting points
- internal/daemon/client.go — EnsureBackgroundDaemon builds args including `-token` at ~line 74-76
- cmd/ycc/main.go:573 — daemon subcommand `-token` flag already Sources cli.EnvVars("YCC_TOKEN")
- cmd/ycc/main.go:553-565 — daemon command description mentions `--token $YCC_TOKEN` client usage
- existing tests: internal/daemon/serve_guardrails_test.go for style reference

## Work log
- 2026-08-07 plan: Goal: `EnsureBackgroundDaemon` (internal/daemon/client.go) must never put the bearer token in the spawned child's argv; pass it via the child's environment instead. The `ycc daemon` subcommand's `-tok
…[truncated]
- 2026-08-07 context hints: 4 recorded with plan
- 2026-08-07 context hints: internal/daemon/client.go lines 40-110: EnsureBackgroundDaemon; `-token` appended to args at ~line 74-76; cmd/ycc/main.go:573 — daemon subcommand token flag with Sources: cli.EnvVars("YCC_TOKEN"); c
…[truncated]
- 2026-08-07 preload: 1 file(s), ~8 KiB seeded into implementer context
- 2026-08-07 implementer report: Implemented task 0203.  Changes: - Refactored background daemon command construction into `backgroundDaemonCmdline`, which always omits bearer tokens from argv. - The spawned process environment now s
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): revise — The code change correctly keeps the background daemon token out of argv, deterministically replaces inherited YCC_TOKEN entries, preserves authenticated readiness probes, avoids logging token values, 
…[truncated]
- 2026-08-07 revision: Addressed the documentation review gap.  Changes: - Updated both global and daemon `--token` entries in `docs/cli.md` to describe the flag as compatibility-only, prefer `YCC_TOKEN`, and warn that argv
…[truncated]
- 2026-08-07 review (sol): accept — The revision addresses the prior documentation gap: both the CLI reference and remote API guide now recommend `YCC_TOKEN`, warn against argv exposure, and use environment-based examples. The implement
…[truncated]
- 2026-08-07 decision: accept — commit: Pass daemon bearer token via YCC_TOKEN env, never child argv (task 0203)
