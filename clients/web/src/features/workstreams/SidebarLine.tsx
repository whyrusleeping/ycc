// Ambient workstream status under the work-loop lines: one quiet, labelled line
// while any workstream in the sidebar's scope is in flight, so parallel work is
// visible without finding the section icon.
import { Link } from "react-router";
import { useWorkstreams } from "../../api/queries";
import { paths } from "../../app/paths";
import { inFlightProject, workstreamIndicator } from "./model";

export function WorkstreamsSidebarLine({ scope }: { scope: string | null }) {
  const workstreams = useWorkstreams(scope ?? "");
  const indicator = workstreamIndicator(workstreams.data);
  if (!indicator) return null;
  // Unscoped, the line counts every project: go straight to the project when
  // they are all in one, rather than to the "which project?" picker.
  const to = paths.workstreams(scope || inFlightProject(workstreams.data));
  return (
    <div className="loop-status ws-status">
      <section className="loop-status-card" aria-label="Workstreams">
        <div className="loop-status-head">
          <span className={`row-gutter loop-mark tone-${indicator.tone}`} aria-hidden="true" />
          <Link to={to} className="loop-status-title" data-track="sidebar.workstreams_status.open">
            Workstreams
          </Link>
          <Link
            to={to}
            className={indicator.tone === "attention" ? "loop-status-attention" : "loop-status-done"}
            title={indicator.title}
            data-track="sidebar.workstreams_status.state"
          >
            {indicator.title}
          </Link>
        </div>
      </section>
    </div>
  );
}
