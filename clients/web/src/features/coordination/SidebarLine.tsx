// The sidebar's live-sessions line: while the scoped project's session
// coordination is on, one quiet line under the loop and workstream lines
// counts its live sessions and active notices and opens the live sessions view.
import { Link } from "react-router";
import { useProjectNotices } from "../../api/queries";
import { paths } from "../../app/paths";

export function CoordinationSidebarLine({ scope }: { scope: string | null }) {
  const board = useProjectNotices(scope ?? "");
  if (!scope || !board.on) return null;
  const notices = board.data?.notices.length ?? 0;
  const sessions = board.data?.sessions.length ?? 0;
  const state = board.data
    ? [`${sessions} live`, notices ? `${notices} ${notices === 1 ? "notice" : "notices"}` : ""].filter(Boolean).join(" · ")
    : "";
  const to = paths.coordination(scope);
  return (
    <div className="loop-status ws-status coord-status">
      <section className="loop-status-card" aria-label="Live sessions">
        <div className="loop-status-head">
          <span className={`row-gutter loop-mark tone-${notices ? "waiting" : "idle"}`} aria-hidden="true" />
          <Link to={to} className="loop-status-title" data-track="sidebar.coordination_status.open">
            Live sessions
          </Link>
          {state && (
            <Link to={to} className="loop-status-done" title={state} data-track="sidebar.coordination_status.state">
              {state}
            </Link>
          )}
        </div>
      </section>
    </div>
  );
}
