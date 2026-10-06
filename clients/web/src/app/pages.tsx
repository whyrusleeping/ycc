// Routed main-pane surfaces.
import { Link, Navigate, useNavigate, useParams } from "react-router";
import { useEffect, useMemo } from "react";
import { useProjects, useSessionFeed } from "../api/queries";
import { errorMessage } from "../api/client";
import { BacklogPage } from "../features/backlog/BacklogPage";
import { projectChoices } from "../features/newSession/model";
import { SessionList } from "../features/sessions/SessionList";
import { SessionView } from "../features/session/SessionView";
import { displayTitle, taskIds } from "../features/sessions/feed";
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
  const focus = useMemo(() => (summary ? taskIds(summary) : []), [summary]);
  return (
    <SessionView key={`${project}\u0000${sessionId}`} project={project} sessionId={sessionId} title={title} focusTasks={focus} />
  );
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

function useDocumentTitle(title: string) {
  useEffect(() => {
    document.title = `${title} · ycc`;
    return () => {
      document.title = "ycc";
    };
  }, [title]);
}

/**
 * `/p/:project/backlog[/:taskId]`, and the unscoped `/backlog[/:taskId]`,
 * which asks for a project (last viewed first) unless there is only one.
 */
export function BacklogRoutePage() {
  const { project, taskId } = useParams();
  if (project !== undefined) return <ScopedBacklog project={project} taskId={taskId ?? null} />;
  return <UnscopedBacklog taskId={taskId ?? null} />;
}

function ScopedBacklog({ project, taskId }: { project: string; taskId: string | null }) {
  const { setScope } = useScope();
  useEffect(() => setScope(project), [project, setScope]);
  useEffect(() => lastViewedProject.set(project), [project]);
  useDocumentTitle(taskId ? `Task ${taskId} · ${project}` : `Backlog · ${project}`);
  return <BacklogPage key={project} project={project} taskId={taskId} />;
}

function UnscopedBacklog({ taskId }: { taskId: string | null }) {
  const projects = useProjects();
  const navigate = useNavigate();
  const list = projects.data ?? [];
  const choices = useMemo(() => projectChoices(list, lastViewedProject.get()), [list]);
  useDocumentTitle(taskId ? `Task ${taskId}` : "Backlog");
  if (projects.isPending) return <div className="page muted">Loading…</div>;
  if (projects.isError) return <div className="page error">{errorMessage(projects.error)}</div>;
  // No registered project: the daemon's own workspace ("" resolves it).
  if (list.length === 0) return <BacklogPage project="" taskId={taskId} />;
  if (list.length === 1) {
    const name = list[0].name;
    return <Navigate replace to={taskId ? paths.task(name, taskId) : paths.backlog(name)} />;
  }
  const last = lastViewedProject.get();
  return (
    <div className="page">
      <header className="page-head">
        <h1>Backlog</h1>
      </header>
      <section className="project-ask" aria-labelledby="ask-backlog-project">
        <h2 id="ask-backlog-project">Which project’s backlog?</h2>
        <div className="choice-grid">
          {choices.map((name, i) => {
            const info = list.find((p) => p.name === name);
            return (
              <button
                key={name}
                type="button"
                className="choice-card"
                autoFocus={i === 0}
                onClick={() => navigate(taskId ? paths.task(name, taskId) : paths.backlog(name))}
              >
                <span className="choice-title">{name}</span>
                {info?.path && <span className="choice-sub mono">{info.path}</span>}
                {i === 0 && name === last && <span className="tag">last viewed</span>}
              </button>
            );
          })}
        </div>
      </section>
    </div>
  );
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
