---
id: "0488"
title: 'Web: health banner and Health panel'
status: done
priority: 2
created: "2026-10-29"
updated: "2026-11-27"
depends_on:
    - "0487"
spec_refs: []
---

## Description

When any daemon health check (GetHealth) is warn/fail, the web shell shows a banner naming the failing checks; clicking opens a Health panel listing every check with status, detail, remedy, last-checked time, and a "Check now" action (GetHealth refresh=true). Polls about once a minute. No banner when everything is ok.

## Outcome

The committed web sidebar names unhealthy checks and links to the per-check Status panel; Check now refreshes health and healthy daemons show no warning. Reviewed b0415f7a and current behavior against the saved scratch-daemon checks; web typecheck, all 522 tests and the embedded bundle check pass.

Commit: Close review of the web health banner and Status panel
