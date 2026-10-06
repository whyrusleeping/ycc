// The backlog browser: ListBacklog as a sortable, filterable table with
// keyboard navigation (j/k or ↑/↓ to move, Enter to open, / to filter, Esc to
// close the task), and the selected task's detail beside it. The URL carries
// the open task (`/p/<project>/backlog/<id>`).
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import type { BacklogTaskSummary } from "../../gen/ycc/v1/ycc_pb";
import { errorMessage } from "../../api/client";
import { queryKeys, useBacklog } from "../../api/queries";
import { paths } from "../../app/paths";
import { isEditableTarget } from "../../app/actions";
import { CAPTURE_SHORTCUT_LABEL, openCapture } from "./CaptureDialog";
import { NewTaskDialog } from "./NewTaskDialog";
import { changeTaskStatus, TaskDetailView } from "./TaskDetail";
import { PriorityBadge, StatusPill, TaskLink } from "./parts";
import { LoopBanner } from "../workloop/WorkLoopPage";
import {
  DEFAULT_FILTER,
  DEFAULT_SORT,
  blockedLabel,
  filterTasks,
  isFiltered,
  moveCursor,
  normalizeTaskId,
  readiness,
  sortTasks,
  statusCounts,
  statusLabel,
  TASK_STATUSES,
  toggleSort,
  type BacklogFilter,
  type BacklogSort,
  type SortKey,
} from "./model";

// Filter and sort survive leaving and re-entering a project's backlog (per tab).
const viewMemory = new Map<string, { filter: BacklogFilter; sort: BacklogSort }>();

const COLUMNS: { key: SortKey; label: string; className: string; title?: string }[] = [
  { key: "id", label: "ID", className: "col-id" },
  { key: "title", label: "Title", className: "col-title" },
  { key: "status", label: "Status", className: "col-status", title: "Active work first, done last; then priority" },
  { key: "priority", label: "Pri", className: "col-prio", title: "Priority (1 is highest)" },
  { key: "deps", label: "Depends on", className: "col-deps", title: "Open (not done) dependencies first when descending" },
  {
    key: "actionable",
    label: "Ready",
    className: "col-ready",
    title: "Actionable: accepted work (todo or in progress) whose dependencies are done",
  },
];

export function BacklogPage({ project, taskId }: { project: string; taskId: string | null }) {
  const backlog = useBacklog(project);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const remembered = viewMemory.get(project);
  const [filter, setFilter] = useState<BacklogFilter>(remembered?.filter ?? DEFAULT_FILTER);
  const [sort, setSort] = useState<BacklogSort>(remembered?.sort ?? DEFAULT_SORT);
  const [cursor, setCursor] = useState<string | null>(taskId ? normalizeTaskId(taskId) : null);
  const [creating, setCreating] = useState(false);
  const [promoting, setPromoting] = useState<string | null>(null);
  const filterRef = useRef<HTMLInputElement>(null);
  const tableRef = useRef<HTMLTableSectionElement>(null);
  const openId = taskId ? normalizeTaskId(taskId) : null;

  useEffect(() => {
    viewMemory.set(project, { filter, sort });
  }, [project, filter, sort]);

  // The opened task becomes the cursor (so j/k continue from it).
  useEffect(() => {
    if (openId) setCursor(openId);
  }, [openId]);

  const all = backlog.data;
  const rows = useMemo(() => (all ? sortTasks(filterTasks(all, filter), sort) : []), [all, filter, sort]);
  const counts = useMemo(() => statusCounts(all ?? []), [all]);
  const ids = useMemo(() => rows.map((r) => r.id), [rows]);

  const open = (id: string) => navigate(paths.task(project, id));
  const close = () => navigate(paths.backlog(project));

  // Keep the cursor row in view.
  useEffect(() => {
    if (!cursor) return;
    const el = tableRef.current?.querySelector<HTMLElement>(`tr[data-id="${CSS.escape(cursor)}"]`);
    el?.scrollIntoView({ block: "nearest" });
  }, [cursor]);

  // Table keyboard navigation; never while typing, with modifiers, or under a dialog.
  const keyState = useRef({ ids, cursor, openId });
  keyState.current = { ids, cursor, openId };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || e.isComposing) return;
      if (isEditableTarget(e.target) || document.querySelector("dialog[open]")) return;
      const { ids: visible, cursor: cur, openId: opened } = keyState.current;
      switch (e.key) {
        case "j":
        case "ArrowDown":
          e.preventDefault();
          setCursor(moveCursor(visible, cur, 1));
          return;
        case "k":
        case "ArrowUp":
          e.preventDefault();
          setCursor(moveCursor(visible, cur, -1));
          return;
        case "Enter": {
          // Links, buttons, and the task pane keep their own Enter.
          const t = e.target as HTMLElement | null;
          if (t && t.closest("a, button, summary, [role='button'], .task-pane")) return;
          if (cur && visible.includes(cur)) {
            e.preventDefault();
            navigate(paths.task(project, cur));
          }
          return;
        }
        case "/":
          e.preventDefault();
          filterRef.current?.focus();
          filterRef.current?.select();
          return;
        case "Escape":
          if (opened) {
            e.preventDefault();
            navigate(paths.backlog(project));
          }
          return;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [navigate, project]);

  const promote = async (t: BacklogTaskSummary) => {
    if (promoting) return;
    setPromoting(t.id);
    await changeTaskStatus(qc, project, t.id, "todo");
    setPromoting(null);
  };

  const toggleStatus = (s: (typeof TASK_STATUSES)[number]) =>
    setFilter((f) => ({
      ...f,
      statuses: f.statuses.includes(s) ? f.statuses.filter((x) => x !== s) : [...f.statuses, s],
    }));

  return (
    <div className={`backlog-page${openId ? " with-detail" : ""}`}>
      <div className="backlog-main">
        <header className="page-head">
          <h1>
            Backlog {project && <span className="muted">· {project}</span>}
          </h1>
          <div className="page-actions">
            <button
              type="button"
              className="btn ghost"
              onClick={() => void qc.invalidateQueries({ queryKey: queryKeys.backlog(project) })}
              disabled={backlog.isFetching}
              title="Refresh"
              aria-label="Refresh backlog"
            >
              ↻
            </button>
            <button
              type="button"
              className="btn"
              onClick={() => openCapture(project)}
              title={`Describe a task and let the capture agent write it up (${CAPTURE_SHORTCUT_LABEL})`}
            >
              Quick capture <kbd>{CAPTURE_SHORTCUT_LABEL}</kbd>
            </button>
            <button type="button" className="btn primary" onClick={() => setCreating(true)}>
              + New task
            </button>
          </div>
        </header>
        <LoopBanner project={project} />
        <div className="backlog-toolbar" role="toolbar" aria-label="Backlog filters">
          <input
            ref={filterRef}
            type="search"
            className="field backlog-filter"
            placeholder="Filter tasks…  ( / )"
            title="Filter by id, title, status, or dependency"
            aria-label="Filter tasks"
            value={filter.text}
            onChange={(e) => setFilter((f) => ({ ...f, text: e.target.value }))}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                if (filter.text) setFilter((f) => ({ ...f, text: "" }));
                else e.currentTarget.blur();
              } else if (e.key === "ArrowDown" || e.key === "Enter") {
                // Hand the keyboard to the table.
                e.preventDefault();
                e.currentTarget.blur();
                setCursor(moveCursor(ids, null, 1));
              }
            }}
          />
          <div className="status-filters" role="group" aria-label="Status">
            {TASK_STATUSES.map((s) => (
              <button
                key={s}
                type="button"
                className={`filter-chip st-${s}${filter.statuses.includes(s) ? " on" : ""}`}
                aria-pressed={filter.statuses.includes(s)}
                onClick={() => toggleStatus(s)}
              >
                {statusLabel(s)} <span className="count">{counts[s] ?? 0}</span>
              </button>
            ))}
          </div>
          <label className="check">
            <input
              type="checkbox"
              checked={filter.actionableOnly}
              onChange={(e) => setFilter((f) => ({ ...f, actionableOnly: e.target.checked }))}
            />
            Actionable only
          </label>
          <label className="check">
            <input
              type="checkbox"
              checked={filter.showDone}
              onChange={(e) => setFilter((f) => ({ ...f, showDone: e.target.checked }))}
            />
            Show done
          </label>
          {(isFiltered(filter) || filter.showDone) && (
            <button type="button" className="link small" onClick={() => setFilter(DEFAULT_FILTER)}>
              Reset
            </button>
          )}
          <span className="muted small backlog-count">
            {all ? `${rows.length} of ${all.length} tasks` : ""}
          </span>
        </div>
        {backlog.isPending ? (
          <p className="muted pad">Loading backlog…</p>
        ) : backlog.isError && !all ? (
          <p className="error pad">{errorMessage(backlog.error, "Couldn’t load the backlog.")}</p>
        ) : !all || all.length === 0 ? (
          <div className="pad empty-list">
            <p className="muted">The backlog is empty.</p>
            <button type="button" className="btn primary small" onClick={() => setCreating(true)}>
              Create a task
            </button>
          </div>
        ) : (
          <div className="backlog-table-wrap">
            <table className="backlog-table" aria-label="Backlog tasks" aria-rowcount={rows.length}>
              <thead>
                <tr>
                  {COLUMNS.map((c) => (
                    <th
                      key={c.key}
                      className={c.className}
                      aria-sort={sort.key === c.key ? (sort.dir === "asc" ? "ascending" : "descending") : "none"}
                    >
                      <button type="button" className="sort-btn" title={c.title} onClick={() => setSort((s) => toggleSort(s, c.key))}>
                        {c.label}
                        <span className="sort-mark" aria-hidden="true">
                          {sort.key === c.key ? (sort.dir === "asc" ? "▲" : "▼") : ""}
                        </span>
                      </button>
                    </th>
                  ))}
                  <th className="col-actions">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody ref={tableRef}>
                {rows.length === 0 && (
                  <tr>
                    <td colSpan={COLUMNS.length + 1} className="muted pad">
                      No tasks match.{" "}
                      <button type="button" className="link" onClick={() => setFilter(DEFAULT_FILTER)}>
                        Reset filters
                      </button>
                    </td>
                  </tr>
                )}
                {rows.map((t) => (
                  <BacklogRow
                    key={t.id}
                    project={project}
                    task={t}
                    selected={t.id === openId}
                    cursor={t.id === cursor}
                    promoting={promoting === t.id}
                    onOpen={() => {
                      setCursor(t.id);
                      open(t.id);
                    }}
                    onPromote={() => void promote(t)}
                  />
                ))}
              </tbody>
            </table>
            <p className="muted small keys-hint">
              <kbd>j</kbd>/<kbd>k</kbd> or <kbd>↑</kbd>/<kbd>↓</kbd> move · <kbd>Enter</kbd> opens · <kbd>/</kbd> filters ·{" "}
              <kbd>Esc</kbd> closes the task
            </p>
          </div>
        )}
      </div>
      {openId && (
        <section className="task-pane" aria-label={`Task ${openId}`}>
          <TaskDetailView key={`${project}\u0000${openId}`} project={project} taskId={openId} variant="pane" onClose={close} />
        </section>
      )}
      <NewTaskDialog project={project} open={creating} onClose={() => setCreating(false)} />
    </div>
  );
}

function BacklogRow({
  project,
  task: t,
  selected,
  cursor,
  promoting,
  onOpen,
  onPromote,
}: {
  project: string;
  task: BacklogTaskSummary;
  selected: boolean;
  cursor: boolean;
  promoting: boolean;
  onOpen: () => void;
  onPromote: () => void;
}) {
  const status = t.status.toLowerCase();
  const blocked = blockedLabel(t);
  const blocking = new Set(t.blockedBy.map(normalizeTaskId));
  const ready = readiness(t);
  return (
    <tr
      data-id={t.id}
      className={`${selected ? "selected" : ""}${cursor ? " cursor" : ""} st-row-${status}`}
      aria-selected={selected}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest("a, button, input, select")) return;
        onOpen();
      }}
    >
      <td className="col-id mono">
        <Link to={paths.task(project, t.id)} className="task-id-link" aria-current={selected ? "page" : undefined}>
          {t.id}
        </Link>
      </td>
      <td className="col-title">
        <span className="task-row-title">{t.title}</span>
        {blocked && <span className="blocked-note">{blocked}</span>}
      </td>
      <td className="col-status">
        <StatusPill status={t.status} />
      </td>
      <td className="col-prio">
        <PriorityBadge priority={t.priority} />
      </td>
      <td className="col-deps">
        {t.dependsOn.map((d) => {
          const id = normalizeTaskId(d);
          return (
            <TaskLink
              key={d}
              project={project}
              id={id}
              className={`dep-chip${blocking.has(id) ? " blocking" : ""}`}
              title={blocking.has(id) ? `Open task ${id} (not done yet)` : `Open task ${id} (done)`}
            />
          );
        })}
      </td>
      <td className="col-ready">
        {ready.label && (
          <span className={`flag ${ready.kind}`} title={ready.title}>
            {ready.label}
          </span>
        )}
      </td>
      <td className="col-actions">
        {status === "proposed" && (
          <button
            type="button"
            className="btn small"
            disabled={promoting}
            onClick={onPromote}
            title={`Accept task ${t.id} into the active backlog (todo)`}
          >
            {promoting ? "Promoting…" : "Promote"}
          </button>
        )}
      </td>
    </tr>
  );
}
