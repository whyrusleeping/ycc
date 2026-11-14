---
id: "0552"
title: Disable agent watch creation and use background jobs for continuation
status: done
priority: 2
created: "2026-11-14"
updated: "2026-11-14"
depends_on: []
spec_refs:
    - spec.md#9.2 Watches
---

## Description

Agents have treated watches as if completion resumes the creating chat. Remove watch creation from the exposed agent toolset for now and remove automatic watch-proposal guidance. Work whose completion requires the creating session should use a tracked background job and wait.

Acceptance criteria:
- No agent mode exposes schedule_watch, including persistent-daemon attended chats.
- Agent guidance no longer tells models to create watches for unfinished work and instead directs same-session follow-up through background jobs and wait.
- Existing watches, watch-run behavior, manual/UI creation, and chat inspection/edit/pause controls remain available and unchanged.

## Outcome

No agent mode exposes schedule_watch, and automatic watch-proposal guidance is removed. Shared guidance directs same-session follow-up through tracked background jobs and wait. Existing watch management, watch_report, and UI/RPC creation remain unchanged. Retired the obsolete paid watch-proposal eval. Verified absence across modes and reopened persistent-daemon chats, retained watch/background behavior, affected-package race tests, liveeval compilation, and full suite excluding the two previously verified baseline failures.

Commit: Disable agent watch creation in favor of background continuation
