---
id: "0453"
title: 'Web: fold routine job lifecycle noise into transcript activity'
status: done
priority: 3
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
Accepted UI audit improvement 5. Group routine job lifecycle notices with related tool/reasoning activity rather than breaking long activity into multiple noisy blocks. Keep agent/user turns, questions, failures, commits and meaningful outcomes prominent, with execution details expandable. Current activity may summarize existing tool/job facts but must not invent progress. Preserve row identity, search reveal, paging anchors and scroll-away behavior.

## Outcome
Typed routine job/subagent notices join activity runs rather than splitting them; failed tools/jobs remain standalone, unrelated notices still break runs, and summary step counts exclude lifecycle plumbing. Folded entries keep row IDs/search reveal and existing scroll/paging behavior. Regression checks cover decoding, folding, failures and questions; full web build/test and browser visual check pass. No invented progress or new activity API. Commit: `web: polish personal inbox and desktop UX`.
