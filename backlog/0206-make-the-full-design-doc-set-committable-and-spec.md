---
id: "0206"
title: Make the full design-doc set committable and spec-checkable
status: todo
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - Document model#Design docs — entry point + docs set
    - Document model#Spec doctor — drift & coverage checking
---

## Description
This repository has a multi-file design-doc tree, but `ycc spec-check` currently scans only `spec.md` because no docs-set configuration is committed. The configured location is `.ycc/config.toml`, while `.gitignore` ignores the entire `.ycc/` directory. A successful deterministic check therefore gives incomplete coverage.

Provide a clean committed project configuration (or revise the configuration convention) that includes the intended normative docs without accidentally treating advisory `memory.md`, generated files, or backlog items as spec.

## Acceptance criteria
- [ ] The repository can commit its docs-set configuration despite `.ycc/` runtime-state ignores, or the config is moved to a clearly committable documented location with compatibility handling.
- [ ] The configured docs set includes the intended normative files under `docs/` and any other accepted design-doc paths.
- [ ] `memory.md`, `backlog/`, session state, and non-normative/generated content remain excluded as designed.
- [ ] `ycc spec-check` reports scanning more than the single entry-point document in this repository.
- [ ] Tests cover ignore/config discovery behavior and multi-document deterministic checks.
- [ ] README/spec instructions explain how projects commit docs-set configuration.
- [ ] `go test ./...` and `ycc spec-check` pass.

## Work log
