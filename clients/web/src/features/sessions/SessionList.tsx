// The session list: the daemon-wide Recent feed (or one project's sessions),
// needs-answer sessions pinned on top, then most recent first.
import { Link } from "react-router";
import { useEffect, useState } from "react";
import { useSessionFeed } from "../../api/queries";
import { errorMessage } from "../../api/client";
import { paths } from "../../app/paths";
import {
  displayProject,
  displayTitle,
  lifecycleLabel,
  metadataItems,
  needsAnswer,
  relativeTime,
  sections,
  taskChipLabels,
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
}: {
  row: FeedRow;
  showProject: boolean;
  active: boolean;
  now: number;
  variant: "sidebar" | "page";
}) {
  const s = row.session;
  const lifecycle = lifecycleLabel(s);
  const project = displayProject(row);
  const chips = taskChipLabels(s);
  const when = relativeTime(s.lastActivity || s.startedAt, now);
  return (
    <Link
      to={paths.session(row.project, s.sessionId)}
      className={`session-row ${variant}${active ? " active" : ""}${needsAnswer(s) ? " needs" : ""}`}
      aria-current={active ? "page" : undefined}
    >
      <div className="session-row-title">
        {needsAnswer(s) && (
          <span className="needs-marker" title="Needs an answer" aria-label="needs answer">
            ●
          </span>
        )}
        <span className="title-text">{displayTitle(s)}</span>
      </div>
      <div className="session-row-meta">
        {lifecycle && <span className={`badge badge-${lifecycle}`}>{lifecycle}</span>}
        {s.live && <span className="badge badge-live">live</span>}
        {showProject && project && <span className="project">{project}</span>}
        {chips.map((c) => (
          <span key={c} className="chip">
            {c}
          </span>
        ))}
        {variant === "page" && metadataItems(s).map((m) => <span key={m}>{m}</span>)}
        {variant === "sidebar" && (
          <span>
            {Number(s.turns)} {Number(s.turns) === 1 ? "turn" : "turns"}
          </span>
        )}
        {when && <span className="when">{when}</span>}
      </div>
    </Link>
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
  if (isLoading) return <p className="muted pad">Loading sessions…</p>;
  if (error) return <p className="error pad">{errorMessage(error, "Couldn’t load sessions.")}</p>;
  if (!feed) return null;
  if (feed.error) return <p className="error pad">{feed.error}</p>;
  const groups = sections(feed.rows);
  return (
    <div className={`session-list ${variant}`}>
      {feed.warning && <p className="warn pad small">{feed.warning}</p>}
      {groups.length === 0 && <p className="muted pad">No sessions yet.</p>}
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
