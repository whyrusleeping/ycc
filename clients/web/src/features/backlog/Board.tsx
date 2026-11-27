// The backlog as a kanban board: one column per status in workflow order.
// Cards drag between columns to change status (UpdateTask); the page owns the
// keyboard (h/j/k/l or arrows move, Shift+←/→ moves the card, Enter opens).
import { Fragment, useState, type DragEvent } from "react";
import type { BacklogTaskSummary } from "../../gen/ycc/v1/ycc_pb";
import { PriorityBadge, TaskLink, TaskPreviewLink } from "./parts";
import { normalizeTaskId, statusLabel, type BoardColumn, type BoardSort } from "./model";
import { HeldBadge, type HolderLiveness } from "../coordination/parts";

const DRAG_TYPE = "application/x-ycc-task";

export function BacklogBoard({
  project,
  columns,
  boardSort,
  decisionsTaskId,
  sessionLiveness,
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
  boardSort: BoardSort;
  decisionsTaskId?: string | null;
  sessionLiveness?: ReadonlyMap<string, HolderLiveness>;
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
  const decisionsId = decisionsTaskId ? normalizeTaskId(decisionsTaskId) : null;

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
        const splitTodo = col.status === "todo" && boardSort === "priority";
        const firstBlockedIdx = splitTodo ? col.tasks.findIndex((t) => !t.ready) : -1;
        const showTodoDivider = firstBlockedIdx > 0;
        return (
          <section key={col.status} className={`board-col st-col-${col.status}${target}`} aria-label={label} {...dropProps(col.status)}>
            <header className="board-col-head">
              <span className="board-col-name">{label}</span>
              <span className="count">{col.tasks.length}</span>
            </header>
            <ul className="board-cards">
              {col.tasks.map((t, idx) => (
                <Fragment key={t.id}>
                  {showTodoDivider && idx === firstBlockedIdx && (
                    <li className="board-divider" aria-hidden="true">
                      Waiting on dependencies
                    </li>
                  )}
                  <BoardCard
                    project={project}
                    task={t}
                    isDecisions={decisionsId !== null && col.status === "proposed" && t.id === decisionsId}
                    liveness={t.heldBy ? sessionLiveness?.get(t.heldBy.sessionId) : undefined}
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
                </Fragment>
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
  isDecisions,
  liveness,
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
  isDecisions: boolean;
  liveness?: HolderLiveness;
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
  const blockers = status === "done" || t.ready ? [] : t.blockedBy.map(normalizeTaskId);
  const depBlocked = status === "todo" && blockers.length > 0;
  const showPromote = status === "proposed" && !isDecisions;
  return (
    <li
      data-id={t.id}
      className={`board-card${selected ? " selected" : ""}${cursor ? " cursor" : ""}${moving ? " moving" : ""}${depBlocked ? " is-dep-blocked" : ""}${isDecisions ? " is-decisions" : ""} st-card-${status}`}
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
        <TaskPreviewLink project={project} id={t.id} className="task-id-link" draggable={false} aria-current={selected ? "page" : undefined} />
        {isDecisions && <span className="decisions-tag">Decisions</span>}
        <HeldBadge project={project} holder={t.heldBy} compact liveness={liveness} />
        {showPromote && (
          <button
            type="button"
            className="btn ghost small board-promote"
            disabled={promoting || moving}
            onClick={onPromote}
            title={`Accept task ${t.id} into the active backlog (todo)`}
          >
            {promoting ? "Promoting…" : "Promote →"}
          </button>
        )}
        <PriorityBadge priority={t.priority} />
      </div>
      <div className="board-card-title" title={t.title}>
        {t.title}
      </div>
      {blockers.length > 0 && (
        <div className="board-card-foot">
          <span className="card-blocked">
            <span className="card-blocked-label">Blocked by</span>
            {blockers.map((id) => (
              <TaskLink key={id} project={project} id={id} className="dep-chip blocking" title={`Open task ${id} (not done yet)`} />
            ))}
          </span>
        </div>
      )}
    </li>
  );
}
