---
id: "0629"
title: 'Clients: drop the dead ambiguous-claim display and deprecate ClaimedPath.ambiguous'
status: proposed
priority: 5
created: "2026-11-26"
updated: "2026-11-26"
depends_on: []
spec_refs: []
---

## Description
## Why
Since 0627 the daemon never records ambiguous claims: a path two sessions' shells may have written is left unattributed (unclaimed), and `ListProjectNotices` never sets `ClaimedPath.ambiguous`. The web Live sessions view (`clients/web/src/features/coordination/claims.ts`, `CoordinationPage.tsx`), the iOS view (`clients/ios/App/CoordinationViews.swift`), and `ycc notices` (`cmd/ycc/notices.go`) still split claims into definite and "possibly this session's" groups, which now only matters against an older daemon.

## Scope
- Remove the ambiguous-claim grouping from web, iOS and `ycc notices` (and their tests), or keep it only if older-daemon compatibility is wanted.
- Mark `ClaimedPath.ambiguous` deprecated in `proto/ycc/v1/ycc.proto` and regenerate bindings.
- Update spec.md §7.3 (overlap notices paragraph).

## Acceptance criteria
- No client renders an ambiguous-claim group; the proto field is documented as deprecated.
- Web typecheck/tests pass; iOS builds on the Mac with no new warnings.

## Work log
