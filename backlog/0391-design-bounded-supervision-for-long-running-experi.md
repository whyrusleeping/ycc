---
id: "0391"
title: Design bounded supervision for long-running experiments and health check-ins
status: proposed
priority: 2
created: "2026-09-17"
updated: "2026-09-17"
depends_on: []
spec_refs:
    - 7.3 Subagents and asynchronous jobs
    - 9.1 Unattended work loop
    - 11. Questions, unattended work, and confirmation
---

## Description
Vals retrospective: completed jobs historically waited hours for user check-ins (idle wakeup fixed by 0379), while external recordings/soaks could die or stall long before the next check-in. A hand-rolled hourly watcher failed immediately due to sh/bash mismatch. Another check-in reported +70k events as healthy until the user prompted rate/drop-count analysis, revealing ~13 events/s and dropped batches (s_0dd031d407fe4524 #948-990). Process completion wakeup alone is not remote-experiment health supervision.

Design/evaluation scope, not approval for unbounded autonomous hardware work:
- A bounded run record should identify owner/session, source/build, command/runtime, artifacts, resource lease/authorization, expected cadence, stop conditions, and monitored processes; distinguish local Bash job lifetime from external service lifetime.
- Evaluate a reusable explicit heartbeat/health check mechanism with failure notification and daemon-restart semantics rather than a model-authored infinite sleep loop. Preserve explicit stop and resource budgets; no silent remote restart or destructive recovery.
- Check-in receipts should normalize deltas by elapsed time and compare only compatible baselines; surface stalls, drop/error counts, and incomplete monitoring. Baseline/workload mismatch must be explicit, not an automatic failure classification.
- Fixtures: watcher crashes before first check, workload exits while parent is idle, missed heartbeat, intentional stop/restart, and incomparable benchmark profiles. Verify bounded discovery/notification latency and no duplicate actions.
- Pre-authorized resource policy, if added, requires explicit scoped user consent and expiry/revocation; never infer standing wipe authorization from prior permissive answers.

Related existing 0256 resource leases, 0324 preflight, 0366 limits, 0379 completion wakeup. Evidence/report: docs/reports/vals-harness-retrospective.md.

## Acceptance criteria

## Work log
