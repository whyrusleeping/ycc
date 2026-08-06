---
id: "0173"
title: 'ycc cost: --task filter for a single-task agent/cost view'
status: done
priority: 4
created: "2026-07-07"
updated: "2026-08-06"
depends_on: []
spec_refs: []
---

## Description
`ycc cost --by task,agent` already produces the per-task per-agent breakdown, but answering "what did task 0093 cost, by agent?" requires scanning the full table. Add a `--task <id>` filter to the cost command (and GetUsage RPC) that restricts entries to one task before aggregation, so `ycc cost --task 0093 --by agent` prints just that task's agent breakdown plus its total.

Acceptance criteria:
- `ycc cost --task 0093 --by agent` shows only rows attributed to task 0093, with a TOTAL row for that task.
- Filter composes with `--by`, `--since`, `--until`.
- Unknown/empty task id behaves sensibly (empty table, no error).
- GetUsage RPC gains the corresponding field; daemon applies the filter server-side.

## Acceptance criteria

## Plan

Add a server-side single-task filter to the usage breakdown, exposed as `ycc cost --task <id>` and a `task` field on GetUsageRequest.

1. Proto (`proto/ycc/v1/ycc.proto`): add `string task = 5;` to `GetUsageRequest` with a short comment ("restrict to one backlog task id, optional; empty = no filter"). Regenerate Go with `buf generate` (buf + protoc-gen-go/protoc-gen-connect-go live in ~/go/bin). Also try the Swift template (`buf generate --template buf.gen.swift.yaml`) since `clients/ios/YccKit/Sources/YccProto/ycc/v1/ycc.pb.swift` is committed; it needs remote BSR plugins, so if the network is unavailable, leave the Swift file untouched and say so in the report (do NOT hand-edit generated code).

2. `internal/usage/usage.go`: add `Task string` to `Options` (documented: exact match on the entry's task id; empty means no filter) and apply it in `Aggregate` alongside the since/until filters, before grouping — so both rows and the TOTAL reflect only that task. Trim whitespace on the option value when comparing (or have the callers trim; pick one and be consistent). An unknown id simply matches nothing → zero rows and a zero TOTAL (no error).

3. `internal/server/server.go` `GetUsage`: pass `strings.TrimSpace(req.Msg.Task)` into `usage.Options.Task` so the filter is applied server-side (Manager.UsageReport already forwards Options).

4. `cmd/ycc/main.go` `costCommand`: add `&cli.StringFlag{Name: "task", Usage: "restrict to one backlog task `id`"}` and set `Task:` on the GetUsageRequest. Rendering is unchanged (the existing TOTAL row covers "total for that task"); an empty result prints just the header + a zeroed TOTAL row, which is the "sensible" empty-table behaviour.

5. Tests:
   - `internal/usage/usage_test.go`: filter alone, filter composed with `--by agent` grouping and with Since/Until, unknown id → no rows and zero TOTAL, empty Task → unchanged behaviour.
   - `internal/server/server_test.go` (if a GetUsage server test harness exists there or is cheap to add): a request with `task` set returns only that task's rows; otherwise cover it at the usage layer only — don't build heavy new scaffolding.

6. Docs: add the `--task` row + an example (`ycc cost --task 0093 --by agent`) to the `ycc cost` table in `docs/cli.md`, and mention the `task` filter field in the GetUsage section of `docs/remote-api.md`. Check spec §20.5 mentions of the cost view and update only if it enumerates the flags.

Verify with `go build ./... && go test ./...` (note in the report any pre-existing flakes: internal/session TestReconcileWorkstreams, internal/setup TestConfigPath, internal/tools TestBackgroundBashWaitReturnsExitAndOutput).

### Starting points
- internal/usage/usage.go: Options{GroupBy,Since,Until} and Aggregate() where since/until are compared as YYYY-MM-DD strings
- internal/server/server.go:817 GetUsage builds usage.Options and calls s.mgr.UsageReport(req.Msg.Project, opts)
- cmd/ycc/main.go:436 costCommand()
- proto/ycc/v1/ycc.proto:448 GetUsageRequest
- docs/cli.md:113 '### `ycc cost`' flag table; docs/remote-api.md:576 GetUsage section
- buf regen: buf.gen.yaml (local Go plugins), buf.gen.swift.yaml (remote BSR plugins, needs network)

## Work log
- 2026-08-06 plan: Add a server-side single-task filter to the usage breakdown, exposed as `ycc cost --task <id>` and a `task` field on GetUsageRequest.  1. Proto (`proto/ycc/v1/ycc.proto`): add `string task = 5;` to `G
…[truncated]
- 2026-08-06 context hints: 6 recorded with plan
- 2026-08-06 context hints: internal/usage/usage.go: Options{GroupBy,Since,Until}, Aggregate() filters since/until as YYYY-MM-DD strings before grouping; internal/server/server.go:817 GetUsage builds usage.Options then s.mgr.Usa
…[truncated]
- 2026-08-06 implementer report: Implemented Task 0173’s single-task usage filter end to end.  Changes: - Added optional `task = 5` to `GetUsageRequest`; regenerated Go and Swift protobuf outputs successfully. - Added `usage.Option
…[truncated]
- 2026-08-06 review tier: simple (coordinator self-review)
- 2026-08-06 decision: accept — commit: cost: --task filter for a single-task usage breakdown (GetUsageRequest.task)
- 2026-08-06 usage: 1,147,088 tok (in 199,267, out 12,141, cache_r 1,419,358, cache_w 15,128) · cost n/a (unpriced)
  implementer: 1,142,017 tok (in 199,247, out 7,090, cache_r 935,680, cache_w 0) · cost n/a (unpriced)
  coordinator: 5,071 tok (in 20, out 5,051, cache_r 483,678, cache_w 15,128) · cost n/a (unpriced)
