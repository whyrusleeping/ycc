---
id: "0463"
title: 'Chat sessions: commit accepted task work instead of ending on "should I commit?"'
status: done
priority: 3
created: "2026-10-12"
updated: "2026-11-10"
depends_on: []
spec_refs: []
---

## Description

**Evidence (vals):** chat sessions s_bfed51f8 (0403/0406, 2026-10-09) and s_8666773a (0366/0371/0372/0377, 2026-10-11) each implemented, reviewed and integrated tasks. Each ended with a closing question ("commit now or after the hardware check?" / "Do you want me to commit just these four tasks' files?") that was never answered. The work then sat uncommitted for 1–3 days and wedged the next work loop.

**Proposal:** pick one of these:
- (a) In chat mode, once a task's review is accepted and its software criteria are met, commit it as the work prompt already does, and mention it in the report rather than asking.
- (b) If the session asks anyway, persist the open question so it stays visible after the session goes idle, e.g. as a home/iOS "needs answer" item, and in work-loop preflight.

**Acceptance:** the chosen behaviour is specified in the spec and prompts. A reviewed task in a chat session no longer ends uncommitted without a persistent, visible prompt.

**Update (owner accepted, 2026-11-09 usage analysis):** across all projects since June, about 12% of the owner's follow-up messages (128) are "commit", "commit and push" or "commit everything". In 78 of 104 cases the agent's last report had ended with a statement like "changes are uncommitted", not a question. This applies to ordinary chat edits too, not only reviewed backlog tasks. Prefer (a). Add a per-project policy for finished chat work: `commit`, `commit+push`, or `ask` (today's behaviour). The default must remove the need to type "commit". Pushing requires the explicit `commit+push` setting. The final report says what was committed (and pushed). Related: 0462 removes the commit-tool restrictions behind "commit everything when I say commit".

## Outcome

Chat mode has a commit tool: it commits the session's attributed files whole (plus named paths), or with task_id+outcome finalizes the task like work mode; it records commit_made. Project policy [chat] finished_work in .ycc/config.toml: commit (default), commit+push (explicit current-branch-to-upstream refspec, credential-scrubbed env, failure reported without undoing the commit), ask (agent asks via ask_user first). The chat prompt carries policy-specific guidance; spec §9 and README document it. Unit tests cover policy parsing, commit scope, task finalization and push scope/env. A live opus scratch-daemon run committed a task and a plain edit unprompted, and asked under ask. sol accepted on round 2.

Commit: Chat sessions commit finished work by default: chat commit tool, [chat] finished_work = commit|commit+push|ask (0463)
