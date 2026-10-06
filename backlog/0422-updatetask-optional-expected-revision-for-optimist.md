---
id: "0422"
title: 'UpdateTask: optional expected_revision for optimistic concurrency'
status: proposed
priority: 4
created: "2026-10-06"
updated: "2026-10-06"
depends_on: []
spec_refs: []
---

## Description
The web task editor (0413) guards concurrent edits client-side: it sends only changed fields and re-reads the task via GetTask right before saving, refusing when an edited field changed on disk. A write that lands between that re-read and the UpdateTask call is still silently overwritten.

Add an optional `expected_revision` (content hash of the task file, returned by GetTask/ListBacklog) to UpdateTaskRequest; the daemon rejects with FailedPrecondition when it no longer matches. Web and iOS editors pass it when known.

## Acceptance criteria
- [ ] UpdateTask with a stale expected_revision fails without writing; with a matching or absent one behaves as today.
- [ ] Web editor passes it and surfaces the conflict through its existing keep-draft flow.

## Work log
