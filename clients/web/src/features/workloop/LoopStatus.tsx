// Ambient work-loop status at the top of the sidebar inbox: what each loop is
// working on now and what it finished recently. Loop sessions stay out of the
// personal inbox, so this is status only — no unread marks or notifications.
import { useState } from "react";
import { Link } from "react-router";
import { useSessionFeed, useWorkLoops } from "../../api/queries";
import { paths } from "../../app/paths";
import { TaskLink } from "../backlog/parts";
import { displayTitle, taskIds } from "../sessions/feed";
import {
  ambientLoops,
  currentSessionId,
  isActive,
  isFailureOutcome,
  loopState,
  recentCompleted,
  stateTitle,
  waitingLine,
} from "./model";

const DISMISSED_KEY = "ycc.loopStatusDismissed";
const SHOWN_DONE = 3;

function readDismissed(): Set<string> {
  try {
    const raw = JSON.parse(localStorage.getItem(DISMISSED_KEY) ?? "[]");
    return new Set(Array.isArray(raw) ? raw.filter((x): x is string => typeof x === "string") : []);
  } catch {
    return new Set();
  }
}

export function LoopStatus({ scope }: { scope: string | null }) {
  const loops = useWorkLoops();
  const { feed } = useSessionFeed(scope);
  const [dismissed, setDismissed] = useState(readDismissed);
  const shown = ambientLoops(
    loops.filter((l) => scope === null || l.project === scope),
    Date.now(),
    dismissed,
  );
  if (!shown.length) return null;
  const dismiss = (loopId: string) => {
    const next = new Set(dismissed).add(loopId);
    setDismissed(next);
    try {
      localStorage.setItem(DISMISSED_KEY, JSON.stringify([...next].slice(-50)));
    } catch {
      // A UI convenience; an unwritable store just forgets the dismissal.
    }
  };
  return (
    <div className="loop-status">
      {shown.map(({ project, loop }) => {
        const state = loopState(loop);
        const current = currentSessionId(loop);
        const currentRow = current ? feed?.rows.find((r) => r.session.sessionId === current) : undefined;
        const currentTask = currentRow ? taskIds(currentRow.session)[0] : undefined;
        const done = recentCompleted(loop);
        const attention = [
          loop.blocked.length ? `${loop.blocked.length} blocked` : "",
          loop.inReview.length ? `${loop.inReview.length} in review` : "",
        ].filter(Boolean).join(" · ");
        const outcome = loop.outcome.trim();
        return (
          <section key={loop.loopId} className={`loop-status-card state-${state}`} aria-label={`Work loop${scope === null ? ` in ${project}` : ""}`}>
            <div className="loop-status-head">
              <Link to={paths.loop(project)} className="loop-status-title" data-track="sidebar.loop_status.open">
                <span className={`nav-badge tone-${state === "running" ? "live" : state === "waiting" ? "warn" : "idle"}`}>
                  {stateTitle(state).toLowerCase()}
                </span>
                <span>Work loop{scope === null ? ` · ${project}` : ""}</span>
              </Link>
              {state === "finished" && (
                <button
                  type="button"
                  className="btn ghost small icon-btn"
                  aria-label="Dismiss work loop status"
                  title="Dismiss"
                  data-track="sidebar.loop_status.dismiss"
                  onClick={() => dismiss(loop.loopId)}
                >
                  ×
                </button>
              )}
            </div>
            {state === "waiting" && <div className="loop-status-line muted">{waitingLine(loop)}</div>}
            {state === "finished" && outcome && (
              <div className={`loop-status-line${isFailureOutcome(outcome) ? " error" : " muted"}`}>{outcome}</div>
            )}
            {current && (
              <div className="loop-status-line">
                <span className="loop-status-label"><span className="live-dot" aria-hidden="true" /> Now</span>
                {currentTask && <TaskLink project={project} id={currentTask} className="chip task-chip" inInspector />}
                <Link to={paths.session(project, current)} className="loop-status-session" data-track="sidebar.loop_status.follow">
                  {currentRow ? displayTitle(currentRow.session) : "Live session"}
                </Link>
              </div>
            )}
            {isActive(state) && !current && state !== "waiting" && <div className="loop-status-line muted">Between sessions…</div>}
            {done.length > 0 && (
              <div className="loop-status-line">
                <span className="loop-status-label">✓ Done</span>
                {done.slice(0, SHOWN_DONE).map((t) => (
                  <TaskLink key={t.id} project={project} id={t.id} className="chip task-chip" title={`${t.id} ${t.title}`} inInspector />
                ))}
                {done.length > SHOWN_DONE && (
                  <Link to={paths.loop(project)} className="muted">+{done.length - SHOWN_DONE}</Link>
                )}
              </div>
            )}
            {attention && (
              <Link to={paths.loop(project)} className="loop-status-line loop-status-attention" data-track="sidebar.loop_status.attention">
                ⚠ {attention}
              </Link>
            )}
          </section>
        );
      })}
    </div>
  );
}
