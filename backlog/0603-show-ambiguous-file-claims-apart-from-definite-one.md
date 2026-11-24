---
id: "0603"
title: Show ambiguous file claims apart from definite ones (proto, web, iOS, CLI, list_sessions)
status: todo
priority: 2
created: "2026-11-24"
updated: "2026-11-24"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description
Current state: todo. Accepted by the user 2026-11-24 in session s_5a6bb9c25a12632a (UX review of the live wheeler work loop).

A path that changes while shells of several sessions overlap gets an ambiguous claim for each of them (`RecordAmbiguous`, `internal/workspacelease/claims.go`; `Claim.Ambiguous`). The claim list sent to clients drops that flag: `internal/server/server.go` builds `v1.ClaimedPath{Path, Since}` only, and `ClaimedPath` has no field for it.

Observed 2026-11-24 on wheeler loop `loop_d074bc58` (4 slots, 6 live sessions): every live session listed the same six `embed/meme/*.py` paths as claims, each 1-2 minutes old. wheeler's `.git/ycc/claims.json` recorded `embed/meme/__main__.py` as ambiguous for 6 sessions. The web Live sessions page, the iOS Live sessions screen and `ycc notices` all showed them as plain claims, so the claim lists did not say which session was editing which file. Loop sessions also spent tool calls on `release_claim` for these paths (two calls each in the visible transcript windows of the 0272 and 0449 sessions).

Open question for this task (authoring agent's inference, not a user decision): whether a session whose only overlap is a long-running background job (a monitor, a `sleep`/`wait` loop) should receive ambiguous claims at all. Record the answer in the task; changing the attribution rule is out of scope unless the user accepts it.

## Acceptance criteria
- `ClaimedPath` carries whether the claim is ambiguous, set from `Claim.Ambiguous`.
- Web Live sessions cards and iOS Live sessions rows list definite claims first and show ambiguous ones apart (a separate, collapsed "possibly this session's" group or equivalent), with the count of each.
- `ycc notices` and the agent-facing `list_sessions` / `list_backlog` footer text mark ambiguous paths as such.
- Tests cover the proto mapping; web typecheck and vitest pass; the iOS change is built and screenshot-checked on the Mac simulator.
- The open question above has a recorded answer or a follow-up proposal.

## Work log
