# Design: git-forge integration (issues → tasks, workstreams → PRs)

> Status: **proposal** (design spike, task 0146). No code lands with this doc;
> the follow-on implementation tasks in §11 are filed separately.
> Grounded in the current architecture: spec §6.2 (backlog structure, per-file
> tasks, `spec_refs`), §8 (tools), §11 (interaction levels + the "Exception —
> confirmation gates" rule), §12 (RPC surface), §14.1 (parallel workstreams:
> branch `ycc/ws/<id>`, `workstreams.json` registry, conflict-aware
> review-gated merge, lifecycle events, RPC surface), and the session→markdown
> exporter (task 0144, `cmd/ycc/export.go` + `internal/export`).

## 1. Context / problem

ycc's core units already line up, almost one-to-one, with the primitives that
GitHub and GitLab expose. Three seams already exist in the codebase and each
maps naturally onto a forge concept:

- **Issues → backlog.** A backlog task (spec §6.2) is a title + description +
  `spec_refs` + a work log. A forge issue is a title + body + URL + labels.
  Importing an issue as a task — with a link back to the issue and optional
  status sync when the task is done — is the killer feature for any team whose
  intake already lives in issues. The seam is `ycc task import <issue-url>`
  slotting into `cmd/ycc/task.go` beside `add`/`list`/`show`.
- **Workstream → PR.** A workstream (spec §14.1) is *already* a branch
  (`ycc/ws/<id>`) with a review-gated terminal transition. Today the terminal
  states are **merge** (integrate to base) and **discard**. "Open a PR instead
  of merging to base" is a natural third terminal state — **publish** — sitting
  right beside `MergeWorkstream`/`DiscardWorkstream` in
  `internal/session/workstream_merge.go`. The PR body writes itself: the session
  transcript exporter (task 0144) already renders reviewer findings + the final
  report to markdown.
- **Work session → PR (non-workstream).** For a plain `work` session that
  committed directly on a branch, an optional `commit → push → gh pr create`
  flow behind a confirmation gate lets a user ship without leaving ycc.

The design question this spike answers: **how does ycc talk to a forge** (shell
out to `gh`/`glab` vs. Go API client libraries), **how much is first-class
tooling vs. agent-prompt runbook**, and **what are the auth, failure, and safety
semantics** for actions that have public, hard-to-reverse side effects.

## 2. Goals & non-goals

**Goals**

- Import a GitHub/GitLab issue as a backlog task with a durable link back to the
  issue, working with both `ycc task` backends (direct `docs.Store` and daemon
  RPC).
- Add a **publish** terminal state to workstreams: push the branch and open a PR
  with a body composed from the session summary + reviewer findings.
- Provide a prompt-level runbook for the non-workstream "ship this session as a
  PR" flow, gated by a human confirmation.
- Pick a forge-access strategy that adds **zero new dependencies** and **reuses
  the user's existing forge auth**.
- Specify failure modes (no CLI, not authenticated, no remote, PR exists,
  network failure mid-publish) and the safety/gating story for public side
  effects.

**Non-goals**

- A general forge abstraction covering every forge. v1 targets GitHub (`gh`)
  first with GitLab (`glab`) as the parallel second; Bitbucket/Gitea/etc. are
  out of scope until demand appears.
- Storing forge credentials in ycc. Auth is delegated entirely to the CLIs (§4).
- Bidirectional issue sync / a forge webhook listener (pulling issue updates
  into the backlog continuously). v1 is import-on-demand + optional close-on-done
  (§6). Live sync is discussed as a rejected alternative (§10).
- Replacing the local merge flow. `MergeWorkstream` (merge to base locally) stays
  the default; publish is an *alternative* terminal state, not a replacement.
- Automatic PR review / merging PRs from ycc. ycc opens the PR; the forge owns
  the review/merge from there.

## 3. Forge access strategy (the core decision)

Two ways to talk to a forge:

### (a) Shell out to `gh` / `glab` (recommended for v1)

Invoke the official CLIs via the same `Bash`/`exec` machinery ycc already uses
for git. Issue import is `gh issue view <url> --json …`; publishing is
`git push` + `gh pr create --title … --body-file …`.

- **Dependencies:** zero new Go modules. ycc already shells out to `git`
  (`internal/git`), so a `gh`/`glab` exec is the same shape.
- **Auth:** *free and correct.* `gh`/`glab` own the keyring/token/SSO/OAuth
  refresh dance (`gh auth login`, `gh auth token`, `GH_TOKEN`, `GITHUB_TOKEN`,
  enterprise hosts, etc.). ycc never sees or stores a credential (§4).
- **Trust model fit:** ycc's daemon runs **on the user's machine** (spec §3.1;
  remote clients only *observe*). The forge CLI is configured on that same
  machine. Delegating to it matches the "daemon is the single writer on the
  user's box" model exactly.
- **Output shape:** `gh --json`/`glab --output json` give stable, parseable JSON
  — no HTML scraping.
- **Cost:** requires the CLI to be installed and authenticated, and requires it
  to be present **in the daemon's environment**, not the attaching client's (see
  the caveat below). Parsing is per-CLI (gh and glab differ in flags/JSON), so
  each forge is a small adapter.

### (b) Go API client libraries (`go-github`, `go-gitlab`)

Link the forge SDKs and call the REST/GraphQL APIs directly.

- **Dependencies:** two sizeable modules + their transitive deps.
- **Auth:** ycc must now **acquire and hold a token** — config field, env var,
  or a secrets-store entry (`internal/secrets`). That is exactly the credential
  surface (a) avoids: token storage, scoping, rotation, enterprise/self-hosted
  base URLs, SSO. It also means a first-class doctor/settings story for tokens.
- **Upside:** no external process; richer error typing; works headless without a
  CLI installed. Genuinely better *only* in a headless/multi-tenant hosted
  deployment — a non-goal today.

### Recommendation

**Shell out to `gh`/`glab` for v1.** It adds no dependencies, reuses the user's
existing auth (the single biggest source of complexity in (b)), and matches
ycc's local-daemon trust model. Revisit API clients only if/when a **headless or
multi-tenant hosted** ycc appears, where "the CLI is installed and logged in on
this box" stops holding — at which point the forge adapter interface (§5) is the
seam to swap the implementation behind.

**Daemon-environment caveat.** For a persistent/remote daemon (`ycc daemon`),
`gh`/`glab` must be installed and authenticated in the **daemon's** environment
and user account — not the attaching client's. A phone/laptop client triggering
`PublishWorkstream` on a remote daemon relies on *that host's* `gh` auth. The
doctor probe (§4) and RPC errors must make this explicit, because "it works on
my laptop's `gh`" is a natural but wrong mental model for remote use.

## 4. Auth strategy

- **No forge tokens in ycc config for v1.** Credentials are delegated entirely to
  `gh`/`glab`. ycc stores nothing forge-secret; there is no new field in the
  config file and no new `internal/secrets` entry.
- **Detection / probe.** A cheap availability + auth probe backs every flow:
  `gh --version` (installed?) and `gh auth status` (logged in? which host?).
  `glab auth status` is the GitLab equivalent. The probe result feeds:
  - **Doctor.** Add a forge check to `ycc doctor` (`cmd/ycc/doctor.go`):
    `✓ gh 2.x, authenticated (github.com)` / `⚠ gh installed but not
    authenticated → run 'gh auth login'` / `⚠ gh not installed → forge features
    (task import, PR publish) unavailable`. This is a **warn**, not a hard fail —
    forge integration is optional and its absence must not break `doctor`'s exit
    code.
  - **RPC/CLI errors.** Each flow surfaces a specific, actionable error when the
    probe fails (§9), rather than a raw non-zero exit from the CLI.
- **Host inference.** The forge (github vs gitlab) and host are inferred from the
  issue URL (import) or the git remote URL (publish). A repo whose remote is
  `github.com` uses `gh`; `gitlab.com`/self-hosted GitLab uses `glab`. An
  unrecognised host is a clean "not a supported forge" error (§9).

## 5. Flow 1 — issues → backlog (`ycc task import`)

**Surface.** A new `ycc task import <issue-url>` subcommand in
`cmd/ycc/task.go`, beside `add`/`list`/`show`. It reuses the existing
`taskBackend` resolution (direct `docs.Store` when no daemon; RPC when a daemon
is reachable or `--project` is given), so import works with or without a daemon,
exactly like `add`.

**Fetch.** From the URL, infer forge + host, then:

```sh
gh issue view <url> --json number,title,body,url,labels,state
# or: glab issue view <url> --output json
```

**Field mapping.**

| Forge issue        | Backlog task (spec §6.2)                                   |
|--------------------|-----------------------------------------------------------|
| title              | `title`                                                    |
| body (markdown)    | task `## Description` body                                 |
| url                | link-back (see below)                                      |
| labels (optional)  | ignored in v1 (could map to priority later)               |
| number/state       | used for dedupe + status sync (§ below)                    |

**Link-back mechanism (the concrete decision).** Record the origin issue URL as
a machine-findable line, not a free-form prose mention. Two candidates:

1. Reuse `spec_refs`: append the issue URL as a `spec_refs` entry. *Rejected* —
   `spec_refs` has defined semantics (spec §6.2: a spec section title or
   `path#Section` into the docs set); an external issue URL is not a doc
   reference and would pollute spec-doctor coverage checks.
2. **A dedicated origin field.** Add an optional `origin:` frontmatter field to
   the task (`docs.Task`), holding the issue URL. This keeps the link
   first-class, queryable, and cleanly separate from `spec_refs`. **Recommended.**
   As a zero-schema-change fallback, the importer can instead write an
   `> Imported from <url>` line at the top of the `## Description` body, but the
   frontmatter field is preferred because dedupe (below) can read it reliably.

**Dedupe on re-import.** Before creating, scan the backlog for a task whose
`origin` equals the issue URL. If found, **update in place** (refresh title/body,
append a work-log breadcrumb) rather than creating a duplicate; print the
existing id. This makes `task import` idempotent and safe to re-run.

**Status sync on done (opt-in, off by default in v1).** When a task imported
from an issue reaches `done`, ycc *can* close the issue and/or drop a comment
(`gh issue close <url> --comment "Resolved by ycc: <commit/PR>"`). Because this
is a public, hard-to-reverse side effect on someone else's tracker, it is
**config-gated and off by default** for v1 (e.g. `forge.close_issue_on_done:
false`). When enabled, it still runs behind the confirmation semantics of §8.
The hook point is the task's transition to `done` (backlog update path); v1 may
ship import-only and defer the close hook to a follow-on (§11) if the done-hook
plumbing is not yet convenient.

**Backend note.** Fetching/parsing the issue is client-side (the CLI runs where
`ycc task import` runs). Task *creation* goes through whichever `backlogBackend`
is resolved — so an `origin` field must be threaded through both `directBackend`
(→ `docs.Store.Create`) and `rpcBackend` (→ `CreateTask` RPC, needing a new
`origin` field on `CreateTaskRequest`/`TaskDetail`).

## 6. Flow 2 — workstream → PR (publish)

Today a workstream ends in **merge** or **discard** (§14.1,
`internal/session/workstream_merge.go`). Add **publish** as a third terminal
state: push the workstream branch to the remote and open a PR, instead of
merging locally to base.

**Why an RPC, not client-side.** Merge/discard are already daemon RPCs
(`MergeWorkstream`/`DiscardWorkstream`) because the daemon owns the worktree, the
registry, the session, and the event log. Publish touches all four (it reads the
session transcript, pushes the daemon-managed branch, updates registry status,
and emits a lifecycle event), so it belongs in the daemon too — a
**`PublishWorkstream` RPC mirroring `MergeWorkstream`**. This also gives remote
clients (phone/laptop attaching to `ycc daemon`) the feature for free, exactly
like the existing workstream RPCs. Placing it client-side would duplicate
registry/event logic and wouldn't work for remote clients.

**Flow (`Manager.PublishWorkstream(id, accept)`):**

1. **Preconditions.** Workstream is active; probe `gh`/`glab` (installed + auth,
   §4); resolve the git remote for the branch and confirm it's a recognised
   forge. Any failure → a specific error (§9), nothing mutated.
2. **Gate.** Pushing a branch and opening a public PR is a hard-to-reverse side
   effect. Mirror `MergeWorkstream`'s gate: under *autonomous* proceed; under
   *interactive/judgement* return `NeedsAccept` with a preview (branch, remote,
   PR title, PR body) until `accept=true` (§8).
3. **Compose the PR body.** Reuse `internal/export`: render the workstream's
   session transcript (reviewer verdicts + final report + commit summary) to
   markdown via the same path `GetSessionTranscript` + `export.Markdown` feed
   `ycc export`. This is the natural, already-built PR-body source (task 0144).
   Title defaults to the focused task title (or the first commit subject).
4. **Push + create.**
   ```sh
   git push -u <remote> ycc/ws/<id>
   gh pr create --head ycc/ws/<id> --base <base> --title <t> --body-file <tmp>
   # or: glab mr create --source-branch … --target-branch … --title … --description …
   ```
   Capture the created PR URL from CLI output.
5. **Record + status.** Emit a new lifecycle event `workstream_published`
   `{ workstream, branch, pr_url }` on the workstream's session stream (via the
   existing `emitWorkstreamEvent`). Set registry status to a new terminal value
   **`published`** (`workstream.Status`), storing the PR URL.
6. **Cleanup semantics — differ deliberately from merge.** On merge/discard,
   `cleanupWorktree` removes the worktree and **deletes the branch**. For
   publish, the branch **must survive** — the PR points at it, and the user may
   push follow-up commits after review. So publish:
   - preserves the session log into the primary workspace
     (`preserveWorkstreamSession`, so the transcript stays viewable),
   - removes the *worktree* (or keeps it, configurably — see below),
   - **does not delete the branch** (it lives on the remote and locally).
   Recommendation: remove the worktree by default (the work is "handed off" to
   the PR) but keep the local + remote branch. If the user wants to keep pushing
   from the worktree, offer a "publish and keep worktree" option that leaves the
   worktree active with status `published`. This is the one place publish's
   lifecycle genuinely differs from merge and deserves an explicit config/param.

**Idempotency / resume.** The push and the PR-create are two steps; a network
failure between them leaves the branch pushed but no PR (§9). `PublishWorkstream`
must be **resumable**: on retry, if the branch already exists on the remote and a
PR already exists for it (`gh pr view <branch>` / `gh pr list --head`), report
the existing PR URL and transition to `published` rather than erroring or opening
a duplicate.

## 7. Flow 3 — work session → PR (non-workstream, prompt-level)

For a plain `work` session (not a workstream) that committed on a branch, "open a
PR for this" stays **prompt-level in v1** — a reusable runbook under `plans/`
(spec §6.3), not first-class tooling.

- **Mechanism.** A `plans/publish-pr.md` runbook the coordinator/implementer can
  execute with the tools they already have: `Bash` for `git push` +
  `gh pr create`, `Read` to pull the final report for the body. The runbook
  documents the steps, the confirmation expectation, and the failure checks.
- **Confirmation gate.** Because push + PR-create are public side effects, the
  runbook instructs the agent to seek an explicit human confirmation before
  pushing (spec §11 "Exception — confirmation gates"): treat it like
  `switch_to_work` — a real human yes/no even under *autonomous*, declined (no
  push) if no human is available.
- **Why not first-class yet.** Unlike a workstream, a plain work session has no
  daemon-owned branch/registry/terminal-state machinery to hang a clean RPC off
  — the branch is whatever the session committed to, cleanup is the user's, and
  the "is this ready to ship" judgement is inherently interactive. A runbook
  driving existing `Bash`/`Read` tools covers it with zero new surface. If usage
  shows this is common, it graduates to a `PublishSession` RPC / `publish_pr`
  coordinator tool in v2 (§11).

## 8. Safety & gating

Pushing branches, opening PRs, and closing issues are **public, hard-to-reverse
side effects** on shared infrastructure — the exact category spec §11 covers with
the "Exception — confirmation gates" rule. Semantics per interaction level:

| Level         | Publish workstream / open PR / close issue                          |
|---------------|---------------------------------------------------------------------|
| interactive   | preview shown, requires explicit accept (like `MergeWorkstream`)    |
| judgement     | requires explicit accept (hard-to-reverse ⇒ gate, per §11)          |
| autonomous    | proceeds — but see note                                             |

Note on autonomous: local merge auto-proceeds under *autonomous* because it only
touches the local base branch, which is recoverable. A **PR/push is externally
visible** and harder to walk back. Two defensible policies:

1. Treat publish like merge — autonomous auto-publishes. Simple, consistent.
2. Treat publish like `switch_to_work` — a **confirmation gate that seeks a real
   human even under autonomous** and *declines* (no push) if none is available.

**Recommended:** policy (2) for the *non-workstream* Flow 3 (an ad-hoc ship is
riskier) and policy (1) for the *workstream publish* Flow 2 when the user
explicitly chose "publish" as the terminal action (the intent to go public is
already expressed by selecting publish). Both are within the spec §11 framework;
the exact default is a small config knob (`forge.confirm_publish`).

## 9. Failure modes

Every flow probes first and returns a **specific, actionable** error rather than
leaking a raw CLI non-zero exit:

| Failure                                  | Behaviour                                                                                   |
|------------------------------------------|--------------------------------------------------------------------------------------------|
| `gh`/`glab` not installed                | Clear error naming the missing CLI + install hint; doctor shows ⚠. No partial action.       |
| CLI installed but not authenticated      | Error → "run `gh auth login`"; doctor shows ⚠.                                              |
| No git remote (publish)                  | Error: cannot publish without a remote; suggest `git remote add`. Nothing pushed.           |
| Remote host not a recognised forge       | Error: "remote `<host>` is not a supported forge (github/gitlab)". Nothing pushed.          |
| Fork vs origin push rights               | Push may fail (no write access) → surface the push error; suggest fork + `--head owner:branch`. Branch not left half-pushed beyond git's own atomicity. |
| PR already exists for the branch         | Idempotent: detect via `gh pr view/list --head`, report existing PR URL, mark `published`. No duplicate. |
| Network failure mid-publish (pushed, no PR) | Resumable: retry detects the pushed branch + missing PR and only creates the PR. Registry stays non-terminal until the PR exists, so a retry is safe. |
| Issue URL unparseable / 404 (import)     | Clean error; no task created.                                                               |
| Re-import of an already-imported issue   | Dedupe on `origin`: update in place, print existing id (§5).                                |
| Daemon lacks CLI/auth (remote client)    | Error names that the **daemon host** needs `gh` auth, not the client (§3 caveat).           |

Guiding principle: **never leave the world in a half-committed public state that
a re-run can't reconcile.** The registry transitions to `published` only after
the PR URL is confirmed; before that, the operation is safe to retry.

## 10. Alternatives considered

- **Go API clients (`go-github`/`go-gitlab`) for v1.** Rejected (§3): adds
  dependencies and forces ycc to own token storage/rotation/host config — the
  complexity `gh`/`glab` already solve. Reconsider for a headless/hosted daemon.
- **Pure git protocol (push only, no PR API).** ycc could `git push` and print
  the "create a PR" URL the forge returns, never calling a forge API. Rejected as
  the *primary* path — it can't set title/body, detect an existing PR, or sync
  issues — but it is a reasonable **degraded fallback** when `gh` is absent but a
  remote exists (push the branch, print the compare URL; §9 could offer this
  instead of a hard error).
- **Forge webhooks / continuous issue sync.** A daemon endpoint the forge calls
  to keep the backlog in lock-step with issues. Rejected for v1: needs a
  reachable daemon, secret management, and reconciliation logic; import-on-demand
  + optional close-on-done covers the intake use case with far less machinery.
- **Storing a PAT in ycc config.** Rejected: same credential-ownership cost as
  the API clients without their upside; the CLIs already hold auth securely.
- **Making publish replace local merge.** Rejected: local merge (§14.1) is the
  right default for solo/local flows; publish is an *additional* terminal state,
  not a replacement.

## 11. Prompt-level vs. first-class, and phased rollout

**What is a tool/RPC vs. prompt/runbook in v1:**

| Capability                         | v1                                   | Later (v2)                          |
|------------------------------------|--------------------------------------|-------------------------------------|
| Issue → backlog import             | **first-class** `ycc task import`    | label→priority mapping; bulk import |
| Issue close/comment on done        | config-gated hook (off by default)   | richer bidirectional sync           |
| Workstream → PR (publish)          | **first-class** `PublishWorkstream` RPC + terminal state | "publish + keep worktree" polish |
| Non-workstream session → PR        | **prompt-level** `plans/publish-pr.md` runbook | graduate to `publish_pr` tool / `PublishSession` RPC if common |
| Forge auth                         | delegated to `gh`/`glab`; doctor probe | API-client option for headless      |
| Forge detection                    | doctor check + per-flow probe        | settings-overlay status             |

### Proposed follow-on implementation tasks

Filed from this doc (as `proposed` — the spike is accepted scope, the
implementation awaits user acceptance, matching the precedent of tasks
0150/0154). Rough scope + dependencies noted. Filed as backlog tasks
0155 (1), 0156 (2), 0159 (3), 0160 (4), 0157 (5), 0161 (6), 0162 (7),
0158 (8), 0163 (9).

1. **Forge probe + doctor check.** A small `internal/forge` (or similar) helper
   that detects `gh`/`glab` availability + auth (`--version`, `auth status`) and
   infers forge/host from a URL or remote. Wire a non-fatal forge check into
   `ycc doctor` (`cmd/ycc/doctor.go`). *Foundation for the others; no deps.*

2. **`ycc task import <issue-url>` (GitHub via `gh`).** New subcommand in
   `cmd/ycc/task.go`; fetch via `gh issue view --json`; map fields (§5); add an
   `origin` field to `docs.Task` + `CreateTask` RPC/`TaskDetail` proto; dedupe on
   `origin`; thread through both `directBackend` and `rpcBackend`. *Depends on 1.*

3. **GitLab import parity (`glab`).** Add the `glab issue view` adapter behind the
   same `task import` command + forge inference. *Depends on 1, 2.*

4. **Optional issue close/comment on done.** Config flag (default off); hook the
   task→`done` transition to `gh issue close --comment` behind §8 gating.
   *Depends on 1, 2.*

5. **`PublishWorkstream` RPC + `published` terminal state.** Mirror
   `MergeWorkstream` in `internal/session/workstream_merge.go` +
   `internal/server/workstream.go` + the proto; compose the PR body from
   `internal/export`; push + `gh pr create`; new `workstream_published` event and
   `workstream.Status` value; publish-specific cleanup (keep branch, remove
   worktree); idempotent/resumable publish. *Depends on 1; largest task.*

6. **GitLab MR parity for publish (`glab mr create`).** *Depends on 1, 5.*

7. **TUI: publish action on the Workstreams panel.** A "Publish (open PR)" action
   beside merge/discard, showing the PR-body preview + accept gate and the
   resulting PR URL; render the `workstream_published` event. *Depends on 5.*

8. **`plans/publish-pr.md` runbook (Flow 3) + prompt guidance.** A committed
   runbook driving `git push` + `gh pr create` behind a confirmation gate, plus a
   line in the coordinator prompt pointing at it. *Depends on 1; no code.*

9. **Spec update.** Add a short §14.2 (or extend §6.2/§14.1) recording forge
   integration: `ycc task import`, the workstream `published` terminal state +
   `workstream_published` event, the `gh`/`glab` delegation + auth model, and the
   confirmation-gate semantics. *Depends on whichever of 2/5 land; keeps the spec
   true (spec §1).*

**Rollout order:** 1 → (2, 8 in parallel) → 5 → 3/4/6/7 → 9. Task 1 unblocks
everything; import (2) and the runbook (8) are the cheapest user-visible wins;
publish (5) is the flagship and gates the TUI (7) and GitLab-MR (6) work.
