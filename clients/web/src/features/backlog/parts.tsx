// Small shared backlog UI pieces: status pill, priority badge, and task links.
import { Link } from "react-router";
import type { MouseEvent, ReactNode } from "react";
import { paths } from "../../app/paths";
import { useInspector } from "../inspector/inspector";
import { statusLabel } from "./model";

export function StatusPill({ status }: { status: string }) {
  const s = status.toLowerCase();
  return <span className={`status-pill st-${s.replace(/[^a-z_]/g, "") || "unknown"}`}>{statusLabel(status)}</span>;
}

export function PriorityBadge({ priority }: { priority: number }) {
  if (priority <= 0) return <span className="prio prio-none">—</span>;
  return (
    <span className={`prio prio-${priority}`} title={`Priority ${priority} (1 is highest)`}>
      P{priority}
    </span>
  );
}

function plainClick(e: MouseEvent): boolean {
  return e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;
}

/**
 * A link to a task. In the inspector, a plain click swaps the inspector to
 * that task (the transcript stays beside it); elsewhere it routes to the
 * backlog. The href is always the real task URL (middle-click, copy link).
 */
export function TaskLink({
  project,
  id,
  inInspector,
  className = "task-link",
  title,
  children,
}: {
  project: string;
  id: string;
  inInspector?: boolean;
  className?: string;
  title?: string;
  children?: ReactNode;
}) {
  const inspector = useInspector();
  return (
    <Link
      to={paths.task(project, id)}
      className={className}
      title={title ?? `Open task ${id}`}
      onClick={(e) => {
        e.stopPropagation();
        if (inInspector && plainClick(e)) {
          e.preventDefault();
          inspector.open({ kind: "task", project, taskId: id });
        }
      }}
    >
      {children ?? id}
    </Link>
  );
}
