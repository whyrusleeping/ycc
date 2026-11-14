---
id: "0551"
title: Make chat watch corrections and completion explicit
status: done
priority: 2
created: "2026-11-14"
updated: "2026-11-14"
depends_on: []
spec_refs:
    - spec.md#9.2 Watches
---

## Description

Recent Wheeler sessions repeatedly asked for watch creation while trying to correct existing definitions. Duplicate names are checked only after confirmation, chat lacks inspection/edit controls, and completion reporting neither stops a watch nor resumes its parent chat.

Acceptance criteria:
- Invalid and existing-name create proposals fail before user confirmation; confirmed creation still rejects duplicate races.
- Chat can inspect existing watches and make explicit, confirmed updates and pause/resume changes without creating companion watches to repair definitions.
- A watch run can report completion, pause future checks, and optionally notify the user; report history and continuity remain intact.
- Tool guidance makes probe gating, progress checks, completion notification, and absence of parent-chat continuation clear.
- Existing watches retain their current behavior unless explicitly edited or completed. No live experiment jobs or personal watch definitions are changed.

## Outcome

Chat can inspect watches and confirm full-definition updates or pause/resume changes. Create/update preflight rejects invalid or duplicate targets before approval, with mutation-time rechecks; exact expiry survives corrections. Completion pauses scheduling and optionally notifies once. Probe and fresh-session guidance clarified. Full affected-package race tests pass; remaining suite passes excluding two failures reproduced on untouched HEAD (TestGetBacklogGroom and stale TestBundleFresh). Existing live watches/jobs unchanged.

Commit: Make watch corrections and completion explicit
