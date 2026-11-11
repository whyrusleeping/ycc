// The backlog page's automatic-grooming strip (GetBacklogGroom, spec §6.2):
// the latest daemon-scheduled groom (state, when, how many changed tasks it
// took on, its session), its final report on demand, the standing "decisions
// needed" task when one exists, and "Groom now", which starts the
// backlog-groom preset like the memory page's groom action.
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

export function GroomBanner({ project }: { project: string }) {
  const q = useBacklogGroom(project);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [starting, setStarting] = useState(false);
  const [showReport, setShowReport] = useState(false);
  const status = q.data;
  if (!status) return null;
  const run = status.lastGroom;
  const running = status.groomSessionId;

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
    <section className={`groom-banner${groomFailed(status) ? " failed" : ""}`} aria-label="Backlog grooming">
      <div className="groom-banner-row">
        <span className="groom-banner-text">
          <strong>Backlog groom</strong> · {groomLine(status)}
          {run && (
            <>
              {" · "}
              <Link
                to={paths.session(project, run.sessionId)}
                title={lastGroomLine(status)}
                data-track="backlog.groom_session"
              >
                {running === run.sessionId ? "session" : "last run"}
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
                Decisions needed ({status.decisionsTaskId})
              </TaskLink>
            </>
          )}
        </span>
        <span className="spacer" />
        {running ? (
          <Link to={paths.session(project, running)} className="btn small" data-track="backlog.watch_groom">
            Watch groom
          </Link>
        ) : (
          <button
            type="button"
            className="btn small"
            disabled={starting}
            data-track="backlog.groom"
            onClick={() => void groom()}
            title="Start a backlog-groom session (pm mode)"
          >
            {starting ? "Starting…" : "Groom now"}
          </button>
        )}
      </div>
      {showReport && run?.report && <Markdown text={run.report} className="groom-report" />}
    </section>
  );
}
