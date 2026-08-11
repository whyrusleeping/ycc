---
id: "0315"
title: Compact completed backlog history and support archival lookup
status: done
priority: 4
created: "2026-08-10"
updated: "2026-08-11"
depends_on:
    - "0310"
spec_refs:
    - CONTRIBUTING.md#Documentation and comments
    - Backlog
    - Event log
---

## Description

Reduce the committed backlog's generated operational detail without breaking old task lookup, dependency resolution, usage attribution, or client history. Define a compact completed-task shape and, if completed files move out of the active directory, add explicit archive-aware lookup rather than relying on a filesystem move the current store ignores.

## Acceptance criteria

- New completed tasks retain intent, acceptance criteria, concise outcome, and commit reference without automatically persisting preload chatter, reviewer transcript boilerplate, or other duplicated session detail.
- Detailed execution history has one documented source of truth (session log and/or git) rather than being copied into task bodies.
- A completed-task archive, if introduced, remains addressable by task ID and does not break dependency status, usage reports, RPCs, TUI, or iOS history.
- Existing completed task files are compacted or archived conservatively with a reversible migration and no loss of information the product still depends on.
- Active backlog listing and scans avoid loading historical detail unnecessarily.
- Go tests cover archive lookup and dependency behavior only at the relevant storage/API boundaries.

## Outcome

Completed backlog records now compact to intent, criteria, a validated concise outcome, and commit subject while detailed execution and usage remain in session events and git. Summary, dependency, CLI, RPC, and work-loop paths use frontmatter-only scans with full task lookup and selective blocked-body hydration; a strict dry-run-first migration and six git-verified curated legacy records provide conservative, reversible history reduction without introducing an archive.

Commit: Compact completed backlog history and metadata scans
