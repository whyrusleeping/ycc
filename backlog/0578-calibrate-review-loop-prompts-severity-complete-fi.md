---
id: "0578"
title: 'Calibrate review-loop prompts: severity, complete first pass, fix the class'
status: proposed
priority: 2
created: "2026-11-21"
updated: "2026-11-21"
depends_on: []
spec_refs: []
---

## Description
Found by 0576 (review-cycle investigation, `docs/reports/review-cycles-2026-11.md` causes 2–4).

Mean review rounds (review-tool calls) per reviewed session rose from 1.1 (July) to 2.1 (November). The `sol` reviewer's revise rate rose from 33% to 55%, and in all 40 split multi-reviewer rounds `sol` revised while Claude/opus accepted. The long cycles show three prompt gaps:
- Coordinators fixed the reproduced instance each round. The next round found another instance of the same class: six TOML forms in 0443 (session coordination setting), three callers of one project-unsafe lookup in 0571 (session search RPC).
- "Major" is undefined, and the two reviewer models grade the same snapshot differently. In the long splits read (0443; 0388, reviewer sandbox scratch; 0566, line-attributed bookkeeping commits) `sol`'s majors were demonstrated acceptance-criterion failures, some of which failed closed, that the accepting reviewer missed or graded minor.
- The reviewer prompt does not say whether supplied evidence can satisfy a gate the sandbox cannot execute, such as the Mac build in 0573 (iOS session search UI).

Demonstrated criterion failures must still block, whether or not they fail closed.

## Acceptance criteria
- The reviewer prompt defines blocker/major as a demonstrated acceptance-criterion failure, data loss or corruption, or a security exposure within the task's scope, whether or not it fails closed; hardening beyond the task's criteria is a minor finding or a proposed follow-up.
- The reviewer prompt asks for every substantive finding in the first round, including other instances of the same problem.
- The reviewer prompt says how to judge supplied evidence for a gate the sandbox cannot execute.
- The coordinator and implementer prompts ask for fixing a finding's class, and for changing approach when successive rounds find defects in the same area.

## Work log
