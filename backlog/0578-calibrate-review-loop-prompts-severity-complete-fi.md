---
id: "0578"
title: 'Calibrate review-loop prompts: fix the class, complete first pass, unrunnable checks'
status: todo
priority: 2
created: "2026-11-21"
updated: "2026-11-22"
depends_on: []
spec_refs: []
---

## Description
Found by 0576 (review-cycle investigation, `docs/reports/review-cycles-2026-11.md` causes 2–4).

Mean review rounds per session/task pair rose from about 1.1 (July) to 2.1 (November) in ycc, and to 2.14 in Wheeler (another project run with ycc), with 13% of Wheeler pairs taking four or more rounds. The `sol` reviewer revises most often, but in the long cycles read its majors were real acceptance-criterion failures that the accepting reviewer missed or graded minor (0443 session coordination setting, 0388 reviewer sandbox scratch, 0566 line-attributed bookkeeping commits). On 2026-11-22 the user chose to keep `gpt-6.2-sol` as the `sol` reviewer, calling it the better model, and asked for a light touch (session s_70ed48449817e056, event 974: "6.2 sol is definitely a better model, so lets keep it. Maybe we can prompt the coordinator better? … i don't want to be too heavy handed"). So this task changes how findings are fixed rather than lowering the reviewer's bar.

Gaps the prompts leave:
- Each fix covered only the reported instance. In 0571 (session search RPC) the next round found another caller of the same project-unsafe lookup, twice. In 0443 most later findings were in code the previous fix had added, so only a different approach would have helped, not a more complete first review.
- Nothing asks the reviewer to report everything it can substantiate in the first round.
- The reviewer prompt does not say how to weigh supplied evidence for a check the sandbox cannot run, such as the Mac build in 0573 (iOS session search UI). 0577 (deliver coordinator verification evidence) gets the evidence to the reviewer but does not say how to judge it.

Out of scope: a definition of major severity, round caps, and instructions to push back on reviewers. A second opinion from `astra` (the configured logical model `gpt-6.2-astra`, 2026-11-22) agreed with this scope and supplied the wording below.

## Acceptance criteria
- The coordinator and implementer prompts each add, in substance: "When fixing a finding, check affected paths for the same defect and fix any in scope."
- The coordinator prompt adds, beside re_review: "If successive revisions expose related defects, reconsider the approach before patching another case."
- The reviewer prompt adds: "On the first pass, report all substantive findings you can substantiate; group instances of the same defect."
- The reviewer prompt adds, after the verification-classification guidance: "For a check you cannot run, judge whether the supplied evidence verifies the assigned snapshot."
- `reReviewPrompt` is unchanged; a complete first pass does not forbid later findings.

## Work log
- 2026-11-22: Rescoped after the user kept `gpt-6.2-sol` and asked for a light touch: dropped the severity definition, moved the review-timing line to 0579 (ready-to-deploy verdict), adopted astra's wording.
- 2026-11-22 groom: cited the user's decision (s_70ed48449817e056, event 974), glossed Wheeler and `astra`. Checked that none of the four prompt lines is in internal/orchestrator/prompts.go yet.
