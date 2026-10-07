// The backlog browser: ListBacklog as a sortable, filterable table or a kanban
// board (cards drag between status columns), with keyboard navigation (j/k or
// ↑/↓ to move — plus h/l or ←/→ across board columns and Shift+←/→ to move a
// card — Enter to open, / to filter, Esc to close the task), and the selected
// task's detail beside it. The URL carries the open task
// (`/p/<project>/backlog/<id>`); the table/board choice persists per browser.
import { Icon } from "../../ui/icons";
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import type { BacklogTaskSummary } from "../../gen/ycc/v1/ycc_pb";
import { errorMessage } from "../../api/client";
import { queryKeys, useBacklog } from "../../api/queries";
import { paths } from "../../app/paths";
import { isEditableTarget } from "../../app/actions";
import { useIntent } from "../../app/intents";
import { CAPTURE_SHORTCUT_LABEL, openCapture } from "./CaptureDialog";
import { NewTaskDialog } from "./NewTaskDialog";
import { changeTaskStatus, TaskDetailView } from "./TaskDetail";
import { PriorityBadge, StatusPill, TaskLink } from "./parts";
import { BacklogBoard } from "./Board";
import { LoopBanner } from "../workloop/WorkLoopPage";
import {
  DEFAULT_FILTER,
  DEFAULT_SORT,
  adjacentStatus,
  blockedLabel,
  boardColumns,
  isTaskStatus,
  moveBoardCursor,
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
  withStatus,
  type BacklogFilter,
  type BacklogView,
  type BoardDirection,
  type BacklogSort,
  type SortKey,
} from "./model";

// Filter and sort survive leaving and re-entering a project's backlog (per tab).
const viewMemory = new Map<string, { filter: BacklogFilter; sort: BacklogSort }>();

const VIEW_KEY = "ycc.backlogView";

function initialView(): BacklogView {
  try {
    return localStorage.getItem(VIEW_KEY) === "board" ? "board" : "table";
  } catch {
    return "table";
  }
}

function saveView(v: BacklogView) {
  try {
    localStorage.setItem(VIEW_KEY, v);
  } catch {
    // ignore
  }
}

const BOARD_KEYS: Record<string, BoardDirection> = {
  h: "left",
  ArrowLeft: "left",
  l: "right",
  ArrowRight: "right",
  j: "down",
  ArrowDown: "down",
  k: "up",
  ArrowUp: "up",
};

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

/** The intent key that asks a project's backlog to open "New task" (palette). */
export function backlogIntentKey(project: string) {
  return `backlog:${project}`;
}

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
  const [view, setViewState] = useState<BacklogView>(initialView);
  const [moving, setMoving] = useState<ReadonlySet<string>>(() => new Set());
  const filterRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const openId = taskId ? normalizeTaskId(taskId) : null;
  useIntent(
    backlogIntentKey(project),
    useCallback((what: string) => {
      if (what === "newTask") setCreating(true);
    }, []),
  );

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
  const columns = useMemo(() => (all && view === "board" ? boardColumns(all, filter) : []), [all, filter, view]);
  // Card ids per expanded column (keyboard movement skips the collapsed Done strip).
  const boardIds = useMemo(() => columns.map((c) => (c.collapsed ? [] : c.tasks.map((t) => t.id))), [columns]);
  const ids = useMemo(() => (view === "board" ? boardIds.flat() : rows.map((r) => r.id)), [view, boardIds, rows]);
  const setView = (v: BacklogView) => {
    setViewState(v);
    saveView(v);
  };

  const open = (id: string) => navigate(paths.task(project, id));
  const close = () => navigate(paths.backlog(project));

  // Keep the cursor row in view.
  useEffect(() => {
    if (!cursor) return;
    const el = listRef.current?.querySelector<HTMLElement>(`[data-id="${CSS.escape(cursor)}"]`);
    el?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [cursor, view]);

  // Table keyboard navigation; never while typing, with modifiers, or under a dialog.
  const keyState = useRef({ ids, cursor, openId, view, boardIds, all });
  keyState.current = { ids, cursor, openId, view, boardIds, all };
  const moveRef = useRef<(t: BacklogTaskSummary, status: string) => void>(() => {});
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || e.isComposing) return;
      if (isEditableTarget(e.target) || document.querySelector("dialog[open]")) return;
      const { ids: visible, cursor: cur, openId: opened, view: mode, boardIds: cols, all: tasks } = keyState.current;
      if (mode === "board") {
        // Shift+←/→ moves the cursor card to the neighbouring status column.
        if (e.shiftKey && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
          const t = cur ? tasks?.find((x) => x.id === cur) : undefined;
          const next = t && adjacentStatus(t.status, e.key === "ArrowLeft" ? -1 : 1);
          if (t && next) {
            e.preventDefault();
            moveRef.current(t, next);
          }
          return;
        }
        const dir = e.shiftKey ? undefined : BOARD_KEYS[e.key];
        if (dir) {
          e.preventDefault();
          setCursor(moveBoardCursor(cols, cur, dir));
          return;
        }
      }
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

  // A board move: show the card in its new column at once, then UpdateTask;
  // on failure (already toasted) refetch so the card snaps back.
  const moveTask = async (t: BacklogTaskSummary, status: string) => {
    if (!isTaskStatus(status) || t.status.toLowerCase() === status || moving.has(t.id)) return;
    const key = queryKeys.backlog(project);
    await qc.cancelQueries({ queryKey: key });
    qc.setQueryData<BacklogTaskSummary[]>(key, (list) => list?.map((x) => (x.id === t.id ? withStatus(x, status) : x)));
    setMoving((m) => new Set(m).add(t.id));
    const updated = await changeTaskStatus(qc, project, t.id, status);
    setMoving((m) => {
      const next = new Set(m);
      next.delete(t.id);
      return next;
    });
    if (!updated) void qc.invalidateQueries({ queryKey: key });
  };
  moveRef.current = (t, status) => void moveTask(t, status);

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
            <span className="segmented inline backlog-view" role="radiogroup" aria-label="Backlog layout">
              {(["table", "board"] as const).map((v) => (
                <button
                  key={v}
                  type="button"
                  role="radio"
                  aria-checked={view === v}
                  className={view === v ? "selected" : ""}
                  onClick={() => setView(v)}
                  title={v === "table" ? "Sortable table" : "Kanban board: drag cards between status columns"}
                >
                  {v === "table" ? "Table" : "Board"}
                </button>
              ))}
            </span>
            <button
              type="button"
              className="btn ghost"
              onClick={() => void qc.invalidateQueries({ queryKey: queryKeys.backlog(project) })}
              disabled={backlog.isFetching}
              title="Refresh"
              aria-label="Refresh backlog"
            >
              <Icon name="refresh" size={15} />
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
            {all ? `${ids.length} of ${all.length} tasks` : ""}
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
        ) : view === "board" ? (
          <div className="backlog-board-wrap" ref={listRef}>
            <BacklogBoard
              project={project}
              columns={columns}
              openId={openId}
              cursor={cursor}
              moving={moving}
              promoting={promoting}
              onOpen={(id) => {
                setCursor(id);
                open(id);
              }}
              onMove={(t, status) => void moveTask(t, status)}
              onPromote={(t) => void promote(t)}
              onExpandDone={() => setFilter((f) => ({ ...f, showDone: true }))}
            />
            <p className="muted small keys-hint">
              Drag cards between columns · <kbd>h</kbd>/<kbd>j</kbd>/<kbd>k</kbd>/<kbd>l</kbd> or arrows move ·{" "}
              <kbd>Shift</kbd>+<kbd>←</kbd>/<kbd>→</kbd> moves the card · <kbd>Enter</kbd> opens · <kbd>/</kbd> filters
            </p>
          </div>
        ) : (
          <div className="backlog-table-wrap" ref={listRef}>
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
              <tbody>
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
