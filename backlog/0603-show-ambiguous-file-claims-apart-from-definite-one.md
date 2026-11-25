---
id: "0603"
title: Show ambiguous file claims apart from definite ones (proto, web, iOS, CLI, list_sessions)
status: done
priority: 2
created: "2026-11-24"
updated: "2026-11-25"
depends_on:
    - "0609"
spec_refs:
    - spec.md#7.3
---

## Description

Current state: todo. Accepted by the user 2026-11-24 in session s_5a6bb9c25a12632a (UX review of the live wheeler work loop).

A path that changes while shells of several sessions overlap gets an ambiguous claim for each of them (`RecordAmbiguous`, `internal/workspacelease/claims.go`; `Claim.Ambiguous`). The claim list sent to clients drops that flag: `internal/server/server.go` builds `v1.ClaimedPath{Path, Since}` only, and `ClaimedPath` has no field for it.

Observed 2026-11-24 on wheeler loop `loop_d074bc58` (4 slots, 6 live sessions): every live session listed the same six `embed/meme/*.py` paths as claims, each 1-2 minutes old. wheeler's `.git/ycc/claims.json` recorded `embed/meme/__main__.py` as ambiguous for 6 sessions. The web Live sessions page, the iOS Live sessions screen and `ycc notices` all showed them as plain claims, so the claim lists did not say which session was editing which file. Loop sessions also spent tool calls on `release_claim` for these paths (two calls each in the visible transcript windows of the 0272 and 0449 sessions).

Depends on 0609 (shell claim attribution: background jobs stop making their session a co-claimant of other sessions' writes; accepted by the user 2026-11-24 in session s_5a6bb9c25a12632a). 0609 removes most ambiguous claims at the source; this task covers the display of the ones that remain, such as paths changed while two sessions' foreground commands overlapped.

## Acceptance criteria

- `ClaimedPath` carries whether the claim is ambiguous, set from `Claim.Ambiguous`.
- Web Live sessions cards and iOS Live sessions rows list definite claims first and show ambiguous ones apart (a separate, collapsed "possibly this session's" group or equivalent), with the count of each.
- `ycc notices` and the agent-facing `list_sessions` / `list_backlog` footer text mark ambiguous paths as such.
- Tests cover the proto mapping; web typecheck and vitest pass; the iOS change is built and screenshot-checked on the Mac simulator.

## Outcome

ClaimedPath.ambiguous (field 3) is set from Claim.Ambiguous via claimedPathsProto, and the Go, web and Swift bindings are regenerated. list_sessions shows definite claims first and ambiguous ones on their own line. While coordination is on, the list_backlog footer flags each session's ambiguous claims (count and first three paths). ycc notices prints separate claims and ambiguous-claims lines. The web Live sessions cards and iOS rows list definite claims first, then a collapsed "Possibly this session's: N paths" group. The spec §7.3 sentence is updated and the web dist is rebuilt. Tests cover the proto mapping, claimLines, the footer and ycc notices, plus a vitest for splitClaims. The full Go, web typecheck and vitest runs pass. The iOS build succeeded on the Mac and was screenshot-checked on the simulator in light and dark, collapsed and expanded. Review: sol accepted in round 3.

Commit: 0603: show ambiguous file claims apart from definite ones (proto, server, list_sessions/list_backlog footer, ycc notices, web, iOS)
