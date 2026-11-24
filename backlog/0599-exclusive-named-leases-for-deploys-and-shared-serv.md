---
id: "0599"
title: Exclusive named leases for deploys and shared services
status: done
priority: 3
created: "2026-11-23"
updated: "2026-11-24"
depends_on: []
spec_refs:
    - spec.md#9.1 Unattended work loop
---

## Description

Status: todo. Fourth step of the parallel work-loop plan (user decision 2026-11-23, session s_a96d1f53776eaa46, event 302). It is a reduced subset of 0256 (exclusive-resource leases with shell-pattern blocking and flock interop), which stays proposed for the rest.

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

## Outcome

Coordinators (outside integrate mode, with or without session coordination) have acquire_lease(name, reason, ttl, job_id?) and release_lease(name): daemon-memory, per-project exclusive leases that fail fast naming the holder and are released on release, expiry, job exit, session end (a stopping session's job-attached lease stays held until its job really stops) and daemon restart, with lease_acquired/lease_released events. Leases show in list_sessions (lease-only while coordination is off), the list_backlog footer, ListProjectNotices sessions (lease holders only while off) and the web/iOS Live sessions views; the iOS More menu always offers Live sessions. The work-loop prompt requires a lease to deploy or restart shared services. Spec §7.3/§9.1 landed in 87490c4.. (three spec commits). Verified: Go tests (session under -race) on HEAD+task files, web typecheck/489 tests and rebuilt bundle, iOS build + 686 YccKit tests on the Mac, web and iOS screenshots against a scratch daemon with a mock model calling acquire_lease.

Commit: Exclusive named leases for deploys and shared services (0599)
