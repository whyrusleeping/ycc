---
id: "0138"
title: ycc doctor — one-shot environment/config health check with remedies
status: done
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 19. Onboarding flows
---

## Description
When something is misconfigured today the user finds out mid-session (401s, unreachable daemon, silently-degraded reviewer sandbox). A single `ycc doctor` command that checks the whole stack and says what's wrong — and how to fix it — collapses most support/onboarding friction into one command. It's also the natural thing to tell people to run in a bug report.

## Acceptance criteria
- [ ] `ycc doctor` reports, with ✓/✗/⚠ per line: config file discovered (and which path), each configured model's key resolution (env / secrets store / MISSING), persistent daemon reachability + whether one is running, sandbox mechanism available (landlock / bwrap / none), git repo present + clean/dirty, docs entry point + backlog found, EXA_API_KEY presence (web tools degrade without it).
- [ ] Each ✗ comes with a one-line remedy ("run `ycc token set OPENAI_API_KEY`", "run `ycc daemon` or use --background", …).
- [ ] Exit code non-zero when any hard failure (unresolvable model key, malformed config) is found, so it's scriptable.
- [ ] Works daemon-free (like `ycc spec-check`); daemon checks are best-effort probes.

## Plan

Add a daemon-free `ycc doctor` subcommand that health-checks the whole stack and prints one ✓/⚠/✗ line per check, each ✗ with a one-line remedy, exiting non-zero on hard failures.

Implementation:
1. New file `cmd/ycc/doctor.go`:
   - `(a *app) doctorCommand() *cli.Command` registered in `newRootCommand`'s Commands list (next to `specCheckCommand`).
   - Testable core `runDoctor(workspace, configPath, addr, token string, out io.Writer) (hardFail bool)` that appends check results and renders them. Represent each check as a small struct {status (ok/warn/fail), label, detail, remedy}; render as `✓/⚠/✗ label: detail` with `  ↳ remedy` on failures/warnings that have one.
2. Checks (in order):
   - **Config file**: use explicit `--config` if given, else `daemon.DiscoverConfig(workspace)`. Found → try `config.Load(path)`: parse/validate error is ✗ HARD (remedy: fix the named error in the file). Not found → ⚠ "no ycc.toml; built-in Anthropic fallback will be used" (remedy: run `ycc` to launch the first-run setup wizard).
   - **Model keys**: for each configured model (sorted by name), resolve its `KeyEnv`: `os.Getenv` hit → ✓ "env", `secrets.Lookup` hit → ✓ "secrets store", empty KeyEnv (e.g. ollama) → ✓ "no key required", otherwise ✗ HARD with remedy `run \`ycc token set <KEY_ENV>\` (or export it)`. With no config, check the fallback `ANTHROPIC_API_KEY` the same way. Never print secret values.
   - **Daemon**: best-effort probes, never hard. If `--addr` given probe it with the token; else probe `daemon.LocalAddr`. Reachable → ✓ "persistent daemon reachable at <addr>"; not → ⚠ "no persistent daemon running (plain `ycc` uses a one-shot in-process daemon)" with remedy "run `ycc daemon` or use `ycc --background`".
   - **Sandbox**: `sandbox.Available()` → landlock/bwrap ✓; None → ⚠ "reviewer bash confinement is prompt-only" (remedy on Linux: install bubblewrap or use a Landlock-capable kernel ≥5.13; elsewhere just note unsupported platform).
   - **Git**: MUST NOT mutate the workspace — do NOT use git.Open (it inits a repo). Run `git rev-parse --is-inside-work-tree` directly (exec.Command with Dir=workspace). In a repo → check `git status --porcelain`: clean ✓ / dirty ⚠ "N uncommitted change(s)". Not a repo → ⚠ "not a git repository (ycc will git init on first session)". git binary missing → ✗ HARD? No — treat missing git as ✗ with remedy "install git" but NOT counting toward exit code per AC? AC says hard failures are unresolvable model key / malformed config; keep git issues as ⚠ only.
   - **Docs**: `docs.NewStore(workspace)`: spec entry point (`SpecPath()`) exists → ✓ (print rel path); missing → ⚠ (remedy: create spec.md or set spec_path in .ycc/config.toml). Backlog: store dir (`Dir()`) exists → ✓ with task count from `List()`; missing → ⚠ "no backlog/ yet (created on first task)".
   - **EXA_API_KEY**: env or `secrets.Lookup` → ✓ (env / secrets store); missing → ⚠ "web_search/fetch_page tools disabled" with remedy `ycc token set EXA_API_KEY`.
3. Exit code: after rendering, print a one-line summary (`N ok, N warnings, N failures`). If any ✗ → return `cli.Exit("", 1)`. Warnings alone exit 0.
4. Tests `cmd/ycc/doctor_test.go` (pattern after speccheck_test.go if it tests runSpecCheck): drive `runDoctor` against temp workspaces — (a) valid config with key in env → no hard fail, key reported from env; (b) config with missing key → hardFail=true, output names the KEY_ENV and the `ycc token set` remedy; (c) malformed TOML → hardFail=true; (d) no config, ANTHROPIC_API_KEY unset → warn not hard-fail path exercised as designed (fallback key MISSING is a hard fail per AC "unresolvable model key" — decide: with no config file, missing fallback key should be ✗ HARD since every session would 401; remedy points at wizard/`ycc token set ANTHROPIC_API_KEY`). Use t.Setenv to control env; point secrets store away from the real one if secrets.Path allows (check internal/secrets for an env override; if none, only use env-based cases).
5. Docs: add a `ycc doctor` section to docs/cli.md (near spec-check). Run `go build ./... && go test ./cmd/... ./internal/...` and `go run ./cmd/ycc spec-check` to make sure docs references still resolve.

### Starting points
- cmd/ycc/main.go — newRootCommand Commands list, app struct (addr/token/configPath/workspace)
- cmd/ycc/speccheck.go — pattern for a daemon-free subcommand with testable core
- internal/daemon/client.go — DiscoverConfig, Reachable, LocalAddr
- internal/config/config.go — Load, Model.KeyEnv, resolveKey precedence (env then secrets)
- internal/secrets/secrets.go — Lookup, Path (check for env override for tests)
- internal/sandbox/sandbox.go — Available() Mechanism (landlock/bwrap/none)
- internal/docs/spec.go — Store.SpecPath/DocFiles; docs.NewStore(ws).Dir() is backlog dir
- internal/git/git.go — note: git.Open INITIALIZES a repo; doctor must probe read-only instead
- docs/cli.md — CLI reference to update (EXA_API_KEY section ~line 175)

## Work log
- 2026-07-06 plan: Add a daemon-free `ycc doctor` subcommand that health-checks the whole stack and prints one ✓/⚠/✗ line per check, each ✗ with a one-line remedy, exiting non-zero on hard failures.  Implementat
…[truncated]
- 2026-07-06 context hints: 9 recorded with plan
- 2026-07-06 context hints: cmd/ycc/main.go — newRootCommand Commands list; app struct fields addr/token/workspace/configPath; cmd/ycc/speccheck.go + speccheck_test.go — pattern for daemon-free subcommand with a testable cor
…[truncated]
- 2026-07-06 implementer report: Implemented `ycc doctor` — a daemon-free one-shot environment/config health check.  ## Changes - **cmd/ycc/doctor.go** (new): `(a *app) doctorCommand()` plus the testable core `runDoctor(workspace, 
…[truncated]
- 2026-07-06 revision: Fixed the .gitignore overmatch that was silently ignoring new source files.  ## Changes - **.gitignore**: replaced `/cmd/**/ycc` → `/cmd/*/ycc` and `/cmd/**/yccd` → `/cmd/*/yccd`. In gitignore sem
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change adds a daemon-free `ycc doctor` subcommand with a testable `runDoctor` core covering all required checks (config discovery, per-model key resolution with env→secrets precedence matching c
…[truncated]
- 2026-07-06 decision: accept — commit: ycc doctor: one-shot environment/config health check with remedies (task 0138); fix .gitignore /cmd/** overmatch that silently untracked cmd/ycc sources (rescues speccheck.go)
