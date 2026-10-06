// Task detail (backlog pane or inspector): GetTask with the markdown body and
// work log, in-place status changes (promoting proposed → todo is one click and
// never waits on a session: UpdateTask takes only the backlog store lock), the
// task's dependencies and focused sessions as links, and the editor for the
// user-maintained frontmatter and body. A failed save keeps the draft and
// shows why; a successful one replaces local state with the daemon's response.
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState, type FormEvent, type KeyboardEvent, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import type { TaskDetail } from "../../gen/ycc/v1/ycc_pb";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { installTask, queryKeys, useBacklog, useProjects, useSessionFeed, useTask } from "../../api/queries";
import { paths } from "../../app/paths";
import { Markdown } from "../markdown/Markdown";
import { FileLinksProvider, type FileLinkHandler } from "../files/FileRef";
import { useInspector } from "../inspector/inspector";
import { CopyButton } from "../../ui/CopyButton";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { toast } from "../../ui/toast";
import { displayTitle, taskIds } from "../sessions/feed";
import {
  blockedLabel,
  isActionable,
  normalizeTaskId,
  splitWorkLog,
  statusLabel,
  TASK_STATUSES,
  taskIdFromPath,
  type TaskStatus,
} from "./model";
import {
  changedFields,
  conflictMessage,
  draftFrom,
  draftStore,
  planSave,
  validateDraft,
  type DraftField,
  type OpenDraft,
  type TaskDraft,
} from "./draft";
import { PriorityBadge, StatusPill, TaskLink } from "./parts";

function handleAuth(err: unknown): boolean {
  if (isUnauthorized(err)) {
    authStore.expire();
    return true;
  }
  return false;
}

/** Change a task's status; resolves to the canonical detail (or null on failure, already reported). */
export async function changeTaskStatus(
  qc: ReturnType<typeof useQueryClient>,
  project: string,
  id: string,
  status: TaskStatus,
): Promise<TaskDetail | null> {
  try {
    const resp = await client.updateTask({ project, id, status });
    if (!resp.task) return null;
    installTask(qc, project, resp.task);
    return resp.task;
  } catch (err) {
    if (!handleAuth(err)) toast(`Couldn’t change task ${id} to ${statusLabel(status)}: ${errorMessage(err)}`);
    return null;
  }
}

export function TaskDetailView({
  project,
  taskId,
  variant,
  onClose,
}: {
  project: string;
  taskId: string;
  variant: "pane" | "inspector";
  onClose?: () => void;
}) {
  const id = normalizeTaskId(taskId);
  const q = useTask(project, id);
  const backlog = useBacklog(project);
  const projects = useProjects();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState<OpenDraft | null>(() => draftStore.get(project, id) ?? null);
  const [statusBusy, setStatusBusy] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);

  const summary = backlog.data?.find((t) => t.id === id);
  const task = q.data;
  const inInspector = variant === "inspector";

  const projectPath = projects.data?.find((p) => p.name === project)?.path ?? "";
  const inspector = useInspector();
  const showFile = inInspector ? inspector.push : inspector.open;
  const links = useMemo<FileLinkHandler>(
    () => ({
      // Task bodies live in backlog/: relative links (sibling tasks) resolve there.
      context: { project, sessionId: "", baseDirectory: "backlog", absoluteRoots: projectPath ? [projectPath] : [] },
      route: (ref) => {
        const target = taskIdFromPath(ref.path);
        return target ? paths.task(project, target) : null;
      },
      // Any other file opens in the viewer beside the task.
      open: (ref) =>
        showFile({ kind: "file", project, sessionId: "", path: ref.path, isDirectory: ref.isDirectory, lines: ref.lines }),
    }),
    [project, projectPath, showFile],
  );

  const setStatus = async (status: TaskStatus) => {
    if (statusBusy) return;
    setStatusBusy(status);
    await changeTaskStatus(qc, project, id, status);
    setStatusBusy(null);
  };

  const startWork = async (t: TaskDetail) => {
    if (starting) return;
    setStarting(true);
    try {
      const prompt = `Work on task ${t.id}${t.title ? `: ${t.title}` : ""}.`;
      const resp = await client.startSession({ project, mode: "work", prompt });
      void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
      navigate(paths.session(project, resp.sessionId));
    } catch (err) {
      if (!handleAuth(err)) toast(`Couldn’t start work on ${t.id}: ${errorMessage(err)}`);
    } finally {
      setStarting(false);
    }
  };

  const beginEdit = () => {
    if (!task) return;
    const next = { base: task, draft: draftFrom(task) };
    draftStore.set(project, id, next);
    setOpen(next);
  };
  const endEdit = () => {
    draftStore.delete(project, id);
    setOpen(null);
  };

  if (q.isError && !task) {
    const notFound = /not found|no such/i.test(errorMessage(q.error));
    return (
      <div className="task-detail">
        <TaskTopBar id={id} variant={variant} project={project} onClose={onClose} />
        <p className="error">{notFound ? `Task ${id} was not found in this backlog.` : errorMessage(q.error)}</p>
        <button type="button" className="btn small" onClick={() => void q.refetch()}>
          Retry
        </button>
      </div>
    );
  }

  // Header fields come from the backlog row while the detail loads.
  const header = task ?? summary;
  if (!header) {
    return (
      <div className="task-detail">
        <TaskTopBar id={id} variant={variant} project={project} onClose={onClose} />
        <p className="muted">Loading task…</p>
      </div>
    );
  }
  const status = header.status.toLowerCase();
  const blocked = blockedLabel(header);
  const actionable = isActionable(header);

  return (
    <div className={`task-detail variant-${variant}`}>
      <TaskTopBar id={id} variant={variant} project={project} onClose={onClose}>
        {!open && task && (
          <button type="button" className="btn small" onClick={beginEdit} title="Edit title, priority, dependencies, spec refs, and body">
            Edit
          </button>
        )}
      </TaskTopBar>
      <h2 className="task-title">{header.title || "(untitled)"}</h2>
      <div className="task-status-row">
        <StatusPill status={header.status} />
        <PriorityBadge priority={header.priority} />
        {actionable && (
          <span className="tag ok" title="Accepted work whose dependencies are done: the work loop may pick it">
            actionable
          </span>
        )}
        {status !== "done" && !header.ready && <span className="tag warn">blocked by deps</span>}
        <label className="status-select">
          <span className="sr-only">Status</span>
          <select
            aria-label="Status"
            value={(TASK_STATUSES as readonly string[]).includes(status) ? status : ""}
            disabled={statusBusy !== null}
            onChange={(e) => void setStatus(e.target.value as TaskStatus)}
          >
            {!(TASK_STATUSES as readonly string[]).includes(status) && <option value="">{header.status || "unknown"}</option>}
            {TASK_STATUSES.map((s) => (
              <option key={s} value={s}>
                {statusLabel(s)}
              </option>
            ))}
          </select>
        </label>
        {status === "proposed" && (
          <button
            type="button"
            className="btn primary small"
            disabled={statusBusy !== null}
            onClick={() => void setStatus("todo")}
            title="Accept this proposed task into the active backlog (status todo)"
          >
            {statusBusy === "todo" ? "Promoting…" : "Promote to todo"}
          </button>
        )}
        {statusBusy && statusBusy !== "todo" && <span className="muted small">Saving…</span>}
      </div>
      {open ? (
        <TaskEditor
          key={`${project}\u0000${id}`}
          project={project}
          id={id}
          open={open}
          onChange={(next) => {
            draftStore.set(project, id, next);
            setOpen(next);
          }}
          onDone={endEdit}
        />
      ) : (
        <>
          <TaskMeta project={project} task={task} header={header} blocked={blocked} inInspector={inInspector} />
          <FocusedSessions project={project} id={id} />
          {task && status !== "done" && status !== "proposed" && (
            <div className="task-actions">
              <button type="button" className="btn small" disabled={starting} onClick={() => void startWork(task)}>
                {starting ? "Starting…" : "Start work on this task"}
              </button>
            </div>
          )}
          {task ? (
            <FileLinksProvider value={links}>
              <TaskBody body={task.body} />
            </FileLinksProvider>
          ) : (
            <p className="muted">Loading…</p>
          )}
        </>
      )}
    </div>
  );
}

function TaskTopBar({
  id,
  variant,
  project,
  onClose,
  children,
}: {
  id: string;
  variant: "pane" | "inspector";
  project: string;
  onClose?: () => void;
  children?: ReactNode;
}) {
  return (
    <div className="task-topbar">
      <span className="chip task-id">{id}</span>
      <span className="spacer" />
      {children}
      {variant === "inspector" && (
        <Link to={paths.task(project, id)} className="btn ghost small" title="Open this task in the backlog browser">
          Open in backlog
        </Link>
      )}
      {variant === "pane" && onClose && (
        <button type="button" className="btn ghost small" onClick={onClose} aria-label="Close task" title="Close (Esc)">
          ×
        </button>
      )}
    </div>
  );
}

function TaskMeta({
  project,
  task,
  header,
  blocked,
  inInspector,
}: {
  project: string;
  task: TaskDetail | undefined;
  header: { dependsOn: string[]; blockedBy: string[] };
  blocked: string | null;
  inInspector: boolean;
}) {
  const backlog = useBacklog(project);
  const byId = new Map((backlog.data ?? []).map((t) => [t.id, t]));
  const blocking = new Set(header.blockedBy.map(normalizeTaskId));
  return (
    <dl className="task-meta">
      {header.dependsOn.length > 0 && (
        <>
          <dt>Depends on</dt>
          <dd className="dep-list">
            {header.dependsOn.map((dep) => {
              const nid = normalizeTaskId(dep);
              const row = byId.get(nid);
              return (
                <span key={dep} className={`dep${blocking.has(nid) ? " blocking" : ""}`}>
                  <TaskLink project={project} id={nid} inInspector={inInspector} title={row ? `${nid}: ${row.title}` : undefined}>
                    {nid}
                  </TaskLink>
                  {row ? <span className="dep-title">{row.title}</span> : <span className="dep-title muted">not in backlog</span>}
                  {row && <StatusPill status={row.status} />}
                </span>
              );
            })}
          </dd>
        </>
      )}
      {blocked && (
        <>
          <dt>Readiness</dt>
          <dd className="warn">{blocked}</dd>
        </>
      )}
      {task && task.specRefs.length > 0 && (
        <>
          <dt>Spec refs</dt>
          <dd>
            <ul className="spec-refs">
              {task.specRefs.map((r) => (
                <li key={r}>{r}</li>
              ))}
            </ul>
          </dd>
        </>
      )}
      {task?.created && (
        <>
          <dt>Created</dt>
          <dd>{task.created}</dd>
        </>
      )}
      {task?.updated && (
        <>
          <dt>Updated</dt>
          <dd>{task.updated}</dd>
        </>
      )}
      {task?.path && (
        <>
          <dt>File</dt>
          <dd className="task-path">
            <span className="mono" title={task.path}>
              {task.path.split("/").slice(-2).join("/")}
            </span>{" "}
            <CopyButton text={task.path} title="Copy the task file path" />
          </dd>
        </>
      )}
    </dl>
  );
}

/** Live sessions whose task focus includes this task (usually one). */
function FocusedSessions({ project, id }: { project: string; id: string }) {
  const { feed } = useSessionFeed(null);
  const rows = (feed?.rows ?? []).filter(
    (r) =>
      (project === "" || r.project === project) &&
      taskIds(r.session).some((t) => normalizeTaskId(t) === id),
  );
  if (!rows.length) return null;
  return (
    <div className="focused-sessions">
      <div className="label">Sessions focused on this task</div>
      <ul>
        {rows.slice(0, 6).map((r) => (
          <li key={r.session.sessionId}>
            <Link to={paths.session(r.project, r.session.sessionId)}>{displayTitle(r.session)}</Link>{" "}
            {r.session.live && <span className="badge badge-live">live</span>}{" "}
            <span className="muted small">{r.session.status}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function TaskBody({ body }: { body: string }) {
  const { main, workLog } = splitWorkLog(body);
  return (
    <div className="task-body">
      {main.trim() ? <Markdown text={main} /> : <p className="muted">No description.</p>}
      {workLog !== null && (
        <details className="work-log" open>
          <summary>Work log</summary>
          {workLog.trim() ? <Markdown text={workLog} /> : <p className="muted small">No entries yet.</p>}
        </details>
      )}
    </div>
  );
}

interface SaveState {
  busy: boolean;
  error: string | null;
  conflict: { fields: DraftField[]; current: TaskDetail } | null;
}

function TaskEditor({
  project,
  id,
  open,
  onChange,
  onDone,
}: {
  project: string;
  id: string;
  open: OpenDraft;
  onChange: (next: OpenDraft) => void;
  onDone: () => void;
}) {
  const qc = useQueryClient();
  const [state, setState] = useState<SaveState>({ busy: false, error: null, conflict: null });
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const { draft, base } = open;
  const dirty = changedFields(base, draft).length > 0;
  const invalid = validateDraft(draft, id);

  const set = <K extends keyof TaskDraft>(key: K, value: TaskDraft[K]) => onChange({ base, draft: { ...draft, [key]: value } });

  // A validation/RPC error is about the draft that was sent: clear it once the
  // user edits. A conflict stays visible until the next save decides again.
  useEffect(() => {
    setState((s) => (s.error && !s.conflict ? { ...s, error: null } : s));
  }, [draft]);

  const save = async (force = false) => {
    if (state.busy) return;
    setState({ busy: true, error: null, conflict: null });
    let current: TaskDetail | null = null;
    try {
      current = (await client.getTask({ project, id })).task ?? null;
      if (current) qc.setQueryData(queryKeys.task(project, id), current);
    } catch (err) {
      if (handleAuth(err)) return;
      // The update below reports the real problem (missing task, daemon down).
    }
    const plan = planSave({ project, id, base, current, draft, force });
    switch (plan.kind) {
      case "invalid":
        setState({ busy: false, error: plan.message, conflict: null });
        return;
      case "noop":
        setState({ busy: false, error: null, conflict: null });
        onDone();
        return;
      case "conflict":
        setState({ busy: false, error: conflictMessage(id, plan.fields), conflict: { fields: plan.fields, current: plan.current } });
        return;
      case "save":
        break;
    }
    try {
      const resp = await client.updateTask(plan.request);
      if (!resp.task) throw new Error("The daemon returned no task.");
      installTask(qc, project, resp.task);
      setState({ busy: false, error: null, conflict: null });
      onDone();
      toast(`Saved task ${id}.`, "info");
    } catch (err) {
      if (handleAuth(err)) return;
      setState({ busy: false, error: `Not saved: ${errorMessage(err)} Your draft is kept.`, conflict: null });
    }
  };

  const discardForCurrent = () => {
    const current = state.conflict?.current;
    setState({ busy: false, error: null, conflict: null });
    if (current) qc.setQueryData(queryKeys.task(project, id), current);
    onDone();
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    void save(false);
  };
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey) && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void save(false);
    }
  };

  return (
    <form className="task-editor" onSubmit={onSubmit} onKeyDown={onKeyDown} aria-label={`Edit task ${id}`}>
      {state.error && (
        <div className={`banner ${state.conflict ? "warn" : "error"}`} role="alert">
          <div>{state.error}</div>
          {state.conflict && (
            <div className="banner-actions">
              <button type="button" className="btn small danger" onClick={() => void save(true)} disabled={state.busy}>
                Overwrite with my draft
              </button>
              <button type="button" className="btn small" onClick={discardForCurrent} disabled={state.busy}>
                Discard my draft, load theirs
              </button>
            </div>
          )}
        </div>
      )}
      <label className="field-label">
        Title
        <input
          className="field"
          value={draft.title}
          onChange={(e) => set("title", e.target.value)}
          aria-invalid={!draft.title.trim()}
          autoFocus
        />
      </label>
      <div className="editor-row">
        <label className="field-label">
          Priority
          <select value={draft.priority} onChange={(e) => set("priority", Number(e.target.value))}>
            {[1, 2, 3, 4, 5].map((p) => (
              <option key={p} value={p}>
                P{p}
                {p === 1 ? " · highest" : p === 5 ? " · lowest" : ""}
              </option>
            ))}
          </select>
        </label>
        <label className="field-label grow">
          Depends on
          <input
            className="field mono"
            value={draft.dependsOn}
            placeholder="task ids, e.g. 0410, 0411"
            onChange={(e) => set("dependsOn", e.target.value)}
          />
        </label>
      </div>
      <label className="field-label">
        Spec refs <span className="muted small">one per line · a section title, or path#Section</span>
        <textarea rows={2} value={draft.specRefs} onChange={(e) => set("specRefs", e.target.value)} />
      </label>
      <label className="field-label">
        Body <span className="muted small">Markdown · description, acceptance criteria, work log</span>
        <textarea className="mono body-input" rows={18} value={draft.body} onChange={(e) => set("body", e.target.value)} />
      </label>
      <div className="editor-actions">
        <span className="muted small">{invalid ?? (dirty ? "Unsaved changes · Ctrl/⌘+Enter saves" : "No changes")}</span>
        <button type="button" className="btn" disabled={state.busy} onClick={() => (dirty ? setConfirmDiscard(true) : onDone())}>
          Cancel
        </button>
        <button type="submit" className="btn primary" disabled={state.busy}>
          {state.busy ? "Saving…" : "Save"}
        </button>
      </div>
      <ConfirmDialog
        open={confirmDiscard}
        title="Discard your changes?"
        body={`Your unsaved edits to task ${id} will be lost.`}
        confirmLabel="Discard"
        danger
        onCancel={() => setConfirmDiscard(false)}
        onConfirm={() => {
          setConfirmDiscard(false);
          onDone();
        }}
      />
    </form>
  );
}
