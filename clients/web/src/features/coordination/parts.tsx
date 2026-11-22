// Shared pieces of the session coordination surfaces: notice text with linked
// session ids, the notice list (live sessions view and the question screen),
// and the held-task badge (backlog board, table and task detail).
import { Link } from "react-router";
import type { ReactNode } from "react";
import type { ProjectLiveSession, ProjectNotice } from "../../gen/ycc/v1/ycc_pb";
import { paths } from "../../app/paths";
import { relativeTime } from "../sessions/feed";
import { useProjectNotices } from "../../api/queries";
import { expiresIn, heldTasks, posterLabel, shortSessionId, splitSessionIds } from "./model";

/** Plain text whose session ids link to that session. */
export function SessionLinkedText({ project, text }: { project: string; text: string }) {
  return (
    <>
      {splitSessionIds(text).map((p, i) =>
        p.kind === "text" ? (
          <span key={i}>{p.text}</span>
        ) : (
          <Link key={i} to={paths.session(project, p.id)} className="session-id-link mono" title={`Open session ${p.id}`}>
            {p.id}
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
        <span className="notice-text">{n.text}</span>
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
                <Link to={paths.task(project, t)} className="task-link">
                  {t}
                </Link>
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

/** Active notices above a pending question, while the project's coordination is on. */
export function QuestionNotices({ project }: { project: string }) {
  const board = useProjectNotices(project);
  const notices = board.data?.notices ?? [];
  if (!board.on || notices.length === 0) return null;
  return (
    <details className="question-notices" open>
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

/** Live sessions holding each task of the project (empty while coordination is off). */
export function useHeldTasks(project: string): Map<string, ProjectLiveSession[]> {
  const board = useProjectNotices(project);
  const sessions = board.data?.sessions;
  // heldTasks is cheap; the backlog re-renders on its own poll anyway.
  return sessions ? heldTasks(sessions) : EMPTY;
}

const EMPTY = new Map<string, ProjectLiveSession[]>();

/** "held" badge linking to the live session working on the task. */
export function HeldBadge({ project, holders }: { project: string; holders: readonly ProjectLiveSession[] | undefined }) {
  if (!holders || holders.length === 0) return null;
  const first = holders[0];
  const more = holders.length - 1;
  const title =
    `Held by live session ${first.sessionId}` +
    (first.title ? ` (${first.title})` : "") +
    (more > 0 ? ` and ${more} more` : "");
  return (
    <Link
      to={paths.session(project, first.sessionId)}
      className="held-badge"
      title={title}
      aria-label={title}
      draggable={false}
      data-track="backlog.open_holder"
      onClick={(e) => e.stopPropagation()}
    >
      held{more > 0 ? ` +${more}` : ""}
    </Link>
  );
}
