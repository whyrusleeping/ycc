---
id: "0008"
title: Sandbox reviewer bash to prevent workspace mutation
status: todo
priority: 6
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0005"]
spec_refs: ["Tools", "Open questions"]
---

## Description
Reviewers are given read/inspect tools plus bash so they can run `git diff`, read files,
etc. They are prompted not to modify the workspace, but with bash available that is not
enforced. Add a sandbox so reviewer tool calls cannot mutate the workspace (read-only
mount / overlay, restricted bash, or a syscall/FS guard). Until then, reviewer
non-mutation is prompt-enforced only.

## Acceptance criteria
- [ ] reviewer bash cannot write to or delete from the workspace
- [ ] read-only inspection (git diff, cat, grep, ls) still works
- [ ] mechanism documented; degrades gracefully if unavailable

## Work log
