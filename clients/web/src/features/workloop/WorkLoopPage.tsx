// The work-loop surface (`/p/<project>/loop`): the daemon-owned unattended
// backlog drain. GetWorkLoop status (polled while live), Start with its
// options (work implementation, the budget envelope it captures) and a
// graceful Stop, a live worker target, links to the sessions it ran and the
// tasks it touched, and the incremental/final digest. Mirrors iOS WorkLoopView.
import { Icon } from "../../ui/icons";
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router";
import type { WorkLoopDigestTask, WorkLoopInfo, WorkLoopSession, WorkLoopSlot } from "../../gen/ycc/v1/ycc_pb";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useBacklog, useBudget, useModels, useWorkLoop, useWorkLoopPreflight } from "../../api/queries";
import { paths } from "../../app/paths";
import { useIntent } from "../../app/intents";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { Modal } from "../../ui/Modal";
import { toast } from "../../ui/toast";
import { track } from "../../app/analytics";
import { useInspector } from "../inspector/inspector";
import { Markdown } from "../markdown/Markdown";
import { StatusPill, TaskLink } from "../backlog/parts";
import { relativeTime } from "../sessions/feed";
import { shortSessionId } from "../coordination/model";
import { spawnableTasks } from "../workstreams/model";
import { loopNeedsAnthropicReconnect } from "../settings/anthropic";
import { openAnthropicLogin } from "../settings/AnthropicLogin";
import {
  WORK_IMPLEMENTATIONS,
  bannerLine,
  budgetLine,
  canResize,
  canStart,
  canStop,
  currentSessionId,
  digestSections,
  digestTotalsLine,
  durationText,
  finishAnnouncement,
  isActive,
  isFailureOutcome,
  isParallel,
  isResizeTarget,
  loopState,
  MAX_LOOP_PARALLEL,
  parallelExplanation,
  parallelToSend,
  loopTotalsLine,
  resourceEnvelopeLines,
  stateTitle,
  slotsLine,
  slotStateText,
  stopConfirmBody,
  summaryLine,
  totalsLine,
  uncommittedInReviewLine,
  waitingLine,
  type LoopState,
} from "./model";

export const loopIntentKey = (project: string) => `loop:${project}`;

export function LoopStateBadge({ state }: { state: LoopState }) {
  return <span className={`loop-badge loop-${state}`}>{stateTitle(state)}</span>;
}

/**
 * A one-line loop status for the backlog (iOS's backlog banner): shown while
 * a loop is live and after one finished, keeping its stop reason in view.
 */
export function LoopBanner({ project }: { project: string }) {
  const q = useWorkLoop(project);
  const loop = q.data;
  if (!loop) return null;
  const state = loopState(loop);
  const failed = state === "finished" && isFailureOutcome(loop.outcome);
  return (
    <Link to={paths.loop(project || null)} className={`loop-banner${failed ? " failed" : ""}`} title="Open the work loop">
      <LoopStateBadge state={state} />
      <span className="loop-banner-text">Work loop · {bannerLine(loop)}</span>
    </Link>
  );
}

export function WorkLoopPage({ project }: { project: string }) {
  const q = useWorkLoop(project);
  const qc = useQueryClient();
  const [startOpen, setStartOpen] = useState(false);
  const [stopOpen, setStopOpen] = useState(false);
  const [resizeLoopId, setResizeLoopId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const loop = q.data ?? null;
  const state = loopState(loop);

  useIntent(
    loopIntentKey(project),
    useCallback((what: string) => {
      if (what === "start") setStartOpen(true);
      if (what === "stop") setStopOpen(true);
    }, []),
  );

  const install = (next: WorkLoopInfo | null) => qc.setQueryData(queryKeys.workLoop(project), next);

  const handleError = (err: unknown, what: string, op: string) => {
    if (isUnauthorized(err)) {
      authStore.expire();
      return;
    }
    toast(`${what}: ${errorMessage(err)}`, "error", { op, err });
  };

  const start = async (implementation: string | null, parallel: number) => {
    if (busy) return;
    setBusy(true);
    try {
      if (implementation) {
        await client.setWorkImplementation({ implementation });
        void qc.invalidateQueries({ queryKey: queryKeys.models("") });
      }
      const resp = await client.startWorkLoop({ project, parallel });
      const next = resp.loop ?? null;
      install(next);
      track.submit("loop_start", { implementation: implementation ?? "unchanged", parallel: parallel || "default" });
      setStartOpen(false);
      // Start can return an already-finished loop when setup failed at once.
      const note = finishAnnouncement(null, next, true);
      if (note) toast(note.text, note.failure ? "error" : "info", { op: "loop.start", err: "finished_at_once" });
      else toast(`Work loop started${project ? ` in ${project}` : ""}.`, "info");
      void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
    } catch (err) {
      handleError(err, "Couldn’t start the work loop", "loop.start");
      void q.refetch();
    } finally {
      setBusy(false);
    }
  };

  const stop = async () => {
    if (busy) return;
    setBusy(true);
    setStopOpen(false);
    try {
      const resp = await client.stopWorkLoop({ project });
      install(resp.loop ?? null);
      const next = resp.loop;
      // Sessions the loop let wait keep running and do not delay the stop.
      const draining = !next || next.parallel <= 0 || next.slots.some((s) => !s.detached && s.state === "working");
      const pending = !draining
        ? "Stopping…"
        : next && isParallel(next)
          ? "Stopping once the loop’s working sessions finish…"
          : "Stopping after the current session…";
      toast(next && loopState(next) === "finished" ? "Work loop stopped." : pending, "info");
    } catch (err) {
      handleError(err, "Couldn’t stop the work loop", "loop.stop");
    } finally {
      setBusy(false);
    }
  };

  const resize = async (parallel: number) => {
    const queryKey = queryKeys.workLoop(project);
    const current = qc.getQueryData<WorkLoopInfo | null>(queryKey);
    if (busy || !current || current.loopId !== resizeLoopId || !canResize(loopState(current))) return;
    if (!isResizeTarget(parallel) || parallel === current.parallel) return;
    setBusy(true);
    try {
      // The server also checks the loop ID in case it changed since the last poll.
      const resp = await client.resizeWorkLoop({ project, parallel, loopId: current.loopId });
      // A status poll started before the resize could otherwise overwrite its snapshot.
      await qc.cancelQueries({ queryKey, exact: true });
      install(resp.loop ?? null);
      track.submit("loop_resize", { parallel });
      setResizeLoopId(null);
      toast("Work loop resized.", "info");
    } catch (err) {
      handleError(err, "Couldn’t resize the work loop", "loop.resize");
      void q.refetch();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="page loop-page">
      <header className="page-head">
        <h1>
          Work loop {project && <span className="muted">· {project}</span>}
        </h1>
        <div className="page-actions">
          <button
            type="button"
            className="btn ghost"
            onClick={() => void q.refetch()}
            disabled={q.isFetching}
            data-track="loop.refresh"
            title="Refresh"
            aria-label="Refresh work loop"
          >
            <Icon name="refresh" size={15} />
          </button>
          {loop && canResize(state) && (
            <button
              type="button"
              className="btn"
              disabled={busy}
              data-track="loop.resize"
              onClick={() => setResizeLoopId(loop.loopId)}
            >
              Resize loop…
            </button>
          )}
          {isActive(state) ? (
            <button
              type="button"
              className="btn danger"
              disabled={busy || !canStop(state)}
              data-track="loop.stop"
              onClick={() => setStopOpen(true)}
            >
              {state === "stopping" ? "Stopping…" : "Stop loop"}
            </button>
          ) : (
            <button
              type="button"
              className="btn primary"
              disabled={busy || q.isPending || !canStart(state)}
              data-track="loop.start"
              onClick={() => setStartOpen(true)}
            >
              Start loop…
            </button>
          )}
        </div>
      </header>
      {q.isPending ? (
        <p className="muted">Loading…</p>
      ) : q.isError && !loop ? (
        <p className="error">{errorMessage(q.error, "Couldn’t load the work loop.")}</p>
      ) : !loop ? (
        <EmptyLoop project={project} onStart={() => setStartOpen(true)} />
      ) : (
        <LoopDetail project={project} loop={loop} refreshError={q.isError ? errorMessage(q.error) : null} />
      )}
      <StartLoopDialog
        project={project}
        open={startOpen}
        busy={busy}
        onCancel={() => setStartOpen(false)}
        onStart={(impl, parallel) => void start(impl, parallel)}
      />
      <Modal
        open={loop !== null && loop.loopId === resizeLoopId && canResize(state)}
        onClose={() => !busy && setResizeLoopId(null)}
        title={`Resize the work loop${project ? ` · ${project}` : ""}`}
        view="loop_resize"
        flow="loop_resize"
      >
        {loop && (
          <ResizeLoopForm
            current={loop.parallel}
            busy={busy}
            onCancel={() => setResizeLoopId(null)}
            onResize={(parallel) => void resize(parallel)}
          />
        )}
      </Modal>
      <ConfirmDialog
        open={stopOpen}
        title="Stop the work loop gracefully?"
        body={stopConfirmBody(loop)}
        confirmLabel="Stop loop"
        danger
        action="loop.stop"
        onConfirm={() => void stop()}
        onCancel={() => setStopOpen(false)}
      />
    </div>
  );
}

function ReadyTasks({ project, limit = 5 }: { project: string; limit?: number }) {
  const backlog = useBacklog(project);
  const ready = useMemo(() => spawnableTasks(backlog.data ?? []), [backlog.data]);
  if (backlog.isPending) return <p className="muted small">Checking the backlog…</p>;
  if (backlog.isError) return <p className="error small">{errorMessage(backlog.error, "Couldn’t read the backlog.")}</p>;
  if (ready.length === 0) {
    return (
      <div className="banner warn" role="note">
        No ready tasks: the loop would finish at once. Promote or unblock work in the{" "}
        <Link to={paths.backlog(project || null)}>backlog</Link> first.
      </div>
    );
  }
  return (
    <div className="loop-ready">
      <div className="label">
        {ready.length} ready {ready.length === 1 ? "task" : "tasks"}, highest priority first
      </div>
      <ul>
        {ready.slice(0, limit).map((t) => (
          <li key={t.id}>
            <TaskLink project={project} id={t.id} className="task-chip" /> <span>{t.title}</span>
          </li>
        ))}
        {ready.length > limit && <li className="muted">… and {ready.length - limit} more</li>}
      </ul>
    </div>
  );
}

/**
 * The start dialog's preflight: in-review tasks whose work is uncommitted.
 * Loop sessions leave it to its owner, so starting anyway is a choice. An
 * older daemon without the RPC shows nothing.
 */
function UncommittedReviewWarning({ project, tasks, paths }: { project: string; tasks: WorkLoopDigestTask[]; paths: number }) {
  const line = uncommittedInReviewLine(tasks, paths);
  if (!line) return null;
  return (
    <div className="banner warn loop-uncommitted" role="alert">
      <div>
        <strong>{line}.</strong> Loop sessions work around it. Commit it first, or start anyway.
      </div>
      <ul>
        {tasks.slice(0, 8).map((t) => (
          <li key={t.id}>
            <TaskLink project={project} id={t.id} className="task-chip" /> <span>{t.title}</span>
            {t.reason && <div className="muted small mono">{t.reason}</div>}
          </li>
        ))}
        {tasks.length > 8 && <li className="muted">… and {tasks.length - 8} more</li>}
      </ul>
    </div>
  );
}

function EmptyLoop({ project, onStart }: { project: string; onStart: () => void }) {
  return (
    <section className="loop-empty">
      <h2>No work loop has run{project ? ` for ${project}` : ""} yet</h2>
      <p className="muted">
        The work loop works through this project’s ready backlog unattended, a fresh <code>work</code> session per task, until
        none remain, a budget cap trips, or you stop it.
      </p>
      <ReadyTasks project={project} />
      <p>
        <button type="button" className="btn primary" data-track="loop.start" onClick={onStart}>
          Start loop…
        </button>
      </p>
    </section>
  );
}

function StartLoopDialog({
  project,
  open,
  busy,
  onCancel,
  onStart,
}: {
  project: string;
  open: boolean;
  busy: boolean;
  onCancel: () => void;
  /** `parallel` is StartWorkLoopRequest.parallel: 0 keeps the project's configured count. */
  onStart: (implementation: string | null, parallel: number) => void;
}) {
  const models = useModels("", open);
  const budget = useBudget(open);
  const preflight = useWorkLoopPreflight(project, open);
  // Hold Start until this opening's check answers, so the warning can't arrive
  // after the click. A failed check (e.g. an older daemon without the RPC)
  // warns about nothing rather than blocking the loop.
  const checking = open && preflight.isFetching;
  const uncommitted = preflight.isError ? [] : (preflight.data?.uncommittedInReview ?? []);
  const current = models.data?.workImplementation || "direct";
  const [choice, setChoice] = useState<string | null>(null);
  const implementation = choice ?? current;
  // 0 when the daemon does not report the project's count (older daemons).
  const configured = preflight.isError ? 0 : (preflight.data?.configuredParallel ?? 0);
  const [slotChoice, setSlotChoice] = useState<number | null>(null);
  const slots = slotChoice ?? (configured || 1);
  const close = () => {
    setChoice(null);
    setSlotChoice(null);
    onCancel();
  };
  const detail = WORK_IMPLEMENTATIONS.find((o) => o.value === implementation)?.detail ?? "";
  const defaultNote = configured && configured !== slots ? ` Project default: ${configured}.` : "";
  return (
    <Modal open={open} onClose={() => !busy && close()} title={`Start the work loop${project ? ` · ${project}` : ""}`} className="loop-start-dialog" view="loop_start" flow="loop_start">
      <form
        className="task-editor"
        onSubmit={(e) => {
          e.preventDefault();
          if (checking) return;
          onStart(implementation !== current ? implementation : null, parallelToSend(slotChoice, configured));
          setChoice(null);
          setSlotChoice(null);
        }}
      >
        {checking ? (
          <p className="muted small">Checking for uncommitted in-review work…</p>
        ) : (
          <UncommittedReviewWarning project={project} tasks={uncommitted} paths={preflight.data?.uncommittedPaths ?? 0} />
        )}
        <div className="loop-option">
          <label htmlFor="loop-start-slots">Sessions at once</label>
          <select id="loop-start-slots" value={slots} disabled={checking} onChange={(e) => setSlotChoice(Number(e.target.value))}>
            {Array.from({ length: MAX_LOOP_PARALLEL }, (_, i) => i + 1).map((n) => (
              <option key={n} value={n}>
                {n}
                {n === configured ? " (default)" : ""}
              </option>
            ))}
          </select>
          <p className="muted small">
            {parallelExplanation(slots)}
            {defaultNote}
          </p>
        </div>
        <div className="loop-option">
          <span id="loop-start-impl">Who writes the code</span>
          <span className="segmented inline" role="group" aria-labelledby="loop-start-impl">
            {WORK_IMPLEMENTATIONS.map((o) => (
              <button
                key={o.value}
                type="button"
                className={implementation === o.value ? "selected" : ""}
                aria-pressed={implementation === o.value}
                disabled={models.isPending}
                onClick={() => setChoice(o.value)}
              >
                {o.label}
              </button>
            ))}
          </span>
          <p className="muted small">
            {detail}
            {implementation !== current && " Also changes the default for new work sessions."}
          </p>
        </div>
        <ReadyTasks project={project} limit={3} />
        <div className="editor-actions">
          <span className="muted small">
            {budget.data ? `${budgetLine(budget.data)} ` : ""}Keeps running while this tab is closed.
          </span>
          <button type="button" className="btn" onClick={close} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy || checking}>
            {busy ? "Starting…" : checking ? "Checking…" : uncommitted.length > 0 ? "Start anyway" : "Start loop"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function ResizeLoopForm({ current, busy, onCancel, onResize }: {
  current: number;
  busy: boolean;
  onCancel: () => void;
  onResize: (parallel: number) => void;
}) {
  const [choice, setChoice] = useState<number | null>(null);
  const target = choice ?? current;
  const valid = isResizeTarget(target);
  const canSubmit = !busy && valid && target !== current;
  return (
    <form className="task-editor" onSubmit={(e) => {
      e.preventDefault();
      if (canSubmit) onResize(target);
    }}>
      <div className="loop-option">
        <label htmlFor="loop-resize-slots">Worker target</label>
        <select
          id="loop-resize-slots"
          value={valid ? target : ""}
          disabled={busy}
          aria-describedby="loop-resize-help"
          onChange={(e) => setChoice(Number(e.target.value))}
        >
          {!isResizeTarget(current) && <option value="" disabled>Select a target</option>}
          {Array.from({ length: MAX_LOOP_PARALLEL }, (_, i) => i + 1).map((n) => (
            <option key={n} value={n}>{n}{n === current ? " (current)" : ""}</option>
          ))}
        </select>
        <p id="loop-resize-help" className="muted small">
          This changes only the current loop, not the project default. Lowering the target won’t interrupt sessions;
          the loop waits until the working count is below the target before starting more.
        </p>
      </div>
      <div className="editor-actions">
        <button type="button" className="btn" disabled={busy} onClick={onCancel}>Cancel</button>
        <button type="submit" className="btn primary" disabled={!canSubmit}>
          {busy ? "Resizing…" : "Resize loop"}
        </button>
      </div>
    </form>
  );
}

function LoopDetail({ project, loop, refreshError }: { project: string; loop: WorkLoopInfo; refreshError: string | null }) {
  const state = loopState(loop);
  const current = currentSessionId(loop);
  const started = loop.startedAt ? relativeTime(loop.startedAt) : "";
  const outcome = loop.outcome.trim();
  return (
    <div className="loop-detail">
      <section className="loop-card" aria-label="Loop status">
        <div className="loop-card-head">
          <LoopStateBadge state={state} />
          {started && (
            <span className="muted small" title={new Date(Date.parse(loop.startedAt)).toLocaleString()}>
              started {started}
            </span>
          )}
        </div>
        <div className="loop-summary">{summaryLine(loop)}</div>
        <div className="muted mono small">{loopTotalsLine(loop)}</div>
        {isActive(state) && slotsLine(loop) && <div className="muted small">{slotsLine(loop)}</div>}
        {state === "waiting" && <div className="loop-waiting">{waitingLine(loop)}</div>}
        {state === "finished" && outcome && (
          <div className={`banner ${isFailureOutcome(outcome) ? "error" : "ok"} loop-outcome`} role="status">
            {outcome}
          </div>
        )}
        {loopNeedsAnthropicReconnect(loop, state === "finished") && (
          <div className="loop-reconnect">
            <button type="button" className="btn" onClick={() => openAnthropicLogin()}>
              Reconnect Anthropic…
            </button>
            <span className="muted small">The loop stopped on an Anthropic login problem. Reconnecting does not restart the loop.</span>
          </div>
        )}
        {loop.uncommittedInReview.length > 0 && (
          <div className="warn small loop-uncommitted-note">
            At start, {uncommittedInReviewLine(loop.uncommittedInReview, loop.uncommittedPaths)}; loop sessions route around
            that work (listed below).
          </div>
        )}
        {refreshError && <div className="warn small">Couldn’t refresh: {refreshError}</div>}
        {current && !isParallel(loop) && (
          <div className="loop-current">
            <span className="live-dot" aria-hidden="true" /> Now running{" "}
            <Link to={paths.session(project, current)} className="mono">
              {current}
            </Link>{" "}
            <Link to={paths.session(project, current)} className="btn small">
              Follow live session →
            </Link>
          </div>
        )}
        {isActive(state) && !current && state !== "waiting" && !isParallel(loop) && (
          <div className="muted small">Between sessions…</div>
        )}
      </section>

      {loop.slots.length > 0 && (
        <section className="loop-section" aria-labelledby="loop-slots">
          <h2 id="loop-slots">Live sessions</h2>
          <ul className="digest-list">
            {loop.slots.map((slot, i) => (
              <SlotRow key={slot.sessionId || `starting-${i}`} project={project} slot={slot} />
            ))}
          </ul>
        </section>
      )}

      {loop.sessions.length > 0 && (
        <section className="loop-section" aria-labelledby="loop-sessions">
          <h2 id="loop-sessions">Sessions run</h2>
          <ol className="loop-sessions">
            {loop.sessions.map((s, i) => (
              <LoopSessionRow key={s.sessionId || i} project={project} session={s} index={i + 1} />
            ))}
          </ol>
        </section>
      )}

      {digestSections(loop).map((sec) => (
        <section key={sec.key} className={`loop-section digest-${sec.key}`} aria-labelledby={`digest-${sec.key}`}>
          <h2 id={`digest-${sec.key}`}>
            {sec.title} <span className="count muted">{sec.rows.length}</span>
          </h2>
          <ul className="digest-list">
            {sec.rows.map((t) => (
              <DigestRow
                key={t.id}
                project={project}
                task={t}
                reason={sec.key === "blocked" ? "error" : sec.key === "uncommittedInReview" ? "note" : null}
              />
            ))}
          </ul>
        </section>
      ))}

      {loop.manualLandings.length > 0 && (
        <section className="loop-section" aria-labelledby="loop-manual">
          <h2 id="loop-manual">
            Other commits <span className="count muted">{loop.manualLandings.length}</span>
          </h2>
          <p className="muted small">Commits made during the loop that no loop session recorded.</p>
          <ul className="digest-list">
            {loop.manualLandings.map((c) => (
              <li key={c.sha}>
                <span className="mono">{c.sha.slice(0, 10)}</span> {c.subject}
              </li>
            ))}
          </ul>
        </section>
      )}

      <section className="loop-section" aria-labelledby="loop-envelope">
        <h2 id="loop-envelope">Resource envelope</h2>
        <ul className="plain-list muted small">
          {resourceEnvelopeLines(loop).map((l) => (
            <li key={l}>{l}</li>
          ))}
        </ul>
      </section>
    </div>
  );
}

/** Re-render every `ms` so elapsed times stay current between polls. */
function useNow(ms: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

/**
 * One live loop session: its task, declared intent, working or waiting and
 * for how long, and its latest activity. The task and the session open from it.
 */
function SlotRow({ project, slot }: { project: string; slot: WorkLoopSlot }) {
  const now = useNow(15_000);
  const active = slot.lastActivity ? relativeTime(slot.lastActivity, now) : "";
  return (
    <li className={`digest-row loop-slot slot-${slot.state}`}>
      <div className="digest-head">
        {slot.taskId ? (
          <>
            <TaskLink project={project} id={slot.taskId} className="task-chip" />
            {slot.taskTitle && <span className="digest-title">{slot.taskTitle}</span>}
          </>
        ) : (
          <span className="muted">{slot.sessionId ? "Choosing a task…" : "Starting…"}</span>
        )}
        <span className="tag loop-slot-state" title={slot.since ? `Since ${new Date(Date.parse(slot.since)).toLocaleString()}` : undefined}>
          {slotStateText(slot, now)}
        </span>
      </div>
      {slot.intent && <div className="loop-slot-intent">{slot.intent}</div>}
      {slot.sessionId && (
        <div className="digest-meta muted small">
          <Link to={paths.session(project, slot.sessionId)} className="mono" title={`Open session ${slot.sessionId}`}>
            {shortSessionId(slot.sessionId)}
          </Link>
          {active && (
            <span title={new Date(Date.parse(slot.lastActivity)).toLocaleString()}>
              {active === "now" ? "active now" : `last active ${active}`}
            </span>
          )}
        </div>
      )}
    </li>
  );
}

function LoopSessionRow({ project, session: s, index }: { project: string; session: WorkLoopSession; index: number }) {
  const totals = totalsLine(s.tokens, s.cost, s.priceStatus);
  const duration = durationText(s.durationSecs);
  const failed = s.errorKind.trim() !== "";
  return (
    <li className={`loop-session${failed ? " failed" : ""}`}>
      <div className="loop-session-head">
        <Link to={paths.session(project, s.sessionId)} className="mono" title={`Open session ${s.sessionId}`}>
          session #{index}
        </Link>
        {s.focus && <TaskLink project={project} id={s.focus} className="task-chip" />}
        {s.attempt > 1 && <span className="tag">attempt {s.attempt}</span>}
        <span className="muted small mono">
          {totals}
          {duration ? ` · ${duration}` : ""}
        </span>
      </div>
      {failed && (
        <div className="loop-error">
          Failed ({s.errorKind}
          {s.errorRetryable ? ", retryable" : ""}){s.errorMessage ? `: ${s.errorMessage}` : ""}
        </div>
      )}
      {s.createdTasks.length > 0 && (
        <div className="muted small">
          Created {s.createdTasks.length === 1 ? "task" : `${s.createdTasks.length} tasks`}:{" "}
          {s.createdTasks.map((id) => (
            <TaskLink key={id} project={project} id={id} className="task-chip" />
          ))}
        </div>
      )}
      {s.evidence && <ClampedText text={s.evidence} />}
    </li>
  );
}

function DigestRow({
  project,
  task: t,
  reason,
}: {
  project: string;
  task: WorkLoopDigestTask;
  /** How to show t.reason: a blocked reason, an uncommitted-files note, or not at all. */
  reason: "error" | "note" | null;
}) {
  const inspector = useInspector();
  const totals = digestTotalsLine(t);
  const details = [
    t.latestEvidence && { label: "Latest evidence", text: t.latestEvidence },
    t.remainingCriteria && { label: "Remaining criteria", text: t.remainingCriteria },
    t.nextStep && { label: "Next step", text: t.nextStep },
  ].filter(Boolean) as { label: string; text: string }[];
  return (
    <li className="digest-row">
      <div className="digest-head">
        <TaskLink project={project} id={t.id} className="task-chip" />
        <span className="digest-title">{t.title}</span>
        {t.status && <StatusPill status={t.status} />}
        {t.sha && (
          <button
            type="button"
            className="link mono small"
            title="Show this commit in the inspector"
            onClick={() => inspector.open({ kind: "commit", project, sha: t.sha })}
          >
            {t.sha.slice(0, 7)}
          </button>
        )}
      </div>
      <div className="digest-meta muted small">
        {t.attempts > 0 && <span>{t.attempts === 1 ? "1 attempt" : `${t.attempts} attempts`}</span>}
        {t.verdictTally && <span>{t.verdictTally}</span>}
        {totals && <span className="mono">{totals}</span>}
      </div>
      {reason && t.reason && <div className={reason === "error" ? "loop-error" : "muted small mono"}>{t.reason}</div>}
      {details.length > 0 && (
        <details className="digest-details">
          <summary>Evidence and next steps</summary>
          {details.map((d) => (
            <div key={d.label} className="digest-detail">
              <div className="label">{d.label}</div>
              <Markdown text={d.text} className="loop-md" />
            </div>
          ))}
        </details>
      )}
    </li>
  );
}

/**
 * Untrusted report text, rendered as the transcript renders reports (safe
 * markdown: no raw HTML, inert unsafe links), clamped until expanded.
 */
function ClampedText({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const long = text.length > 280 || text.split("\n").length > 4;
  return (
    <div className="loop-evidence">
      <div className={`loop-md${long && !open ? " clamped" : ""}`}>
        <Markdown text={text} />
      </div>
      {long && (
        <button type="button" className="link small" onClick={() => setOpen((o) => !o)}>
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </div>
  );
}
