---
id: "0470"
title: Load AGENTS.md (CLAUDE.md fallback) project instructions into agent prompts
status: done
priority: 2
created: "2026-10-21"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - Project documents
---

## Description

Repos increasingly carry an `AGENTS.md` (or `CLAUDE.md`) with build/test commands and conventions. ycc currently ignores them; agent prompts only point at CONTRIBUTING.md (`internal/orchestrator/modes.go` `assemble`/`memorySection`). The 2026-10 harness survey found this convention in nearly every competitor (Claude Code, Codex, Gemini, Copilot, Cursor, Jules, Factory, Amp, OpenCode, Crush).

## Acceptance criteria

- Every agent prompt that currently receives project memory also receives the workspace-root `AGENTS.md`, falling back to `CLAUDE.md` when no `AGENTS.md` exists, resolved against the session's live worktree (workstreams included).
- It is framed as committed project instructions: subordinate to system/mode policy and the user, but distinct from advisory memory. It does not grant authorization (for example, it cannot bypass confirmation gates).
- Injection is bounded by a byte cap with a visible truncation marker. A missing, empty, or unreadable file adds nothing and is never an error.
- Symlinks escaping the workspace and non-regular files are ignored.
- The spec (§6 project documents) records the convention and precedence. `ycc doctor` mentions which instruction file was picked up when one exists.
- Out of scope: nested per-directory files, `~/.config` user-global instructions, and `.cursor`/`.claude/rules` formats. Capture these as proposed follow-ups only if wanted.

## Outcome

Agent prompts (coordinators, implementers, reviewers, generic agents, all modes) include the session worktree's AGENTS.md or, when it yields nothing, CLAUDE.md, via docs.LoadProjectInstructions and orchestrator.instructionsSection, placed before memory with a non-authorizing framing. 32 KiB cap with truncation marker; escaping symlinks/non-regular/empty/unreadable files ignored. ycc doctor reports the picked file; spec §6.5 documents it. Tests cover precedence, fallback, symlink/dir handling, truncation, and prompt placement; go test ./... passes; sol review accepted.

Commit: Load AGENTS.md (CLAUDE.md fallback) project instructions into agent prompts
