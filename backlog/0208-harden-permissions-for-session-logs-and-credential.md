---
id: "0208"
title: Harden permissions for session logs and credential-bearing config
status: todo
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - Session & event log#Storage
    - Backends & model registry
    - Persistence & remote sync
---

## Description
Session directories/logs currently default to `0755`/`0644` even though transcripts can contain prompts, source excerpts, tool output, and sensitive material. `config.Save` writes `0644` even though `Config.Notify.Auth` may contain an authorization credential. The ignored root `ant-key` also illustrates how easy it is for local plaintext credentials to remain broadly readable, though code must not inspect or migrate arbitrary user files without consent.

Define and enforce a private-by-default permissions policy for ycc-owned sensitive state, and move credential-bearing config values toward environment/secrets-store references where practical.

## Acceptance criteria
- [ ] Newly created `.ycc` session-state directories and `events.jsonl` files use owner-only permissions by default on Unix.
- [ ] Existing log files opened by ycc have overly broad permissions repaired where safe and documented.
- [ ] User-global config containing `notify.auth` is not left world-readable; either config files are private or notify credentials are referenced through the secrets store/environment.
- [ ] Project-local committed config behavior is addressed explicitly so tightening permissions does not pretend a committed bearer secret is safe.
- [ ] Daemon logs and other ycc-owned files are audited and assigned an intentional sensitivity/permission policy.
- [ ] Tests assert Unix modes and skip appropriately on platforms without Unix permission semantics.
- [ ] Documentation tells users to migrate loose plaintext key files into `ycc token` and never prints/reads such values during migration guidance.
- [ ] `go test ./...` passes.

## Work log
