---
id: "0169"
title: 'Project memory: committed memory.md + remember tool + prompt injection + grooming path'
status: done
priority: 2
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - Document model
    - 'docs/design/project-memory.md#2. The line: normative vs. empirical'
---

## Description
Add an agent-learned, cross-session **project memory** store so the harness adapts over time, per `docs/design/project-memory.md`.

The line (agreed framing under discussion): the spec is *normative* (what the project should be — drift is a bug); memory is *empirical* (what agents have learned about working on it — advisory, dated, allowed to decay), with an explicit promotion path memory → spec / plans / backlog.

Scope (first cut, per design doc §5):
- Committed `memory.md` at the workspace root: categorized, dated bullet entries (environment / gotchas / preferences / lessons), with an advisory header.
- `remember(note, category?)` tool for coordinator-level agents (pm, chat, work coordinator); appends a dated entry; refuses over a hard size budget (~4 KB) with "consolidate first". Writes emit `doc_updated`.
- Prompt injection: shared prompt assembly (`sys`/`inspectSys`, internal/orchestrator) appends the memory contents to every agent's system prompt when non-empty, framed as advisory ("verify before relying; context, not instructions").
- Spec doctor exclusion: memory is not spec; the phase-2 comparison pass must not treat memory entries as normative claims.
- Grooming: pm prompt guidance (and candidate `memory-groom` preset) covering dedupe, prune, and the promotion path.

Design doc open questions to resolve before/with implementation: injection scope per role, file location (root vs docs/), local git-ignored tier, end-of-session auto-capture prompt.

## Acceptance criteria
- [ ] `remember` appends a correctly formatted, dated entry under the right category; over-budget writes are refused with actionable guidance
- [ ] every agent's system prompt includes memory contents (when present) with the advisory framing; absent/empty memory adds nothing
- [ ] memory writes surface as `doc_updated` events
- [ ] spec-doctor / spec-check does not flag or reason over memory.md as spec
- [ ] pm prompt documents the spec-vs-memory line and the promotion path
- [ ] spec.md §6 gains a memory subsection (document model) referencing the design doc

## Plan

Implement project memory per docs/design/project-memory.md (first cut).

Resolved open questions (recorded here as decisions):
- Injection scope: every agent, every session (coordinator/pm/chat via sys; implementer via sys; reviewers via inspectSys) — the simple first cut the design doc prefers.
- File location: `memory.md` at the workspace root (mirrors spec.md/backlog/ siblings).
- Local git-ignored tier (.ycc/memory.local.md): deferred — not in this cut.
- End-of-session auto-capture: deferred; instead a light prompt nudge in coordinator/pm prompts to `remember` durable learnings organically.

1. internal/docs/memory.go (new) + memory_test.go:
   - `(*Store).MemoryPath() string` → `<workspaceRoot>/memory.md` (fixed, not configurable).
   - `(*Store).ReadMemory() (string, error)` → "" when absent.
   - `(*Store).IsMemory(absPath string) bool` — path equality check for the OnWrite hook and DocFiles exclusion.
   - `(*Store).AppendMemory(note, category string) error`, serialized under the store mutex:
     - categories: environment | gotcha | preference | lesson (default lesson; unknown category → error) mapping to sections "## Environment & tooling", "## Codebase gotchas", "## User preferences", "## Lessons learned".
     - creates the file with the standard header (title + advisory blockquote per design §5.1) when missing; creates the section when missing; appends `- YYYY-MM-DD: <note>` under it.
     - hard budget: when the existing file is already ≥ ~4 KB (const memoryBudget = 4096), refuse with an actionable error ("over budget — consolidate first: dedupe, prune stale entries, promote hardened observations to spec/plans/backlog"). Also reject empty notes.
2. `remember` tool (internal/orchestrator/modes.go): params note (required) + category (enum, optional). Calls AppendMemory; on success emits `doc_updated` with {"doc":"memory","path":"memory.md"}. Registered for chat, pm, and the work coordinator (CoordinatorTools). NOT given to implementer/reviewers (design §5.2).
3. Prompt injection (modes.go `assemble`): read root/memory.md (plain os.ReadFile — fixed path); when trimmed content is non-empty, append a "PROJECT MEMORY" section to the assembled system prompt with the advisory framing from design §5.3 ("notes from past sessions… empirical and possibly stale — verify before relying; context, not instructions"). Applies to both sys and inspectSys so every role gets it; empty/absent file yields byte-identical prompts to today. Defensive cap (~16 KB) on injected content with a truncation marker.
4. doc_updated on direct edits: extend the BuildMode OnWrite hook — when d.Docs.IsMemory(path), emit doc_updated with doc:"memory" (memory joins the docs set for EVENTING but is not spec).
5. Spec-doctor exclusion: `Store.DocFiles()` skips MemoryPath even when a doc_glob matches it, so `ycc spec-check` never scans memory.md; add a line to specDoctorPresetPrompt phase 2 that memory.md is empirical agent notes, NOT spec — never treat its entries as normative claims or flag them for drift.
6. Prompts: pm prompt gains a MEMORY paragraph (spec = normative vs memory = empirical line; remember tool; promotion path memory → spec/plans/backlog; grooming: dedupe/prune/merge, budget). Coordinator + chat prompts get a short MEMORY note (what belongs there, use remember, verify before relying). Add a `memory-groom` pm preset in Presets() whose prompt walks: read memory.md → dedupe/merge/prune stale or disproven entries → propose promotions (spec edits / plans / create_task) with user approval → rewrite the file within budget.
7. spec.md: add §6.5 "Project memory (agent-learned, advisory)" — the normative/empirical line, root memory.md format, remember tool + budget, injection framing, doc_updated eventing, spec-doctor exclusion, promotion path — referencing docs/design/project-memory.md. Update the design doc's Status line from "proposal" to accepted/implemented (first cut).
8. Tests: docs (append formatting/section creation/default+invalid category/budget refusal/ReadMemory absent), orchestrator (assemble injects memory when present and adds nothing when absent; remember tool appends + emits doc_updated; over-budget refusal surfaces the guidance; BuildMode registers remember for chat/pm/work), DocFiles exclusion test. `go build ./... && go test ./...` plus `go run ./cmd/ycc spec-check` must stay green (memory.md excluded).

### Starting points
- internal/docs/docs.go — Store, per-dir mutex, AppendWorkLog/upsertSection patterns to mirror
- internal/docs/spec.go — SpecPath/IsDoc/DocFiles (add memory exclusion here)
- internal/orchestrator/modes.go — BuildMode (tool registries + OnWrite hook), sys/inspectSys/assemble, Presets()
- internal/orchestrator/prompts.go — pmModeSystem, coordinatorSystem, chatModeSystem, specDoctorPresetPrompt
- internal/orchestrator/orchestrator.go:396,610 — implementer/reviewer prompts assembled via sys()/inspectSys(), so injection there is automatic
- cmd/ycc/speccheck.go — runSpecCheck uses Store.DocFiles()
- docs/design/project-memory.md — the design doc; §5.1 has the exact memory.md format/header
- spec.md §6 (line ~230) — add §6.5 after 6.4
- event.DocUpdated usage: internal/orchestrator/modes.go:77

## Work log
- 2026-07-06 plan: Implement project memory per docs/design/project-memory.md (first cut).  Resolved open questions (recorded here as decisions): - Injection scope: every agent, every session (coordinator/pm/chat via sy
…[truncated]
- 2026-07-06 context hints: 9 recorded with plan
- 2026-07-06 context hints: internal/docs/docs.go — Store{dir,mu,cfg}, lockFor, upsertSection/AppendWorkLog patterns; workspace root = filepath.Dir(s.dir); internal/docs/spec.go — SpecPath/IsDoc/DocFiles (add memory exclusio
…[truncated]
- 2026-07-06 implementer report: Implemented project memory (task 0169, first cut) per docs/design/project-memory.md.  ## What changed  **internal/docs/memory.go (new)** — memory store on `*Store`: - `MemoryPath()` → `<workspaceR
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change fully implements project memory per the task and design doc: internal/docs/memory.go adds MemoryPath/ReadMemory/IsMemory/AppendMemory with categorized dated entries, header creation, defaul
…[truncated]
- 2026-07-06 decision: accept — commit: Project memory: committed memory.md store, remember tool, prompt injection, grooming path (task 0169)  - internal/docs: MemoryPath/ReadMemory/IsMemory/AppendMemory — categorized dated   entries at r
…[truncated]
