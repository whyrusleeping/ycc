---
id: "0155"
title: Forge probe helper + ycc doctor check (gh/glab availability & auth)
status: done
priority: 4
created: "2026-07-06"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - docs/design/forge-integration.md#4. Auth strategy
---

## Description
From docs/design/forge-integration.md §3/§4 (design spike 0146). Foundation for all forge work.

Add a small `internal/forge` helper that:
- detects `gh`/`glab` availability + auth (`gh --version`, `gh auth status`; `glab` equivalents),
- infers forge + host from an issue URL or a git remote URL (github.com → gh, gitlab.com/self-hosted GitLab → glab; unrecognised host → clean "not a supported forge" error).

Wire a **non-fatal** forge check into `ycc doctor` (cmd/ycc/doctor.go): ✓ installed+authenticated / ⚠ installed but not authenticated (suggest `gh auth login`) / ⚠ not installed (forge features unavailable). Must be a warn — absence of forge CLIs must not affect doctor's exit code.

## Acceptance criteria
- [ ] `internal/forge` probe: installed/auth status + URL/remote → forge/host inference, unit-tested (exec stubbed).
- [ ] `ycc doctor` shows the forge check as a warn-only line; exit code unaffected when gh/glab absent.
- [ ] Errors are specific and actionable (name the missing CLI / the auth command).

## Plan

Add a small, dependency-free `internal/forge` probe package and wire a warn-only forge check into `ycc doctor` (design doc §3/§4/§9).

## 1. `internal/forge/forge.go`

Package doc: why we shell out to `gh`/`glab` (zero deps, auth delegated to the CLIs, local-daemon trust model) per docs/design/forge-integration.md §3/§4; note the daemon-environment caveat (the CLI must be installed/authed where the DAEMON runs, not on an attaching client).

- `type Kind string` with `GitHub Kind = "github"`, `GitLab Kind = "gitlab"`; methods `CLI() string` ("gh"/"glab") and `LoginCommand() string` ("gh auth login"/"glab auth login"). `Kinds()` (or a var) listing both in a stable order.
- `type Status struct { Kind Kind; CLI string; Installed bool; Version string; Authenticated bool; Hosts []string; Detail string }` — `Detail` carries a short reason when not installed/authenticated (never raw multiline CLI output; trim to one line).
- Injectable exec seam for tests:
  ```go
  type RunFunc func(ctx context.Context, name string, args ...string) (output string, err error)
  type Prober struct{ Run RunFunc; Look func(string) (string, error) } // zero value → real exec/LookPath
  func (p Prober) Probe(ctx context.Context, k Kind) Status
  func Probe(ctx context.Context, k Kind) Status // package-level, default prober
  ```
  Default runner uses `exec.CommandContext` + `CombinedOutput` (gh/glab write `auth status` to stderr on some versions) and returns the combined output plus the error; non-zero exit from `auth status` = not authenticated.
- Probe steps: `LookPath(cli)`; if absent → `Installed:false`. Else `<cli> --version` → parse version with a `\d+\.\d+(\.\d+)?` regex (tolerate unparseable: leave Version empty, still Installed). Then `<cli> auth status`; on success parse hosts from `Logged in to <host>` occurrences (dedupe, keep order); `Authenticated = err == nil`. Never contact anything ourselves; keep everything ctx-bounded.
- Errors + readiness helper for the follow-on flows (0156/0157):
  ```go
  var ErrNotInstalled, ErrNotAuthenticated, ErrUnsupportedForge = errors.New(...)
  func (s Status) Ready() error   // nil, or a wrapped, actionable error naming the CLI / the login command
  func Require(ctx context.Context, k Kind) error // Probe + Ready
  ```
  Messages must be specific and actionable, e.g. `gh is not installed (needed for GitHub issue import / PR publish); install it from https://cli.github.com` and `gh is installed but not authenticated; run \`gh auth login\``.
- Inference: `func Detect(rawURL string) (Kind, host string, err error)` accepting an issue URL, an https remote, an `ssh://git@host/o/r.git` remote and an scp-style `git@host:o/r.git` remote. Extract the host (strip userinfo, port, `www.`, lowercase); classify: host == `github.com` or prefix `github.` / contains `github` → GitHub; `gitlab.com` / prefix `gitlab.` / contains `gitlab` → GitLab (covers self-hosted/enterprise); otherwise `fmt.Errorf("%w: host %q is not a supported forge (github/gitlab)", ErrUnsupportedForge, host)`. Empty/unparseable input → clean error, never a panic.

## 2. `internal/forge/forge_test.go`

Table tests, exec fully stubbed via `Prober{Run: ..., Look: ...}` (no real gh/glab needed, no network):
- not installed; installed+authenticated (parse version + host(s) from realistic `gh auth status` and `glab auth status` fixture output, including the ✓/multi-host shapes); installed but `auth status` exits non-zero → Authenticated false; unparseable `--version` output → still Installed.
- `Ready()`/`Require` error text names the right CLI and login command and wraps the sentinel errors (`errors.Is`).
- `Detect` table: github.com issue URL, https remote, scp-style `git@github.com:o/r.git`, `ssh://git@gitlab.example.com/o/r.git`, `gitlab.com`, enterprise `github.mycorp.com`, unsupported `bitbucket.org` (→ `errors.Is(err, ErrUnsupportedForge)`), garbage/empty input.

## 3. Doctor wiring (`cmd/ycc/doctor.go`)

Add `forgeChecks()` producing WARN-ONLY lines (never `statusFail`), inserted after the git check, with a short ctx timeout (~5s, `context.WithTimeout`) so a slow `gh auth status` can't hang doctor:
- If neither CLI is installed → a single `⚠ forge: no forge CLI (gh/glab) installed; forge features (task import, PR publish) unavailable` with remedy `install gh (https://cli.github.com) or glab to enable forge features`.
- Otherwise one line per INSTALLED CLI: `✓ forge (gh): gh 2.62.0, authenticated (github.com)` or `⚠ forge (gh): gh installed but not authenticated` + remedy `run \`gh auth login\``. Uninstalled CLIs are simply not listed (no noise for a GitHub-only user).
Update the command's `Description`/`runDoctor` doc comment to mention the forge check. Confirm `runDoctor` still returns `fail > 0` only, so the exit code is unaffected.

## 4. `cmd/ycc/doctor_test.go`

Add a test that forces "no forge CLI" hermetically by `t.Setenv("PATH", t.TempDir())` (keeps git/gh out of PATH): assert the output contains the forge warn line and `runDoctor` reports no hard failure. Keep it robust on machines where gh IS installed by controlling PATH rather than asserting on real state.

## Verification
`gofmt`, `go build ./...`, `go test ./internal/forge/... ./cmd/ycc/...`, and a manual `go run ./cmd/ycc doctor` sanity check of the rendered line. No new module dependencies. No spec.md change (forge integration is still a design doc; the spec update is task 0163).

## Work log
- 2026-08-07 plan: Add a small, dependency-free `internal/forge` probe package and wire a warn-only forge check into `ycc doctor` (design doc §3/§4/§9).  ## 1. `internal/forge/forge.go`  Package doc: why we shell out
…[truncated]
- 2026-08-07 context hints: docs/design/forge-integration.md §3 (shell-out strategy + daemon-environment caveat), §4 (probe/doctor wording), §9 (failure-mode table); cmd/ycc/doctor.go: `check`/`checkStatus` types, `runDoctor`
…[truncated]
- 2026-08-07 implementer report: Implemented Task 0155.  Changes: - Added dependency-free `internal/forge` package with GitHub/GitLab kinds, stable kind enumeration, CLI/login-command mapping, injectable command/PATH seams, `gh`/`gla
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): accept — The implementation satisfies Task 0155. It adds a dependency-free, injectable forge probe with installed/version/auth/host reporting, actionable sentinel-wrapping readiness errors, and robust GitHub/G
…[truncated]
- 2026-08-07 decision: accept — commit: forge: gh/glab probe helper + warn-only ycc doctor check (task 0155)
