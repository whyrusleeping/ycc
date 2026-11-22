---
id: "0576"
title: Investigate review cycles
status: done
priority: 2
created: "2026-11-21"
updated: "2026-11-22"
depends_on: []
spec_refs: []
---

## Description

I often see reviewers going back and forth with implementers four or more times, and while this is perfectly acceptable, getting the code right is paramount, i wonder if there’s anything we can do in the prompting or structure of the harness to improve this

## Acceptance criteria

- The causes of long review cycles are identified from session evidence, and concrete prompt or harness changes are proposed.

## Outcome

docs/reports/review-cycles-2026-11.md: from 274 reviewed sessions, mean review rounds rose from 1.1 (Jul) to 2.1 (Nov); sol's revise rate went from 33% to 55%, and all 40 split multi-reviewer rounds were sol revising. Causes: retain-mode re_review silently drops the coordinator's handoff (8 cases; 0573 spent rounds 2-7 asking for Mac evidence the handoffs carried), fixes patch the reproduced instance instead of the class (0443, 0571, 0566), "major" is undefined, and the reviewer cannot execute some gates. Filed 0577 (deliver coordinator verification evidence to reviewers) and 0578 (calibrate review-loop prompts) as proposed.

Commit: docs: review-cycle investigation report; propose 0577 and 0578
