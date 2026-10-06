---
id: "0428"
title: 'Pre-auth hardening: HTTP-level bearer check, read limits, native h2c, server timeouts'
status: done
priority: 1
created: "2026-10-06"
updated: "2026-10-06"
depends_on: []
spec_refs:
    - docs/remote-api.md#Connection & auth
---

## Description

From security audit 0427 (docs/reports/security-audit-2026-10.md F1, F10). Confirmed live via ngrok: connect-go reads and decompresses unary bodies before interceptors run auth, and no ReadMaxBytes is set, so an unauthenticated ~MB gzip body can OOM the daemon. x/net/h2c ReadAll's Upgrade bodies before routing.

## Acceptance criteria

- Bearer check runs in net/http middleware wrapping the Connect handler (and /debug/latency) before any body read; it returns a Connect-shaped 401 JSON. The interceptor stays as a second layer.
- `connect.WithReadMaxBytes` (~32 MiB, which must exceed 4×5 MiB images base64'd in JSON) on the handler.
- Replace x/net/h2c with net/http native unencrypted HTTP/2 (http.Server.Protocols); set HTTP2 MaxConcurrentStreams/idle timeout. Clients that use prior-knowledge h2c keep working.
- http.Server: ReadHeaderTimeout, IdleTimeout, MaxHeaderBytes (no WriteTimeout, so Subscribe streams aren't cut).
- Tests: unauthenticated gzip/oversized body gets 401 without the body being read; an oversized authenticated body gets resource_exhausted; the h2c Upgrade path no longer buffers.

## Outcome

Bearer token now checked in net/http middleware (server.RequireBearer, protocol-appropriate 401 via connect ErrorWriter) before any body read for Connect RPCs and /debug/latency; interceptor kept as second layer. connect.WithReadMaxBytes(32 MiB) bounds raw and decompressed messages (resource_exhausted). x/net/h2c replaced by native http.Server.Protocols (HTTP/1 + prior-knowledge h2c + TLS h2) with MaxConcurrentStreams 250, ReadHeaderTimeout 10s, IdleTimeout 3m, MaxHeaderBytes 64 KiB, no read/write timeouts. Tests cover unread unauthenticated gzip/oversized/Upgrade bodies, read limits, TLS h2, and Connect/gRPC/gRPC-Web auth errors; docs/remote-api.md updated.

Commit: daemon: pre-auth bearer middleware, 32 MiB read limit, native h2c, server timeouts
