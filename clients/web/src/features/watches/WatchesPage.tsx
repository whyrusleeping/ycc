// The watches surface (`/p/<project>/watches[/<name>]`, docs/design/watches.md):
// a project's watches with latest status, schedule and waiting questions; one
// watch's definition, notes, standing decisions and report timeline with links
// to run sessions; pause/resume, run now, create/edit and confirmed delete.
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import type { Watch, WatchReport } from "../../gen/ycc/v1/ycc_pb";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useModels, useWatch, useWatches } from "../../api/queries";
import { paths } from "../../app/paths";
import { track } from "../../app/analytics";
import { Icon } from "../../ui/icons";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { Modal } from "../../ui/Modal";
import { toast } from "../../ui/toast";
import { relativeTime } from "../sessions/feed";
import {
  buildDefinition,
  draftError,
  draftFromWatch,
  emptyDraft,
  formatInterval,
  kindNote,
  parkedLabel,
  reportLabel,
  reportTone,
  scheduleState,
  sortWatches,
  untilTime,
  type IntervalUnit,
  type WatchDraft,
  type WatchTone,
} from "./model";

const REPORT_PAGE = 50;

/** WatchTone → the shared .ws-badge tone classes. */
const BADGE_TONE: Record<WatchTone, string> = { ok: "live", warn: "warn", alert: "attention", muted: "done" };

const nameOf = (w: Watch) => w.definition?.name ?? "";

function onActionError(err: unknown, what: string, op: string) {
  if (isUnauthorized(err)) {
    authStore.expire();
    return;
  }
  toast(`${what}: ${errorMessage(err)}`, "error", { op, err });
}

/** Pause/resume, run now and delete for one watch, keeping every cached copy in step. */
function useWatchActions(project: string) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const install = useCallback(
    (w: Watch) => {
      const name = nameOf(w);
      qc.setQueryData<Watch[]>(queryKeys.watches(project), (rows) => rows?.map((r) => (nameOf(r) === name ? w : r)));
      void qc.invalidateQueries({ queryKey: queryKeys.watchesAll });
    },
    [qc, project],
  );
  const run = async (name: string, op: string, what: string, fn: () => Promise<void>) => {
    if (busy) return false;
    setBusy(name);
    try {
      await fn();
      return true;
    } catch (err) {
      onActionError(err, what, op);
      return false;
    } finally {
      setBusy(null);
    }
  };
  return {
    busy,
    install,
    setPaused: (w: Watch, paused: boolean) =>
      run(nameOf(w), paused ? "watches.pause" : "watches.resume", paused ? "Couldn’t pause the watch" : "Couldn’t resume the watch", async () => {
        const resp = await client.setWatchPaused({ project, name: nameOf(w), paused });
        if (resp.watch) install(resp.watch);
      }),
    runNow: (w: Watch) =>
      run(nameOf(w), "watches.run_now", "Couldn’t start a run", async () => {
        await client.runWatchNow({ project, name: nameOf(w) });
        toast(`Started a run of ${nameOf(w)}.`, "info");
        void qc.invalidateQueries({ queryKey: queryKeys.watchesAll });
      }),
    remove: (w: Watch) =>
      run(nameOf(w), "watches.delete", "Couldn’t delete the watch", async () => {
        await client.deleteWatch({ project, name: nameOf(w) });
        qc.setQueryData<Watch[]>(queryKeys.watches(project), (rows) => rows?.filter((r) => nameOf(r) !== nameOf(w)));
        void qc.invalidateQueries({ queryKey: queryKeys.watchesAll });
        toast(`Deleted ${nameOf(w)}.`, "info");
      }),
  };
}

export function WatchesPage({ project, name }: { project: string; name: string | null }) {
  return name ? <WatchDetail key={name} project={project} name={name} /> : <WatchList project={project} />;
}

function WatchList({ project }: { project: string }) {
  const q = useWatches(project);
  const actions = useWatchActions(project);
  const [editing, setEditing] = useState<Watch | "new" | null>(null);
  const [deleting, setDeleting] = useState<Watch | null>(null);
  const list = useMemo(() => sortWatches(q.data ?? []), [q.data]);
  return (
    <div className="page workstreams-page watches-page">
      <header className="page-head">
        <h1>
          Watches {project && <span className="muted">· {project}</span>}
        </h1>
        <div className="page-actions">
          <button
            type="button"
            className="btn ghost"
            onClick={() => void q.refetch()}
            disabled={q.isFetching}
            data-track="watches.refresh"
            title="Refresh"
            aria-label="Refresh watches"
          >
            <Icon name="refresh" size={15} />
          </button>
          <button type="button" className="btn primary" data-track="watches.create" onClick={() => setEditing("new")}>
            + New watch
          </button>
        </div>
      </header>
      {q.isPending ? (
        <p className="muted">Loading watches…</p>
      ) : q.isError && !q.data ? (
        <p className="error">{errorMessage(q.error, "Couldn’t load watches.")}</p>
      ) : (
        <>
          {q.isError && <p className="warn small">Couldn’t refresh: {errorMessage(q.error)}</p>}
          {list.length === 0 ? (
            <div className="loop-empty">
              <h2>No watches</h2>
              <p className="muted">
                A watch is a recurring check the daemon runs on a schedule: each run is a short session that reads the brief and
                the previous run’s notes, checks the system, and reports <code>ok</code>, <code>warn</code> or <code>alert</code>.
                Questions park only their own run. Ask a session to “check this every hour”, or create one here.
              </p>
              <p>
                <button type="button" className="btn primary" data-track="watches.create" onClick={() => setEditing("new")}>
                  Create a watch
                </button>
              </p>
            </div>
          ) : (
            <ul className="ws-list" aria-label="Watches">
              {list.map((w) => (
                <WatchRow
                  key={nameOf(w)}
                  project={project}
                  watch={w}
                  busy={actions.busy === nameOf(w)}
                  onPause={(paused) => void actions.setPaused(w, paused)}
                  onRunNow={() => void actions.runNow(w)}
                  onEdit={() => setEditing(w)}
                  onDelete={() => setDeleting(w)}
                />
              ))}
            </ul>
          )}
        </>
      )}
      <WatchEditor project={project} watch={editing} onClose={() => setEditing(null)} onSaved={actions.install} />
      <ConfirmDialog
        open={deleting !== null}
        title={`Delete ${deleting ? nameOf(deleting) : "watch"}?`}
        body="This removes the watch, its notes, standing decisions and report history. Its run sessions stay in the session history."
        confirmLabel="Delete"
        danger
        action="watches.delete"
        onConfirm={() => {
          const w = deleting;
          setDeleting(null);
          if (w) void actions.remove(w);
        }}
        onCancel={() => setDeleting(null)}
      />
    </div>
  );
}

function StatusBadge({ report }: { report: WatchReport | undefined }) {
  return <span className={`ws-badge tone-${BADGE_TONE[reportTone(report)]}`}>{reportLabel(report)}</span>;
}

function ScheduleBadge({ watch }: { watch: Watch }) {
  const s = scheduleState(watch);
  return (
    <span className={`ws-badge tone-${s.running ? "live" : "done"}`} title={watch.nextDue && !watch.paused ? new Date(watch.nextDue).toLocaleString() : undefined}>
      {s.running && <span className="spinner" aria-hidden="true" />}
      {s.label}
    </span>
  );
}

/** Links to runs waiting on an answer (the first one when there are several). */
function ParkedLink({ project, watch }: { project: string; watch: Watch }) {
  const label = parkedLabel(watch);
  if (!label) return null;
  return (
    <Link to={paths.session(project, watch.parkedSessionIds[0])} className="tag warn" data-track="watches.parked" title="Open the run that asked">
      {label}
    </Link>
  );
}

function WatchControls({
  watch,
  busy,
  onPause,
  onRunNow,
  onEdit,
  onDelete,
}: {
  watch: Watch;
  busy: boolean;
  onPause: (paused: boolean) => void;
  onRunNow: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const expired = watch.paused && watch.pausedReason === "expired";
  return (
    <>
      <button
        type="button"
        className="btn small"
        disabled={busy || watch.running}
        title={watch.running ? "A run is already active" : "Start a run now without changing the schedule"}
        data-track="watches.run_now"
        onClick={onRunNow}
      >
        Run now
      </button>
      {expired ? (
        <button type="button" className="btn small" disabled={busy} title="It expired: set a later expiry to resume it" data-track="watches.extend" onClick={onEdit}>
          Extend…
        </button>
      ) : (
        <button
          type="button"
          className="btn small"
          disabled={busy}
          data-track={watch.paused ? "watches.resume" : "watches.pause"}
          title={watch.paused ? "Resume the schedule (missed runs are not replayed)" : "Stop scheduling runs until resumed"}
          onClick={() => onPause(!watch.paused)}
        >
          {watch.paused ? "Resume" : "Pause"}
        </button>
      )}
      <button type="button" className="btn small" disabled={busy} data-track="watches.edit" onClick={onEdit}>
        Edit…
      </button>
      <button type="button" className="btn small ghost danger-text" disabled={busy} data-track="watches.delete" onClick={onDelete}>
        Delete…
      </button>
    </>
  );
}

function WatchRow({
  project,
  watch: w,
  busy,
  ...controls
}: {
  project: string;
  watch: Watch;
  busy: boolean;
  onPause: (paused: boolean) => void;
  onRunNow: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const d = w.definition;
  const tone = reportTone(w.last);
  return (
    <li className={`ws-row${tone === "alert" ? " tone-attention" : ""}${w.paused ? " watch-paused" : ""}`} data-name={nameOf(w)}>
      <div className="ws-row-head">
        <Link to={paths.watches(project, nameOf(w))} className="ws-branch mono" data-track="watches.open">
          {nameOf(w)}
        </Link>
        <StatusBadge report={w.last} />
        <ScheduleBadge watch={w} />
        <ParkedLink project={project} watch={w} />
      </div>
      <div className="ws-meta muted small">
        <span>every {formatInterval(d?.intervalSecs ?? 0n)}</span>
        {w.last?.at && <span title={new Date(w.last.at).toLocaleString()}>last {relativeTime(w.last.at)}</span>}
        {d?.probe && <span>probe{d.probeAlways ? " + run" : ""}</span>}
        {d?.model && <span>model {d.model}</span>}
        {d?.expires && !w.paused && <span title={new Date(d.expires).toLocaleString()}>expires {untilTime(d.expires).replace(/^due now$/, "now")}</span>}
      </div>
      {w.last?.summary && <div className="watch-summary pre-text clamped">{w.last.summary}</div>}
      <div className="ws-actions">
        <WatchControls watch={w} busy={busy} {...controls} />
      </div>
    </li>
  );
}

function WatchDetail({ project, name }: { project: string; name: string }) {
  const [limit, setLimit] = useState(REPORT_PAGE);
  const q = useWatch(project, name, limit);
  const actions = useWatchActions(project);
  const navigate = useNavigate();
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const w = q.data?.watch;
  const reports = q.data?.reports ?? [];
  const d = w?.definition;
  return (
    <div className="page workstreams-page watches-page">
      <header className="page-head">
        <h1>
          <Link to={paths.watches(project)} className="muted" data-track="watches.back">
            Watches
          </Link>{" "}
          <span className="muted">›</span> <span className="mono">{name}</span>
        </h1>
        <div className="page-actions">
          <button
            type="button"
            className="btn ghost"
            onClick={() => void q.refetch()}
            disabled={q.isFetching}
            data-track="watches.refresh"
            title="Refresh"
            aria-label="Refresh watch"
          >
            <Icon name="refresh" size={15} />
          </button>
          {w && (
            <WatchControls
              watch={w}
              busy={actions.busy === name}
              onPause={(paused) => void actions.setPaused(w, paused)}
              onRunNow={() => void actions.runNow(w)}
              onEdit={() => setEditing(true)}
              onDelete={() => setDeleting(true)}
            />
          )}
        </div>
      </header>
      {q.isPending ? (
        <p className="muted">Loading watch…</p>
      ) : !w ? (
        <p className="error">{q.isError ? errorMessage(q.error, "Couldn’t load the watch.") : "Watch not found."}</p>
      ) : (
        <>
          {q.isError && <p className="warn small">Couldn’t refresh: {errorMessage(q.error)}</p>}
          <div className="ws-row-head watch-status">
            <StatusBadge report={w.last} />
            <ScheduleBadge watch={w} />
            {w.paused && <span className="muted small">{w.pausedReason === "expired" ? "Its expiry passed." : "Paused by you."}</span>}
          </div>
          {w.parkedSessionIds.length > 0 && (
            <section className="watch-section" aria-labelledby="watch-parked">
              <h2 id="watch-parked">Waiting for you</h2>
              <p className="muted small">These runs asked a question and are parked until you answer; later runs carry on.</p>
              <ul className="plain-list">
                {w.parkedSessionIds.map((id) => (
                  <li key={id}>
                    <Link to={paths.session(project, id)} className="btn small primary" data-track="watches.parked">
                      Answer in run {id}
                    </Link>
                  </li>
                ))}
              </ul>
            </section>
          )}
          <section className="watch-section" aria-labelledby="watch-definition">
            <h2 id="watch-definition">Definition</h2>
            <dl className="task-meta">
              <dt>Brief</dt>
              <dd className="pre-text">{d?.brief}</dd>
              <dt>Every</dt>
              <dd>{formatInterval(d?.intervalSecs ?? 0n)}</dd>
              <dt>Model</dt>
              <dd>{d?.model || <span className="muted">coordinator default</span>}</dd>
              <dt>Probe</dt>
              <dd>
                {d?.probe ? (
                  <>
                    <code className="pre-text">{d.probe}</code>
                    <div className="muted small">
                      {d.probeAlways ? "A model run follows every probe." : "A passing probe (exit 0) records ok without a model run."}
                    </div>
                  </>
                ) : (
                  <span className="muted">none</span>
                )}
              </dd>
              <dt>Expires</dt>
              <dd>{d?.expires ? new Date(d.expires).toLocaleString() : <span className="muted">never</span>}</dd>
              <dt>Allowed actions</dt>
              <dd>
                {d?.allowedActions.length ? (
                  <ul className="watch-actions-list">
                    {d.allowedActions.map((a, i) => (
                      <li key={i}>{a}</li>
                    ))}
                  </ul>
                ) : (
                  <span className="muted">low-risk only</span>
                )}
              </dd>
              {w.createdAt && (
                <>
                  <dt>Created</dt>
                  <dd title={new Date(w.createdAt).toLocaleString()}>{relativeTime(w.createdAt)}</dd>
                </>
              )}
            </dl>
          </section>
          <section className="watch-section" aria-labelledby="watch-notes">
            <h2 id="watch-notes">Notes</h2>
            {w.notes ? <div className="watch-notes pre-text">{w.notes}</div> : <p className="muted small">No notes yet: each run passes its notes to the next.</p>}
          </section>
          {w.decisions.length > 0 && (
            <section className="watch-section" aria-labelledby="watch-decisions">
              <h2 id="watch-decisions">
                Standing decisions <span className="count muted">{w.decisions.length}</span>
              </h2>
              <ul className="ws-list compact">
                {w.decisions.map((dec, i) => (
                  <li key={`${dec.sessionId}:${dec.seq}:${i}`} className="ws-row">
                    <div className="watch-question">{dec.question}</div>
                    <div className="watch-answer pre-text">
                      <span className="muted">Answer: </span>
                      {dec.answer}
                    </div>
                    <div className="ws-meta muted small">
                      {dec.at && <span title={new Date(dec.at).toLocaleString()}>{relativeTime(dec.at)}</span>}
                      {dec.sessionId && (
                        <Link to={paths.session(project, dec.sessionId)} data-track="watches.decision_session">
                          run session
                        </Link>
                      )}
                    </div>
                  </li>
                ))}
              </ul>
            </section>
          )}
          <section className="watch-section" aria-labelledby="watch-reports">
            <h2 id="watch-reports">
              Reports <span className="count muted">{reports.length}</span>
            </h2>
            {reports.length === 0 ? (
              <p className="muted small">No reports yet.</p>
            ) : (
              <ol className="ws-list watch-timeline" aria-label="Report timeline, newest first">
                {reports.map((r, i) => (
                  <ReportRow key={`${r.at}:${i}`} project={project} report={r} />
                ))}
              </ol>
            )}
            {reports.length >= limit && (
              <p>
                <button type="button" className="btn small" disabled={q.isFetching} data-track="watches.older" onClick={() => setLimit((n) => n + REPORT_PAGE)}>
                  {q.isFetching ? "Loading…" : "Show older reports"}
                </button>
              </p>
            )}
          </section>
        </>
      )}
      <WatchEditor project={project} watch={editing && w ? w : null} onClose={() => setEditing(false)} onSaved={actions.install} />
      <ConfirmDialog
        open={deleting}
        title={`Delete ${name}?`}
        body="This removes the watch, its notes, standing decisions and report history. Its run sessions stay in the session history."
        confirmLabel="Delete"
        danger
        action="watches.delete"
        onConfirm={() => {
          setDeleting(false);
          if (w)
            void actions.remove(w).then((ok) => {
              if (ok) navigate(paths.watches(project));
            });
        }}
        onCancel={() => setDeleting(false)}
      />
    </div>
  );
}

function ReportRow({ project, report: r }: { project: string; report: WatchReport }) {
  const tone = reportTone(r);
  const late = r.scheduledFor && r.at && Date.parse(r.at) - Date.parse(r.scheduledFor) > 120_000;
  return (
    <li className={`ws-row watch-report kind-${r.kind}${tone === "alert" ? " tone-attention" : ""}`}>
      <div className="ws-row-head">
        <span className="watch-when" title={r.at ? new Date(r.at).toLocaleString() : undefined}>
          {r.at ? relativeTime(r.at) : "—"}
        </span>
        {r.kind === "skipped" ? <span className="ws-badge tone-done">skipped</span> : <StatusBadge report={r} />}
        {kindNote(r.kind) && <span className="muted small">{kindNote(r.kind)}</span>}
        {r.sessionId && (
          <Link to={paths.session(project, r.sessionId)} className="btn small" data-track="watches.run_session">
            Run session
          </Link>
        )}
      </div>
      {r.summary && <div className="watch-summary pre-text">{r.summary}</div>}
      {r.actions.length > 0 && (
        <ul className="watch-actions-list">
          {r.actions.map((a, i) => (
            <li key={i}>
              {a.action}
              {a.undo && <span className="muted"> — undo: {a.undo}</span>}
            </li>
          ))}
        </ul>
      )}
      {late && (
        <div className="ws-meta muted small">
          <span>scheduled for {new Date(r.scheduledFor).toLocaleString()}</span>
        </div>
      )}
    </li>
  );
}

const UNITS: { value: IntervalUnit; label: string }[] = [
  { value: "minutes", label: "minutes" },
  { value: "hours", label: "hours" },
  { value: "days", label: "days" },
];

/** Create (`watch` "new") or edit a watch; the name is fixed once created. */
function WatchEditor({
  project,
  watch,
  onClose,
  onSaved,
}: {
  project: string;
  watch: Watch | "new" | null;
  onClose: () => void;
  onSaved: (w: Watch) => void;
}) {
  const open = watch !== null;
  const creating = watch === "new";
  const qc = useQueryClient();
  const navigate = useNavigate();
  const models = useModels("", open);
  const [draft, setDraft] = useState<WatchDraft>(emptyDraft);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Seed the draft once per opening: the detail page passes its polled watch,
  // and a poll (a run starting, a new report) must not discard unsaved edits.
  const target = watch === null ? null : watch === "new" ? "new" : `watch:${nameOf(watch)}`;
  const latest = useRef(watch);
  latest.current = watch;
  useEffect(() => {
    const w = latest.current;
    if (w === null) return;
    setDraft(w === "new" ? emptyDraft() : draftFromWatch(w));
    setError(null);
  }, [target]);
  const set = <K extends keyof WatchDraft>(k: K, v: WatchDraft[K]) => {
    setDraft((d) => ({ ...d, [k]: v }));
    setError(null);
  };
  const modelChoices = (models.data?.models ?? []).filter((m) => !m.disabled || m.name === draft.model);
  const problem = draftError(draft, creating);

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (busy) return;
    if (problem) {
      setError(problem);
      track.error(creating ? "watches.create" : "watches.edit", "invalid");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const definition = buildDefinition(draft);
      const resp = creating ? await client.createWatch({ project, definition }) : await client.updateWatch({ project, definition });
      if (resp.watch) {
        if (creating) qc.setQueryData<Watch[]>(queryKeys.watches(project), (rows) => (rows ? [...rows, resp.watch!] : rows));
        onSaved(resp.watch);
      }
      track.submit(creating ? "watch_create" : "watch_edit", { probe: definition.probe !== "", expires: definition.expires !== "" });
      toast(creating ? `Created ${definition.name}; its first run starts now.` : `Saved ${definition.name}.`, "info");
      onClose();
      if (creating) navigate(paths.watches(project, String(definition.name)));
    } catch (err) {
      if (isUnauthorized(err)) {
        authStore.expire();
        return;
      }
      track.error(creating ? "watches.create" : "watches.edit", err);
      setError(`Not saved: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={() => !busy && onClose()}
      title={typeof watch === "object" && watch ? `Edit ${nameOf(watch)}` : "New watch"}
      className="watch-editor"
      view="watch_editor"
      flow={creating ? "watch_create" : "watch_edit"}
    >
      <form
        className="task-editor"
        onSubmit={(e) => void submit(e)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
            e.preventDefault();
            void submit();
          }
        }}
      >
        <p className="muted small">
          {project ? `In ${project}. ` : ""}Each run is an unattended session that may take low-risk, reversible actions (and the
          ones you allow below) without asking, and asks about anything else.
          {creating && " The first run starts as soon as you create it."}
        </p>
        {error && (
          <div className="banner error" role="alert">
            {error}
          </div>
        )}
        <label className="field-label">
          Name <span className="muted small">lowercase letters, digits, - or _</span>
          <input
            className="field mono"
            value={draft.name}
            readOnly={!creating}
            disabled={!creating}
            placeholder="e.g. prod-ingest"
            onChange={(e) => set("name", e.target.value)}
            autoFocus={creating}
          />
        </label>
        <label className="field-label">
          Brief <span className="muted small">what each run checks, and what counts as wrong</span>
          <textarea rows={5} value={draft.brief} onChange={(e) => set("brief", e.target.value)} autoFocus={!creating} />
        </label>
        <div className="editor-row">
          <label className="field-label">
            Every
            <input className="field" type="number" min={1} step={1} value={draft.every} onChange={(e) => set("every", e.target.value)} />
          </label>
          <label className="field-label">
            Unit
            <select value={draft.unit} onChange={(e) => set("unit", e.target.value as IntervalUnit)}>
              {UNITS.map((u) => (
                <option key={u.value} value={u.value}>
                  {u.label}
                </option>
              ))}
            </select>
          </label>
          <label className="field-label grow">
            Model
            <select value={draft.model} onChange={(e) => set("model", e.target.value)} disabled={models.isPending}>
              <option value="">Coordinator default</option>
              {modelChoices.map((m) => (
                <option key={m.name} value={m.name}>
                  {m.name}
                  {m.disabled ? " (disabled)" : ""}
                </option>
              ))}
            </select>
          </label>
        </div>
        <label className="field-label">
          Probe <span className="muted small">optional shell command run first in the project root; exit 0 records ok without a model run</span>
          <input className="field mono" value={draft.probe} placeholder="e.g. curl -fsS https://example.com/health" onChange={(e) => set("probe", e.target.value)} />
        </label>
        {draft.probe.trim() && (
          <label className="field-label check-label">
            <input type="checkbox" checked={draft.probeAlways} onChange={(e) => set("probeAlways", e.target.checked)} />
            Start a model run even when the probe passes
          </label>
        )}
        <label className="field-label">
          Expires <span className="muted small">optional · the watch pauses itself then</span>
          <input className="field" type="datetime-local" value={draft.expires} onChange={(e) => set("expires", e.target.value)} />
        </label>
        <label className="field-label">
          Allowed actions <span className="muted small">optional · one per line, beyond the low-risk default</span>
          <textarea rows={3} value={draft.allowedActions} placeholder="e.g. restart the ingest worker" onChange={(e) => set("allowedActions", e.target.value)} />
        </label>
        <div className="editor-actions">
          <span className="muted small">Ctrl/⌘+Enter saves</span>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy || problem !== null} title={problem ?? undefined}>
            {busy ? "Saving…" : creating ? "Create watch" : "Save"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
