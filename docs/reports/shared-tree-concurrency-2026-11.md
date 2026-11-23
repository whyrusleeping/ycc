# Shared-tree concurrency — November 2026

## Scope and evidence

This is a read-only analysis of this repository's session logs (`.ycc/sessions/*/events.jsonl`) for November 20–22,
2026, together with a reading of `internal/tools/shellclaims.go` and spec §7.3. It covers eight sessions:
s_9e8ba8743514c9b3 (0461, work-loop preflight), s_b33f38df5f019e1c (0580 and 0585, the other-work review note),
s_e4ab341a89ccd33e (0582, the project sessions view and user-posted notices), s_70ed48449817e056 (0576, the
review-cycle report), s_749564ca87c7d8d4 (0567, refusing commits of shared files), s_547e11253e912220,
s_18f059896f075e5a, and s_e8819b5b72d9ac7a. Most incidents fall on November 22, 18:38–19:15 UTC,
when 0461, 0580/0585 and 0582 ran in the shared tree together. Incidents were classified by hand from keyword passes,
so the counts are approximate (±1–2). Commit counts come from `git log --since=2026-11-01` up to `b5c61d7`.

## Finding

The fixes since 0440 (write-scoped claims) make the shared tree safer to commit from: 0566 attributes backlog and
memory lines, 0567 refuses shared files, and 0585 adds the other-work review note. The time lost in this window was
spent earlier. A session needs its own change, without other sessions' hunks, to build it, review it, and commit it.
The shared tree cannot provide that change for a file two sessions both edited. Three sessions (0461, 0580, 0582)
each built a throwaway `git worktree add --detach` copy under `/tmp` for this purpose, and each did so only after
the conflict appeared.

## Incidents

| Category | Incidents | Cost |
| --- | ---: | --- |
| Review snapshot carried another session's hunks | 9 rounds | about 6 extra rounds; 7 blockers that belonged to other tasks |
| Own files left out of the snapshot because another scope claimed them | 1 | 1 round; 29 no-op Edit/revert calls on 15 files to re-claim them |
| Shell run claimed another session's concurrent writes | about 6 | fed the review rounds above |
| Generated outputs carrying several tasks' changes | 4–5 | 3 `TestBundleFresh` majors on 0582 |
| Landing around the commit tool | 5 landings | 2 `git apply --cached` commits, 1 `update-ref` and `git reset` in the shared tree, 2 scratch worktrees |
| Waiting for another session's commit | 3 | about 23 minutes, including a 12-minute `git diff` poll |
| Shared build broken by in-progress work | 3 | scratch-worktree test runs |

Examples:
- Every 0582 review round (s_e4ab341a89ccd33e, events 463–666) had the blocker that 0461's `GetWorkLoopPreflight`
  handler "breaks both the server and CLI builds". Round 2 declined the session's isolated-worktree evidence as "a
  different candidate".
- 0461's first snapshot left out its own regenerated Go/web/Swift bindings (s_9e8ba8743514c9b3, event 525). Another
  session's shell had claimed them. The coordinator then made 29 no-op edits to claim them back.
- s_b33f38df5f019e1c (event 318) found that "the claim system seems to have attributed another session's
  simultaneous write to my Bash run". The same session later noted that no tool releases a claim (event 325).
- 0582 landed as `b5c61d7` without a reviewer accept. It was committed in a `/tmp` worktree from keyword-filtered
  hunks, and the session then moved `master` with `update-ref` and ran `git reset` in the shared tree.
- On November 21 (0571, session search RPC), a commit swept in 16 backlog files from a concurrent groom session
  after 0566 had landed (s_e8819b5b72d9ac7a, event 598).

## Hot spots

In 158 commits since November 1, `spec.md` changed in 54, `internal/web/dist` in 44, `proto/ycc/v1/ycc.proto` in 27,
and `internal/server/server.go` in 22. A proto change also changes five generated files: `ycc.pb.go`,
`yccv1connect/ycc.connect.go`, `clients/web/src/gen/ycc/v1/ycc_pb.ts`, and the two Swift files under
`clients/ios/YccKit/Sources/YccProto`. A web change rewrites `dist/index.html` and `manifest.json` and renames the
hashed `index-*.js` and `index-*.css` assets. Any two tasks running at once in the web client or the API therefore
share files. The other shared files were `docs/remote-api.md`, `internal/orchestrator/orchestrator.go`,
`reviewdiff.go`, `internal/session/workloop.go`, and `workloop_persist.go`.

## Shell attribution

`claimShellChanges` claims for the command's scope every path that changed during the command and that no other
scope claims at the command's end. When two scopes' shells overlap, the first to finish claims the paths the other
is still writing, such as generator output or a web build. The other shell later finds those paths claimed, records
nothing for them, and the paths drop out of its scope's changeset.

## What worked

- Notices and intents. Notice n1 ("Regenerate from the shared proto; don't revert others' hunks") and 0461's intent
  ("Please don't revert GetWorkLoopPreflight hunks") meant no session reverted another's work.
- After notice n2 announced that 0582 had landed, 0461 requested re-review within about 40 seconds.
- Overlap notices and `list_sessions path=` identified the claimants quickly.
- After 0585 made the other-work note non-blocking, 0585 passed in two rounds despite a build that other work had
  broken. The reviewer called the failure "attributable to the other session, not a blocker".

## Workstreams

No session used the workstream tool. 0582 considered one mid-task and chose an ad-hoc worktree instead. While
designing 0567, s_749564ca87c7d8d4 noted that a workstream "doesn't fix the current commit either". Choosing a
workstream at the start would have made integration conflict on the same generated files.

## Follow-ups

Four proposed tasks came from this report:
- 0591: line attribution for all docs-layer Markdown.
- 0592: generated outputs rebuilt from the committed sources.
- 0593: a candidate tree for verification, review and commit, absorbing 0586 (line attribution of code files).
- 0594: shell attribution that does not guess while shells overlap.
