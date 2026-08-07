# Design: automatic workstream integration (agent-driven merge queue)

> Status: **approved direction** (pre-implementation). Sequel to
> `docs/design/parallel-workstreams.md` (worktree isolation, shipped) and spec
> §14.1. This doc covers what happens *after* a workstream has done its work:
> how its branch gets back onto the base branch automatically, safely, and
> without a human diffing every merge.

## 1. Context / problem

Worktree isolation is implemented: `Manager.SpawnWorkstream` creates
`<state>/ycc/worktrees/<project>/<id>` on branch `ycc/ws/<id>[-<task>]` and runs
a `work` session there; `MergeWorkstream` trial-merges, gates on an explicit
accept, merges `--no-ff`, and cleans up
(`internal/session/workstream_merge.go`). That is enough to *run* parallel work
and enough to merge one stream by hand. It is not enough to make parallel work
a default habit, for four reasons:

1. **Merge has no base branch.** `git.Repo.Merge` runs `git merge` in the
   project's primary tree, i.e. into whatever is checked out there
   (`internal/git/merge.go`). `workstream.Workstream` records only
   `BaseCommit` — there is no base *branch* in the model. If the primary tree
   sits on a feature branch, or is dirty, integration lands in the wrong place
   or fails midway.
2. **A clean merge is not a working merge.** Nothing rebases the branch onto the
   advanced base and nothing builds or tests before/after integration. With N
   streams in flight, *semantic* conflicts (both sides merge cleanly, the result
   is broken) are the normal case, not the exception.
3. **Nothing signals "done".** The workstream's session simply ends. There is no
   readiness event, so no automation can be attached to it.
4. **Every merge costs a human review round-trip**, which defeats the point of
   running three streams at once.

## 2. Decisions

- **Auto-integration is the default**, per project, and is **performed by an
  agent** when mechanical integration is not trivially clean. The daemon owns
  the irreversible steps; the agent owns the judgement.
- **Rebase-then-fast-forward, in the workstream's own worktree.** Conflicts and
  test failures surface *inside* the stream that caused them, where an agent has
  full context — never in the primary tree.
- **`verify` gates auto.** A project with no configured verify command degrades
  to the review gate: ycc will not auto-merge unverified code.
- **No resource-lease/mutual-exclusion subsystem for now** (explicitly punted,
  see §8). The cheap stand-in is a per-project cap on concurrent workstreams.
- **Worktrees stay in the daemon state dir**; a `ycc ws path <id>` helper covers
  the "I want to open this in an editor" case.

## 3. Lifecycle, extended

```
spawn ──► working ──► ready ──► integrating ──► merged
                        │            │
                        │            ├─► needs_attention (conflict / verify red / agent gave up)
                        │            └─► gated (mode=gate: awaiting human accept)
                        └─► (session errored/blocked → needs_attention, never auto)
```

**Ready** = the workstream's session reached a terminal state *and* the branch
has commits since base *and* the session did not end in error/blocked. When the
workstream targets a backlog task, the task must be `in_review` or `done`.
Emitting `workstream_ready` on the workstream's session stream is the hook every
later stage hangs off.

## 4. The integration queue

One integrator per project, **serialized** (it subsumes today's `mergeMu`).
Serialization is what makes "rebase onto the *current* base" meaningful: stream
B integrates against a base that already contains stream A, so cross-stream
breakage is discovered one stream at a time.

Per attempt:

1. **Fast path (no tokens).** In the workstream's worktree:
   `git rebase <base>` → run `verify` (config). If both succeed, go to step 3.
   Most integrations end here; the common case must not cost a model call.
2. **Agent path.** If the rebase conflicts or `verify` fails, spawn an
   **`integrate`-mode session scoped to the worktree**: worker tools + git, seed
   prompt naming the base, the conflicted paths / failing output, and the rule
   "resolve or fix, re-run verify, then request integration; if the right call
   isn't yours, `report_blocked`". Bounded attempts (default 1). Success → step
   3. Failure/blocked → `needs_attention` + notification; worktree and branch are
   left intact.
3. **Advance base (daemon-owned, mechanical).** After a successful rebase the
   branch is a descendant of base, so this is *always* a fast-forward:
   - base branch is checked out in some worktree (the usual case: the primary
     tree) → `git merge --ff-only <ws-branch>` there, requiring that tree clean;
     if dirty, defer the integration and notify rather than touching the user's
     uncommitted work;
   - base branch is not checked out anywhere → `git fetch . <ws-branch>:<base>`,
     which updates the ref with no working tree involved.
   Never `git update-ref` a branch that is checked out elsewhere: it silently
   desynchronizes that working tree.
4. **Finish.** Preserve the session log into the primary workspace, remove the
   worktree, delete the branch, prune, set status `merged`, emit
   `workstream_merged`, notify.

**Invariant:** the base branch is never left conflicted and never advanced past
a failing `verify`. That invariant is what makes `mode = "auto"` defensible.

## 5. Configuration

```toml
[integration]
base         = "master"     # default: the repo's default branch
mode         = "auto"       # auto | gate | manual
verify       = "go build ./... && go test ./..."   # required for auto
strategy     = "rebase-ff"  # rebase-ff | squash | merge-no-ff
max_parallel = 3            # cap on concurrently active workstreams (0 = unlimited)
agent_attempts = 1          # integrate-mode retries before needs_attention
```

- `mode = "gate"` keeps today's accept-diff behaviour but adds **"merge all
  ready"** as one keystroke in the TUI and one button on the phone.
- `mode = "manual"` is today's behaviour exactly.
- `strategy = "rebase-ff"` is the default: linear history, and "is the branch a
  descendant of base?" becomes a cheap, reliable safety check.

## 6. Worktree ergonomics

A fresh worktree contains only tracked files. Without a bootstrap step, real
projects fail at the first build (missing `.env`, no dependencies installed, no
local config), which is the fastest way to abandon worktrees.

```toml
[worktree]
copy  = [".env", ".env.local"]   # untracked files seeded from the primary tree
link  = ["node_modules", ".venv"]# symlinked (heavy, and safe to share read-mostly)
setup = ["go mod download"]      # run once after creation, before the session starts
env   = { }                      # extra env for sessions in this worktree
setup_timeout_seconds = 300      # per command; 300s is also the default when omitted
```

This bootstrap runs after `git worktree add` and before the session starts, in
`copy` → `link` → `setup` order. A `[worktree]` table in the project's primary-tree
`ycc.toml` wins as a whole; otherwise the daemon config's table is used. Paths are
workspace-relative and may not escape the tree. Missing sources and destinations
that already exist are skipped, so optional local files are safe and tracked files
are never clobbered. Setup commands run sequentially with the configured `env`; a
failure aborts the spawn, removes its worktree and branch, and includes the command
output in the reported error. The same environment is supplied to agent shells in
freshly spawned and reopened workstream sessions.

Two more pieces of grit that parallelism exposes:

- **Backlog id allocation.** `docs.Store.nextID` is `max(existing)+1` scanned in
  the *current* tree, so two parallel streams mint the same id. This has already
  produced duplicate ids in this repo. Id minting must be serialized per project
  by the daemon (a state-dir counter seeded from the primary tree's max), not by
  the worktree's filesystem.
- **Shared mutable docs.** Code conflicts between well-chosen parallel tasks are
  rare; `spec.md` / `memory.md` conflicts are near-certain, because every stream
  is prompted to keep the spec true. Policy: a workstream edits code and *its
  own* task file; broad spec/memory grooming happens on the base branch after
  integration.

## 7. Surfaces

- **Events:** `workstream_ready`, `workstream_integrating`,
  `workstream_needs_attention` join the existing `workstream_created` /
  `_merged` / `_conflict` / `_discarded`.
- **RPC:** `WorkstreamInfo` gains `base_branch` and an integration state;
  `RetryIntegration(workstream_id)` re-queues a `needs_attention` stream.
  `MergeWorkstream` stays as the manual/gated path.
- **TUI / iOS:** integration state per row, "merge all ready", retry, and the
  existing drill-into-session for the `integrate` session's transcript.
- **CLI:** `ycc ws list`, `ycc ws path <id>` (worktrees stay in the state dir;
  the helper covers `cd $(ycc ws path ws_3f9a)`).
- **Notifications:** ntfy on `needs_attention` and (optionally) on merged, so an
  unattended run surfaces only the cases that need a human.

## 8. Deliberately deferred: exclusive resources

Some projects have resources exactly one session may touch at a time —
GPU/bench hardware, a production deployment target, a fixed dev port. The
general answer is a daemon-owned **named lease** (`capacity`, project- or
global-scoped) acquired transparently by the bash tool when a command matches a
configured pattern, backed by a `flock`ed lockfile so a human shell can contend
on the same lock, with `lease_waiting`/`lease_acquired` events so blocked
sessions are visible rather than mysteriously stuck.

**Not being built yet.** Until it exists, the mitigations are: set
`max_parallel = 1` for such a project (parallel streams still cost nothing to
run, they just never overlap), and prefer making deploys an *integration-time*
action on the base branch — the integration queue is already serialized — rather
than something an unmerged workstream performs.

## 9. Rejected alternatives

- **Merging into the primary tree's current HEAD** (today's behaviour): depends
  on what the user happens to have checked out, and can fail or land wrong.
- **Resolving conflicts on the base branch:** an agent fixing a conflict on
  `master` risks leaving the branch everyone builds from in a broken state; the
  worktree is the correct blast radius.
- **Auto-merge without a verify command:** clean-merging garbage is still
  garbage; auto degrades to the gate instead.
- **Auto-merge driven purely by git plumbing (no agent):** handles the clean case
  (which is why it stays as the fast path) but converts every conflict or red
  test into a human interrupt, which is the cost auto-integration exists to
  remove.
