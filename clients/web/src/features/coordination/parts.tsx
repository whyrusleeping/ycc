// Shared pieces of the session coordination surfaces: notice text with linked
// session ids, the notice list (live sessions view and the question screen),
// and the held-task badge (backlog board, table and task detail).
import { Link } from "react-router";
import type { ReactNode } from "react";
import type { ProjectNotice, TaskHolder } from "../../gen/ycc/v1/ycc_pb";
import { paths } from "../../app/paths";
import { relativeTime } from "../sessions/feed";
import { useProjectNotices } from "../../api/queries";
import { TaskPreviewLink } from "../backlog/parts";
import { taskReferences } from "../backlog/taskReferences";
import { compactHolderLabel, expiresIn, holderLabel, posterLabel, shortSessionId, splitSessionIds } from "./model";

/** Plain text whose session ids link to that session. */
export function SessionLinkedText({ project, text }: { project: string; text: string }) {
  return (
    <>
      {splitSessionIds(text).map((p, i) =>
        p.kind === "text" ? (
          <span key={i}>{taskReferences(p.text, project)}</span>
        ) : (
          <Link key={i} to={paths.session(project, p.id)} className="session-id-link mono" title={`Open session ${p.id}`}>
            {shortSessionId(p.id)}
          </Link>
        ),
      )}
    </>
  );
}

export function SessionLink({ project, id, children }: { project: string; id: string; children?: ReactNode }) {
  return (
    <Link to={paths.session(project, id)} className="session-id-link mono" title={`Open session ${id}`}>
      {children ?? shortSessionId(id)}
    </Link>
  );
}

/** One notice: subject, text, poster, focused tasks, age and expiry. */
export function NoticeItem({
  project,
  notice: n,
  now,
  action,
}: {
  project: string;
  notice: ProjectNotice;
  now: number;
  action?: ReactNode;
}) {
  const by = posterLabel(n);
  const expiry = expiresIn(n.expiresAt, now);
  return (
    <li className="notice-item" data-notice-id={n.id}>
      <div className="notice-line">
        <span className="chip notice-id">{n.id}</span>
        {n.subject && <span className="notice-subject">{n.subject}</span>}
        <span className="notice-text">{taskReferences(n.text, project)}</span>
      </div>
      <div className="notice-meta">
        {n.sessionId ? (
          <span>
            by <SessionLink project={project} id={n.sessionId} />
            {by && ` (${by})`}
          </span>
        ) : (
          <span>by you</span>
        )}
        {n.taskIds.length > 0 && (
          <span>
            {n.taskIds.length === 1 ? "task" : "tasks"}{" "}
            {n.taskIds.map((t, i) => (
              <span key={t}>
                {i > 0 && ", "}
                <TaskPreviewLink project={project} id={t} className="task-link" />
              </span>
            ))}
          </span>
        )}
        {n.postedAt && <span>{relativeTime(n.postedAt, now)}</span>}
        {expiry && <span className={expiry === "expired" ? "muted" : ""}>expires {expiry}</span>}
        {action && <span className="notice-action">{action}</span>}
      </div>
    </li>
  );
}

export function NoticeList({
  project,
  notices,
  now = Date.now(),
  action,
}: {
  project: string;
  notices: readonly ProjectNotice[];
  now?: number;
  action?: (n: ProjectNotice) => ReactNode;
}) {
  return (
    <ul className="notice-list">
      {notices.map((n) => (
        <NoticeItem key={n.id} project={project} notice={n} now={now} action={action?.(n)} />
      ))}
    </ul>
  );
}

/**
 * Active notices above a pending question, while the project's coordination is
 * on. Folded by default so the transcript the question refers to keeps its room.
 */
export function QuestionNotices({ project }: { project: string }) {
  const board = useProjectNotices(project);
  const notices = board.data?.notices ?? [];
  if (!board.on || notices.length === 0) return null;
  return (
    <details className="question-notices">
      <summary>
        <span className="sum-title">Project notices</span>
        <span className="muted small">
          {notices.length} active in this project · context for your answer, not an answer
        </span>
        <Link to={paths.coordination(project)} className="link small" data-track="question.open_notices">
          All live sessions
        </Link>
      </summary>
      <NoticeList project={project} notices={notices} />
    </details>
  );
}

export type HolderLiveness = "working" | "jobs" | "question" | "idle";

const LIVENESS_TEXT: Record<HolderLiveness, string> = {
  working: "working",
  jobs: "waiting on background jobs",
  question: "needs your answer",
  idle: "idle",
};

/** "held by <holder>" badge linking to the live session working on the task. */
export function HeldBadge({
  project,
  holder,
  compact = false,
  liveness,
}: {
  project: string;
  holder: TaskHolder | undefined;
  /** Compact form for Kanban cards: omits "held by" and uses the tight handle. */
  compact?: boolean;
  /** Live session state from the session feed (colors the dot and clarifies the tooltip). */
  liveness?: HolderLiveness;
}) {
  if (!holder) return null;
  const liveSuffix = liveness ? ` · ${LIVENESS_TEXT[liveness]}` : "";
  const title =
    `Held by ${holder.loop ? "work-loop session" : "live session"} ${holder.sessionId}` +
    (holder.title ? ` (${holder.title})` : "") +
    liveSuffix;
  const tone = liveness ? ` held-${liveness}` : "";
  return (
    <Link
      to={paths.session(project, holder.sessionId)}
      className={`held-badge${compact ? " compact" : ""}${tone}`}
      title={title}
      aria-label={title}
      draggable={false}
      data-track="backlog.open_holder"
      onClick={(e) => e.stopPropagation()}
    >
      {!compact && "held by "}
      <span className="held-by">{compact ? compactHolderLabel(holder) : holderLabel(holder)}</span>
    </Link>
  );
}
