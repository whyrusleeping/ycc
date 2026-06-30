---
id: "0087"
title: Add web search + fetch-page agent tools (via Exa)
status: done
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
Implement two new agent tools so the agent can look up documentation and read web content as needed:

- **Web search** — query the web and return ranked results (title, URL, snippet).
- **Fetch page** — retrieve the contents of a given URL as readable text/markdown for the agent to consume.

Use [Exa](https://exa.ai) as the backend provider (consistent with prior projects; Exa supports both search and content/contents retrieval). Wire the API key through the existing config/secrets mechanism rather than hardcoding it, and register both tools alongside the existing agent tools so they're available to the agent.

## Acceptance criteria
- [ ] A `web_search` tool is implemented and registered, returning structured results (title, URL, snippet) for a query.
- [ ] A `fetch_page` (page-read) tool is implemented and registered, returning readable content for a given URL.
- [ ] Both tools call Exa's API; the Exa API key is supplied via config/env (not hardcoded) and the tools degrade gracefully (clear error) when unset.
- [ ] Reasonable bounds on output size (truncation/limits) so results don't blow up the context window.
- [ ] Errors (network, rate-limit, bad URL) are surfaced as tool errors rather than crashing the agent.
- [ ] Tools follow the existing tool definition/registration conventions in the codebase.

## Acceptance criteria

## Work log
- 2026-06-30 plan: Add two new agent tools backed by Exa: `web_search` and `fetch_page`.  1. New file `internal/tools/web.go`:    - `exaAPIKey()` resolves the key following the project's existing precedence (mirror conf
…[truncated]
- 2026-06-30 implementer report: Added two Exa-backed agent tools, `web_search` and `fetch_page`, following the existing tool conventions in `internal/tools`.  ## Changes - **`internal/tools/web.go`** (new):   - `exaAPIKey()` resolve
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change adds two Exa-backed agent tools (web_search, fetch_page) in a new internal/tools/web.go, registered by appending Web() into Editing()/Worker() so the agent receives them. The Exa API key is
…[truncated]
- 2026-06-30 decision: accept — commit: tools: add Exa-backed web_search + fetch_page agent tools (task 0087)
- 2026-06-30 usage: 18,545 tok (in 58, out 18,487, cache_r 888,184, cache_w 67,156) · cost n/a (unpriced)
