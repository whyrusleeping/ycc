# Backlog

> Generated index. Canonical task data lives in `backlog/<id>-<slug>.md`.

| id | title | status | pri | depends on |
|----|-------|--------|-----|------------|
| [0001](backlog/0001-gollama-unified-turn.md) | Add unified Turn dispatch to gollama | done | 1 | — |
| [0002](backlog/0002-agent-loop.md) | Core agent loop with worker tools (M0 spike) | done | 1 | 0001 |
| [0003](backlog/0003-daemon-event-log.md) | Daemon, event log, and first client (M1) | done | 2 | 0002 |
| [0004](backlog/0004-work-mode-happy-path.md) | work mode happy path (M2) | done | 2 | 0003 |
| [0005](backlog/0005-multimodel-review-revise-levels.md) | Multi-model review, revise loop, interaction levels (M3) | done | 3 | 0004 |
| [0006](backlog/0006-home-menu-modes-tui.md) | Home menu, spec/backlog/feature/bug modes, TUI (M4) | done | 4 | 0005 |
| [0007](backlog/0007-remote-sync.md) | Remote session sync + phone-facing surface (M5) | todo | 5 | 0006 |
| [0008](backlog/0008-reviewer-sandboxing.md) | Sandbox reviewer bash to prevent workspace mutation | todo | 6 | 0005 |
| [0009](backlog/0009-session-lifecycle-interrupt.md) | Session lifecycle — Interrupt RPC and stop/GC | done | 3 | 0003 |
| [0010](backlog/0010-context-window-management.md) | Context-window management for long sessions | todo | 3 | 0002 |
| [0011](backlog/0011-multiline-input.md) | Multiline session input (textarea) | todo | 3 | 0006 |
| [0012](backlog/0012-settings-overlay.md) | Settings overlay (esc) with mid-session interaction level + per-role model config | done | 2 | 0006 |
| [0013](backlog/0013-structured-questions.md) | Structured interactive ask_user questions (option pickers) | done | 2 | 0006 |
| [0014](backlog/0014-daemon-lifecycle-oneshot.md) | Daemon lifecycle — one-shot in-process default, opt-in persistence | done | 2 | 0003 |
| [0015](backlog/0015-multi-project-registry.md) | Multi-project daemon — project registry, RPCs, and TUI picker | done | 2 | 0014, 0006 |
| [0016](backlog/0016-quick-add-backlog-mid-session.md) | Quick-add backlog items mid-session (TUI capture overlay) | done | 3 | 0006 |
| [0017](backlog/0017-tool-call-syntax-highlighting.md) | Smarter language inference for tool-call syntax highlighting | done | 3 | 0006 |
| [0018](backlog/0018-implementer-turn-limit.md) | Remove / raise / make-configurable the implementer turn limit | done | 2 | 0002 |
| [0020](backlog/0020-save-rerun-plans.md) | Persist and re-run coordinator / testing plans (plan library) | todo | 3 | 0004 |
| [0021](backlog/0021-collapse-modes-into-pm.md) | Collapse spec/backlog/feature/bug into a single `pm` (project manager) mode | done | 2 | 0006 |
| [0022](backlog/0022-config-save-write-ycc-toml-from-a-config.md) | config.Save — write ycc.toml from a Config | done | 2 | — |
| [0023](backlog/0023-first-run-setup-wizard-configure-model-providers-r.md) | First-run setup wizard — configure model providers & roles (write ycc.toml) | done | 2 | 0022 |
| [0024](backlog/0024-per-project-onboarding-greenfield-full-spec-vs-bro.md) | Per-project onboarding — greenfield (full spec) vs brownfield (scoped) pm presets | done | 2 | — |
| [0025](backlog/0025-thinking-levels-other-models.md) | Verify thinking levels (effort) across backends as models are added | todo | 3 | 0005 |
| [0026](backlog/0026-capture-per-turn-token-usage-on-model-turn-events.md) | Capture per-turn token usage on model_turn events | done | 2 | 0002 |
| [0027](backlog/0027-record-session-task-focus-for-cost-attribution.md) | Record session→task focus for cost attribution | done | 2 | 0004 |
| [0028](backlog/0028-per-model-pricing-config-cost-computation.md) | Per-model pricing config + cost computation | done | 3 | — |
| [0029](backlog/0029-usage-cost-aggregation-ycc-cost-view-getusage-rpc.md) | Usage/cost aggregation + ycc cost view, GetUsage RPC, work-log summary | done | 2 | 0026, 0027, 0028 |
| [0030](backlog/0030-let-the-work-coordinator-create-backlog-tasks-spli.md) | Let the work coordinator create backlog tasks (split + follow-on work) | done | 2 | — |
| [0031](backlog/0031-backlog-browser-view-and-inspect-tasks-in-the-tui.md) | Backlog browser — view and inspect tasks in the TUI (ListBacklog/GetTask RPCs) | done | 3 | 0006 |
| [0032](backlog/0032-bug-follow-up-user-message-not-displayed-until-age.md) | Bug: follow-up user message not displayed until agent's next response (user_input echo emitted on dequeue, not on send) | done | 2 | — |
| [0033](backlog/0033-durable-session-index-listsessionhistory-rpc.md) | Durable session index + ListSessionHistory RPC | done | 3 | — |
| [0034](backlog/0034-reopen-resume-a-persisted-session-reconstruct-loop.md) | Reopen/resume a persisted session (reconstruct loop history) | done | 3 | 0033 |
| [0035](backlog/0035-tui-session-browser-list-transcript-reopen-shared.md) | TUI session browser (list, transcript, reopen) + shared list+detail modal | todo | 3 | 0033, 0034, 0031 |
| [0036](backlog/0036-per-role-thinking-level-independent-reasoning-per.md) | Per-role thinking level (independent reasoning per agent) | done | 3 | — |
| [0037](backlog/0037-review-land-leftover-settings-overlay-thinking-ui.md) | Review & land leftover settings-overlay thinking-UI rework (inline +/- thinking) | done | 3 | — |
| [0038](backlog/0038-per-task-work-log-usage-summary-for-all-tasks-work.md) | Per-task work-log usage summary for all tasks worked in a session (not just current focus) | todo | 3 | 0029 |
| [0039](backlog/0039-tui-cost-view-browse-usage-cost-breakdown-via-getu.md) | TUI cost view — browse usage/cost breakdown via GetUsage (shared browser modal) | todo | 3 | 0029, 0035 |
| [0040](backlog/0040-interrupt-steer-a-running-agent-pause-correct-resu.md) | Interrupt & steer a running agent (pause / correct / resume) | done | 2 | — |
| [0041](backlog/0041-manage-model-backends-from-the-settings-overlay-li.md) | Manage model backends from the settings overlay (live add/edit/remove, persisted) | done | 2 | — |
| [0042](backlog/0042-select-different-model-ids-sharing-one-provider-s.md) | Select different model ids sharing one provider's credentials (opus/sonnet/haiku) | done | 2 | 0041 |
| [0043](backlog/0043-unify-tool-call-params-and-response-into-one-colla.md) | Unify tool-call params and response into one collapsed chat-log row | todo | 3 | 0006 |
| [0044](backlog/0044-settings-overlay-model-backends-management-form-ad.md) | Settings-overlay "Model backends" management form (add/edit/duplicate/remove) | done | 2 | 0041 |
| [0045](backlog/0045-removemodel-should-also-reject-models-referenced-b.md) | RemoveModel should also reject models referenced by live session role assignments | done | 3 | 0041 |
| [0046](backlog/0046-configurable-review-tiers-let-the-work-agent-choos.md) | Configurable review tiers — let the work agent choose review intensity per change | done | 3 | — |
| [0047](backlog/0047-support-multiple-questions-with-per-question-answe.md) | Support multiple questions with per-question answer sets in ask-user tool | todo | 3 | — |
| [0048](backlog/0048-default-the-backlog-overlay-view-to-hiding-complet.md) | Default the backlog overlay view to hiding completed tasks | done | 3 | — |
| [0049](backlog/0049-show-agent-action-log-in-quick-add-task-overlay-af.md) | Show agent action log in quick-add task overlay after submit | todo | 3 | — |
| [0050](backlog/0050-auto-retry-transient-llm-api-call-failures-with-ba.md) | Auto-retry transient LLM API call failures with backoff | done | 3 | — |
| [0051](backlog/0051-bug-status-header-stuck-on-error-never-returns-to.md) | Bug: status header stuck on "error" — never returns to "running" after recovery | done | 2 | — |
| [0052](backlog/0052-fix-last-line-of-agent-final-output-hidden-behind.md) | Fix: last line of agent final output hidden behind input box | done | 2 | — |
| [0053](backlog/0053-make-edit-tool-require-a-unique-match-and-error-on.md) | Make edit tool require a unique match and error on multiple matches | done | 3 | — |
| [0054](backlog/0054-automatic-idle-session-gc-on-disk-log-retention.md) | Automatic idle-session GC + on-disk log retention | todo | 3 | 0009 |
| [0055](backlog/0055-record-timing-in-model-output-and-tool-call-logs.md) | Record timing in model-output and tool-call logs | todo | 3 | — |
| [0056](backlog/0056-replay-fidelity-for-mid-run-truncation-nudges-on-r.md) | Replay fidelity for mid-Run truncation nudges on reopen | todo | 3 | 0034 |
