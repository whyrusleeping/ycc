// The session list: the daemon-wide Recent feed (or one project's sessions),
// needs-answer sessions pinned on top, then most recent first.
import { Link, useNavigate } from "react-router";
import { useEffect, useState } from "react";
import { useSessionFeed } from "../../api/queries";
import { errorMessage } from "../../api/client";
import { paths } from "../../app/paths";
import { requestReopen } from "../session/useSession";
import { useLoopSessionIds } from "../workloop/hooks";
import {
  displayProject,
  displayTitle,
  lifecycleLabel,
  metadataItems,
  needsAnswer,
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
}: {
  row: FeedRow;
  showProject: boolean;
  active: boolean;
  now: number;
  variant: "sidebar" | "page";
  /** Started by a work loop (marked like iOS's "via loop"). */
  loopOwned?: boolean;
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
    <div className={`session-row variant-${variant}${active ? " active" : ""}${needsAnswer(s) ? " needs" : ""}`}>
      <div className="session-row-title">
        {needsAnswer(s) && (
          <span className="needs-marker" title="Needs an answer" aria-label="needs answer">
            ●
          </span>
        )}
        <Link
          to={paths.session(row.project, s.sessionId)}
          className="session-row-link title-text"
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
            <Link key={c} to={paths.task(row.project, c)} className="chip task-chip" title={`Open task ${c}`}>
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
  const loopIds = useLoopSessionIds();
  if (isLoading) return <p className="muted pad">Loading sessions…</p>;
  if (error) return <p className="error pad">{errorMessage(error, "Couldn’t load sessions.")}</p>;
  if (!feed) return null;
  if (feed.error) return <p className="error pad">{feed.error}</p>;
  const groups = sections(feed.rows);
  return (
    <div className={`session-list variant-${variant}`}>
      {feed.warning && <p className="warn pad small">{feed.warning}</p>}
      {groups.length === 0 && (
        <div className="pad empty-list">
          <p className="muted">No sessions yet.</p>
          <Link to={paths.newSession(scope)} className="btn primary small">
            Start a session
          </Link>
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
            />
          ))}
        </section>
      ))}
      {feed.hasMore && (
        <div className="pad">
          <button type="button" className="btn ghost small" disabled={loadingOlder} onClick={() => void loadOlder()}>
            {loadingOlder ? "Loading…" : "Load older sessions"}
          </button>
        </div>
      )}
    </div>
  );
}
