---
id: "0470"
title: Load AGENTS.md (CLAUDE.md fallback) project instructions into agent prompts
status: todo
priority: 2
created: "2026-10-21"
updated: "2026-10-21"
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

## Work log
