// Backlog automatic-grooming cues (GetBacklogGroom, spec §6.2):
// - GroomHeaderPill: compact header indicator while an automatic groom is live.
// - GroomBanner: dismissible strip shown only when the latest automatic groom
//   failed (with report toggle and Retry). Resting/due states stay out of the
//   way; manual grooming lives in the backlog header's "⋯" menu and the
//   standing "decisions needed" task pins to the top of Proposed.
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { authStore } from "../../api/auth";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { queryKeys, useBacklogGroom } from "../../api/queries";
import { paths } from "../../app/paths";
import { toast } from "../../ui/toast";
import { Markdown } from "../markdown/Markdown";
import { groomFailed, groomLine, lastGroomLine } from "./model";
import { TaskLink } from "./parts";

const DISMISSED_GROOM_KEY = "ycc.groomBannerDismissed";

function readDismissedGrooms(): Set<string> {
  try {
    const raw = JSON.parse(localStorage.getItem(DISMISSED_GROOM_KEY) ?? "[]");
    return new Set(Array.isArray(raw) ? raw.filter((x): x is string => typeof x === "string") : []);
  } catch {
    return new Set();
  }
}

export function GroomHeaderPill({ project }: { project: string }) {
  const q = useBacklogGroom(project);
  const status = q.data;
  if (!status?.groomSessionId) return null;
  return (
    <Link
      to={paths.session(project, status.groomSessionId)}
      className="header-status-pill tone-running"
      title={`Backlog groom · ${groomLine(status)}`}
      data-track="backlog.watch_groom"
    >
      <span className="status-dot" aria-hidden="true" />
      Grooming…
    </Link>
  );
}

export function GroomBanner({ project }: { project: string }) {
  const q = useBacklogGroom(project);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [starting, setStarting] = useState(false);
  const [showReport, setShowReport] = useState(false);
  const [dismissed, setDismissed] = useState(readDismissedGrooms);
  const status = q.data;
  if (!status || !groomFailed(status)) return null;
  const run = status.lastGroom;
  const key = run?.sessionId || `${project}:failed`;
  if (dismissed.has(key)) return null;

  const dismiss = () => {
    const next = new Set(dismissed).add(key);
    setDismissed(next);
    try {
      localStorage.setItem(DISMISSED_GROOM_KEY, JSON.stringify([...next].slice(-50)));
    } catch {
      // ignore
    }
  };

  const groom = async () => {
    if (starting) return;
    setStarting(true);
    try {
      const resp = await client.startSession({ project, mode: "pm", preset: "backlog-groom" });
      void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
      navigate(paths.session(project, resp.sessionId));
    } catch (err) {
      if (isUnauthorized(err)) {
        authStore.expire();
        return;
      }
      toast(`Couldn’t start grooming: ${errorMessage(err)}`, "error", { op: "backlog.groom", err });
      setStarting(false);
    }
  };

  return (
    <section className="groom-banner failed" aria-label="Backlog grooming">
      <div className="groom-banner-row">
        <span className="groom-banner-text">
          <strong>Backlog groom</strong> · {groomLine(status)}
          {run && (
            <>
              {" · "}
              <Link to={paths.session(project, run.sessionId)} title={lastGroomLine(status)} data-track="backlog.groom_session">
                session
              </Link>
            </>
          )}
          {run?.report && (
            <>
              {" · "}
              <button
                type="button"
                className="link"
                aria-expanded={showReport}
                data-track="backlog.groom_report"
                onClick={() => setShowReport((v) => !v)}
              >
                {showReport ? "Hide report" : "Report"}
              </button>
            </>
          )}
          {status.decisionsTaskId && (
            <>
              {" · "}
              <TaskLink
                project={project}
                id={status.decisionsTaskId}
                className="groom-decisions"
                title="Open the grooms’ standing list of decisions for you"
              >
                Decisions ({status.decisionsTaskId})
              </TaskLink>
            </>
          )}
        </span>
        <span className="spacer" />
        <button
          type="button"
          className="btn small"
          disabled={starting}
          data-track="backlog.groom"
          onClick={() => void groom()}
          title="Start a backlog-groom session (pm mode)"
        >
          {starting ? "Starting…" : "Retry groom"}
        </button>
        <button type="button" className="btn ghost small icon-btn" aria-label="Dismiss groom failure" title="Dismiss" onClick={dismiss}>
          ×
        </button>
      </div>
      {showReport && run?.report && <Markdown project={project} text={run.report} className="groom-report" />}
    </section>
  );
}
