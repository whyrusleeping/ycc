// Ambient work-loop status at the top of the sidebar inbox: one quiet line per
// loop (state, finished count, blocked/in-review attention) and, while it runs,
// the task and session it is on now. Loop sessions are listed (tagged "loop")
// but carry no unread marks or notifications, so this is status only.
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
        const failed = state === "finished" && !!outcome && isFailureOutcome(outcome);
        const name = scope === null ? `${project} loop` : "Work loop";
        return (
          <section key={loop.loopId} className={`loop-status-card state-${state}`} aria-label={`Work loop${scope === null ? ` in ${project}` : ""}`}>
            <div className="loop-status-head">
              <span className={`row-gutter loop-mark tone-${state}`} aria-hidden="true" />
              {/* A routine outcome ("loop complete: …") is the title's tooltip; failures show inline. */}
              <Link
                to={paths.loop(project)}
                className="loop-status-title"
                title={state === "finished" && outcome && !failed ? outcome : `${name}: ${stateTitle(state).toLowerCase()}`}
                data-track="sidebar.loop_status.open"
              >
                {name}
              </Link>
              {state !== "running" && <span className={`loop-status-state tone-${state}`}>{stateTitle(state).toLowerCase()}</span>}
              {done.length > 0 && (
                <Link
                  to={paths.loop(project)}
                  className="loop-status-done"
                  title={done.slice(0, 12).map((t) => `${t.id} ${t.title}`).join("\n") + (done.length > 12 ? `\n+${done.length - 12} more` : "")}
                  data-track="sidebar.loop_status.done"
                >
                  {done.length} done
                </Link>
              )}
              <span className="loop-status-trail">
                {attention && (
                  <Link to={paths.loop(project)} className="loop-status-attention" data-track="sidebar.loop_status.attention">
                    {attention}
                  </Link>
                )}
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
              </span>
            </div>
            {state === "waiting" && <div className="loop-status-line muted">{waitingLine(loop)}</div>}
            {failed && <div className="loop-status-line error">{outcome}</div>}
            {current && (
              <div className="loop-status-line">
                {currentTask && <TaskLink project={project} id={currentTask} className="row-task" inInspector />}
                <Link to={paths.session(project, current)} className="loop-status-session" data-track="sidebar.loop_status.follow">
                  {currentRow ? displayTitle(currentRow.session) : "Live session"}
                </Link>
              </div>
            )}
            {isActive(state) && !current && state !== "waiting" && <div className="loop-status-line muted">Between sessions…</div>}
          </section>
        );
      })}
    </div>
  );
}
