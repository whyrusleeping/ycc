---
id: "0599"
title: Exclusive named leases for deploys and shared services
status: todo
priority: 3
created: "2026-11-23"
updated: "2026-11-23"
depends_on: []
spec_refs:
    - spec.md#9.1 Unattended work loop
---

## Description
Status: todo. Fourth step of the parallel work-loop plan (user decision 2026-11-23, session s_a96d1f53776eaa46). It is a reduced subset of 0256 (exclusive-resource leases with shell-pattern blocking and flock interop), which stays proposed for the rest.

## Why
In wheeler, the user's data-pipeline project, the costly non-git collisions between parallel sessions were deploys and restarts:
- A sibling's normalize-v2 restart interrupted a running probe (wheeler reports/2026-11-22-reddit-2026-09-load.md).
- A deploy shipped another task's unreviewed code (wheeler task 0221).
- A session waited on `ask_user` from 17:45 to 20:13 over whether to deploy master with a sibling's commits in it (s_5a316b94976a87d4 event 530).

Notices worked as advice (notice n7 "Hold append jobs until I clear this"), but nothing made them exclusive.

User decision (2026-11-23, s_a96d1f53776eaa46): an unattended loop agent may deploy or restart shared services only while holding an exclusive lease for that target.

## Acceptance criteria
- Coordinator tools `acquire_lease(name, reason, ttl)` and `release_lease(name)` operate on a daemon-owned, per-project record (name, holder session or job, reason, acquired time, expiry).
- Acquiring a held lease fails immediately, naming the holder, reason and expiry. There is no blocking wait or polling.
- A lease can be attached to a background job, so it outlives the turn and is released when the job exits.
- Leases are released on `release_lease`, on expiry, when the holder session ends (unless attached to a running job), and on daemon restart.
- Held leases appear in `list_sessions`, the `list_backlog` footer and clients' session views. `lease_acquired` and `lease_released` events are recorded.
- The work-loop prompt says to deploy or restart shared services only while holding a lease for the target.
- Not included: shell-command pattern blocking, flock/CLI interop and cross-project leases. 0256 keeps them.
- Tests cover contention, expiry, job-attached release and session-end release.
- spec.md records the tool and the loop rule.

## Work log
