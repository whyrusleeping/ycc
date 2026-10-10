// The session list: the daemon-wide Recent feed (or one project's sessions),
// needs-answer sessions pinned on top, then most recent first.
import { Link, useNavigate } from "react-router";
import { useEffect, useState } from "react";
import { useSessionFeed, useSetFollowUp } from "../../api/queries";
import { errorMessage } from "../../api/client";
import { paths } from "../../app/paths";
import { requestReopen } from "../session/useSession";
import { useLoopSessionIds } from "../workloop/hooks";
import { useReadMarks } from "./unread";
import {
  displayProject,
  displayTitle,
  lifecycleLabel,
  metadataItems,
  needsAnswer,
  onlyFollowUp,
  relativeTime,
  sections,
  taskChipLabels,
  taskIds,
  type FeedRow,
} from "./feed";

/** Re-render periodically so relative times stay fresh. */
function useNow(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(t);
  }, [intervalMs]);
  return now;
}

export function SessionRowView({
  row,
  showProject,
  active,
  now,
  variant,
  loopOwned = false,
  unread = false,
  onMarkRead,
  onToggleFollowUp,
  followUpPending = false,
}: {
  row: FeedRow;
  showProject: boolean;
  active: boolean;
  now: number;
  variant: "sidebar" | "page";
  /** Started by a work loop (marked like iOS's "via loop"). */
  loopOwned?: boolean;
  /** Agent activity this browser hasn't shown yet. */
  unread?: boolean;
  onMarkRead?: () => void;
  onToggleFollowUp: () => void;
  followUpPending?: boolean;
}) {
  const s = row.session;
  const lifecycle = lifecycleLabel(s);
  const project = displayProject(row);
  const chips = taskChipLabels(s);
  const when = relativeTime(s.lastActivity || s.startedAt, now);
  const navigate = useNavigate();
  const ids = taskIds(s);
  // The row is one stretched link (its ::after covers the row) so the focus
  // task chips can be links of their own without nesting anchors.
  const link = (
    <div className={`session-row variant-${variant}${active ? " active" : ""}${needsAnswer(s) ? " needs" : ""}${unread ? " unread" : ""}`}>
      <div className="session-row-title">
        {needsAnswer(s) && (
          <span className="needs-marker" title="Needs an answer" aria-label="needs answer">
            ●
          </span>
        )}
        {unread && (
          <button
            type="button"
            className="unread-dot"
            title="New agent activity — click to mark read"
            aria-label={`Unread: mark ${displayTitle(s)} read`}
            data-track="sessions.mark_read"
            onClick={onMarkRead}
          />
        )}
        <button
          type="button"
          className={`follow-up-flag${s.followUp ? " flagged" : ""}`}
          aria-pressed={s.followUp}
          aria-label={s.followUp ? `Clear follow-up on ${displayTitle(s)}` : `Flag ${displayTitle(s)} for follow-up`}
          title={s.followUp ? `Flagged for follow-up${s.followUpAt ? ` since ${new Date(s.followUpAt).toLocaleString()}` : ""} — click to clear` : "Flag for follow-up"}
          data-track="sessions.follow_up"
          disabled={followUpPending}
          onClick={onToggleFollowUp}
        >
          ⚑
        </button>
        <Link
          to={paths.session(row.project, s.sessionId)}
          className="session-row-link title-text"
          data-track="sessions.open"
          aria-current={active ? "page" : undefined}
        >
          {displayTitle(s)}
        </Link>
      </div>
      <div className="session-row-meta">
        {lifecycle && <span className={`badge badge-${lifecycle}`}>{lifecycle}</span>}
        {s.live && <span className="badge badge-live">live</span>}
        {loopOwned && (
          <span className="tag loop-tag" title="Started by the work loop">
            loop
          </span>
        )}
        {showProject && project && <span className="project">{project}</span>}
        {chips.map((c) =>
          ids.includes(c) ? (
            <Link key={c} to={paths.task(row.project, c)} className="chip task-chip" title={`Open task ${c}`} data-track="sessions.task_chip">
              {c}
            </Link>
          ) : (
            <span key={c} className="chip" title={ids.slice(2).join(", ")}>
              {c}
            </span>
          ),
        )}
        {variant === "page" && metadataItems(s).map((m) => <span key={m}>{m}</span>)}
        {variant === "sidebar" && (
          <span>
            {Number(s.turns)} {Number(s.turns) === 1 ? "turn" : "turns"}
          </span>
        )}
        {when && <span className="when">{when}</span>}
      </div>
    </div>
  );
  if (s.live) return link;
  // Persisted sessions can be re-opened in place: navigate at once so the
  // history paints while ResumeSession runs, then the view goes live.
  return (
    <div className="session-row-wrap">
      {link}
      <button
        type="button"
        className="btn small row-resume"
        title="Resume this session"
        aria-label={`Resume ${displayTitle(s)}`}
        data-track="sessions.resume"
        onClick={() => {
          requestReopen(row.project, s.sessionId);
          navigate(paths.session(row.project, s.sessionId));
        }}
      >
        Resume
      </button>
    </div>
  );
}

export function SessionList({
  scope,
  activeSessionId,
  variant,
}: {
  scope: string | null;
  activeSessionId?: string;
  variant: "sidebar" | "page";
}) {
  const { feed, isLoading, error, loadOlder, loadingOlder } = useSessionFeed(scope);
  const now = useNow();
  const followUp = useSetFollowUp();
  const [followUpOnly, setFollowUpOnly] = useState(false);
  const loopIds = useLoopSessionIds();
  const marks = useReadMarks();
  // The open session is being read right now.
  const isUnread = (row: FeedRow) => row.session.sessionId !== activeSessionId && marks.isUnread(row.session);
  if (isLoading) return <p className="muted pad">Loading sessions…</p>;
  if (error) return <p className="error pad">{errorMessage(error, "Couldn’t load sessions.")}</p>;
  if (!feed) return null;
  if (feed.error) return <p className="error pad">{feed.error}</p>;
  const flagged = onlyFollowUp(feed.rows);
  const visibleRows = followUpOnly ? flagged : feed.rows;
  const groups = sections(visibleRows);
  const unreadRows = visibleRows.filter(isUnread);
  return (
    <div className={`session-list variant-${variant}`}>
      {(variant === "page" || flagged.length > 0 || followUpOnly) && (
        <div className="session-list-toolbar">
          <button
            type="button"
            className="btn ghost small follow-up-filter"
            aria-pressed={followUpOnly}
            data-track="sessions.follow_up_filter"
            onClick={() => setFollowUpOnly((value) => !value)}
          >
            Follow-up ({flagged.length})
          </button>
        </div>
      )}
      {feed.warning && <p className="warn pad small">{feed.warning}</p>}
      {variant === "page" && unreadRows.length > 0 && (
        <div className="unread-bar">
          <span className="unread-dot static" aria-hidden="true" />
          <span>
            {unreadRows.length} {unreadRows.length === 1 ? "session has" : "sessions have"} new agent activity
          </span>
          <button
            type="button"
            className="btn ghost small"
            data-track="sessions.mark_all_read"
            onClick={() => marks.markAllRead(unreadRows.map((r) => r.session))}
          >
            Mark all read
          </button>
        </div>
      )}
      {groups.length === 0 && (
        <div className="pad empty-list">
          <p className="muted">{followUpOnly ? "No sessions flagged for follow-up." : "No sessions yet."}</p>
          {!followUpOnly && (
            <Link to={paths.newSession(scope)} className="btn primary small" data-track="sessions.start_first">
              Start a session
            </Link>
          )}
        </div>
      )}
      {groups.map((g) => (
        <section key={g.kind} className={`session-section ${g.kind}`}>
          {g.title && <h3 className="section-title">{g.title}</h3>}
          {g.rows.map((row) => (
            <SessionRowView
              key={`${row.project}\u0000${row.session.sessionId}`}
              row={row}
              showProject={scope === null}
              active={row.session.sessionId === activeSessionId}
              now={now}
              variant={variant}
              loopOwned={loopIds.has(row.session.sessionId)}
              unread={isUnread(row)}
              onMarkRead={() => marks.markSummaryRead(row.session)}
              onToggleFollowUp={() => void followUp.toggle(row)}
              followUpPending={followUp.isPending(row)}
            />
          ))}
        </section>
      ))}
      {feed.hasMore && (
        <div className="pad">
          <button type="button" className="btn ghost small" disabled={loadingOlder} data-track="sessions.load_older" onClick={() => void loadOlder()}>
            {loadingOlder ? "Loading…" : "Load older sessions"}
          </button>
        </div>
      )}
    </div>
  );
}
