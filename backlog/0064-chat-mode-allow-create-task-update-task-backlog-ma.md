---
id: "0064"
title: 'Chat mode: allow create_task/update_task backlog management'
status: done
priority: 3
created: "2026-06-29"
updated: "2026-06-29"
depends_on: []
spec_refs: []
---

## Description
## Description
There is orphaned, uncommitted working-tree work (discovered during task 0049) that extends the `chat` mode to manage the backlog directly: it adds `create_task` and `update_task` to the chat mode toolset in `internal/orchestrator/modes.go`, documents these in the chat system prompt (`internal/orchestrator/prompts.go`), and adds a chat-mode toolset assertion to `internal/orchestrator/modes_test.go`.

These changes are unrelated to 0049 and were deliberately kept out of that commit (unstaged, preserved in the working tree). This task tracks finishing/validating and committing them on their own.

## Acceptance criteria
- [ ] chat mode exposes create_task and update_task (plus the existing file/read tools) but NOT the implementation pipeline tools (spawn_implementer, spawn_reviewers, commit, switch_to_work).
- [ ] chat system prompt documents create_task/update_task as the preferred way to manage the backlog.
- [ ] modes_test.go asserts the chat toolset (present + absent tools).
- [ ] go build ./... and go test ./... pass; committed as a focused change.

## Notes
- The relevant diff already exists unstaged in the working tree (modes.go, modes_test.go, prompts.go). Verify it is correct/complete before committing.


## Acceptance criteria

## Work log
- 2026-06-29 plan: The implementation already exists, staged, in the working tree (modes.go adds create_task/update_task to chat toolset; modes_test.go asserts present/absent tools; prompts.go documents them). I verifie
…[truncated]
- 2026-06-29 review tier: simple (coordinator self-review)
- 2026-06-29 decision: accept — commit: chat mode: allow create_task/update_task backlog management  Add create_task and update_task to the chat-mode toolset, document them in the chat system prompt as the preferred way to manage the backlo
…[truncated]
