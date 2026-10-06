// The work-loop surface (`/p/<project>/loop`): the daemon-owned unattended
// backlog drain. GetWorkLoop status (polled while live), Start with its
// options (work implementation, the budget envelope it captures) and a
// graceful Stop, links to the sessions it ran and the tasks it touched, and
// the incremental/final digest. Mirrors iOS WorkLoopView.
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo, useState } from "react";
import { Link } from "react-router";
import type { WorkLoopDigestTask, WorkLoopInfo, WorkLoopSession } from "../../gen/ycc/v1/ycc_pb";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useBacklog, useBudget, useModels, useWorkLoop } from "../../api/queries";
import { paths } from "../../app/paths";
import { useIntent } from "../../app/intents";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { Modal } from "../../ui/Modal";
import { toast } from "../../ui/toast";
import { useInspector } from "../inspector/inspector";
import { StatusPill, TaskLink } from "../backlog/parts";
import { relativeTime } from "../sessions/feed";
import { spawnableTasks } from "../workstreams/model";
import {
  WORK_IMPLEMENTATIONS,
  bannerLine,
  budgetLines,
  canStart,
  canStop,
  currentSessionId,
  digestSections,
  durationText,
  finishAnnouncement,
  isActive,
  isFailureOutcome,
  loopState,
  loopTotalsLine,
  resourceEnvelopeLines,
  stateTitle,
  summaryLine,
  totalsLine,
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

  const handleError = (err: unknown, what: string) => {
    if (isUnauthorized(err)) {
      authStore.expire();
      return;
    }
    toast(`${what}: ${errorMessage(err)}`);
  };

  const start = async (implementation: string | null) => {
    if (busy) return;
    setBusy(true);
    try {
      if (implementation) {
        await client.setWorkImplementation({ implementation });
        void qc.invalidateQueries({ queryKey: queryKeys.models("") });
      }
      const resp = await client.startWorkLoop({ project });
      const next = resp.loop ?? null;
      install(next);
      setStartOpen(false);
      // Start can return an already-finished loop when setup failed at once.
      const note = finishAnnouncement(null, next, true);
      if (note) toast(note.text, note.failure ? "error" : "info");
      else toast(`Work loop started${project ? ` in ${project}` : ""}.`, "info");
      void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
    } catch (err) {
      handleError(err, "Couldn’t start the work loop");
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
      toast(resp.loop && loopState(resp.loop) === "finished" ? "Work loop stopped." : "Stopping after the current session…", "info");
    } catch (err) {
      handleError(err, "Couldn’t stop the work loop");
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
            title="Refresh"
            aria-label="Refresh work loop"
          >
            ↻
          </button>
          {isActive(state) ? (
            <button type="button" className="btn danger" disabled={busy || !canStop(state)} onClick={() => setStopOpen(true)}>
              {state === "stopping" ? "Stopping…" : "Stop loop"}
            </button>
          ) : (
            <button
              type="button"
              className="btn primary"
              disabled={busy || q.isPending || !canStart(state)}
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
        onStart={(impl) => void start(impl)}
      />
      <ConfirmDialog
        open={stopOpen}
        title="Stop the work loop gracefully?"
        body={
          state === "waiting"
            ? "The loop is waiting for the provider; stopping ends the wait at once. The daemon will not pick another backlog task."
            : "The current session finishes its work; the daemon will not pick another backlog task."
        }
        confirmLabel="Stop loop"
        danger
        onConfirm={() => void stop()}
        onCancel={() => setStopOpen(false)}
      />
    </div>
  );
}

function ReadyTasks({ project }: { project: string }) {
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
        {ready.slice(0, 5).map((t) => (
          <li key={t.id}>
            <TaskLink project={project} id={t.id} className="task-chip" /> <span>{t.title}</span>
          </li>
        ))}
        {ready.length > 5 && <li className="muted">… and {ready.length - 5} more</li>}
      </ul>
    </div>
  );
}

function EmptyLoop({ project, onStart }: { project: string; onStart: () => void }) {
  return (
    <section className="loop-empty">
      <h2>No work loop has run{project ? ` for ${project}` : ""} yet</h2>
      <p className="muted">
        The work loop drains this project’s ready backlog unattended: it starts a fresh <code>work</code> session per task, one
        after another, until no ready task remains, a budget cap trips, or you stop it. It keeps running while this tab is
        closed.
      </p>
      <ReadyTasks project={project} />
      <p>
        <button type="button" className="btn primary" onClick={onStart}>
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
  onStart: (implementation: string | null) => void;
}) {
  const models = useModels("", open);
  const budget = useBudget(open);
  const current = models.data?.workImplementation || "delegate";
  const [choice, setChoice] = useState<string | null>(null);
  const implementation = choice ?? current;
  const close = () => {
    setChoice(null);
    onCancel();
  };
  return (
    <Modal open={open} onClose={() => !busy && close()} title="Start the work loop" className="loop-start-dialog">
      <form
        className="task-editor"
        onSubmit={(e) => {
          e.preventDefault();
          onStart(implementation !== current ? implementation : null);
          setChoice(null);
        }}
      >
        <p className="muted small">
          {project ? `In ${project}. ` : ""}The daemon drains ready backlog tasks unattended until none remain, a budget cap trips,
          or you stop it. It spends tokens while this tab is closed.
        </p>
        <ReadyTasks project={project} />
        <fieldset className="loop-options">
          <legend>Work implementation</legend>
          {WORK_IMPLEMENTATIONS.map((o) => (
            <label key={o.value} className="radio-row">
              <input
                type="radio"
                name="work-implementation"
                value={o.value}
                checked={implementation === o.value}
                disabled={models.isPending}
                onChange={() => setChoice(o.value)}
              />
              <span>
                <strong>{o.label}</strong>
                {o.value === current && <span className="tag">current</span>} <span className="muted small">{o.detail}</span>
              </span>
            </label>
          ))}
          <p className="muted small">A daemon-wide setting: it applies to every new work session, including this loop’s.</p>
        </fieldset>
        <div className="loop-options">
          <div className="label">Budget caps this loop captures</div>
          {budget.isPending ? (
            <p className="muted small">Loading…</p>
          ) : budget.isError ? (
            <p className="error small">{errorMessage(budget.error)}</p>
          ) : (
            <ul className="plain-list small">
              {budgetLines(budget.data).map((l) => (
                <li key={l}>{l}</li>
              ))}
            </ul>
          )}
        </div>
        <div className="editor-actions">
          <span className="muted small">Configured in ycc.toml ([budget], [work])</span>
          <button type="button" className="btn" onClick={close} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy}>
            {busy ? "Starting…" : "Start loop"}
          </button>
        </div>
      </form>
    </Modal>
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
          <span className="mono muted small">{loop.loopId}</span>
          {started && (
            <span className="muted small" title={new Date(Date.parse(loop.startedAt)).toLocaleString()}>
              started {started}
            </span>
          )}
        </div>
        <div className="loop-summary">{summaryLine(loop)}</div>
        <div className="muted mono small">{loopTotalsLine(loop)}</div>
        {state === "waiting" && <div className="loop-waiting">{waitingLine(loop)}</div>}
        {state === "finished" && outcome && (
          <div className={`banner ${isFailureOutcome(outcome) ? "error" : "ok"} loop-outcome`} role="status">
            {outcome}
          </div>
        )}
        {refreshError && <div className="warn small">Couldn’t refresh: {refreshError}</div>}
        {current && (
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
        {isActive(state) && !current && state !== "waiting" && <div className="muted small">Between sessions…</div>}
      </section>

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
              <DigestRow key={t.id} project={project} task={t} blocked={sec.key === "blocked"} />
            ))}
          </ul>
        </section>
      ))}

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

function LoopSessionRow({ project, session: s, index }: { project: string; session: WorkLoopSession; index: number }) {
  const totals = totalsLine(s.tokens, s.cost, s.priceStatus);
  const duration = durationText(s.durationSecs);
  const failed = s.errorKind.trim() !== "";
  return (
    <li className={`loop-session${failed ? " failed" : ""}`}>
      <div className="loop-session-head">
        <span className="muted">#{index}</span>
        <Link to={paths.session(project, s.sessionId)} className="mono" title="Open this session">
          {s.sessionId}
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
      {s.evidence && <ClampedText text={s.evidence} />}
    </li>
  );
}

function DigestRow({ project, task: t, blocked }: { project: string; task: WorkLoopDigestTask; blocked: boolean }) {
  const inspector = useInspector();
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
        <span className="mono">{totalsLine(t.tokens, t.cost, t.priceStatus)}</span>
      </div>
      {blocked && t.reason && <div className="loop-error">{t.reason}</div>}
      {details.length > 0 && (
        <details className="digest-details">
          <summary>Evidence and next steps</summary>
          {details.map((d) => (
            <div key={d.label} className="digest-detail">
              <div className="label">{d.label}</div>
              <div className="pre-text">{d.text}</div>
            </div>
          ))}
        </details>
      )}
    </li>
  );
}

/** Untrusted report text: plain, pre-wrapped, clamped until expanded. */
function ClampedText({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const long = text.length > 280 || text.split("\n").length > 4;
  return (
    <div className="loop-evidence">
      <div className={`pre-text${long && !open ? " clamped" : ""}`}>{text}</div>
      {long && (
        <button type="button" className="link small" onClick={() => setOpen((o) => !o)}>
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </div>
  );
}
