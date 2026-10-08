// The workstreams surface (`/p/<project>/workstreams`): parallel worktrees
// with per-stream lifecycle and integration-queue state, Spawn, Preview and
// Merge (the integrated diff opens in the inspector), Retry integration,
// confirmed Discard, and links to each stream's session and integration log.
// Mirrors iOS WorkstreamsView.
import { Icon } from "../../ui/icons";
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { Link } from "react-router";
import type { WorkstreamInfo } from "../../gen/ycc/v1/ycc_pb";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useBacklog, useWorkstreams } from "../../api/queries";
import { paths } from "../../app/paths";
import { useIntent } from "../../app/intents";
import { useAction, type AppAction } from "../../app/actions";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { Modal } from "../../ui/Modal";
import { toast } from "../../ui/toast";
import { track } from "../../app/analytics";
import { useInspector } from "../inspector/inspector";
import { TaskLink } from "../backlog/parts";
import { relativeTime } from "../sessions/feed";
import {
  badgeTitle,
  badgeTone,
  branchLabel,
  buildSpawnRequest,
  canRetry,
  canSpawn,
  commitSummary,
  groupWorkstreams,
  integrationMode,
  integrationState,
  isDiscardable,
  isGateEligible,
  isMergeable,
  isTerminal,
  mergeAllMessage,
  mergeAllReady,
  spawnableTasks,
  statusReason,
  taskPrompt,
  workstreamStatus,
  type SpawnDraft,
} from "./model";

export const workstreamsIntentKey = (project: string) => `workstreams:${project}`;

function useActionError() {
  return useCallback((err: unknown, what: string, op: string) => {
    if (isUnauthorized(err)) {
      authStore.expire();
      return;
    }
    toast(`${what}: ${errorMessage(err)}`, "error", { op, err });
  }, []);
}

const MODE_NOTES: Record<string, string> = {
  auto: "auto — a ready workstream is rebased, verified, and fast-forwarded onto its base without review",
  gate: "gate — each ready workstream waits for you to review and accept its merge",
  manual: "manual — merge each workstream yourself",
};

export function WorkstreamsPage({ project }: { project: string }) {
  const q = useWorkstreams(project);
  const qc = useQueryClient();
  const inspector = useInspector();
  const onError = useActionError();
  const [spawnOpen, setSpawnOpen] = useState(false);
  const [discard, setDiscard] = useState<WorkstreamInfo | null>(null);
  const [mergeAllOpen, setMergeAllOpen] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const list = q.data;
  const { inFlight, finished } = useMemo(() => groupWorkstreams(list ?? []), [list]);
  const gated = useMemo(() => inFlight.filter(isGateEligible), [inFlight]);
  const mode = inFlight.map(integrationMode).find((m) => m) ?? "";
  const queue = useMemo(() => {
    const integrating = inFlight.filter((w) => integrationState(w) === "integrating").length;
    const queued = inFlight.filter((w) => integrationState(w) === "queued").length;
    return { integrating, queued };
  }, [inFlight]);

  useIntent(
    workstreamsIntentKey(project),
    useCallback((what: string) => {
      if (what === "spawn") setSpawnOpen(true);
    }, []),
  );

  // Palette: merge every gated stream (asks first, like the button).
  const gatedCount = gated.length;
  useAction(
    useMemo<AppAction | null>(
      () =>
        gatedCount > 0
          ? { id: "workstreams.mergeAll", title: `Merge all ready workstreams (${gatedCount})…`, group: "Workstreams", run: () => setMergeAllOpen(true) }
          : null,
      [gatedCount],
    ),
  );

  const refresh = () => void qc.invalidateQueries({ queryKey: queryKeys.workstreamsAll });
  const patch = (id: string, change: (w: WorkstreamInfo) => WorkstreamInfo) =>
    qc.setQueryData<WorkstreamInfo[]>(queryKeys.workstreams(project), (rows) => rows?.map((w) => (w.id === id ? change(w) : w)));

  const retry = async (w: WorkstreamInfo) => {
    if (busyId) return;
    setBusyId(w.id);
    try {
      const resp = await client.retryIntegration({ workstreamId: w.id });
      if (resp.workstream?.id) patch(w.id, () => resp.workstream!);
      toast(`Retrying integration of ${branchLabel(w)}.`, "info");
      refresh();
    } catch (err) {
      onError(err, "Couldn’t retry integration", "workstreams.retry");
    } finally {
      setBusyId(null);
    }
  };

  const doDiscard = async (w: WorkstreamInfo) => {
    setDiscard(null);
    if (busyId) return;
    setBusyId(w.id);
    try {
      await client.discardWorkstream({ workstreamId: w.id });
      patch(w.id, (row) => ({ ...row, status: "discarded", integrationState: "" }));
      if (inspector.item?.kind === "merge" && inspector.item.workstreamId === w.id) inspector.close();
      toast(`Discarded ${branchLabel(w)}.`, "info");
      refresh();
    } catch (err) {
      onError(err, "Couldn’t discard the workstream", "workstreams.discard");
    } finally {
      setBusyId(null);
    }
  };

  const doMergeAll = async () => {
    setMergeAllOpen(false);
    setBusyId("*");
    try {
      const summary = await mergeAllReady(
        gated,
        async (id) => {
          const r = await client.mergeWorkstream({ workstreamId: id, accept: true });
          if (r.merged) patch(id, (row) => ({ ...row, status: "merged", integrationState: "" }));
          return r;
        },
        (err) => errorMessage(err),
      );
      toast(mergeAllMessage(summary), summary.error ? "error" : "info", { op: "workstreams.merge_all" });
      refresh();
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div className="page workstreams-page">
      <header className="page-head">
        <h1>
          Workstreams {project && <span className="muted">· {project}</span>}
        </h1>
        <div className="page-actions">
          <button
            type="button"
            className="btn ghost"
            onClick={() => void q.refetch()}
            disabled={q.isFetching}
            data-track="workstreams.refresh"
            title="Refresh"
            aria-label="Refresh workstreams"
          >
            <Icon name="refresh" size={15} />
          </button>
          {gated.length > 0 && (
            <button type="button" className="btn" disabled={busyId !== null} data-track="workstreams.mergeAll" onClick={() => setMergeAllOpen(true)}>
              Merge all ready ({gated.length})
            </button>
          )}
          <button
            type="button"
            className="btn primary"
            disabled={!project}
            title={project ? "Start a work session in a new worktree and branch" : "Choose a project first"}
            data-track="workstreams.spawn"
            onClick={() => setSpawnOpen(true)}
          >
            + Spawn workstream
          </button>
        </div>
      </header>
      {(mode || queue.integrating > 0 || queue.queued > 0) && (
        <div className="ws-queue muted small" aria-label="Integration queue">
          {mode && <span>Integration: {MODE_NOTES[mode] ?? mode}</span>}
          {(queue.integrating > 0 || queue.queued > 0) && (
            <span className="ws-queue-state">
              Queue: {queue.integrating} integrating · {queue.queued} queued
            </span>
          )}
        </div>
      )}
      {q.isPending ? (
        <p className="muted">Loading workstreams…</p>
      ) : q.isError && !list ? (
        <p className="error">{errorMessage(q.error, "Couldn’t load workstreams.")}</p>
      ) : (
        <>
          {q.isError && <p className="warn small">Couldn’t refresh: {errorMessage(q.error)}</p>}
          {inFlight.length === 0 ? (
            <div className="loop-empty">
              <h2>No workstreams in flight</h2>
              <p className="muted">
                A workstream is a parallel <code>work</code> session in its own git worktree and branch. When it finishes with
                commits it becomes ready to integrate back onto the project’s base branch.
              </p>
              {project && (
                <p>
                  <button type="button" className="btn primary" data-track="workstreams.spawn" onClick={() => setSpawnOpen(true)}>
                    Spawn a workstream
                  </button>
                </p>
              )}
            </div>
          ) : (
            <ul className="ws-list" aria-label="Workstreams in flight">
              {inFlight.map((w) => (
                <WorkstreamRow
                  key={w.id}
                  ws={w}
                  showProject={!project}
                  busy={busyId === w.id || busyId === "*"}
                  previewing={inspector.item?.kind === "merge" && inspector.item.workstreamId === w.id}
                  onPreview={() => inspector.open({ kind: "merge", project: w.project, workstreamId: w.id, branch: branchLabel(w) })}
                  onRetry={() => void retry(w)}
                  onDiscard={() => setDiscard(w)}
                />
              ))}
            </ul>
          )}
          {finished.length > 0 && (
            <details className="ws-history" onToggle={(e) => e.currentTarget.open && track.action("workstreams.history", "click")}>
              <summary>
                Merged and discarded <span className="count muted">{finished.length}</span>
              </summary>
              <ul className="ws-list compact">
                {finished.map((w) => (
                  <WorkstreamRow key={w.id} ws={w} showProject={!project} busy={false} previewing={false} />
                ))}
              </ul>
            </details>
          )}
        </>
      )}
      <SpawnDialog project={project} open={spawnOpen} onClose={() => setSpawnOpen(false)} />
      <ConfirmDialog
        open={discard !== null}
        title={`Discard ${discard ? branchLabel(discard) : "workstream"}?`}
        body="This stops its session and deletes the worktree and branch without merging. It cannot be undone."
        confirmLabel="Discard"
        danger
        action="workstreams.discard"
        onConfirm={() => discard && void doDiscard(discard)}
        onCancel={() => setDiscard(null)}
      />
      <ConfirmDialog
        open={mergeAllOpen}
        title="Merge all ready workstreams?"
        body={`${gated.length} gated workstream${gated.length === 1 ? "" : "s"} will be merged one after another without individual review. The operation stops at the first failure.`}
        confirmLabel="Merge all ready"
        action="workstreams.mergeAll"
        onConfirm={() => void doMergeAll()}
        onCancel={() => setMergeAllOpen(false)}
      />
    </div>
  );
}

function WorkstreamRow({
  ws: w,
  showProject,
  busy,
  previewing,
  onPreview,
  onRetry,
  onDiscard,
}: {
  ws: WorkstreamInfo;
  showProject: boolean;
  busy: boolean;
  previewing: boolean;
  onPreview?: () => void;
  onRetry?: () => void;
  onDiscard?: () => void;
}) {
  const status = workstreamStatus(w);
  const reason = statusReason(w);
  const tone = badgeTone(w);
  const created = w.createdAt ? relativeTime(w.createdAt) : "";
  return (
    <li className={`ws-row tone-${tone}${previewing ? " previewing" : ""}`} data-id={w.id}>
      <div className="ws-row-head">
        <span className="ws-branch mono" title={w.worktreePath || undefined}>
          {branchLabel(w)}
        </span>
        <span className={`ws-badge tone-${tone}`}>
          {integrationState(w) === "integrating" && <span className="spinner" aria-hidden="true" />}
          {badgeTitle(w)}
        </span>
        {showProject && w.project && <span className="tag">{w.project}</span>}
        {w.taskId && <TaskLink project={w.project} id={w.taskId} className="task-chip" />}
      </div>
      <div className="ws-meta muted small">
        {/* Commit counts are only computed for in-flight streams. */}
        {!isTerminal(status) && <span>{commitSummary(w)}</span>}
        {w.baseBranch && <span>onto {w.baseBranch}</span>}
        {w.sessionStatus && <span>session {w.sessionStatus}</span>}
        {created && <span>{created}</span>}
        <span className="mono">{w.id}</span>
      </div>
      {reason && status === "needs_attention" && (
        <div className="ws-reason" role="note">
          {reason}
        </div>
      )}
      <div className="ws-actions">
        {w.sessionId && (
          <Link to={paths.session(w.project, w.sessionId)} className="btn small" data-track="workstreams.open_session">
            Open session
          </Link>
        )}
        {w.integrateSessionId && (
          <Link to={paths.session(w.project, w.integrateSessionId)} className="btn small" title="The integration agent’s session" data-track="workstreams.integration_log">
            Integration log
          </Link>
        )}
        {onPreview && isMergeable(status) && (
          <button type="button" className="btn small" disabled={busy} data-track="workstreams.preview" onClick={onPreview}>
            Preview &amp; merge…
          </button>
        )}
        {onRetry && canRetry(w) && (
          <button
            type="button"
            className={`btn small${status === "needs_attention" ? " primary" : ""}`}
            disabled={busy}
            data-track="workstreams.retry"
            onClick={onRetry}
            title="Re-queue automatic integration"
          >
            Retry integration
          </button>
        )}
        {onDiscard && isDiscardable(status) && (
          <button type="button" className="btn small ghost danger-text" disabled={busy} data-track="workstreams.discard" onClick={onDiscard}>
            Discard…
          </button>
        )}
      </div>
    </li>
  );
}

const EMPTY_DRAFT: SpawnDraft = { taskId: "", prompt: "", baseRef: "" };

function SpawnDialog({ project, open, onClose }: { project: string; open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const backlog = useBacklog(project, open && project !== "");
  const tasks = useMemo(() => spawnableTasks(backlog.data ?? []), [backlog.data]);
  const [draft, setDraft] = useState<SpawnDraft>(EMPTY_DRAFT);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (open) {
      setDraft(EMPTY_DRAFT);
      setError(null);
    }
  }, [open]);

  const chooseTask = (id: string) => {
    setDraft((d) => {
      const prev = tasks.find((t) => t.id === d.taskId);
      const next = tasks.find((t) => t.id === id);
      // Replace the prompt only while it is still the previous task's default.
      const autoPrompt = d.prompt.trim() === "" || (prev && d.prompt === taskPrompt(prev));
      return { ...d, taskId: id, prompt: autoPrompt ? (next ? taskPrompt(next) : "") : d.prompt };
    });
    setError(null);
  };

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (busy) return;
    if (!canSpawn(project, draft)) {
      setError("Choose a task or describe the work.");
      track.error("workstreams.spawn", "required");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const resp = await client.spawnWorkstream(buildSpawnRequest(project, draft));
      const w = resp.workstream;
      if (w) {
        qc.setQueryData<WorkstreamInfo[]>(queryKeys.workstreams(project), (rows) => (rows ? [w, ...rows.filter((r) => r.id !== w.id)] : rows));
        toast(`Spawned ${branchLabel(w)}.`, "info");
      }
      void qc.invalidateQueries({ queryKey: queryKeys.workstreamsAll });
      track.submit("workstream_spawn", { task: draft.taskId !== "", base: draft.baseRef.trim() !== "" });
      onClose();
    } catch (err) {
      if (isUnauthorized(err)) {
        authStore.expire();
        return;
      }
      track.error("workstreams.spawn", err);
      setError(`Not spawned: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open={open} onClose={() => !busy && onClose()} title="Spawn a workstream" className="ws-spawn-dialog" view="workstream_spawn" flow="workstream_spawn">
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
          In {project}: a new worktree and branch off the base, with its own <code>work</code> session.
        </p>
        {error && (
          <div className="banner error" role="alert">
            {error}
          </div>
        )}
        <label className="field-label">
          Task <span className="muted small">optional · ready tasks</span>
          <select value={draft.taskId} onChange={(e) => chooseTask(e.target.value)} disabled={backlog.isPending}>
            <option value="">No task — describe the work below</option>
            {tasks.map((t) => (
              <option key={t.id} value={t.id}>
                {t.id} · {t.title}
              </option>
            ))}
          </select>
        </label>
        {draft.taskId && (
          <p className="muted small">
            It becomes ready to integrate once the session finishes with commits and the task is in review or done.
          </p>
        )}
        <label className="field-label">
          Prompt
          <textarea
            rows={4}
            value={draft.prompt}
            placeholder="What should this workstream do?"
            onChange={(e) => {
              setDraft((d) => ({ ...d, prompt: e.target.value }));
              setError(null);
            }}
            autoFocus
          />
        </label>
        <label className="field-label">
          Base <span className="muted small">optional · branch or commit to start from</span>
          <input
            className="field mono"
            value={draft.baseRef}
            placeholder="HEAD (the project’s current branch)"
            onChange={(e) => setDraft((d) => ({ ...d, baseRef: e.target.value }))}
          />
        </label>
        <div className="editor-actions">
          <span className="muted small">Ctrl/⌘+Enter spawns</span>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy || !canSpawn(project, draft)}>
            {busy ? "Spawning…" : "Spawn"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
