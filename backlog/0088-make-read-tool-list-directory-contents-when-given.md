---
id: "0088"
title: Make Read tool list directory contents when given a directory path
status: todo
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
Agents frequently call the `Read` tool on a directory path. Today this returns an error like `read <path>: is a directory`, which wastes a turn and confuses the model. Claude Code's Read tool handles this gracefully by listing the directory's contents instead. We should mirror that behavior.

## Behavior
- When the `Read` tool is invoked with a path that is a directory, return a listing of that directory's entries instead of an error.
- Distinguish files from subdirectories in the output (e.g. a trailing `/` on dirs) so the agent can navigate.
- Keep the existing file-reading behavior unchanged for regular files.
- Apply a sane cap on the number of entries returned (consistent with the existing line-limit approach) and indicate when the listing is truncated.

## Acceptance criteria
- Calling `Read` on a directory returns a readable list of that directory's immediate entries rather than an error.
- Subdirectories are visually distinguishable from files in the output.
- Reading a regular file behaves exactly as before.
- Large directories are truncated with a clear indication that more entries exist.
- Tests cover the directory-listing path (including truncation) and confirm file reads are unaffected.

## Acceptance criteria

## Work log
