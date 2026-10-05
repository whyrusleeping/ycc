// Routed main-pane surfaces.
import { Link, useParams } from "react-router";
import { useEffect } from "react";
import { useSessionFeed } from "../api/queries";
import { SessionList } from "../features/sessions/SessionList";
import { SessionView } from "../features/session/SessionView";
import { displayTitle } from "../features/sessions/feed";
import { NewSessionPage as NewSession } from "../features/newSession/NewSessionPage";
import { lastViewedProject } from "./memory";
import { PROJECT_SECTIONS, paths } from "./paths";
import { useScope } from "./scope";

/** `/`: the daemon-wide Recent sessions page. */
export function HomePage() {
  const { setScope } = useScope();
  useEffect(() => setScope(null), [setScope]);
  return (
    <div className="page">
      <header className="page-head">
        <h1>Recent sessions</h1>
        <Link to={paths.newSession(null)} className="btn primary">
          + New session
        </Link>
      </header>
      <SessionList scope={null} variant="page" />
    </div>
  );
}

/** `/p/:project`: one project's sessions. */
export function ProjectPage() {
  const { project = "" } = useParams();
  const { setScope } = useScope();
  useEffect(() => setScope(project), [project, setScope]);
  useEffect(() => lastViewedProject.set(project), [project]);
  return (
    <div className="page">
      <header className="page-head">
        <h1>{project}</h1>
        <Link to={paths.newSession(project)} className="btn primary">
          + New session
        </Link>
      </header>
      <SessionList scope={project} variant="page" />
    </div>
  );
}

/** `/p/:project/s/:sessionId` and `/s/:sessionId`. */
export function SessionPage() {
  const { project = "", sessionId = "" } = useParams();
  const { feed } = useSessionFeed(null);
  const summary = feed?.rows.find((r) => r.session.sessionId === sessionId)?.session;
  const title = summary ? displayTitle(summary) : sessionId;
  useEffect(() => lastViewedProject.set(project), [project]);
  useEffect(() => {
    document.title = `${title} · ycc`;
    return () => {
      document.title = "ycc";
    };
  }, [title]);
  return <SessionView key={`${project}\u0000${sessionId}`} project={project} sessionId={sessionId} title={title} />;
}

/** `/new` and `/p/:project/new`: start a session (asks for the project when unscoped). */
export function NewSessionPage() {
  const { project } = useParams();
  useEffect(() => {
    document.title = "New session · ycc";
    return () => {
      document.title = "ycc";
    };
  }, []);
  return <NewSession key={project ?? ""} routeProject={project ?? null} />;
}

/** A project surface that a later phase fills in. */
export function SectionPlaceholder({ section }: { section: string }) {
  const { project = "" } = useParams();
  const label = PROJECT_SECTIONS.find((s) => s.key === section)?.label ?? section;
  return (
    <div className="page placeholder">
      <h1>
        {label} <span className="muted">· {project}</span>
      </h1>
      <p className="muted">This view arrives in a later phase of the desktop web client.</p>
    </div>
  );
}

export function SettingsPlaceholder() {
  return (
    <div className="page placeholder">
      <h1>Settings</h1>
      <p className="muted">Model, role, and review settings arrive in a later phase of the desktop web client.</p>
    </div>
  );
}

export function NotFoundPage() {
  return (
    <div className="page placeholder">
      <h1>Not found</h1>
      <p>
        <Link to={paths.home()}>Back to recent sessions</Link>
      </p>
    </div>
  );
}
