---
id: "0087"
title: Add web search + fetch-page agent tools (via Exa)
status: todo
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
