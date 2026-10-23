---
id: "0479"
title: Bump Vite 8.3.2 → 8.3.5 (dev-server CVEs) and rebuild internal/web/dist
status: todo
priority: 3
created: "2026-10-23"
updated: "2026-10-23"
depends_on: []
spec_refs: []
---

## Description
npm audit (2026-10-23) flags vite 8.3.2 high: GHSA-rq7h-c2jc-7f22 (files outside server.fs.allow served), GHSA-9jrq-w75r-8gcw (cross-origin script exec via middleware), GHSA-vfpm-58rq-9qcg, plus launch-editor advisories. Dev server only (`npx vite`), but on this multi-user host other local users can reach a running dev server. The production bundle is unaffected.

Deferred from the CVE sweep because package.json/package-lock.json are web bundle inputs: the bump requires rebuilding and committing internal/web/dist, which had another agent's uncommitted changes at the time.

Acceptance: clients/web/package.json pins vite 8.3.5 (or later patched), lockfile updated, `npm audit` clean of vite advisories, web tests pass, dist rebuilt with Node from .nvmrc and `go test ./internal/web` passes.

## Acceptance criteria

## Work log
