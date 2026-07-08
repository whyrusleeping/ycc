# Project memory

> Agent-maintained operational notes. Advisory, not normative — verify before relying.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.

## Codebase gotchas
- 2026-07-06: Usage accounting: OpenAI reports cached tokens as a SUBSET of prompt_tokens while Anthropic reports cache reads/writes disjoint from input_tokens; engine/loop.go normalizes to disjoint classes at emit time and built-in default pricing lives in internal/config/default_pricing.go (config price_* always overrides).
- 2026-07-07: File-access policy (since write_roots change): Read tool is unrestricted (any path); Write/Edit are confined to the workspace plus config write_roots (tools.Workspace.WriteRoots, symlink-aware); the old read_roots config/machinery was removed and the key is silently ignored.

## Environment & tooling
- 2026-07-07: Tool-failure forensics: agent session transcripts live in <workspace>/.ycc/sessions/*/events.jsonl (tool_call args + tool_result pairs keyed by id) and can be replayed to diagnose tool UX issues; Edit not-found diagnostics live in internal/tools/editdiag.go.

## User preferences
- 2026-07-08: Home-menu action affordances must be ctrl-chords, never naked letter keys (user rule; w/s/c became ctrl+w/ctrl+s/ctrl+l in 2026-07); keep new menu shortcuts consistent with this.
- 2026-07-08: iOS client decisions (2026-07): app lives in-repo at clients/ios (XcodeGen + YccKit SPM package, iPhone-only iOS 17+, connect-swift with committed generated code), notifications stay ntfy + ycc:// deep links (no APNs), and the work loop moves daemon-side (task 0179) rather than being client-driven.
