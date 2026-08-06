---
id: "0256"
title: Exclusive-resource leases (hardware, prod deploys, ports) — deferred design
status: proposed
priority: 4
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - 'docs/design/workstream-integration.md#8. Deliberately deferred: exclusive resources'
---

## Description

**Deferred by decision** (2026-08-06) — captured so the design isn't lost; the stand-in is
`[integration] max_parallel` (task 0252) plus doing hardware/prod work on the base branch.

Some projects have resources exactly one session may touch at a time: bench/GPU hardware
(vals), a production deployment target (oldgrowth), a fixed dev port. Worktree isolation does
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
