---
id: "0389"
title: Keep credential entry out of ordinary session prompts and question answers
status: done
priority: 1
created: "2026-09-17"
updated: "2026-09-17"
depends_on: []
spec_refs:
    - 5.1 Storage and durability
    - 11. Questions, unattended work, and confirmation
    - 13. Models, credentials, and review tiers
---

## Description

Vals retrospective found credentials supplied in ordinary prompts/ask_user answers and later retrieved from retained session logs. No values are reproduced here. Current owner-only logs protect access but do not prevent transcripts/replay/exports from becoming an accidental secrets store. Design and implement an explicit secret-entry/reference workflow. Promoted to accepted work during user-authorized backlog triage; this does not authorize rewriting existing logs or rotating third-party credentials.

Acceptance criteria:
- Provide a dedicated secret input/store path where ordinary durable events and model history receive an opaque reference or confirmation, not the value; agents should request the credential name/reference rather than invite pasting a value into ask_user.
- Model-visible tools can use authorized named secrets without returning raw values; define narrow scope and audit behavior rather than broadly injecting secrets into every process.
- Document limits: arbitrary tool output can still leak credentials, so avoid promising perfect regex redaction. Protect exports/debug views and warn on obvious accidental disclosure without silently corrupting replay.
- Define safe user-controlled handling of existing exposed logs (retention, removal/redaction tradeoffs, credential rotation guidance); do not mutate append-only history automatically.
- Add tests with fake sentinel credentials ensuring the dedicated entry path does not put values in events, model payloads, UI history or routine diagnostics.

Evidence: docs/reports/vals-harness-retrospective.md; vals s_d1a15d39d944c779 #63, s_84d3c8c0940ee9e6 #2, and retrieval request s_66adfde7ea26f5bb #61. Inspect metadata only unless secret contents are truly necessary.

## Outcome

Added no-echo local token entry, single-use workspace/tool-scoped Exa secret grants with value-free audit records, pre-record credential disclosure guards, and best-effort CLI/export redaction without rewriting history. Documented limits and user-controlled handling of existing exposures. Sentinel tests cover entry, authorization, history/display protection, and reflected credentials across truncation boundaries. Full Go tests, focused race tests, vet, and fresh touched-package tests passed; both security reviewers accepted. Existing logs and unrelated dirty-tree changes were preserved.

Commit: Add private credential entry and scoped named-secret tool authorization
