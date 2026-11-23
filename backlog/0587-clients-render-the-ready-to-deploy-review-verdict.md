---
id: "0587"
title: 'Clients: render the ready_to_deploy review verdict'
status: proposed
priority: 4
created: "2026-11-22"
updated: "2026-11-22"
depends_on: []
spec_refs: []
---

## Description
0579 added a third review verdict, `ready_to_deploy`, with a `pending_criteria` list on `review_submitted` events. Clients still handle only accept and revise:
- Web: `reviewVerdict` in `clients/web/src/features/session/projection.ts` maps it to "unknown", so the row tag reads UNKNOWN while the heading reads READY_TO_DEPLOY.
- iOS: `SessionView.swift` labels it "Ready_To_Deploy" with the question-mark symbol and secondary tint.
- `pending_criteria` is not in the sessionview `presentationKeys`, so page summaries drop it.

## Acceptance criteria
- Web and iOS show a distinct "Ready to deploy" verdict label, with its own symbol and tint, and list the pending criteria under it.
- Page summaries keep `pending_criteria` for review rows.

## Work log
