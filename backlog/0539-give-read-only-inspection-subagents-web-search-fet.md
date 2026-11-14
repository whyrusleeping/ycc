---
id: "0539"
title: Give read-only inspection subagents web_search/fetch_page
status: done
priority: 4
created: "2026-11-10"
updated: "2026-11-13"
depends_on: []
spec_refs: []
---

## Description

From memory note m-dm98dgt1j4vh (observation, 2026-10-20): "Read-only spawn_agent subagents don't get the web_search/fetch_page tools; for research they fell back to curl on primary docs, so tell them to use curl (or make them mutating) when web access matters."

Verified 2026-11-10: `tools.ReadOnlyInspect` (internal/tools/reviewer.go) returns Read, Search, sandboxed Bash and tool_output only, while `tools.Editing` appends `Web()`. Research is the main read-only use case, so the gap pushes agents to grant write access or use curl.

## Acceptance criteria

- Read-only inspection agents get web_search and fetch_page (still disabled when EXA_API_KEY is unset), or the spec states why they must not.
- Reviewer tool set decision recorded (include or exclude, with reason).
- Tool-set tests cover the change; memory note m-dm98dgt1j4vh retired once it lands.

## Outcome

ReadOnlyInspect now appends Web() (web_search, fetch_page; still error without EXA_API_KEY), so read-only spawn_agent subagents can research without curl or write access. Reviewers deliberately exclude web tools (verdicts rest on the assigned snapshot and source-bound evidence; the coordinator supplies external facts in the handoff), recorded in code and spec §§ generic agents/tools. TestWebToolsetMembership pins both. tools/orchestrator suites pass; server TestGetBacklogGroom fails identically at HEAD (date-dependent, proposed 0548).

Commit: 10034b7 — tools: give read-only inspection subagents web_search/fetch_page (0539)

## Work log
- 2026-11-14 groom: added the commit hash. Memory note m-dm98dgt1j4vh was retired in b1ba827, which meets the last criterion. Correction to the Outcome: the TestGetBacklogGroom failure is not date-dependent. It started at 65ff85c (done tasks no longer count toward groom churn), and 0548 (fix that test) now records that cause.
