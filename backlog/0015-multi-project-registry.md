---
id: "0015"
title: Multi-project daemon — project registry, RPCs, and TUI picker
status: todo
priority: 2
created: 2026-06-26
updated: 2026-06-26
depends_on: ["0014", "0006"]
spec_refs: ["Core concepts", "RPC protocol (Connect)", "Persistence & remote sync"]
---

## Description
A persistent daemon should manage multiple projects (spec §2, §3.1). A project is a named
workspace. The daemon holds a registry (name → path) persisted in its state dir
(`~/.local/state/ycc/projects.json`), separate from each project's per-workspace `.ycc/`.
Today `session.Manager` has only a single `defaultWorkspace` and sessions carry a
free-form workspace string; there is no first-class, listable project.

Decision (confirmed with user): **registry + auto-add** — explicit registration AND
auto-register a new workspace when a session starts there.

Scope:
- Project registry: load/persist `projects.json`; register via `ycc project add <path>`
  + `AddProject` RPC; auto-register a new workspace on session start.
- RPCs: `ListProjects`, `AddProject` (+ likely `RemoveProject`). `StartSession` gains an
  optional `project` (name) resolving to a workspace; `ListSessions` filterable by project.
- TUI: a project-picker screen shown when attached to a persistent/remote daemon; pick a
  project, then the existing home menu / session view scoped to it. One-shot skips the
  picker (single implicit project = cwd).

## Acceptance criteria
- [ ] daemon persists a project registry across restarts in its state dir
- [ ] `ycc project add <path> [--name N]` and `AddProject` register a project; starting a
      session in an unknown workspace auto-registers it
- [ ] `ListProjects` returns name + path; `StartSession` accepts a project name; sessions
      and logs still live under each project's own `<workspace>/.ycc/`
- [ ] TUI shows a project picker when attached to a daemon and scopes the session UI to the
      chosen project; one-shot goes straight to the project
- [ ] proto regenerated (`buf generate`); spec §12 reflects the new RPCs/messages
- [ ] go test ./... green

## Work log
