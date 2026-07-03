---
id: "0008"
title: Sandbox reviewer bash to prevent workspace mutation
status: done
priority: 6
created: "2026-06-25"
updated: "2026-07-03"
depends_on:
    - "0005"
spec_refs:
    - Tools
    - Open questions
---

## Description

Reviewers are given read/inspect tools plus bash so they can run `git diff`, read files,
etc. They are prompted not to modify the workspace, but with bash available that is not
enforced. Add a sandbox so reviewer tool calls cannot mutate the workspace (read-only
mount / overlay, restricted bash, or a syscall/FS guard). Until then, reviewer
non-mutation is prompt-enforced only.

## Acceptance criteria

- [x] reviewer bash cannot write to or delete from the workspace
- [x] read-only inspection (git diff, cat, grep, ls) still works
- [x] mechanism documented; degrades gracefully if unavailable
- [x] symlink-aware path confinement in tools.Workspace.resolve (review 2026-06-26 #5:
      the current check is textual; a symlink inside the workspace pointing out isn't caught)

## Plan

Goal: hard-enforce reviewer non-mutation of the workspace (bash + file tools), keep read-only inspection working, degrade gracefully with a logged warning, and make Workspace.resolve symlink-aware.

Mechanism (Linux; graceful no-op elsewhere), in a new `internal/sandbox` package:

1. Detection — `sandbox.Available() Mechanism` (cached via sync.Once), returning one of "landlock", "bwrap", "none":
   - Landlock preferred (dependency-free, and its deny-by-default write policy is symlink-proof): probe with `landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION)` (via unix.Syscall + unix.SYS_LANDLOCK_CREATE_RULESET); ABI >= 1 → available.
   - Else bubblewrap: exec.LookPath("bwrap") + a one-shot probe run (`bwrap --die-with-parent --dev-bind / / /bin/true`) since user namespaces may be disabled.
   - Else (or non-Linux): "none".

2. `sandbox.Command(ctx, workspaceRoot, script) (*exec.Cmd, Mechanism)` builds the command to run:
   - landlock: re-exec self — `exec.CommandContext(ctx, os.Executable(), HelperArg, root, "sh", "-c", script)`. A hidden helper entry (`sandbox.MaybeHelper()` called at the very top of cmd/ycc main, dispatching on os.Args[1] == HelperArg) applies the Landlock policy then `unix.Exec`s the command ON THE SAME LOCKED OS THREAD (runtime.LockOSThread; landlock_restrict_self is per-thread and execve proceeds from that thread). Fail CLOSED: if applying the policy fails, exit non-zero with a message — never run the command unsandboxed.
   - Landlock policy: handled accesses = full v1 FS set, plus REFER if ABI>=2 and TRUNCATE if ABI>=3; rules: read+execute (READ_FILE|READ_DIR|EXECUTE) beneath "/", full RW beneath a modest write allowlist — os.TempDir(), /var/tmp, /dev, /run, os.UserCacheDir() (go-build cache), and the Go module cache dir (env-derived, mirroring tools.goModCache) — SKIPPING any entry that (symlink-resolved) equals, contains, or is inside the workspace root. Missing dirs skipped. Open rule dirs with O_PATH|O_DIRECTORY|O_CLOEXEC. prctl(PR_SET_NO_NEW_PRIVS,1) before landlock_restrict_self.
   - GOTCHA: pass an 8-byte ruleset attr (access_fs only) with size=8 — x/sys's 24-byte LandlockRulesetAttr gets E2BIG on older kernels (this host is ABI v1).
   - bwrap: `bwrap --die-with-parent --dev-bind / / --ro-bind <root> <root> --chdir <root> sh -c <script>` (whole FS as-is, workspace overlaid read-only).
   - none: plain `sh -c` (today's behavior).

3. Wire into tools (internal/tools):
   - Refactor bash() so an exec-builder can be swapped in; add a sandboxed variant. `Reviewer(ws)` becomes readFile + sandboxed bash + submitReview (Inspect()/Worker() unchanged — authoring modes and workers keep plain bash; scope stays reviewers-only).
   - The sandboxed bash tool's Description tells the model the workspace is read-only for it (so failed writes aren't confusing); when Available()=="none" keep the current description (prompt-only enforcement).
   - Orchestrator (spawn_reviewers, internal/orchestrator/orchestrator.go ~565): when Available()=="none", emit an event.Narration warning ("reviewer sandbox unavailable; falling back to prompt-only enforcement") once per spawn.

4. Symlink-aware resolve (4th acceptance criterion): tools.Workspace.resolve gains the same symlink-resolved containment check resolveRead uses (withinRoot/evalExisting) after the textual check, so Write/Edit through an in-workspace symlink pointing outside is rejected. Update its doc comment.

5. Tests:
   - internal/sandbox: TestMain dispatches HelperMain when argv[1]==HelperArg (so os.Executable()==test binary works). Linux-only tests, skipped when Available()=="none": sandboxed `touch <ws>/x` fails and file absent; delete fails; `cat`/`ls`/`grep` and `git diff` in a real git workspace succeed; write to /tmp succeeds; (landlock) write through a /tmp symlink into the workspace is denied.
   - internal/tools: same TestMain dispatch; reviewer bash cannot create a file in ws (skip if unavailable) while worker bash still can; resolve() symlink-escape tests (no sandbox needed, run everywhere).
6. go.mod: promote golang.org/x/sys to a direct dependency.
7. Docs: spec.md §8 (Tools) — short paragraph documenting the reviewer bash sandbox (mechanisms, write allowlist idea, fail-closed helper, graceful degradation + warning); §17 Open questions — update the "Diff capture for reviewers" bullet: sandboxing now implemented on Linux (landlock/bwrap), prompt-only elsewhere.
8. Verify: go vet/build/test ./...; plus a live check on this host (Landlock ABI v1, no bwrap): run a sandboxed command against a scratch git repo confirming ro-workspace + working git diff.

### Starting points
- internal/tools/worker.go: bash() builds exec.CommandContext("sh","-c",cmd), Setpgid+Cancel+WaitDelay pattern — keep that for the sandboxed variant too
- internal/tools/reviewer.go: Inspect()/Reviewer(); internal/tools/tools.go: resolve() (textual), resolveRead()/withinRoot()/evalExisting() (symlink-aware helpers to reuse), goModCache()
- internal/orchestrator/orchestrator.go:561-571 reviewer registry construction; event.Narration is the free-text warning event type
- cmd/ycc/main.go func main() — helper dispatch must run before urfave/cli parsing
- golang.org/x/sys@v0.46.0/unix has SYS_LANDLOCK_CREATE_RULESET/ADD_RULE/RESTRICT_SELF, LANDLOCK_ACCESS_FS_* and LANDLOCK_RULE_PATH_BENEATH + LandlockPathBeneathAttr, but NO wrapper funcs — raw unix.Syscall; pass an 8-byte access_fs-only ruleset attr (24-byte struct → E2BIG on ABI v1 kernels)
- host facts: kernel 5.15 (Landlock ABI v1: no TRUNCATE/REFER), landlock in /sys/kernel/security/lsm, bwrap NOT installed — live verification must go through the landlock path

## Work log
- 2026-07-05 unblocked (pm grooming with user): user will attend the implementation
  session. No hard mechanism preference was expressed — implementer should make the
  pragmatic pick for Linux (bubblewrap if available, Landlock as a no-dependency
  fallback, else degrade to the current prompt-only enforcement with a logged warning).
  Linux-only enforcement is acceptable; graceful degradation elsewhere.
- 2026-07-02 blocked: parked for the overnight autonomous run — sandboxing mechanism is platform-dependent (bwrap/landlock/overlay choices) and hard to verify unattended; user wants to be present. Unblock for an attended session.

- 2026-06-26 plan: Goal: prevent reviewer bash/tool calls from mutating the workspace, while keeping read-only inspection (git diff, cat, grep, ls) working. Degrade gracefully when sandboxing is unavailable. Approach:
  …[truncated]
- 2026-07-03 plan: Goal: hard-enforce reviewer non-mutation of the workspace (bash + file tools), keep read-only inspection working, degrade gracefully with a logged warning, and make Workspace.resolve symlink-aware.  M
…[truncated]
- 2026-07-03 context hints: 6 recorded with plan
- 2026-07-03 context hints: internal/tools/worker.go: bash() — exec.CommandContext("sh","-c",cmd) with Setpgid + Cancel(kill -pgid) + WaitDelay; keep that process-group/timeout handling in the sandboxed variant; internal/tools
…[truncated]
- 2026-07-03 implementer report: Implemented reviewer bash sandboxing (task 0008) with symlink-aware path confinement.  ## What changed  **New package `internal/sandbox`** (`sandbox.go`, `sandbox_linux.go`, `sandbox_other.go`, `sandb
…[truncated]
- 2026-07-03 review tier: high-powered — reviewers: Claude
- 2026-07-03 review (Claude): accept — The change fully satisfies task 0008. It adds an internal/sandbox package that hard-enforces reviewer bash non-mutation via Landlock (preferred, symlink-proof) or bubblewrap, degrading gracefully to p
…[truncated]
- 2026-07-03 revision: Addressed the reviewer's minor finding: `sandbox.Command`'s Landlock path no longer silently falls back to an unconfined command when `os.Executable()` fails.  ## Change In `internal/sandbox/sandbox.g
…[truncated]
- 2026-07-03 review (Claude): accept — The revision resolves the prior minor finding: the Landlock path in sandbox.Command now fails closed when os.Executable() fails — instead of silently degrading to an unconfined command, it returns a
…[truncated]
- 2026-07-03 decision: accept — commit: sandbox reviewer bash via Landlock/bwrap; symlink-aware Workspace.resolve (task 0008)  New internal/sandbox package hard-enforces reviewer non-mutation of the workspace: Landlock preferred (deny-by-de
…[truncated]
