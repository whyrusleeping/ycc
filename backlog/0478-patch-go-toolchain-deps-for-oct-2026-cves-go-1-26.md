---
id: "0478"
title: Patch Go toolchain/deps for Oct 2026 CVEs (Go 1.26.10, x/net v0.60.0)
status: done
priority: 1
created: "2026-10-23"
updated: "2026-10-23"
depends_on: []
spec_refs: []
---

## Description
govulncheck (2026-10-23) found 22 reachable stdlib vulns in the daemon build (go1.26.2), including pre-auth HTTP/2 server DoS (CVE-2026-97032 HPACK race crash, CVE-2026-78659 trailer memory, CVE-2026-78669, CVE-2026-78663) reachable via the daemon's native h2c listener, plus GO-2026-6809 (compromised eMudhra intermediates).

Acceptance: go.mod pins `toolchain go1.26.10`; golang.org/x/net >= v0.60.0, x/crypto >= v0.56.0, buger/jsonparser >= v1.1.2; CI go-version 1.26.10; vet + tests pass; govulncheck reports 0 reachable vulnerabilities.

Outcome: done — govulncheck 0 reachable (remaining: gin via mcp-golang and x/crypto/openpgp, both uncalled).

## Acceptance criteria

## Work log
