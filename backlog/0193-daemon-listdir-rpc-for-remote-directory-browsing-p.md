---
id: "0193"
title: 'Daemon: ListDir RPC for remote directory browsing (project add)'
status: done
priority: 3
created: "2026-07-10"
updated: "2026-07-10"
depends_on: []
spec_refs: []
---

## Description
Add a `ListDir` RPC so remote clients (iOS) can browse the daemon host's filesystem to pick a workspace to register via the existing `AddProject` RPC.

Shape: `ListDirRequest{path}` → `ListDirResponse{path, parent, entries: [{name, is_git_repo, is_registered}]}`.

- Empty `path` ⇒ daemon user's home directory; response echoes the resolved absolute path and its parent so the client can navigate up.
- Entries are **directories only** — never files, never file contents. Hidden dirs skipped.
- `is_git_repo`: dir contains `.git`; `is_registered`: path matches an already-registered project.
- Optionally a `suggestions` mode/field: git repos found in the parent dirs of already-registered projects (siblings), to power a one-tap "likely projects" list in the client.
- Security note for docs/remote-api.md: bearer token already permits `StartSession` in an arbitrary workspace path, so directory-name listing does not expand the trust surface.

Acceptance:
- proto + Go regen, server handler with tests (empty path, nonexistent path → NotFound/InvalidArgument, annotations correct).
- docs/remote-api.md documents the RPC.
- Update docs/design/ios-client.md §7 note that `AddProject` is now in client scope.

## Acceptance criteria

## Work log

- 2026-07-10: Implemented. `ListDir(path, suggest)` → resolved `path`, `parent`, sorted dir-only `entries` (hidden skipped, symlinks followed) with `is_git_repo` (.git dir OR file, so worktrees count) + `is_registered`; empty path ⇒ daemon user's home; relative ⇒ InvalidArgument, missing ⇒ NotFound, file ⇒ InvalidArgument, EACCES ⇒ PermissionDenied. `suggest=true` adds git-repo siblings of registered projects (registered excluded, capped 50). Code: proto/ycc/v1/ycc.proto (+Go & Swift regen), internal/server/listdir.go, tests internal/server/listdir_test.go (all pass). Docs: remote-api.md "AddProject / ListDir" (incl. trust note), ios-client.md §7 scope note.
