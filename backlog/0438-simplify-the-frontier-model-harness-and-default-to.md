---
id: "0438"
title: Simplify the frontier-model harness and default to direct implementation
status: done
priority: 2
created: "2026-10-09"
updated: "2026-10-09"
depends_on: []
spec_refs: []
---

## Description
Make direct implementation the default while preserving independent other-model review and explicit delegated implementation. Cut permanent prompt overhead and remove obsolete argument reconstruction and synthetic truncation recovery.

## Acceptance criteria
- Unset work strategy resolves to direct; explicit delegate and independent review remain available.
- Permanent guidance is materially smaller while scope, authority, evidence, and async-job rules remain intact.
- XML parameter leaks return actionable errors without execution or argument rewriting; literal markup remains usable.
- No-tool truncation records usage and fails clearly without poisoning live/reopened history; legacy logs remain readable.
- Relevant behavioral tests pass and independent review is addressed.

## Outcome
Direct is the unset default in config, mode building, and web fallbacks. Sessions persist their strategy across reopen; legacy implementer calls retain delegation. Reviewer tier/model selection is unchanged. Shared concise prompts reduce the direct work role by 61% and delegated coordinator by 69%; tool guidance is shorter. XML reconstruction is replaced by a 38-line rejection check. Output truncation without tools stops immediately on clean pending history; no nudges/retries or implementer-specific cap floor remain.

Verification: initial full Go suite passed with TestRepositoryDocsConfig excluded. The archive-only check skipped that repository-dependent test; final Git-backed verification confirmed a pre-existing stale expected docs list, corrected before commit. Targeted race checks and go vet passed. Web bundle rebuilt, 333 web tests passed, protobuf comments regenerated for Go/Swift/web. Independent Opus review found no blocker/major issues; its optional bookkeeping guidance was retained in the reviewer prompt. Changes remain uncommitted alongside unrelated concurrent edits.
