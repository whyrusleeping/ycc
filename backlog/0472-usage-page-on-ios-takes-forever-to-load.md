---
id: "0472"
title: Usage page on ios takes forever to load
status: done
priority: 3
created: "2026-10-21"
updated: "2026-10-28"
depends_on: []
spec_refs: []
---

## Description

## Description

## Acceptance criteria

## Outcome

GetUsage (behind the iOS Usage page) went from ~26s per all-projects request on this host to ~1.9s cold (once per daemon start) and ~0.1s warm. The cached, incremental usage.Scanner gives entries identical to the old full decode on real project logs, and TestScannerIncremental covers append, torn tail, replaced log, and removed log. The iOS client was unchanged; restart the daemon to pick this up.

Commit: usage: cache and incrementally reduce event logs for GetUsage
