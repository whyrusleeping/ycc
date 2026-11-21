---
id: "0256"
title: Exclusive-resource leases (hardware, prod deploys, ports) — deferred design
status: proposed
priority: 4
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - 'docs/design/workstream-integration.md#Bootstrap and resources'
---

## Description

**Deferred by decision** (2026-08-06): asked whether to build leases or a parallelism cap first, the user answered "maybe we just punt on this and dont have a resource manager thing yet" (session s_c5badc8fb90d408b, event 81). Captured so the design isn't lost; the stand-in is
`[integration] max_parallel` (task 0252, the integration queue, done) plus doing hardware/prod work on the base branch.
The design doc's former "§8 Deliberately deferred: exclusive resources" was pruned in `c6907b1`;
its "Bootstrap and resources" section now states only that the queue does not allocate exclusive
external resources.

Some projects have resources exactly one session may touch at a time: bench/GPU hardware
(the user's vals project), a production deployment target (the user's oldgrowth project), a fixed dev port. Worktree isolation does
not help — the contended thing is outside the tree.

Proposed model: daemon-owned **named leases**.

```toml
[[resources]]
name = "gpu";  capacity = 1; scope = "project"; match = ["make bench*"]
[[resources]]
name = "prod"; capacity = 1; scope = "global";  match = ["./deploy.sh*"]; confirm = true
```

- **Choke point is the daemon's bash tool** — all shell (coordinator, implementer, reviewer,
  background jobs) flows through `internal/tools`, so a command matching `match` transparently
  blocks on the lease. No prompt cooperation required, nothing to forget.
- Plus an explicit `acquire_lease(name, reason, ttl)` tool for multi-command critical sections
  (deploy → smoke → rollback), and an optional workstream-level `requires = [...]` at spawn.
- **Back leases with a `flock`ed lockfile** so a human shell (`ycc lease acquire gpu -- ./bench.sh`)
  and cron jobs contend on the same lock, not just ycc sessions.
- Safety: TTL + forced reclaim, release on session death/daemon restart, one lease at a time
  (or sorted acquisition) to avoid deadlock.
- Visibility: `lease_waiting` / `lease_acquired` / `lease_released` events and a
  "waiting on gpu, held by ws_3f9a for 4m" badge in TUI + iOS — invisible blocking is the
  failure mode that makes people rip such a system out.

## Acceptance criteria

- [ ] Config parsed; a matching command blocks until the lease is free, then runs.
- [ ] Leases survive nothing: daemon restart / session death releases them.
- [ ] A human CLI holder blocks an agent and vice versa (flock interop).
- [ ] Waiting is visible in both clients with holder + duration.


## Work log
- 2026-11-21 groom: cited the source of the 2026-08-06 deferral (session s_c5badc8fb90d408b, event 81). Repointed spec_refs from the pruned design-doc §8 (removed in `c6907b1`) to "Bootstrap and resources". Glossed 0252, vals and oldgrowth. Not implemented: there is no `[[resources]]` config or lease tool. Still proposed.
