---
id: "0017"
title: Smarter language inference for tool-call syntax highlighting
status: todo
priority: 3
created: "2026-06-26"
updated: "2026-06-26"
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
