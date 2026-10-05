# Protocol regeneration

The ordinary repository test command is documented in `README.md`. This runbook covers the less
frequent protocol regeneration step whose three committed client outputs must stay in sync.

## Steps

After changing `proto/ycc/v1/ycc.proto`, regenerate all three targets:

```
buf generate                                   # Go: proto/ycc/v1
buf generate --template buf.gen.swift.yaml     # Swift: clients/ios/YccKit/Sources/YccProto
(cd clients/web && npm ci)                     # once, with Node from clients/web/.nvmrc
buf generate --template buf.gen.web.yaml       # TypeScript: clients/web/src/gen
scripts/web-build.sh                           # rebuild the embedded bundle (internal/web/dist)
```

The Go plugins are resolved from `PATH`. Swift generation uses remote Buf Schema Registry plugins
and requires network access. Web generation runs the local `protoc-gen-es` from
`clients/web/node_modules`, so Node from `clients/web/.nvmrc` must be first on `PATH`. A changed
web client source (including `src/gen`) makes the committed bundle stale; `go test ./internal/web`
fails until `scripts/web-build.sh` has rebuilt `internal/web/dist`. Review and commit the Go output
under `proto/ycc/v1/`, the Swift output under `clients/ios/YccKit/Sources/YccProto/`, the web
output under `clients/web/src/gen/`, and `internal/web/dist/`, then run the normal Go, web, and iOS
build/tests.

## Pass condition

All generation commands succeed, no client has an unexplained generated diff, `go test
./internal/web` passes, and a second regeneration is clean.
