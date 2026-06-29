---
id: "0017"
title: Smarter language inference for tool-call syntax highlighting
status: done
priority: 3
created: "2026-06-26"
updated: "2026-06-29"
depends_on:
    - "0006"
spec_refs:
    - Client UI (TUI)
---

## Description
The TUI already syntax-highlights markdown (glamour/chroma), colorizes diffs, dims cat -n
line numbers, and pretty-prints JSON tool args. Extend highlighting to tool-call *content*
by inferring the language, best-effort:

- **Read results**: infer the language from the `file_path` argument's extension and run
  the matching chroma lexer over the file body — preserving the dimmed `cat -n` line-number
  gutter (highlight only the content after it).
- **grep / ripgrep (Bash) results**: highlight match lines when the language is unambiguous
  — e.g. the command restricts to a single-extension glob (`rg -g '*.go'`, `rg ... --type
  go`), or every `path:line:` result prefix shares one extension. When ambiguous, render
  plain. Keep the `path:line:` prefixes readable (dimmed), highlight only the match text.

Principle: only highlight when confidently inferable; never mangle or drop output. The
inference must be a pure, testable mapping (path/command → chroma lexer name), in keeping
with the existing pure-helper TUI tests (eventAt, diff/catn/json).

## Acceptance criteria
- [ ] Read tool results are highlighted by `file_path` extension (chroma lexer), with the
      line-number gutter preserved/dimmed
- [ ] ripgrep/grep results are highlighted when the language is unambiguous (single-extension
      glob/type flag, or uniform result paths); otherwise rendered plain
- [ ] language inference is a centralized pure helper with unit tests (incl. the ambiguous →
      no-highlight cases)
- [ ] no regression to existing markdown / diff / JSON-args highlighting
- [ ] graceful fallback for unknown extensions and binary/oversized content

## Work log
- 2026-06-29 plan: Add chroma-based syntax highlighting for tool-call result content in the TUI, gated on confident language inference.  1. Engine (internal/engine/loop.go): add `"id": call.ID` to the ToolResult event e
…[truncated]
- 2026-06-29 implementer report: Implemented smarter language inference for tool-call syntax highlighting in the TUI (task 0017).  ## Changes  **internal/engine/loop.go** — Added `"id": call.ID` to the `ToolResult` event payload so
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change satisfies the task's acceptance criteria. Language inference is centralized in a new pure-helper file (internal/tui/highlight.go) with good unit-test coverage including ambiguous cases (mix
…[truncated]
- 2026-06-29 revision: Addressed the reviewer's defect: `dataField` couldn't surface JSON booleans, so the `tool_result` error-routing check `dataField(ev, "error") == "true"` was dead code (the engine emits `"error": res.I
…[truncated]
- 2026-06-29 review (Claude): accept — The revision resolves the only prior finding: dataField now converts JSON booleans to \"true\"/\"false\", so the tool_result error-routing branch (dataField(ev,\"error\")==\"true\") works as intended 
…[truncated]
- 2026-06-29 decision: accept — commit 8b0f11e: TUI: infer language for tool-call result syntax highlighting (task 0017)  Highlight Read results by file_path extension (preserving the dimmed cat -n gutter) and grep/ripgrep results when the language
…[truncated]
