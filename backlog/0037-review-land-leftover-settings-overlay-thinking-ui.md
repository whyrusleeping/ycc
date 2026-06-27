---
id: "0037"
title: Review & land leftover settings-overlay thinking-UI rework (inline +/- thinking)
status: done
priority: 3
created: "2026-06-27"
updated: "2026-06-27"
depends_on: []
spec_refs: []
---

## Description
A prior session left an **uncommitted** rework of the settings overlay's per-role
thinking-level UI in `internal/tui/tui.go` (working tree, not committed). It removes the
separate `ovThinkCoord/ovThinkImpl/ovThinkRev` overlay rows and instead renders the
thinking level inline on each role row (e.g. `claude opus (xhigh)`), adding an
`overlayAdjustThinking` handler bound to `+`/`-` keys. This was excluded from the task 0023
commit to keep that change focused.

This task is to review that leftover diff on its own merits, decide whether to keep/adjust
it, and land it cleanly (or discard it). The change currently exists only in the working
tree — confirm it still applies, build/test it, and commit it under this task if accepted.

Note: task 0036 ("Per-role thinking level") delivered the underlying per-role thinking
feature and was committed at 0683f85; its status bookkeeping (in_progress → done) was
entangled with this leftover diff. Finalize 0036's status as part of landing this.

## Acceptance criteria
- [ ] The leftover `internal/tui/tui.go` thinking-overlay rework is reviewed and either
      landed (committed) or intentionally discarded with a note.
- [ ] If landed: `go build ./...` and `go test ./...` pass; the overlay still lets the user
      change per-role thinking levels (now inline with +/-), and the help text matches.
- [ ] Task 0036's status is finalized (it is functionally done as of 0683f85).
- [ ] No unrelated changes are bundled into the commit.

## Acceptance criteria

## Work log
- 2026-06-27 decision: accept — commit 0db488c: Settings overlay: inline per-role thinking level with +/- keys (§18.2)  Land the leftover tui.go rework: remove the separate coordinator/implementer/ reviewer thinking rows and render the thinking le
…[truncated]
