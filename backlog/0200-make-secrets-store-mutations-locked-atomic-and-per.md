---
id: "0200"
title: Make secrets-store mutations locked, atomic, and permission-safe
status: todo
priority: 1
created: "2026-07-15"
updated: "2026-07-15"
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

## Work log
