---
id: "0200"
title: Make secrets-store mutations locked, atomic, and permission-safe
status: done
priority: 1
created: "2026-07-15"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - Backends & model registry#Credential mechanisms
---

## Description
`internal/secrets` performs token updates as an unlocked load/modify/rewrite of the whole JSON file. Concurrent provider refreshes or separate ycc processes can each load stale state and overwrite the other's credential update. Direct `os.WriteFile` also does not repair an existing file with overly broad permissions, and a crash can leave a partial file.

Add shared process-local and cross-process serialization plus atomic durable replacement while preserving the existing secrets-store API.

## Acceptance criteria
- [ ] `Set` and `Remove` serialize the complete read-modify-write transaction across goroutines and ycc processes.
- [ ] Saving uses a same-directory temporary file, restrictive permissions, sync as appropriate, and atomic rename.
- [ ] The secrets directory is mode `0700` and the final secrets file is mode `0600`, including when an existing file previously had broader permissions.
- [ ] Concurrent updates to different keys cannot lose either update.
- [ ] A failed/interrupted write leaves the previous valid store readable.
- [ ] Tests cover concurrent goroutine updates, a cross-process or lock-helper scenario, permission repair, and write-failure preservation.
- [ ] OAuth refresh and `ycc token set/rm` continue to use the same store successfully.
- [ ] `go test ./...` and `go test -race ./internal/secrets/...` pass.

## Plan

Rework internal/secrets so mutations are serialized (in-process and cross-process), atomic on disk, and permission-repairing, while keeping the existing exported API (Path, Load, Save, Lookup, Set, Remove, Keys).

1. Locking:
   - Package-level sync.Mutex for goroutine-level serialization of mutations.
   - Cross-process lock: flock(2) on a sibling lock file (e.g. `secrets.lock`, mode 0600) in the secrets dir, using golang.org/x/sys/unix Flock with LOCK_EX. Put the flock code in a small internal helper (e.g. lock.go with `//go:build unix` and a fallback lock_other.go that degrades to just the process-local mutex for non-unix builds, since golang.org/x/sys/windows isn't wired up and the project targets unix).
   - Set and Remove: acquire mutex → acquire flock → Load → mutate → save → release. The whole read-modify-write happens under the lock.

2. Atomic, durable, permission-safe save:
   - MkdirAll(dir, 0700) then os.Chmod(dir, 0700) to repair an existing overly-broad dir.
   - Write JSON to a same-directory temp file created with os.CreateTemp, chmod 0600 before writing data (or open with O_EXCL 0600), fsync the file, close, then os.Rename over secrets.json; best-effort fsync of the directory. Remove the temp file on any error path.
   - After rename (or when the target pre-exists), os.Chmod(final, 0600) to repair broad perms on an existing file.
   - Keep Store.Save exported and route it through the same atomic writer (Save itself need not take the cross-process lock to preserve API behavior, but Set/Remove must; if simple, have Save also take the locks — check existing callers: only secrets internal + anthropicauth/openaiauth use Set, nobody calls Save directly outside the package per rg, so locking inside Save is safe and simplest).

3. Failure behavior: a failed/interrupted write must leave the previous secrets.json intact — guaranteed by temp+rename.

4. Tests (internal/secrets/secrets_test.go additions):
   - Concurrent goroutine updates: N goroutines each Set a distinct key; verify all N present afterward (run under -race).
   - Cross-process/lock-helper: exercise the flock helper directly — two independent opens of the lock file contend (flock is per open-file-description, so two locks in one process do conflict); verify the second acquire blocks until the first releases, or use a re-exec subprocess helper if cleaner.
   - Permission repair: pre-create secrets.json with 0644 and dir 0755, call Set, verify file is 0600 and dir 0700.
   - Write-failure preservation: force the atomic write to fail (e.g. make the secrets dir read-only so temp creation fails), verify Set returns an error and the previous file still loads with prior contents.
   - Keep existing tests passing.

5. Verify: `go test ./internal/secrets/... -race` and `go test ./...` (note known flaky tests in internal/session, internal/setup, internal/tools background-bash — compare against HEAD before blaming).

### Starting points
- internal/secrets/secrets.go — whole store is ~116 lines; keep exported API intact
- internal/secrets/secrets_test.go — setupDir(t) pattern sets XDG_CONFIG_HOME+HOME to a TempDir
- callers of secrets.Set: internal/anthropicauth/anthropicauth.go:242, internal/openaiauth/openaiauth.go:444, internal/setup/setup.go:176, cmd/ycc/main.go:504
- golang.org/x/sys is already a direct dependency (use unix.Flock)
- flock locks attach to the open file description — two separate os.OpenFile handles in ONE process do contend, so the lock helper is testable in-process

## Work log
- 2026-08-07 plan: Rework internal/secrets so mutations are serialized (in-process and cross-process), atomic on disk, and permission-repairing, while keeping the existing exported API (Path, Load, Save, Lookup, Set, Re
…[truncated]
- 2026-08-07 context hints: 5 recorded with plan
- 2026-08-07 context hints: internal/secrets/secrets.go (whole store, ~116 lines); internal/secrets/secrets_test.go — setupDir(t) sets XDG_CONFIG_HOME+HOME to a TempDir; external callers only use Lookup/Set/Remove/Keys/Path: a
…[truncated]
- 2026-08-07 preload: 2 file(s), ~7 KiB seeded into implementer context
- 2026-08-07 implementer report: Implemented task 0200 in `internal/secrets`.  Changes: - Added package-level mutation serialization covering complete `Set`/`Remove` read-modify-write transactions. - Added Unix cross-process locking
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): accept — The change satisfies task 0200: Set/Remove serialize the full transaction with process-local and Unix flock locking, writes use a same-directory synced 0600 temp file and atomic rename, directory/file
…[truncated]
- 2026-08-07 decision: accept — commit: Make secrets-store mutations locked, atomic, and permission-safe (task 0200)  Set/Remove/Save now serialize the whole read-modify-write via a package mutex plus a cross-process flock on a sibling secr
…[truncated]
