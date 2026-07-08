---
id: "0178"
title: 'iOS: buf Swift codegen — connect-swift client for proto/ycc/v1 (committed)'
status: done
priority: 2
created: "2026-07-08"
updated: "2026-07-08"
depends_on: []
spec_refs:
    - 12. RPC protocol (Connect)
    - docs/design/ios-client.md#4. Generated Swift client (decision)
---

## Description
Set up Swift code generation for the iOS client per `docs/design/ios-client.md` §4.

## Description
- Add `buf.gen.swift.yaml` at the repo root using the remote `connect-swift` and `swift-protobuf` plugins, generating from `proto/ycc/v1/ycc.proto` into `clients/ios/YccKit/Sources/YccProto/`.
- Commit the generated Swift (same posture as the generated Go code) so `swift test`/`xcodebuild` need no buf step.
- Document the regen command (`buf generate --template buf.gen.swift.yaml`) — add a short note wherever proto regen is currently documented (e.g. plans/build-and-test.md or a README near the proto).

## Acceptance criteria
- `buf generate --template buf.gen.swift.yaml` succeeds and its output is committed under `clients/ios/YccKit/Sources/YccProto/`.
- Generated code includes the `SessionService` client interface (all RPCs incl. the `Subscribe` server stream) and message types.
- `go build ./...` is unaffected (nothing under `clients/` touched by the Go module).
- Regen instructions documented.

## Plan

Set up committed Swift codegen for the Connect client per docs/design/ios-client.md §4.

1. Create `buf.gen.swift.yaml` at the repo root (v2 template) with two REMOTE plugins, both generating into `clients/ios/YccKit/Sources/YccProto/`:
   - `buf.build/apple/swift` (swift-protobuf) with `Visibility=Public`
   - `buf.build/connectrpc/swift` with `Visibility=Public` and `GenerateAsyncMethods=true` (async/await interface; callback methods off)
   Consider `clean: true` so regen replaces stale output.
2. Run `buf generate --template buf.gen.swift.yaml` (buf 1.71.0 is installed at ~/go/bin/buf, on PATH; remote plugin execution verified working). Output lands as `clients/ios/YccKit/Sources/YccProto/ycc/v1/ycc.pb.swift` + `ycc.connect.swift`; commit the generated files.
3. Verify generated code: `ycc.connect.swift` contains the SessionService client protocol/implementation with ALL RPCs including the `Subscribe` server stream (serverStream method), and `ycc.pb.swift` has the message types.
4. Document regen: add a short "Proto regeneration" note to `plans/build-and-test.md` (Go: `buf generate`; Swift: `buf generate --template buf.gen.swift.yaml`; both outputs committed).
5. Verify Go module unaffected: `go build ./...`, `go vet ./...`, `go test ./...` all pass and nothing under `clients/` is touched by Go tooling.

### Starting points
- buf.yaml (repo root) — v2 module at path proto; buf.gen.yaml is the existing Go template to mirror in style
- proto/ycc/v1/ycc.proto — the single proto file; SessionService with Subscribe returning `stream Event`
- docs/design/ios-client.md §4 — the accepted decision this implements
- Verified working: buf generate with remote plugins buf.build/apple/swift and buf.build/connectrpc/swift produces ycc/v1/{ycc.pb.swift,ycc.connect.swift}
- plans/build-and-test.md — place to document the regen commands

## Work log
- 2026-07-08 plan: Set up committed Swift codegen for the Connect client per docs/design/ios-client.md §4.  1. Create `buf.gen.swift.yaml` at the repo root (v2 template) with two REMOTE plugins, both generating into `c
…[truncated]
- 2026-07-08 context hints: 5 recorded with plan
- 2026-07-08 context hints: buf.yaml (repo root): v2, module path proto; buf.gen.yaml is the existing Go template to mirror stylistically; proto/ycc/v1/ycc.proto — SessionService; Subscribe returns `stream Event`; docs/design/
…[truncated]
- 2026-07-08 implementer report: Implemented Task 0178 — committed connect-swift codegen for proto/ycc/v1.  Changes: - Added `buf.gen.swift.yaml` at repo root (v2 template, `clean: true`) with two remote plugins, both emitting into
…[truncated]
- 2026-07-08 review tier: simple (coordinator self-review)
- 2026-07-08 decision: accept — commit: iOS codegen: committed connect-swift client for proto/ycc/v1 (task 0178) + iOS client design docs & backlog
