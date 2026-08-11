# Protocol regeneration

The ordinary repository test command is documented in `README.md`. This runbook covers the less
frequent protocol regeneration step whose two committed client outputs must stay in sync.

## Steps

After changing `proto/ycc/v1/ycc.proto`, regenerate both targets:

```
buf generate
buf generate --template buf.gen.swift.yaml
```

The Go plugins are resolved from `PATH`. Swift generation uses remote Buf Schema Registry plugins
and requires network access. Review and commit both the Go output under `proto/ycc/v1/` and Swift
output under `clients/ios/YccKit/Sources/YccProto/`, then run the normal Go and iOS build/tests.

## Pass condition

Both generation commands succeed, neither client has an unexplained generated diff, and a second
regeneration is clean.
