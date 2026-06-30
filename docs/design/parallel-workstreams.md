# Design: Parallel agent workstreams via git worktrees

> Status: **proposal** (design spike, task 0078). No code lands with this doc.
> Grounded in the current architecture: spec §3 (daemon is the single writer;
> clients are thin), §3.1 (one-shot vs persistent multi-project daemon; project
> registry name→path), §4 (process & data-flow), §5 (append-only JSONL event log
> per session under `<workspace>/.ycc/sessions/<id>/events.jsonl`), §14
> (single-writer invariant), and §17 (the deferred "Implementer isolation"
> decision this spike revisits).

## 1. Context / problem

Today a `work` session runs one coordinator that delegates to an implementer
which **edits the codebase directly**, one task at a time. From spec §17:

> **Implementer isolation:** *Decided.* Implementers work **directly on the
> codebase** (single task at a time). Git worktrees revisited only if/when we
> want parallel tasks.

This spike is that revisit. We want to **run multiple agent workstreams in
parallel** — e.g. three independent backlog tasks progressing at once — and
**integrate their results back to the main branch** without them clobbering each
other's working tree.

The hard constraint is architectural, not cosmetic. The whole persistence and
sync model (spec §14) rests on a **single-writer invariant**: for a given
working tree, exactly one daemon/coordinator mutates the filesystem, and the
event log is the serialized record of those mutations. The `git` helper
(`internal/git/git.go`) and the worker tools (`tools.Workspace{Root}`, which
confines file ops to one root) both assume a single tree rooted at one absolute
path. Sessions are scoped to one absolute `Workspace` path
(`session.Config.Workspace`, resolved in `Manager.Start`/`newSession`), and the
project registry maps one name → one path. None of this is wrong; it just means
"parallel" cannot mean "many writers, one tree." Each parallel workstream needs
**its own working tree** so the single-writer invariant holds *per tree*.

The design question is therefore: **how do we give each workstream its own
isolated tree cheaply, and how do we merge the results back with the
human-in-the-loop review ycc already values (spec §1: "No code is committed
without a plan and at least one review pass")?**

## 2. Goals & non-goals

**Goals**

- Run N agent workstreams concurrently, each isolated so they cannot corrupt
  each other's working tree or each other's `.ycc/` session logs.
- Preserve the single-writer-per-tree invariant (spec §14) unchanged.
- Reuse the existing session/event-log machinery rather than inventing a parallel
  state model.
- Integrate completed workstreams back to a base branch (usually `main`) with
  explicit, reviewable, conflict-aware merges — never silent auto-resolution.
- Give the user a clear way to spawn, monitor, and reconcile workstreams from the
  TUI / RPC.

**Non-goals**

- Distributed execution across machines (still one daemon, one repo on one
  workspace machine; remote *observation* remains the §7/§14 sync story).
- Automatic task **decomposition** (deciding which tasks can run in parallel is a
  separate planning concern; here we assume the user or pm mode picks them).
- Removing the human review gate. Parallelism speeds up *doing*; it does not
  bypass *accepting*.
- Cross-workstream live coordination (workstreams are independent until merge;
  shared-context collaboration is explicitly out of scope).

## 3. Candidate isolation approaches

For each: isolation strength, disk/setup cost, interaction with the
daemon-as-single-writer model, where `.ycc/` session logs live, and merge
friction.

### (a) Git worktrees — one linked working tree per workstream

`git worktree add <path> -b <branch>` creates an additional working directory
linked to the **same** `.git` object store, checked out on its own branch. The
daemon would keep a base/primary tree (the registered project path) and spawn one
linked worktree per workstream under a state dir.

- **Isolation strength:** Strong at the filesystem level — each worktree is a
  separate directory with its own index, `HEAD`, and checked-out files. Two
  workstreams editing the same file never collide on disk; they only "collide" as
  divergent commits that surface at merge time (exactly where we want conflicts
  to appear). Git enforces that the same branch cannot be checked out in two
  worktrees, which is a useful guardrail.
- **Disk / setup cost:** Low. Worktrees share the object database, so only the
  checked-out files (working tree + index) are duplicated, not history. Creation
  is a single fast `git` command; no re-fetch, no re-clone. For a Go repo this
  also means each worktree needs its own build cache, but source duplication is
  cheap.
- **Single-writer interaction:** Clean fit. Each worktree is a distinct absolute
  path, so a session scoped to that path has exactly one writing
  daemon/coordinator — the invariant holds per tree with **no change** to the
  invariant itself. `tools.Workspace{Root}` already confines edits to a root; we
  just point it at the worktree path.
- **`.ycc/` session logs:** Live under each worktree's own
  `<worktree>/.ycc/sessions/<id>/events.jsonl`, exactly as today (the path is
  derived from the absolute workspace). `.gitignore` already excludes `.ycc/`
  (see `git.Open`), so per-worktree session logs never pollute the merged branch.
- **Merge friction:** Low–moderate. Because all worktrees share one object store,
  merging workstream branch → base is a local `git merge`/`rebase` with no remote
  round-trip. Conflicts are normal git conflicts, detectable up front with a
  trial merge.

### (b) Separate branches in a single shared working tree (serialized switching)

One tree; each workstream is a branch, and the daemon `git switch`es between them
as it time-slices work.

- **Isolation strength:** Weak/none for *concurrency*. There is exactly one
  checked-out tree and one index, so two workstreams cannot have files present
  simultaneously. To work workstream B you must stash/switch away from A,
  destroying the "parallel" property — it's really *interleaved*, not parallel.
- **Disk / setup cost:** Minimal (no extra trees) — its only advantage.
- **Single-writer interaction:** Actively **fights** the model. A single tree has
  a single writer by definition, so "parallel" sessions would have to contend for
  one tree, requiring a global mutex + stash/switch dance around every turn. The
  worker tools and `git` helper would need to become branch-aware and serialize,
  which is precisely the complexity the single-writer-per-tree rule exists to
  avoid. In-progress (uncommitted) edits from two agents cannot coexist.
- **`.ycc/` logs:** Would have to be deliberately kept out of the shared tree (one
  `.ycc/` for all), so per-workstream logs need an out-of-tree location anyway —
  losing the "logs live next to their tree" simplicity.
- **Merge friction:** Same as (a) at merge time, but you paid a large concurrency
  tax to get there.

### (c) Separate full clones per workstream

`git clone` (or `clone --shared`/`--reference`) the repo once per workstream into
its own directory.

- **Isolation strength:** Strongest — fully independent repos, independent object
  stores (unless `--shared`), independent everything. No shared-`.git` foot-guns.
- **Disk / setup cost:** Highest. A plain clone duplicates the entire history and
  object store per workstream; `--shared`/`--reference` reduces that but
  reintroduces a shared object store (so most of worktrees' "danger," none of
  their convenience) and adds repo-corruption-on-gc caveats. Setup is slower
  (clone vs. `worktree add`).
- **Single-writer interaction:** Fine — each clone is its own tree with its own
  writer, like (a). But each clone is effectively a *separate project* from the
  daemon's point of view (separate path, separate registry entry), which
  multiplies project-registry and bookkeeping overhead.
- **`.ycc/` logs:** Per clone, like (a).
- **Merge friction:** Highest. Merging means adding the clone as a remote and
  `fetch`+`merge`, or pushing between local repos — extra remotes, extra
  plumbing, and the object stores must exchange objects. Cleanup means deleting a
  whole repo.

## 4. Recommendation

**Use git worktrees (approach a).**

Rationale, tied to the tradeoffs above:

- It gives **real filesystem isolation** (so concurrency is genuine, unlike (b))
  at **near-zero disk and setup cost** (unlike (c)) because history is shared via
  one object store.
- It is the **smallest change to the architecture**: each worktree is just
  another absolute path, so the single-writer-per-tree invariant, the session
  scoping, the `tools.Workspace` confinement, and the `.ycc/` log location all
  work **unchanged** — we extend `internal/git` rather than rewrite the model.
- **Merges are local and cheap** (shared object store, no remotes), which matters
  because reconciling N workstreams back to `main` is the common operation.
- Git's built-in guardrails (one branch per worktree; `worktree list`/`prune`)
  give us lifecycle primitives for free.

Approach (b) is rejected because it contradicts the single-writer-per-tree model
and yields interleaving, not parallelism. Approach (c) is rejected as worktrees'
strictly-heavier cousin: more disk, slower setup, and far more merge/cleanup
plumbing for isolation we don't need beyond what worktrees give. The clone story
only wins when workstreams must live on *different machines*, which is a non-goal
here (and would be served by the existing remote-sync direction, not by local
clones).

## 5. Worktree lifecycle (create → work → merge → cleanup)

A **workstream** is the unit. Its lifecycle:

1. **Create.** The daemon records the current base commit of the project's
   primary tree (e.g. `main` HEAD) and runs, off that base:

   ```sh
   git worktree add <worktree-dir> -b ycc/ws/<workstream-id> <base-commit>
   ```

   - **Branch naming:** `ycc/ws/<workstream-id>` (namespaced under `ycc/ws/` so
     they're easy to list, glob, and clean up, and never collide with human
     branches). `<workstream-id>` is short and stable (mirrors the `s_…`/`p_…`
     id style), optionally suffixed with the focused task id for readability,
     e.g. `ycc/ws/ws_3f9a-0042`.
   - **Where the worktree dir lives:** **out of the primary tree**, under the
     daemon state dir keyed by project — e.g.
     `~/.local/state/ycc/worktrees/<project>/<workstream-id>/`. Keeping it outside
     the project path avoids nesting a worktree inside the very tree being merged
     and keeps `git status` of the primary tree clean. (A sibling-directory layout
     `<project>.ycc-worktrees/<id>/` is an acceptable alternative if users prefer
     worktrees they can `cd` into near the repo; the state-dir default is tidier.)
   - **`.ycc/` per worktree:** sessions for this workstream write to
     `<worktree-dir>/.ycc/sessions/<id>/events.jsonl`, exactly as a normal
     session does, because the session is scoped to the worktree's absolute path.
     `.ycc/` is git-ignored, so it never travels into the merge.

2. **Work.** A normal `work`-mode session (coordinator → implementer → reviewers
   → commit) runs **inside the worktree**, with `deps.Workspace`,
   `tools.Workspace{Root}`, `docs.Store`, and `git.Repo{Dir}` all pointed at the
   worktree path. Commits land on `ycc/ws/<id>`. Everything that exists today
   works without modification because it's "just a session in a directory." The
   single-writer invariant holds: one daemon, one coordinator, one tree.

3. **Merge.** When the workstream reaches a clean/idle accepted state, the user
   (or an autonomous policy) requests integration back to base. See §6 for the
   strategy and conflict handling. On success, base advances to include the
   workstream's commits.

4. **Cleanup.** After a successful merge (or an explicit discard):

   ```sh
   git worktree remove <worktree-dir>        # drops the linked tree
   git branch -d ycc/ws/<id>                  # delete merged branch (-D to force-discard)
   git worktree prune                          # reap any stale admin entries
   ```

   The workstream's `.ycc/` logs can be retained for history (session GC, spec
   §17 open question) or removed with the directory. Stale worktrees from a
   crashed daemon are recoverable via `git worktree list`/`prune` on startup.

## 6. Integration / merge strategy & conflict handling

Options considered:

- **Auto-merge on clean.** If `ycc/ws/<id>` merges into base with no conflicts,
  fast-forward/merge it automatically.
- **Sequential rebase onto base.** Reconcile workstreams one at a time: rebase
  each onto the (possibly already-advanced) base, so the second workstream sees
  the first's changes and conflicts surface early and locally.
- **Manual review gate.** Surface the merge as a diff/decision the user accepts,
  mirroring the existing review-then-commit ethos.

**Recommended default:** a **conflict-aware, sequential, review-gated merge**:

1. The daemon performs a **trial merge** (`git merge --no-commit --no-ff` into a
   throwaway/base-tip, or `git merge-tree`) to *detect* conflicts without
   mutating base.
2. **Clean trial → present for acceptance.** Under autonomous interaction level,
   auto-merge clean workstreams; under interactive/judgement levels, surface the
   integrated diff as a decision (consistent with spec §11 levels and the "review
   before accept" rule). Merges are **sequential** across workstreams so each is
   reconciled against the latest base.
3. **Conflicting trial → surface, never silently resolve.** Emit a
   `workstream_conflict` event listing conflicted paths/hunks and *stop*. Options
   offered: (a) hand the conflict to a fresh resolve-mode session running *in the
   workstream worktree* (rebase onto new base, let an agent resolve, re-review),
   or (b) bounce to the user for manual resolution. The base branch is **never**
   left in a conflicted state by the daemon.

Conflict detection is explicit and up front (trial merge), so the user learns
about a collision *before* any irreversible action. This matches the single-writer
philosophy: the daemon serializes the integration, and a human (or an explicitly
chosen resolve agent) owns any genuine merge decision.

## 7. Session / state model interaction

- **Workstream = a session (or a session group).** The minimal model is
  **one workstream ⇄ one session**, scoped to the worktree path. Its event log
  lives at `<worktree>/.ycc/sessions/<id>/events.jsonl` and reduces to a
  projection exactly as today — no event-model changes required for the work
  itself. (If a workstream later spans multiple sessions, a lightweight
  `workstream-id` grouping key on sessions covers it; not needed for v1.)
- **Single-writer invariant preserved.** Each worktree has exactly one writing
  daemon/coordinator. Parallelism happens *across* trees, each internally
  single-writer — so spec §14 holds verbatim. The daemon gains a small amount of
  shared state: a **workstream registry** (id → {project, base commit, branch,
  worktree path, session id, status}), which is the *only* genuinely new
  persistent structure. It is daemon-owned and serialized like the project
  registry, so it doesn't violate single-writer (it's metadata, not workspace
  mutation).
- **Project-registry implications.** A worktree is **not** a separate top-level
  project; it is a **child of one project**. The project registry (name → path)
  stays as-is and keeps pointing at the primary tree. The new workstream registry
  references its parent project by name and stores the worktree path. This avoids
  polluting the user-facing project picker with ephemeral worktrees while keeping
  `ListByProject`-style queries able to find a project's live workstreams.
- **Sandbox concept.** This dovetails with reviewer sandboxing (task 0008): a
  worktree is itself a coarse sandbox boundary (file ops confined to the worktree
  root by `tools.Workspace`), so workstream isolation and reviewer-bash isolation
  are complementary, not conflicting.

## 8. UX sketch (TUI + RPC)

Conceptually we introduce a first-class **Workstream** alongside Session.

- **Spawn.** From the backlog browser or home menu, the user multi-selects ready
  tasks and chooses "Run in parallel (N workstreams)." Each selection becomes a
  workstream: the daemon creates the worktree + branch and starts a `work`
  session inside it. RPC sketch: `SpawnWorkstream(project, base_ref, task_id?,
  prompt?, interaction_level)` → `{workstream_id, branch, worktree_path,
  session_id}`.
- **Monitor.** A **Workstreams** panel lists active workstreams with status
  (running / idle / awaiting-review / conflict), focused task, branch, and commit
  count. Each row drills into its session transcript (the existing session view,
  unchanged — it's just a session). RPC sketch: `ListWorkstreams(project)` →
  rows; `Subscribe(session_id)` reused verbatim for live event streaming.
- **Reconcile / merge.** A "Merge" action on a workstream (or "Merge all clean")
  triggers the §6 flow. The TUI shows, per workstream: trial-merge result (clean
  / conflicted paths), the integrated diff for the accept gate, and merge
  outcome. RPC sketch: `PreviewMerge(workstream_id)` → `{clean, conflicts[],
  diff}`; `MergeWorkstream(workstream_id, strategy)` →
  `{merged_commit | conflict}`; `DiscardWorkstream(workstream_id)` for cleanup
  without merging.
- **Surfacing progress & conflicts.** New event types on the workstream's
  session stream — `workstream_created`, `workstream_merged`,
  `workstream_conflict` (with paths/hunks), `workstream_discarded` — so any
  client renders the lifecycle the same way it renders today's `commit_made` /
  `decision_made`. Conflicts are loud (a distinct row state + event), never a
  silent failure.

This is a **sketch**, not a final proto; the exact message shapes land in the
follow-up tasks.

## 9. Rejected alternatives

- **Branch-switching in one shared tree (§3b):** rejected — interleaving, not
  parallelism; fights the single-writer-per-tree model with a stash/switch mutex
  dance.
- **Full clones per workstream (§3c):** rejected — strictly heavier than
  worktrees (more disk, slower setup, remote/fetch plumbing for merges, whole-repo
  cleanup) for isolation we don't need locally; only justified for
  cross-machine work, which is a non-goal.
- **Many writers on one tree (file-level locking / path partitioning):**
  rejected outright — directly violates spec §14's single-writer invariant and
  would make the event log no longer a faithful serialization of mutations.
- **Silent auto-resolution of merge conflicts:** rejected — conflicts must be
  surfaced and owned by a human or an explicitly-chosen resolve agent (spec §1's
  "reviewed before accepted").
- **Promoting each worktree to a top-level project:** rejected — pollutes the
  project picker with ephemeral entries; a workstream is a child of a project,
  tracked in a separate daemon-side registry.

## 10. Follow-up implementation tasks

Proposed, well-scoped tasks to realize the recommendation (to be filed in the
backlog by the coordinator):

1. **Worktree primitives in `internal/git`.**
   - Add `AddWorktree(dir, branch, baseRef)`, `RemoveWorktree(dir)`,
     `ListWorktrees()`, and `PruneWorktrees()` wrapping `git worktree …`.
   - Add merge helpers: a non-mutating `TrialMerge(branch)` (conflict detection
     via `git merge-tree`/`--no-commit`) and `Merge(branch, strategy)`.
   - Unit tests over a temp repo covering create → commit-on-branch → trial-merge
     (clean & conflicting) → merge → remove → prune.

2. **Workstream registry + lifecycle in the daemon/session manager.**
   - A daemon-owned, serialized `workstream` registry (id → {project, base,
     branch, worktree path, session id, status}), persisted in the state dir
     beside `projects.json`.
   - `Manager.SpawnWorkstream` creates the worktree and starts a `work` session
     scoped to it; recovery on startup reconciles stale worktrees via
     `git worktree list/prune`.
   - Preserve the single-writer invariant: one session per worktree; reject a
     second writer for the same path.

3. **Merge/integration flow with conflict surfacing.**
   - Implement the §6 sequential, review-gated merge: trial-merge, accept gate by
     interaction level, sequential reconciliation, and a `workstream_conflict`
     path that stops without mutating base.
   - New event types (`workstream_created/merged/conflict/discarded`) + reducer
     handling.
   - Cleanup on success (`worktree remove`, branch delete, prune).

4. **RPC surface for workstreams.**
   - `SpawnWorkstream`, `ListWorkstreams`, `PreviewMerge`, `MergeWorkstream`,
     `DiscardWorkstream` in `proto/ycc/v1`, with server handlers delegating to the
     manager. Reuse `Subscribe` for per-workstream session streams.
   - Acceptance: a scripted client can spawn 2 workstreams, observe both run, and
     merge both back (one clean, one conflicting) with the conflict surfaced.

5. **TUI: Workstreams panel + spawn/monitor/merge UX.**
   - Multi-select spawn from the backlog browser; a Workstreams list with live
     status; drill-into-session reuse of the existing session view; a merge/accept
     overlay showing trial-merge result + integrated diff.
   - Acceptance: a user can spawn N workstreams, watch progress, and merge/discard
     each from the TUI; conflicts are visibly distinct.

6. **Spec update.**
   - Revise spec §17 "Implementer isolation" to record this decision (worktrees
     for parallel workstreams) and add a short § on the workstream concept,
     registry, and merge flow so the spec stays true (spec §1).
