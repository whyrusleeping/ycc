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
| [0007](backlog/0007-remote-sync.md) | Remote session sync + phone-facing surface (M5) | blocked | 5 | 0006 |
| [0008](backlog/0008-reviewer-sandboxing.md) | Sandbox reviewer bash to prevent workspace mutation | blocked | 6 | 0005 |
| [0009](backlog/0009-session-lifecycle-interrupt.md) | Session lifecycle — Interrupt RPC and stop/GC | done | 3 | 0003 |
| [0010](backlog/0010-context-window-management.md) | Context-window management for long sessions | done | 3 | 0002 |
| [0011](backlog/0011-multiline-input.md) | Multiline session input (textarea) | done | 3 | 0006 |
| [0012](backlog/0012-settings-overlay.md) | Settings overlay (esc) with mid-session interaction level + per-role model config | done | 2 | 0006 |
| [0013](backlog/0013-structured-questions.md) | Structured interactive ask_user questions (option pickers) | done | 2 | 0006 |
| [0014](backlog/0014-daemon-lifecycle-oneshot.md) | Daemon lifecycle — one-shot in-process default, opt-in persistence | done | 2 | 0003 |
| [0015](backlog/0015-multi-project-registry.md) | Multi-project daemon — project registry, RPCs, and TUI picker | done | 2 | 0014, 0006 |
| [0016](backlog/0016-quick-add-backlog-mid-session.md) | Quick-add backlog items mid-session (TUI capture overlay) | done | 3 | 0006 |
| [0017](backlog/0017-tool-call-syntax-highlighting.md) | Smarter language inference for tool-call syntax highlighting | done | 3 | 0006 |
| [0018](backlog/0018-implementer-turn-limit.md) | Remove / raise / make-configurable the implementer turn limit | done | 2 | 0002 |
| [0020](backlog/0020-save-rerun-plans.md) | Persist and re-run coordinator / testing plans (plan library) | done | 3 | 0004 |
| [0021](backlog/0021-collapse-modes-into-pm.md) | Collapse spec/backlog/feature/bug into a single `pm` (project manager) mode | done | 2 | 0006 |
| [0022](backlog/0022-config-save-write-ycc-toml-from-a-config.md) | config.Save — write ycc.toml from a Config | done | 2 | — |
| [0023](backlog/0023-first-run-setup-wizard-configure-model-providers-r.md) | First-run setup wizard — configure model providers & roles (write ycc.toml) | done | 2 | 0022 |
| [0024](backlog/0024-per-project-onboarding-greenfield-full-spec-vs-bro.md) | Per-project onboarding — greenfield (full spec) vs brownfield (scoped) pm presets | done | 2 | — |
| [0025](backlog/0025-thinking-levels-other-models.md) | Verify thinking levels (effort) across backends as models are added | blocked | 3 | 0005 |
| [0026](backlog/0026-capture-per-turn-token-usage-on-model-turn-events.md) | Capture per-turn token usage on model_turn events | done | 2 | 0002 |
| [0027](backlog/0027-record-session-task-focus-for-cost-attribution.md) | Record session→task focus for cost attribution | done | 2 | 0004 |
| [0028](backlog/0028-per-model-pricing-config-cost-computation.md) | Per-model pricing config + cost computation | done | 3 | — |
| [0029](backlog/0029-usage-cost-aggregation-ycc-cost-view-getusage-rpc.md) | Usage/cost aggregation + ycc cost view, GetUsage RPC, work-log summary | done | 2 | 0026, 0027, 0028 |
| [0030](backlog/0030-let-the-work-coordinator-create-backlog-tasks-spli.md) | Let the work coordinator create backlog tasks (split + follow-on work) | done | 2 | — |
| [0031](backlog/0031-backlog-browser-view-and-inspect-tasks-in-the-tui.md) | Backlog browser — view and inspect tasks in the TUI (ListBacklog/GetTask RPCs) | done | 3 | 0006 |
| [0032](backlog/0032-bug-follow-up-user-message-not-displayed-until-age.md) | Bug: follow-up user message not displayed until agent's next response (user_input echo emitted on dequeue, not on send) | done | 2 | — |
| [0033](backlog/0033-durable-session-index-listsessionhistory-rpc.md) | Durable session index + ListSessionHistory RPC | done | 3 | — |
| [0034](backlog/0034-reopen-resume-a-persisted-session-reconstruct-loop.md) | Reopen/resume a persisted session (reconstruct loop history) | done | 3 | 0033 |
| [0035](backlog/0035-tui-session-browser-list-transcript-reopen-shared.md) | TUI session browser (list, transcript, reopen) + shared list+detail modal | done | 3 | 0033, 0034, 0031 |
| [0036](backlog/0036-per-role-thinking-level-independent-reasoning-per.md) | Per-role thinking level (independent reasoning per agent) | done | 3 | — |
| [0037](backlog/0037-review-land-leftover-settings-overlay-thinking-ui.md) | Review & land leftover settings-overlay thinking-UI rework (inline +/- thinking) | done | 3 | — |
| [0038](backlog/0038-per-task-work-log-usage-summary-for-all-tasks-work.md) | Per-task work-log usage summary for all tasks worked in a session (not just current focus) | done | 3 | 0029 |
| [0039](backlog/0039-tui-cost-view-browse-usage-cost-breakdown-via-getu.md) | TUI cost view — browse usage/cost breakdown via GetUsage (shared browser modal) | done | 3 | 0029, 0035 |
| [0040](backlog/0040-interrupt-steer-a-running-agent-pause-correct-resu.md) | Interrupt & steer a running agent (pause / correct / resume) | done | 2 | — |
| [0041](backlog/0041-manage-model-backends-from-the-settings-overlay-li.md) | Manage model backends from the settings overlay (live add/edit/remove, persisted) | done | 2 | — |
| [0042](backlog/0042-select-different-model-ids-sharing-one-provider-s.md) | Select different model ids sharing one provider's credentials (opus/sonnet/haiku) | done | 2 | 0041 |
| [0043](backlog/0043-unify-tool-call-params-and-response-into-one-colla.md) | Unify tool-call params and response into one collapsed chat-log row | done | 3 | 0006 |
| [0044](backlog/0044-settings-overlay-model-backends-management-form-ad.md) | Settings-overlay "Model backends" management form (add/edit/duplicate/remove) | done | 2 | 0041 |
| [0045](backlog/0045-removemodel-should-also-reject-models-referenced-b.md) | RemoveModel should also reject models referenced by live session role assignments | done | 3 | 0041 |
| [0046](backlog/0046-configurable-review-tiers-let-the-work-agent-choos.md) | Configurable review tiers — let the work agent choose review intensity per change | done | 3 | — |
| [0047](backlog/0047-support-multiple-questions-with-per-question-answe.md) | Support multiple questions with per-question answer sets in ask-user tool | done | 3 | — |
| [0048](backlog/0048-default-the-backlog-overlay-view-to-hiding-complet.md) | Default the backlog overlay view to hiding completed tasks | done | 3 | — |
| [0049](backlog/0049-show-agent-action-log-in-quick-add-task-overlay-af.md) | Show agent action log in quick-add task overlay after submit | done | 3 | — |
| [0050](backlog/0050-auto-retry-transient-llm-api-call-failures-with-ba.md) | Auto-retry transient LLM API call failures with backoff | done | 3 | — |
| [0051](backlog/0051-bug-status-header-stuck-on-error-never-returns-to.md) | Bug: status header stuck on "error" — never returns to "running" after recovery | done | 2 | — |
| [0052](backlog/0052-fix-last-line-of-agent-final-output-hidden-behind.md) | Fix: last line of agent final output hidden behind input box | done | 2 | — |
| [0053](backlog/0053-make-edit-tool-require-a-unique-match-and-error-on.md) | Make edit tool require a unique match and error on multiple matches | done | 3 | — |
| [0054](backlog/0054-automatic-idle-session-gc-on-disk-log-retention.md) | Automatic idle-session GC + on-disk log retention | done | 3 | 0009 |
| [0055](backlog/0055-record-timing-in-model-output-and-tool-call-logs.md) | Record timing in model-output and tool-call logs | done | 3 | — |
| [0056](backlog/0056-replay-fidelity-for-mid-run-truncation-nudges-on-r.md) | Replay fidelity for mid-Run truncation nudges on reopen | done | 3 | 0034 |
| [0057](backlog/0057-surface-model-tool-timing-duration-ms-in-tui-chat.md) | Surface model/tool timing (duration_ms) in TUI chat-log rows | done | 4 | 0055 |
| [0058](backlog/0058-session-textarea-grow-on-wrapped-long-lines-add-in.md) | Session textarea: grow on wrapped long lines + add input behavior tests | done | 4 | 0011 |
| [0059](backlog/0059-encourage-work-agent-to-commit-after-finishing-a-t.md) | Encourage work agent to commit after finishing a task (avoid leftover uncommitted backlog files) | done | 3 | — |
| [0060](backlog/0060-tui-theme-palette-centralization-make-the-light-th.md) | TUI theme/palette centralization + make the light theme real | done | 3 | — |
| [0061](backlog/0061-unified-tui-app-frame-bordered-modal-cards.md) | Unified TUI app frame + bordered modal cards | done | 3 | 0060 |
| [0062](backlog/0062-live-session-status-bar-mode-level-thinking-elapse.md) | Live session status bar (mode/level/thinking/elapsed/token-cost) + activity spinner | done | 3 | 0060 |
| [0063](backlog/0063-event-stream-visual-polish-type-glyphs-verdict-col.md) | Event-stream visual polish (type glyphs, verdict colors, subagent tree, selection bar) | done | 3 | 0060 |
| [0064](backlog/0064-chat-mode-allow-create-task-update-task-backlog-ma.md) | Chat mode: allow create_task/update_task backlog management | done | 3 | — |
| [0065](backlog/0065-store-llm-backend-tokens-in-the-global-ycc-config.md) | Store LLM backend tokens in the global ycc config dir instead of env-only | done | 3 | 0041 |
| [0066](backlog/0066-use-multiline-textarea-for-all-chat-inputs-style-w.md) | Use multiline textarea for all chat inputs + style with rounded expanding frame (per lsp.webp) | done | 3 | 0058 |
| [0067](backlog/0067-add-settings-option-to-auto-expand-all-agent-logs.md) | Add settings option to auto-expand all agent logs by default | done | 3 | — |
| [0068](backlog/0068-allow-reading-trusted-paths-outside-the-workspace.md) | Allow reading trusted paths outside the workspace (e.g. Go mod cache) | done | 3 | — |
| [0069](backlog/0069-fix-tui-styling-of-thinking-reasoning-text-black-o.md) | Fix TUI styling of thinking/reasoning text (black-on-white background and extra line spacing) | done | 3 | — |
| [0070](backlog/0070-improve-ctrl-n-quick-add-backlog-ui-echo-user-mess.md) | Improve ctrl+n quick-add backlog UI: echo user message in log, wrap log lines, reuse interactive question UI | done | 3 | — |
| [0071](backlog/0071-render-edit-tool-output-as-a-git-style-colored-dif.md) | Render Edit tool output as a git-style colored diff | done | 3 | — |
| [0072](backlog/0072-work-loop-mode-auto-advance-past-a-finished-idle-w.md) | Work-loop mode: auto-advance past a finished (idle) work session | done | 2 | — |
| [0073](backlog/0073-investigate-fix-multi-question-ask-user-ui-interac.md) | Investigate/fix multi-question ask_user UI (interactive/judgement modes) | done | 3 | — |
| [0074](backlog/0074-tui-snapshot-to-image-rendering-for-visual-debuggi.md) | TUI snapshot-to-image rendering for visual debugging | done | 3 | — |
| [0075](backlog/0075-scroll-tui-backlog-view-when-items-exceed-terminal.md) | Scroll TUI backlog view when items exceed terminal height | done | 3 | — |
| [0076](backlog/0076-move-activity-spinner-to-the-bottom-next-to-the-se.md) | Move activity spinner to the bottom next to the session input box | done | 3 | — |
| [0077](backlog/0077-surface-the-plan-library-in-the-tui-rpc.md) | Surface the plan library in the TUI / RPC | done | 4 | 0020 |
| [0078](backlog/0078-spike-design-parallel-agent-workstreams-via-git-wo.md) | Spike: design parallel agent workstreams via git worktrees | done | 4 | — |
| [0079](backlog/0079-let-coordinator-preload-worker-agent-with-file-sni.md) | Let coordinator preload worker agent with file/snippet context hints from the plan | done | 3 | — |
| [0080](backlog/0080-give-capture-agent-bounded-read-access-to-ground-b.md) | Give capture agent bounded read access to ground backlog items in the codebase | done | 3 | — |
| [0081](backlog/0081-worktree-primitives-in-internal-git.md) | Worktree primitives in internal/git | todo | 4 | 0078 |
| [0082](backlog/0082-workstream-registry-lifecycle-in-the-daemon-sessio.md) | Workstream registry + lifecycle in the daemon/session manager | todo | 4 | 0081 |
| [0083](backlog/0083-workstream-merge-integration-flow-with-conflict-su.md) | Workstream merge/integration flow with conflict surfacing | todo | 4 | 0082 |
| [0084](backlog/0084-rpc-surface-for-workstreams-spawn-list-preview-mer.md) | RPC surface for workstreams (spawn/list/preview/merge/discard) | todo | 4 | 0082, 0083 |
| [0085](backlog/0085-tui-workstreams-panel-spawn-monitor-merge-ux.md) | TUI: Workstreams panel + spawn/monitor/merge UX | todo | 4 | 0084 |
| [0086](backlog/0086-spec-document-workstream-concept-worktree-decision.md) | Spec: document workstream concept + worktree decision | todo | 4 | 0078 |
| [0087](backlog/0087-add-web-search-fetch-page-agent-tools-via-exa.md) | Add web search + fetch-page agent tools (via Exa) | done | 3 | — |
| [0088](backlog/0088-make-read-tool-list-directory-contents-when-given.md) | Make Read tool list directory contents when given a directory path | done | 3 | — |
| [0089](backlog/0089-break-down-per-task-work-log-token-usage-by-agent.md) | Break down per-task work-log token usage by agent role | done | 3 | 0038 |
| [0090](backlog/0090-make-backlog-task-detail-view-scrollable-in-the-tu.md) | Make backlog task detail view scrollable in the TUI viewer | done | 3 | — |
| [0091](backlog/0091-drop-spec-feature-bug-backlog-home-menu-presets-in.md) | Drop spec/feature/bug/backlog home-menu presets in favor of just the pm mode | todo | 3 | — |
| [0092](backlog/0092-bump-capture-agent-maxturns-to-32-and-tell-agent-i.md) | Bump capture agent MaxTurns to 32 and tell agent its turn budget | done | 3 | — |
| [0093](backlog/0093-raise-maxtokens-default-and-handle-non-stop-stop-r.md) | Raise MaxTokens default and handle non-stop stop reasons robustly | done | 2 | — |
| [0094](backlog/0094-fix-box-border-alignment-when-spinner-is-at-bottom.md) | Fix box border alignment when spinner is at bottom near text input | done | 3 | — |
| [0095](backlog/0095-onboarding-mode-should-read-existing-spec-backlog.md) | Onboarding mode should read existing spec/backlog first and orient from them | todo | 3 | — |
| [0096](backlog/0096-connection-centric-model-config-multi-model-id-ent.md) | Connection-centric model config: multi model-id entry + backend model discovery | done | 2 | — |
| [0097](backlog/0097-sendinput-answer-ignores-a-pending-batch-multi-que.md) | SendInput/Answer ignores a pending batch (multi-question) ask_user | todo | 4 | — |
| [0098](backlog/0098-work-loop-batch-digest-here-s-what-happened-while.md) | Work-loop batch digest: "here's what happened while you were gone" | in_progress | 2 | — |
| [0099](backlog/0099-tui-backlog-grooming-edit-reprioritize-status-open.md) | TUI backlog grooming: edit/reprioritize/status + open task in $EDITOR | todo | 3 | — |
| [0100](backlog/0100-spec-doctor-detect-spec-code-drift-coverage-gaps-d.md) | Spec-doctor: detect spec/code drift + coverage gaps (design with user first) | blocked | 3 | — |
| [0101](backlog/0101-home-menu-notify-when-tasks-are-blocked-and-waitin.md) | Home menu: notify when tasks are blocked and waiting on the user | done | 2 | — |
| [0102](backlog/0102-tui-ctrl-left-right-for-word-wise-cursor-movement.md) | TUI: Ctrl+Left/Right for word-wise cursor movement in prompt input | todo | 3 | — |
| [0103](backlog/0103-deliver-mid-run-user-input-at-the-next-safe-checkp.md) | Deliver mid-run user input at the next safe checkpoint (steer-by-default) | done | 2 | — |
| [0104](backlog/0104-tui-transient-rpc-errors-must-not-replace-the-ui-w.md) | TUI: transient RPC errors must not replace the UI with a fatal error screen | done | 2 | — |
| [0105](backlog/0105-interrupt-keybinding-that-works-without-kitty-keyb.md) | Interrupt keybinding that works without kitty keyboard protocol (ctrl+i == tab) | todo | 3 | — |
| [0106](backlog/0106-question-picker-number-key-selection-don-t-lock-ou.md) | Question picker: number-key selection + don't lock out scrolling/browsers | todo | 3 | — |
| [0107](backlog/0107-home-menu-surface-live-sessions-that-are-waiting-f.md) | Home menu: surface live sessions that are waiting for the user | todo | 3 | — |
| [0108](backlog/0108-terminal-notification-bell-osc-when-the-agent-need.md) | Terminal notification (bell/OSC) when the agent needs the user or finishes | todo | 3 | — |
| [0109](backlog/0109-guard-ctrl-c-don-t-instantly-kill-a-running-sessio.md) | Guard ctrl+c: don't instantly kill a running session on a one-shot daemon | todo | 3 | — |
| [0110](backlog/0110-settings-overlay-replace-rotating-reviewer-toggle.md) | Settings overlay: replace rotating reviewer toggle with an explicit multi-select | todo | 4 | — |
| [0111](backlog/0111-tui-help-modal-listing-keybindings-per-state.md) | TUI help modal (?) listing keybindings per state | todo | 4 | — |
| [0112](backlog/0112-key-parity-browse-selector-session-browser-reachab.md) | Key parity: browse selector + session browser reachable from within a session | todo | 4 | — |
| [0113](backlog/0113-home-menu-choose-the-interaction-level-at-session.md) | Home menu: choose the interaction level at session start | todo | 4 | — |
| [0114](backlog/0114-stream-model-output-incrementally-into-the-session.md) | Stream model output incrementally into the session view | blocked | 3 | — |
| [0115](backlog/0115-structured-blocked-escalation-from-the-implementer.md) | Structured "blocked" escalation from the implementer to the coordinator | todo | 4 | — |
| [0116](backlog/0116-transcript-search-and-jump-to-event-navigation.md) | Transcript search (/) and jump-to-event navigation | todo | 4 | — |
