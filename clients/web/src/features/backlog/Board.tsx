// The backlog as a kanban board: one column per status in workflow order.
// Cards drag between columns to change status (UpdateTask); the page owns the
// keyboard (h/j/k/l or arrows move, Shift+←/→ moves the card, Enter opens).
import { useState, type DragEvent } from "react";
import { Link } from "react-router";
import type { BacklogTaskSummary } from "../../gen/ycc/v1/ycc_pb";
import { paths } from "../../app/paths";
import { PriorityBadge, TaskLink } from "./parts";
import { blockedLabel, normalizeTaskId, readiness, statusLabel, type BoardColumn } from "./model";

const DRAG_TYPE = "application/x-ycc-task";

export function BacklogBoard({
  project,
  columns,
  openId,
  cursor,
  moving,
  promoting,
  onOpen,
  onMove,
  onPromote,
  onExpandDone,
}: {
  project: string;
  columns: BoardColumn[];
  openId: string | null;
  cursor: string | null;
  moving: ReadonlySet<string>;
  promoting: string | null;
  onOpen: (id: string) => void;
  onMove: (task: BacklogTaskSummary, status: string) => void;
  onPromote: (task: BacklogTaskSummary) => void;
  onExpandDone: () => void;
}) {
  const [dragging, setDragging] = useState<BacklogTaskSummary | null>(null);
  const [over, setOver] = useState<string | null>(null);

  const accepts = (e: DragEvent, status: string) =>
    dragging !== null && e.dataTransfer.types.includes(DRAG_TYPE) && dragging.status.toLowerCase() !== status;

  const dropProps = (status: string) => ({
    onDragOver: (e: DragEvent) => {
      if (!accepts(e, status)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
      if (over !== status) setOver(status);
    },
    onDragLeave: (e: DragEvent) => {
      if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node | null)) setOver((o) => (o === status ? null : o));
    },
    onDrop: (e: DragEvent) => {
      const ok = accepts(e, status);
      const task = dragging;
      setOver(null);
      setDragging(null);
      if (!ok || !task) return;
      e.preventDefault();
      onMove(task, status);
    },
  });

  return (
    <div className={`backlog-board${dragging ? " dragging" : ""}`} aria-label="Backlog board">
      {columns.map((col) => {
        const target = over === col.status ? " drop-over" : "";
        const label = statusLabel(col.status);
        if (col.collapsed) {
          return (
            <section
              key={col.status}
              className={`board-col collapsed st-col-${col.status}${target}`}
              aria-label={`${label} (${col.tasks.length}, collapsed)`}
              {...dropProps(col.status)}
            >
              <button
                type="button"
                className="board-col-expand"
                onClick={onExpandDone}
                title={`Show ${label.toLowerCase()} tasks (drop a card here to mark it ${label.toLowerCase()})`}
              >
                <span className="board-col-name">{label}</span>
                <span className="count">{col.tasks.length}</span>
              </button>
            </section>
          );
        }
        return (
          <section key={col.status} className={`board-col st-col-${col.status}${target}`} aria-label={label} {...dropProps(col.status)}>
            <header className="board-col-head">
              <span className="board-col-name">{label}</span>
              <span className="count">{col.tasks.length}</span>
            </header>
            <ul className="board-cards">
              {col.tasks.map((t) => (
                <BoardCard
                  key={t.id}
                  project={project}
                  task={t}
                  selected={t.id === openId}
                  cursor={t.id === cursor}
                  moving={moving.has(t.id)}
                  promoting={promoting === t.id}
                  onOpen={() => onOpen(t.id)}
                  onPromote={() => onPromote(t)}
                  onDragStart={(e) => {
                    e.dataTransfer.setData(DRAG_TYPE, t.id);
                    e.dataTransfer.setData("text/plain", t.id);
                    e.dataTransfer.effectAllowed = "move";
                    setDragging(t);
                  }}
                  onDragEnd={() => {
                    setDragging(null);
                    setOver(null);
                  }}
                />
              ))}
              {col.tasks.length === 0 && <li className="board-empty muted small">No tasks</li>}
            </ul>
          </section>
        );
      })}
    </div>
  );
}

function BoardCard({
  project,
  task: t,
  selected,
  cursor,
  moving,
  promoting,
  onOpen,
  onPromote,
  onDragStart,
  onDragEnd,
}: {
  project: string;
  task: BacklogTaskSummary;
  selected: boolean;
  cursor: boolean;
  moving: boolean;
  promoting: boolean;
  onOpen: () => void;
  onPromote: () => void;
  onDragStart: (e: DragEvent) => void;
  onDragEnd: () => void;
}) {
  const status = t.status.toLowerCase();
  const blocked = blockedLabel(t);
  const blocking = new Set(t.blockedBy.map(normalizeTaskId));
  // The column already says the status; only "actionable" adds information.
  const ready = readiness(t);
  const actionable = ready.kind === "actionable";
  return (
    <li
      data-id={t.id}
      className={`board-card${selected ? " selected" : ""}${cursor ? " cursor" : ""}${moving ? " moving" : ""} st-card-${status}`}
      aria-current={selected ? "true" : undefined}
      draggable={!moving}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest("a, button, input, select")) return;
        onOpen();
      }}
    >
      <div className="board-card-top">
        <Link to={paths.task(project, t.id)} className="task-id-link" draggable={false} aria-current={selected ? "page" : undefined}>
          {t.id}
        </Link>
        <PriorityBadge priority={t.priority} />
      </div>
      <div className="board-card-title">{t.title}</div>
      {blocked && <span className="blocked-note">{blocked}</span>}
      {(t.dependsOn.length > 0 || actionable || status === "proposed") && (
        <div className="board-card-foot">
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
          <span className="spacer" />
          {status === "proposed" ? (
            <button
              type="button"
              className="btn small"
              disabled={promoting || moving}
              onClick={onPromote}
              title={`Accept task ${t.id} into the active backlog (todo)`}
            >
              {promoting ? "Promoting…" : "Promote"}
            </button>
          ) : (
            actionable && (
              <span className={`flag ${ready.kind}`} title={ready.title}>
                {ready.label}
              </span>
            )
          )}
        </div>
      )}
    </li>
  );
}
