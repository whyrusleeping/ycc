---
id: "0463"
title: 'Chat sessions: commit accepted task work instead of ending on "should I commit?"'
status: proposed
priority: 3
created: "2026-10-12"
updated: "2026-10-12"
depends_on: []
spec_refs: []
---

## Description
**Evidence (vals):** chat sessions s_bfed51f8 (0403/0406, 2026-10-09) and s_8666773a (0366/0371/0372/0377, 2026-10-11) each implemented, reviewed and integrated tasks. Each ended with a closing question ("commit now or after the hardware check?" / "Do you want me to commit just these four tasks' files?") that was never answered. The work then sat uncommitted for 1–3 days and wedged the next work loop.

**Proposal:** pick one of these:
- (a) In chat mode, once a task's review is accepted and its software criteria are met, commit it as the work prompt already does, and mention it in the report rather than asking.
- (b) If the session asks anyway, persist the open question so it stays visible after the session goes idle, e.g. as a home/iOS "needs answer" item, and in work-loop preflight.

**Acceptance:** the chosen behaviour is specified in the spec and prompts. A reviewed task in a chat session no longer ends uncommitted without a persistent, visible prompt.

## Acceptance criteria

## Work log
