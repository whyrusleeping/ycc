---
id: "0209"
title: Add CI gates for Go, Swift, protobuf drift, race, and spec checks
status: todo
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on:
    - "0205"
    - "0206"
spec_refs:
    - Build plan / milestones
    - Client UI (TUI)#Snapshot rendering for debugging (dev/test aid)
---

## Description
The repository has extensive Go tests, a real PTY E2E suite, a Swift package/app, and committed generated protobuf clients, but no checked-in CI workflows. Cross-platform failures, generated-code drift, races, and incomplete docs checks can therefore reach master unnoticed.

Add practical CI workflows with deterministic tool versions/caches. This depends on fixing the known E2E race and making the full docs set spec-checkable so the new gates begin green rather than institutionalizing exceptions.

## Acceptance criteria
- [ ] Go CI runs formatting/check policy, `go vet ./...`, uncached `go test ./...`, and `go test -race ./...` in a PTY-capable Linux environment.
- [ ] The E2E TUI tests execute rather than silently skipping in the primary Linux job.
- [ ] Swift CI runs `swift test` for `clients/ios/YccKit`; an appropriate macOS job also validates the generated Xcode project/app build when feasible.
- [ ] Protobuf generation is reproducible in CI and fails on a dirty diff across Go and committed Swift generated files.
- [ ] `ycc spec-check` runs against the complete configured docs set.
- [ ] Dependency vulnerability scanning is included with a pinned tool/version or an explicitly documented alternative.
- [ ] Workflows never require real provider credentials and do not expose secrets to pull requests.
- [ ] README contributor instructions list the local equivalents of every required gate.

## Work log
